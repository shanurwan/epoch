package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shanurwan/epoch/internal/protocol"
	"github.com/shanurwan/epoch/internal/scenario"
)

type fakeGuest struct {
	call func(context.Context, string, any, any) error
}

func (g *fakeGuest) Call(c context.Context, o string, p, r any) error { return g.call(c, o, p, r) }
func (g *fakeGuest) Close() error                                     { return nil }
func response(v, into any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

func TestClockAckRequiresMeasuredReadback(t *testing.T) {
	target := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, drift := range []time.Duration{0, 10 * time.Second} {
		g := &fakeGuest{call: func(_ context.Context, _ string, _ any, r any) error {
			return response(protocol.ClockSetResult{Target: target, Before: protocol.ClockObservation{Realtime: time.Now(), MonotonicNS: 100}, After: protocol.ClockObservation{Realtime: target.Add(drift), MonotonicNS: 110}, ElapsedNS: 10, ToleranceMS: 1000, ReadbackValid: true}, r)
		}}
		_, err := executeStep(context.Background(), g, scenario.Step{ID: "set", Op: "clock.set", At: target.Format(time.RFC3339), ToleranceMS: 1000})
		if (err == nil) != (drift == 0) {
			t.Fatalf("drift=%s error=%v", drift, err)
		}
	}
}
func TestActionErrorsNeverBecomeExitAssertions(t *testing.T) {
	clock := protocol.ClockObservation{Realtime: time.Now(), MonotonicNS: 1}
	for _, v := range []any{map[string]any{"exit_code": 0, "before": clock, "after": clock}, protocol.ActionResult{ExitCode: 0, StdoutJSON: json.RawMessage(`{}`), StdoutTruncated: true, Before: clock, After: clock}, protocol.ActionResult{ExitCode: -1, StdoutJSON: json.RawMessage(`{}`), Before: clock, After: clock}} {
		g := &fakeGuest{call: func(_ context.Context, _ string, _ any, r any) error { return response(v, r) }}
		if _, err := executeStep(context.Background(), g, scenario.Step{Op: "action.exec", Action: "observe", TimeoutMS: 50}); err == nil {
			t.Fatal("invalid action result accepted")
		}
	}
}
func TestLostActionNotRetriedAndDeadlinePropagates(t *testing.T) {
	calls := 0
	lost := errors.New("lost response")
	g := &fakeGuest{call: func(ctx context.Context, _ string, _ any, _ any) error {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("no deadline")
		}
		return lost
	}}
	_, err := executeStep(context.Background(), g, scenario.Step{Op: "action.exec", Action: "observe", TimeoutMS: 50})
	if !errors.Is(err, lost) || calls != 1 {
		t.Fatalf("error %v calls %d", err, calls)
	}
}
func TestExitPrecedence(t *testing.T) {
	for _, tc := range []struct {
		e, a, c string
		want    int
	}{{"COMPLETED", "PASS", "COMPLETE", 0}, {"COMPLETED", "FAIL", "COMPLETE", 1}, {"ERROR", "PASS", "COMPLETE", 3}, {"INTERRUPTED", "NOT_EVALUATED", "COMPLETE", 3}, {"CANCELLED", "NOT_EVALUATED", "COMPLETE", 130}, {"CANCELLED", "FAIL", "INCOMPLETE", 4}, {"COMPLETED", "NOT_EVALUATED", "COMPLETE", 3}} {
		r := Report{ExecutionStatus: tc.e, AssertionStatus: tc.a, CleanupStatus: tc.c}
		if got := r.ExitCode(); got != tc.want {
			t.Fatalf("%+v: %d", tc, got)
		}
	}
}

func TestWorkloadTimeoutAllowsTypedTerminationResponse(t *testing.T) {
	for _, op := range []string{"action.exec", "service.start", "service.stop"} {
		t.Run(op, func(t *testing.T) {
			fault := &protocol.Error{Code: "execution_error", Message: "action deadline/cancellation: context deadline exceeded"}
			g := &fakeGuest{call: func(ctx context.Context, _ string, payload, result any) error {
				var workloadMS int64
				switch request := payload.(type) {
				case protocol.ActionRequest:
					workloadMS = request.TimeoutMS
				case protocol.ServiceRequest:
					workloadMS = request.TimeoutMS
				default:
					t.Fatalf("unexpected request %T", payload)
				}
				if workloadMS != 10 {
					t.Fatalf("declared workload timeout changed: %d", workloadMS)
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) < 3*time.Second {
					t.Fatalf("response allowance missing: %v", deadline)
				}
				timer := time.NewTimer(30 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
					return fault
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
			_, err := executeStep(context.Background(), g, scenario.Step{Op: op, Action: "hang", Service: "probe", TimeoutMS: 10})
			if !errors.Is(err, fault) {
				t.Fatalf("typed guest error lost after workload timeout: %v", err)
			}
		})
	}
}

func TestWorkloadAndResponseBudgetsClipToParent(t *testing.T) {
	for _, op := range []string{"action.exec", "service.start", "service.stop"} {
		t.Run(op, func(t *testing.T) {
			parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			parentDeadline, _ := parent.Deadline()
			g := &fakeGuest{call: func(ctx context.Context, _ string, payload, result any) error {
				deadline, ok := ctx.Deadline()
				if !ok || !deadline.Equal(parentDeadline) {
					t.Fatalf("host deadline escaped parent: %v != %v", deadline, parentDeadline)
				}
				var budget int64
				switch request := payload.(type) {
				case protocol.ActionRequest:
					budget = request.TimeoutMS
				case protocol.ServiceRequest:
					budget = request.TimeoutMS
				}
				if budget < 1 || budget > 50 {
					t.Fatalf("guest workload budget not clipped: %d", budget)
				}
				<-ctx.Done()
				return ctx.Err()
			}}
			_, err := executeStep(parent, g, scenario.Step{Op: op, Action: "hang", Service: "probe", TimeoutMS: 5000})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("parent deadline not retained: %v", err)
			}
		})
	}
}

func TestParentCancellationBypassesResponseAllowance(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &fakeGuest{call: func(ctx context.Context, _ string, _, _ any) error { cancel(); <-ctx.Done(); return ctx.Err() }}
	_, err := executeStep(parent, g, scenario.Step{Op: "action.exec", Action: "hang", TimeoutMS: 5000})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation lost: %v", err)
	}
}
