package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var (
	notBefore = time.Date(2035, 1, 1, 12, 0, 0, 0, time.UTC)
	expiresAt = time.Date(2035, 1, 1, 12, 0, 30, 0, time.UTC)
)

func validRequest() operationRequest {
	return operationRequest{
		RequestID: "restart-001",
		AgentID:   "incident-agent-01",
		Operation: "worker.restart",
		Resource:  "worker-17",
		Authority: authority{
			Principal:  "agent://incident-agent-01",
			Capability: "worker.restart",
			Resource:   "worker-17",
			NotBefore:  notBefore.Format(time.RFC3339),
			ExpiresAt:  expiresAt.Format(time.RFC3339),
		},
	}
}

func TestValidityIntervalBoundaries(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want authorization
	}{
		{"before-not-before", notBefore.Add(-time.Nanosecond), denyNotYetValid},
		{"at-not-before", notBefore, authorized},
		{"inside", expiresAt.Add(-time.Nanosecond), authorized},
		{"at-expiry", expiresAt, denyExpired},
		{"after-expiry", expiresAt.Add(time.Nanosecond), denyExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := authorize(validRequest(), test.now); got != test.want {
				t.Fatalf("authorize() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestTypedDenialsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*operationRequest)
		want   authorization
	}{
		{"capability", func(r *operationRequest) { r.Authority.Capability = "worker.status" }, denyCapabilityMismatch},
		{"resource", func(r *operationRequest) { r.Authority.Resource = "worker-18" }, denyResourceMismatch},
		{"principal", func(r *operationRequest) { r.Authority.Principal = "agent://other-agent" }, denyPrincipalMismatch},
		{"malformed-authority", func(r *operationRequest) { r.Authority.ExpiresAt = "not-a-time" }, denyMalformedAuthority},
		{"empty-interval", func(r *operationRequest) { r.Authority.ExpiresAt = r.Authority.NotBefore }, denyMalformedAuthority},
		{"malformed-request", func(r *operationRequest) { r.AgentID = "" }, denyMalformedRequest},
		{"unsupported-operation", func(r *operationRequest) { r.Operation = "host.service.restart" }, denyUnsupportedOperation},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validRequest()
			test.mutate(&request)
			if got := authorize(request, notBefore); got != test.want {
				t.Fatalf("authorize() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestImmediateAttemptRevalidatesBeforeSideEffect(t *testing.T) {
	times := []time.Time{expiresAt.Add(-5 * time.Second), expiresAt.Add(5 * time.Second)}
	index := 0
	app := application{
		stateDir: t.TempDir(),
		now: func() time.Time {
			value := times[index]
			index++
			return value
		},
	}
	result, err := app.attempt(validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.InitialAuthorization != authorized || result.ExecutionAuthorization != denyExpired ||
		!result.RequestAccepted || result.SideEffectPerformed {
		t.Fatalf("unexpected revalidation result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(app.stateDir, workerFile)); !os.IsNotExist(err) {
		t.Fatalf("expired execution changed worker state: %v", err)
	}
}

func TestPendingRequestRevalidatesAtExecutionBoundary(t *testing.T) {
	now := expiresAt.Add(-5 * time.Second)
	app := application{stateDir: t.TempDir(), now: func() time.Time { return now }}
	requested, err := app.request(validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if requested.InitialAuthorization != authorized || !requested.RequestAccepted || requested.SideEffectPerformed {
		t.Fatalf("unexpected request result: %+v", requested)
	}
	now = expiresAt.Add(5 * time.Second)
	executed, err := app.executePending()
	if err != nil {
		t.Fatal(err)
	}
	if executed.InitialAuthorization != authorized || executed.ExecutionAuthorization != denyExpired ||
		!executed.RequestAccepted || executed.SideEffectPerformed {
		t.Fatalf("unexpected execution result: %+v", executed)
	}
	if _, err := os.Stat(filepath.Join(app.stateDir, pendingFile)); !os.IsNotExist(err) {
		t.Fatalf("pending request was not consumed: %v", err)
	}
	state, err := app.readWorkerState("worker-17")
	if err != nil {
		t.Fatal(err)
	}
	if state.RestartCount != 0 || state.LastRequestID != "" {
		t.Fatalf("denied execution changed state: %+v", state)
	}
}

func TestAuthorizedRestartOnlyChangesWorkloadLocalState(t *testing.T) {
	now := expiresAt.Add(-time.Second)
	dir := t.TempDir()
	app := application{stateDir: dir, now: func() time.Time { return now }}
	result, err := app.attempt(validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.InitialAuthorization != authorized || result.ExecutionAuthorization != authorized ||
		!result.SideEffectPerformed || result.WorkerState == nil || result.WorkerState.RestartCount != 1 {
		t.Fatalf("unexpected authorized result: %+v", result)
	}
	b, err := os.ReadFile(filepath.Join(dir, workerFile))
	if err != nil {
		t.Fatal(err)
	}
	var state workerState
	if err = json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	if state.Resource != "worker-17" || state.LastRequestID != "restart-001" || state.RestartCount != 1 {
		t.Fatalf("unexpected local state: %+v", state)
	}
}
