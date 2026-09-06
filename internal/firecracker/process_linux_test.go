//go:build linux && amd64

package firecracker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

func helperCommand(mode string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessHelper$")
	cmd.Env = append(os.Environ(), "EPOCH_PROCESS_TEST_HELPER="+mode)
	return cmd
}

// Only subprocesses with this test-only environment enter the helper branches.
func TestProcessHelper(t *testing.T) {
	switch os.Getenv("EPOCH_PROCESS_TEST_HELPER") {
	case "worker":
		for {
			time.Sleep(time.Hour)
		}
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		fmt.Println("ready")
		for {
			time.Sleep(time.Hour)
		}
	case "controller":
		p, err := startOwned(context.Background(), helperCommand("worker"), nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(31)
		}
		if err := json.NewEncoder(os.Stdout).Encode(p.Identity()); err != nil {
			os.Exit(32)
		}
		<-p.Done()
		os.Exit(33)
	}
}

func TestOwnedProcessStopAndIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := startOwned(ctx, helperCommand("worker"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = p.Stop(c)
	})
	state, err := InspectProcess(p.Identity())
	if err != nil || !state.Alive {
		t.Fatalf("inspection: %+v %v", state, err)
	}
	wrong := p.Identity()
	wrong.StartTicks++
	if _, err := InspectProcess(wrong); err == nil {
		t.Fatal("accepted mismatched PID identity")
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("Stop returned before Wait")
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverySignalsOnlyVerifiedProcess(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("recovery refuses root by design")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := startOwned(ctx, helperCommand("worker"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = p.Stop(cleanup)
	})
	wrong := p.Identity()
	wrong.StartTicks++
	if err := StopOwned(ctx, wrong); err == nil {
		t.Fatal("recovery accepted mismatched PID identity")
	}
	state, err := InspectProcess(p.Identity())
	if err != nil || !state.Alive {
		t.Fatalf("mismatched identity affected live process: %+v %v", state, err)
	}
	if err := StopOwned(ctx, p.Identity()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-ctx.Done():
		t.Fatal("recovered process was not reaped")
	}
}

func TestOwnedProcessEscalatesTERM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := helperCommand("ignore-term")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	cmd.Stdout = w
	p, err := startOwned(ctx, cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = p.Stop(c)
	})
	_ = r.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("helper readiness %q %v", line, err)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if p.Err() == nil {
		t.Fatal("terminated helper unexpectedly exited successfully")
	}
}

func TestAbruptControllerDeathKillsOwnedProcess(t *testing.T) {
	// This exercises parent death on disposable test processes only, without KVM.
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			cmd := helperCommand("controller")
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			cmd.Stdout = w
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				w.Close()
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill() }()
			w.Close()
			_ = r.SetReadDeadline(time.Now().Add(5 * time.Second))
			line, err := bufio.NewReader(r).ReadString('\n')
			if err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal(err)
			}
			var id ProcessIdentity
			if err := json.Unmarshal([]byte(line), &id); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal(err)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			deadline := time.Now().Add(3 * time.Second)
			for {
				state, err := InspectProcess(id)
				if err != nil {
					t.Fatal(err)
				}
				if !state.Alive {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("owned worker remained alive after controller SIGKILL")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestParseProcStatWithParentheses(t *testing.T) {
	fields := []string{"S"}
	for i := 0; i < 18; i++ {
		fields = append(fields, "0")
	}
	fields = append(fields, "12345")
	ticks, state, err := parseStat("12 (name with ) parens) " + strings.Join(fields, " "))
	if err != nil || ticks != 12345 || state != "S" {
		t.Fatalf("%d %q %v", ticks, state, err)
	}
}
