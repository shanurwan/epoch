//go:build linux

package guestagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shanurwan/epoch/internal/protocol"
)

// The subprocess is this test binary, under the current ordinary UID. It never
// invokes the agent, guest guard, clock setter, a shell or a privileged command.
func TestGuestProcessHelper(t *testing.T) {
	mode := os.Getenv("EPOCH_GUEST_TEST_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "observe":
		fmt.Println(`{"year_utc":2000}`)
		os.Exit(0)
	case "nonzero":
		fmt.Println(`{"expected_nonzero":true}`)
		os.Exit(7)
	case "malformed":
		fmt.Println("{broken")
		os.Exit(0)
	case "large-output":
		fmt.Print(strings.Repeat("x", protocol.MaxOutput+1))
		os.Exit(0)
	case "large-stderr":
		fmt.Fprint(os.Stderr, strings.Repeat("x", protocol.MaxOutput+1))
		fmt.Println(`{}`)
		os.Exit(0)
	case "hang":
		signal.Ignore(syscall.SIGTERM)
		for {
			time.Sleep(time.Hour)
		}
	case "probe":
		signal.Ignore(syscall.SIGTERM)
		for {
			time.Sleep(time.Hour)
		}
	case "ready":
		fmt.Println(`{"ready":true}`)
		os.Exit(0)
	case "dies":
		os.Exit(17)
	case "child-open", "child-closed":
		child := exec.Command(os.Args[0], "-test.run=^TestGuestProcessHelper$")
		child.Env = append(os.Environ(), "EPOCH_GUEST_TEST_HELPER=descendant")
		if mode == "child-open" {
			child.Stdout = os.Stdout
			child.Stderr = os.Stderr
		}
		if err := child.Start(); err != nil {
			os.Exit(125)
		}
		fmt.Printf("{\"child_pid\":%d}\n", child.Process.Pid)
		os.Exit(0)
	case "descendant":
		// Self-expiry bounds a failing regression test without signalling a
		// potentially reused descendant PID from the test controller.
		time.Sleep(3 * time.Second)
		os.Exit(0)
	default:
		os.Exit(125)
	}
}
func testManager(t *testing.T) *Manager {
	t.Helper()
	m := &Manager{clock: &fakeClock{}, manifest: protocol.WorkloadManifest{Actions: map[string]protocol.Command{}, Services: map[string]protocol.Service{"probe": {ReadinessAction: "ready"}, "dies": {ReadinessAction: "ready"}}}, services: map[string]*ownedProcess{}}
	for _, id := range []string{"observe", "nonzero", "malformed", "large-output", "large-stderr", "hang", "ready", "child-open", "child-closed"} {
		m.manifest.Actions[id] = protocol.Command{}
	}
	m.startOverride = func(kind, id string, input []byte) (*ownedProcess, error) {
		p := &ownedProcess{out: newCapture(), stderr: newCapture(), done: make(chan struct{})}
		cmd := exec.Command(os.Args[0], "-test.run=^TestGuestProcessHelper$")
		cmd.Env = append(os.Environ(), "EPOCH_GUEST_TEST_HELPER="+id)
		cmd.Stdin = bytes.NewReader(input)
		cmd.Stdout = p.out
		cmd.Stderr = p.stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
		p.cmd = cmd
		return startOwned(p)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := m.StopAll(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}
func TestActionObservationsAndFailures(t *testing.T) {
	for _, tc := range []struct {
		id             string
		exit           int
		executionError bool
		truncated      bool
	}{{"observe", 0, false, false}, {"nonzero", 7, false, false}, {"malformed", 0, true, false}, {"large-output", 0, true, true}, {"large-stderr", 0, true, true}} {
		t.Run(tc.id, func(t *testing.T) {
			m := testManager(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			got, err := m.Action(ctx, protocol.ActionRequest{Action: tc.id})
			if (err != nil) != tc.executionError {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			if !tc.executionError && got.ExitCode != tc.exit {
				t.Fatalf("exit=%d", got.ExitCode)
			}
			if tc.truncated && !got.StdoutTruncated && !got.StderrTruncated {
				t.Fatal("overflow not declared")
			}
			if got.Before.Realtime.IsZero() || got.After.Realtime.IsZero() {
				t.Fatal("failure lost fake clock observations")
			}
		})
	}
}
func TestActionTimeoutReapsOwnedProcess(t *testing.T) {
	m := testManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := m.Action(ctx, protocol.ActionRequest{Action: "hang"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("bounded termination exceeded budget")
	}
}
func TestServiceForegroundLifecycleAndUnexpectedDeath(t *testing.T) {
	m := testManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := m.Start(ctx, protocol.ServiceRequest{Service: "probe"}); err != nil {
		t.Fatal(err)
	}
	if m.Active() != 1 {
		t.Fatal("missing owned service")
	}
	if _, err := m.Action(ctx, protocol.ActionRequest{Action: "observe"}); err != nil {
		t.Fatal("action blocked by foreground service:", err)
	}
	if _, err := m.Stop(ctx, protocol.ServiceRequest{Service: "probe"}); err != nil {
		t.Fatal(err)
	}
	if m.Active() != 0 {
		t.Fatal("service not removed after reap")
	}
	_, err := m.Start(ctx, protocol.ServiceRequest{Service: "dies"})
	if err == nil {
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		for m.Health() == nil {
			select {
			case <-deadline.C:
				t.Fatal("service death not detected")
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}
	if m.Health() == nil {
		t.Fatal("unexpected service exit accepted")
	}
}
func TestOutputWriterDrainsAfterQuota(t *testing.T) {
	b := newCapture()
	chunk := bytes.Repeat([]byte("x"), protocol.MaxOutput)
	for range 8 {
		n, err := b.Write(chunk)
		if n != len(chunk) || err != nil {
			t.Fatal("writer stopped draining")
		}
	}
	data, truncated := b.snapshot()
	if len(data) != protocol.MaxOutput || !truncated {
		t.Fatal("capture not bounded")
	}
	select {
	case <-b.quota:
	default:
		t.Fatal("quota notification missing")
	}
}

func TestLeaderExitKillsDescendantsBeforeReap(t *testing.T) {
	for _, id := range []string{"child-open", "child-closed"} {
		t.Run(id, func(t *testing.T) {
			m := testManager(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			start := time.Now()
			result, err := m.Action(ctx, protocol.ActionRequest{Action: id})
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(start) > time.Second {
				t.Fatal("descendant output kept Wait blocked")
			}
			var observation struct {
				ChildPID int `json:"child_pid"`
			}
			if err = json.Unmarshal(result.StdoutJSON, &observation); err != nil || observation.ChildPID <= 0 {
				t.Fatalf("invalid child observation: %s %v", result.StdoutJSON, err)
			}
			deadline := time.Now().Add(250 * time.Millisecond)
			for {
				stat, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", observation.ChildPID))
				if os.IsNotExist(readErr) {
					break
				}
				if readErr != nil {
					t.Fatal(readErr)
				}
				end := strings.LastIndexByte(string(stat), ')')
				fields := strings.Fields(string(stat[end+1:]))
				if len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("same-group descendant still running after successful action cleanup")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
func TestTerminateRacesLeaderExitWithoutSignalsAfterReap(t *testing.T) {
	m := testManager(t)
	for range 20 {
		p, err := m.launch("action", "observe", nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var wg sync.WaitGroup
		failures := make(chan error, 2)
		for range 2 {
			wg.Go(func() { failures <- terminate(ctx, p) })
		}
		wg.Wait()
		close(failures)
		cancel()
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
		p.signalMu.Lock()
		reaping := p.reaping
		p.signalMu.Unlock()
		if !reaping {
			t.Fatal("leader reaped without closing signal admission")
		}
		if err = p.signalGroup(syscall.SIGKILL); err != nil {
			t.Fatal("completed process group was signalled:", err)
		}
	}
}
