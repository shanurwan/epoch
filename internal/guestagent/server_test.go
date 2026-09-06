package guestagent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shanurwan/epoch/internal/protocol"
)

type fakeClock struct {
	valid bool
	sets  int
}

func (f *fakeClock) Read() (protocol.ClockObservation, error) {
	return protocol.ClockObservation{Realtime: time.Now(), MonotonicNS: 10}, nil
}
func (f *fakeClock) Set(protocol.ClockSetRequest) (protocol.ClockSetResult, error) {
	f.sets++
	return protocol.ClockSetResult{ReadbackValid: f.valid}, nil
}

type fakeRunner struct{ active, actions int }

func (f *fakeRunner) Action(context.Context, protocol.ActionRequest) (protocol.ActionResult, error) {
	f.actions++
	return protocol.ActionResult{StdoutJSON: json.RawMessage(`{}`)}, nil
}
func (f *fakeRunner) Start(context.Context, protocol.ServiceRequest) (protocol.ServiceResult, error) {
	f.active++
	return protocol.ServiceResult{}, nil
}
func (f *fakeRunner) Stop(context.Context, protocol.ServiceRequest) (protocol.ServiceResult, error) {
	f.active--
	return protocol.ServiceResult{}, nil
}
func (f *fakeRunner) StopAll(context.Context) error { f.active = 0; return nil }
func (f *fakeRunner) Active() int                   { return f.active }
func (f *fakeRunner) Health() error                 { return nil }
func TestClockReadbackBarrierAndLivePermission(t *testing.T) {
	clock := &fakeClock{}
	runner := &fakeRunner{}
	s := &Server{Clock: clock, Runner: runner, Manifest: protocol.WorkloadManifest{Actions: map[string]protocol.Command{"observe": {}}}}
	action := protocol.Request{Op: "action.exec", Payload: json.RawMessage(`{"action":"observe","timeout_ms":100}`)}
	if _, err := s.handle(context.Background(), action); err == nil || runner.actions != 0 {
		t.Fatal("workload passed closed clock barrier")
	}
	set := protocol.Request{Op: "clock.set", Payload: json.RawMessage(`{"at":"1999-12-31T23:59:00Z"}`)}
	if _, err := s.handle(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	if _, err := s.handle(context.Background(), action); err == nil {
		t.Fatal("invalid readback opened barrier")
	}
	clock.valid = true
	if _, err := s.handle(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	if _, err := s.handle(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	runner.active = 1
	before := clock.sets
	if _, err := s.handle(context.Background(), set); err == nil || clock.sets != before {
		t.Fatal("live clock changed without allow_live")
	}
	set.Payload = json.RawMessage(`{"at":"2000-01-01T00:01:00Z","allow_live":true}`)
	if _, err := s.handle(context.Background(), set); err != nil {
		t.Fatal(err)
	}
}
func TestInputAndTimeoutValidation(t *testing.T) {
	s := &Server{Clock: &fakeClock{}, Runner: &fakeRunner{}, clockReady: true, Manifest: protocol.WorkloadManifest{Actions: map[string]protocol.Command{"observe": {}}}}
	for _, body := range []string{`{"action":"absent"}`, `{"action":"observe","timeout_ms":-1}`, `{"action":"observe","input":{"x":1,"x":2}}`, `{"action":"observe","extra":1}`} {
		if _, err := s.handle(context.Background(), protocol.Request{Op: "action.exec", Payload: json.RawMessage(body)}); err == nil {
			t.Fatal("accepted", body)
		}
	}
}
