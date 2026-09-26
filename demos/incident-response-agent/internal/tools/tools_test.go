package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/api"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/authority"
)

type memoryStore struct {
	worker      api.Worker
	restarts    int
	audits      []api.AuditRecord
	used        map[string]bool
	nextAuditID int64
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		worker: api.Worker{ID: "payments-worker-17", Service: "payments-api", Health: "unhealthy", UpdatedAt: time.Date(2035, 1, 1, 11, 50, 0, 0, time.UTC)},
		used:   map[string]bool{},
	}
}

func (s *memoryStore) ListIncidents(context.Context, string) ([]api.Incident, error) {
	return []api.Incident{{ID: "INC-2026-001", Service: "payments-api", Severity: "critical", Summary: "Elevated payment request failures", Status: "active"}}, nil
}

func (s *memoryStore) InspectIncident(context.Context, string) (api.Incident, []api.Worker, error) {
	incidents, _ := s.ListIncidents(context.Background(), "")
	return incidents[0], []api.Worker{s.worker}, nil
}

func (s *memoryStore) Worker(context.Context, string) (api.Worker, error) { return s.worker, nil }

func (s *memoryStore) audit(agentID, tool, resource string, decision api.Decision, performed bool) api.AuditRecord {
	s.nextAuditID++
	record := api.AuditRecord{ID: s.nextAuditID, AgentID: agentID, Tool: tool, Resource: resource, Decision: decision, SideEffectPerformed: performed, OccurredAt: time.Now().UTC()}
	s.audits = append(s.audits, record)
	return record
}

func (s *memoryStore) RecordAudit(_ context.Context, agentID, tool, resource string, decision api.Decision, performed bool) (api.AuditRecord, error) {
	return s.audit(agentID, tool, resource, decision, performed), nil
}

func (s *memoryStore) RestartWorker(_ context.Context, agentID, resource, jti string) (RestartResult, error) {
	before := s.worker
	if s.used[jti] {
		audit := s.audit(agentID, WorkerRestart, resource, api.DenyReplayedAuthority, false)
		return RestartResult{Before: before, After: before, Audit: audit, Decision: api.DenyReplayedAuthority}, nil
	}
	s.used[jti] = true
	s.worker.RestartCount++
	s.worker.Health = "healthy"
	s.restarts++
	audit := s.audit(agentID, WorkerRestart, resource, api.Authorized, true)
	return RestartResult{Before: before, After: s.worker, Audit: audit, Decision: api.Authorized, Applied: true}, nil
}

func testAuthority(t *testing.T) (string, authority.Validator) {
	t.Helper()
	result, err := authority.IssueEphemeral(authority.IssueRequest{
		Subject: "incident-agent-01", Scope: "worker.restart", Resource: "payments-worker-17",
		NotBefore: time.Date(2035, 1, 1, 12, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2035, 1, 1, 12, 0, 30, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := authority.DecodePublicKey(result.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return result.Token, authority.Validator{PublicKey: key, ExpectedSubject: "incident-agent-01"}
}

func TestExecutionTimeRevalidationDeniesExpiredSideEffect(t *testing.T) {
	token, validator := testAuthority(t)
	store := newMemoryStore()
	times := []time.Time{
		time.Date(2035, 1, 1, 12, 0, 25, 0, time.UTC),
		time.Date(2035, 1, 1, 12, 0, 35, 0, time.UTC),
	}
	index := 0
	service := &Service{
		Store: store, Validator: validator,
		Now:   func() time.Time { value := times[index]; index++; return value },
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	_, result, err := service.workerRestart(context.Background(), nil, api.WorkerRestartInput{
		WorkerID: "payments-worker-17", AuthorityToken: token, ExecutionDelayMS: 7000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.InitialCheck.Decision != api.Authorized || result.ExecutionCheck == nil ||
		result.ExecutionCheck.Decision != api.DenyExpired || result.SideEffectPerformed ||
		result.Worker.RestartCountBefore != 0 || result.Worker.RestartCountAfter != 0 || store.restarts != 0 {
		t.Fatalf("unexpected TOCTOU result: %+v", result)
	}
	if len(store.audits) != 1 || store.audits[0].Decision != api.DenyExpired || store.audits[0].SideEffectPerformed {
		t.Fatalf("unexpected audit: %+v", store.audits)
	}
}

func TestAuthorizedRestartIsOneTimePerAuthority(t *testing.T) {
	token, validator := testAuthority(t)
	store := newMemoryStore()
	now := time.Date(2035, 1, 1, 12, 0, 20, 0, time.UTC)
	service := &Service{Store: store, Validator: validator, Now: func() time.Time { return now }, Sleep: func(context.Context, time.Duration) error { return nil }}
	input := api.WorkerRestartInput{WorkerID: "payments-worker-17", AuthorityToken: token}
	_, first, err := service.workerRestart(context.Background(), nil, input)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := service.workerRestart(context.Background(), nil, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.SideEffectPerformed || first.Worker.RestartCountBefore != 0 || first.Worker.RestartCountAfter != 1 || store.restarts != 1 {
		t.Fatalf("first invocation did not update exactly once: %+v", first)
	}
	if second.SideEffectPerformed || second.ExecutionCheck == nil || second.ExecutionCheck.Decision != api.DenyReplayedAuthority || second.Worker.RestartCountAfter != 1 {
		t.Fatalf("replay was not denied: %+v", second)
	}
}

func TestInvalidSignatureDoesNotModifyState(t *testing.T) {
	token, validator := testAuthority(t)
	parts := strings.Split(token, ".")
	if parts[2][0] == 'A' {
		parts[2] = "B" + parts[2][1:]
	} else {
		parts[2] = "A" + parts[2][1:]
	}
	broken := strings.Join(parts, ".")
	store := newMemoryStore()
	service := &Service{Store: store, Validator: validator, Now: func() time.Time { return time.Date(2035, 1, 1, 12, 0, 20, 0, time.UTC) }}
	_, result, err := service.workerRestart(context.Background(), nil, api.WorkerRestartInput{WorkerID: "payments-worker-17", AuthorityToken: broken})
	if err != nil {
		t.Fatal(err)
	}
	if result.InitialCheck.Decision != api.DenyInvalidSignature || result.SideEffectPerformed || store.restarts != 0 {
		t.Fatalf("invalid signature result: %+v", result)
	}
}

func TestStructuredMCPToolResult(t *testing.T) {
	_, validator := testAuthority(t)
	service := &Service{Store: newMemoryStore(), Validator: validator}
	server := NewMCPServer(service)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "v1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: WorkerStatus, Arguments: api.WorkerStatusInput{WorkerID: "payments-worker-17"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.StructuredContent == nil {
		t.Fatalf("missing structured result: %+v", result)
	}
	b, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output api.WorkerStatusOutput
	if err = json.Unmarshal(b, &output); err != nil {
		t.Fatal(err)
	}
	if output.Worker.ID != "payments-worker-17" || output.Worker.RestartCount != 0 {
		t.Fatalf("unexpected structured output: %+v", output)
	}
}
