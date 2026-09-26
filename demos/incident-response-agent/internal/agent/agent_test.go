package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/api"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/tools"
)

type fakeClient struct {
	calls []string
}

func (f *fakeClient) Discover(context.Context) ([]string, error) {
	return []string{tools.WorkerStatus, tools.IncidentList, tools.WorkerRestart, tools.IncidentInspect}, nil
}

func (f *fakeClient) Call(_ context.Context, name string, _, output any) error {
	f.calls = append(f.calls, name)
	now := time.Date(2035, 1, 1, 12, 0, 20, 0, time.UTC)
	var value any
	switch name {
	case tools.IncidentList:
		value = api.IncidentListOutput{Incidents: []api.Incident{
			{ID: "INC-LOW", Severity: "warning", Status: "active"},
			{ID: "INC-2026-001", Service: "payments-api", Severity: "critical", Status: "active"},
		}}
	case tools.IncidentInspect:
		value = api.IncidentInspectOutput{
			Incident: api.Incident{ID: "INC-2026-001", Service: "payments-api", Severity: "critical", Status: "active"},
			Workers:  []api.Worker{{ID: "payments-worker-17", Service: "payments-api", Health: "unhealthy", UpdatedAt: now}},
		}
	case tools.WorkerStatus:
		health, count := "unhealthy", 0
		if len(f.calls) > 4 {
			health, count = "healthy", 1
		}
		value = api.WorkerStatusOutput{Worker: api.Worker{ID: "payments-worker-17", Service: "payments-api", Health: health, RestartCount: count, UpdatedAt: now}}
	case tools.WorkerRestart:
		value = api.WorkerRestartOutput{
			AgentID: "incident-agent-01", Tool: tools.WorkerRestart, Resource: "payments-worker-17",
			InitialCheck:        api.AuthorizationCheck{Time: now, Decision: api.Authorized},
			ExecutionCheck:      &api.AuthorizationCheck{Time: now, Decision: api.Authorized},
			SideEffectPerformed: true,
			Worker:              api.WorkerTransition{RestartCountBefore: 0, RestartCountAfter: 1, HealthBefore: "unhealthy", HealthAfter: "healthy"},
		}
	}
	encoded, _ := json.Marshal(value)
	return json.Unmarshal(encoded, output)
}

func TestDeterministicDecisionAndToolSequence(t *testing.T) {
	client := &fakeClient{}
	result, err := Run(context.Background(), client, api.AgentInput{
		Scenario: "before-expiry", AgentID: "incident-agent-01", ExecutionDelayMS: 0,
	}, "signed-token")
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{tools.IncidentList, tools.IncidentInspect, tools.WorkerStatus, tools.WorkerRestart, tools.WorkerStatus}
	if !reflect.DeepEqual(client.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", client.calls, wantCalls)
	}
	if !reflect.DeepEqual(result.MCP.Calls, wantCalls) {
		t.Fatalf("trace = %v, want %v", result.MCP.Calls, wantCalls)
	}
	if !result.Decision.RestartRequested || result.Restart == nil || !result.Restart.SideEffectPerformed {
		t.Fatalf("unexpected decision/result: %#v", result)
	}
	if result.WorkerAfter.RestartCount != 1 || result.WorkerAfter.Health != "healthy" {
		t.Fatalf("unexpected database-backed final state: %#v", result.WorkerAfter)
	}
	wantTools := []string{tools.IncidentInspect, tools.IncidentList, tools.WorkerRestart, tools.WorkerStatus}
	if !reflect.DeepEqual(result.MCP.DiscoveredTools, wantTools) {
		t.Fatalf("discovered tools = %v, want %v", result.MCP.DiscoveredTools, wantTools)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "signed-token") {
		t.Fatal("structured evidence contains delegated authority token")
	}
}

func TestSDKClientDiscoversAndDecodesStructuredTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: tools.WorkerStatus}, func(_ context.Context, _ *mcp.CallToolRequest, input api.WorkerStatusInput) (*mcp.CallToolResult, api.WorkerStatusOutput, error) {
		return nil, api.WorkerStatusOutput{Worker: api.Worker{ID: input.WorkerID, Health: "unhealthy"}}, nil
	})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	sdk := SDKClient{Session: clientSession}
	discovered, err := sdk.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(discovered, []string{tools.WorkerStatus}) {
		t.Fatalf("discovered = %v", discovered)
	}
	var status api.WorkerStatusOutput
	if err := sdk.Call(ctx, tools.WorkerStatus, api.WorkerStatusInput{WorkerID: "payments-worker-17"}, &status); err != nil {
		t.Fatal(err)
	}
	if status.Worker.ID != "payments-worker-17" || status.Worker.Health != "unhealthy" {
		t.Fatalf("unexpected structured result: %+v", status)
	}
}
