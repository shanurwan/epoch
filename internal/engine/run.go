package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shanurwan/epoch/internal/artifact"
	"github.com/shanurwan/epoch/internal/config"
	"github.com/shanurwan/epoch/internal/evidence"
	"github.com/shanurwan/epoch/internal/firecracker"
	"github.com/shanurwan/epoch/internal/guestclient"
	"github.com/shanurwan/epoch/internal/hostcheck"
	"github.com/shanurwan/epoch/internal/protocol"
	"github.com/shanurwan/epoch/internal/safefs"
	"github.com/shanurwan/epoch/internal/scenario"
)

func Run(parent context.Context, c config.Config, path string) (*Report, error) {
	return run(parent, c, path, runtimeDeps{doctor: hostcheck.Doctor, executable: firecracker.ValidateExecutable, start: func(ctx context.Context, o firecracker.Options) (VMM, error) {
		p, e := firecracker.Start(ctx, o)
		if e != nil {
			return nil, e
		}
		return p, nil
	}, ready: ready})
}

// These boundaries isolate process/transport faults in tests. Runtime callers
// always use the ordinary-user preflight, verified VMM and real guest transport.
type VMM interface {
	Done() <-chan struct{}
	Err() error
	Identity() firecracker.ProcessIdentity
	Stop(context.Context) error
}
type runtimeDeps struct {
	doctor     func(context.Context, string, bool) (hostcheck.Report, error)
	executable func(context.Context, string, string) (firecracker.Identity, error)
	start      func(context.Context, firecracker.Options) (VMM, error)
	ready      func(context.Context, VMM, string, uint32, string, string, *artifact.Prepared) (Guest, error)
	observe    func() (hostcheck.Observation, error)
}

func run(parent context.Context, c config.Config, path string, deps runtimeDeps) (*Report, error) {
	if deps.observe == nil {
		deps.observe = hostcheck.Observe
	}
	if err := hostcheck.CheckPrivilege(); err != nil {
		return nil, err
	}
	p, err := Validate(parent, c, path)
	if err != nil {
		return nil, &ValidationError{err}
	}
	preflight, err := deps.doctor(parent, c.FirecrackerPath, false)
	if err != nil {
		return nil, err
	}
	executable, err := deps.executable(parent, c.FirecrackerPath, p.Image.Manifest.FirecrackerVersion)
	if err != nil {
		return nil, err
	}
	if err = safefs.EnsurePrivateDir(c.RuntimeRoot); err != nil {
		return nil, err
	}
	if err = safefs.EnsurePrivateDir(c.EvidenceDir); err != nil {
		return nil, err
	}
	lock, err := safefs.AcquireLock(c.RuntimeRoot)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err = admit(c); err != nil {
		return nil, err
	}
	id, err := randomID(16)
	if err != nil {
		return nil, err
	}
	nonce, err := randomID(32)
	if err != nil {
		return nil, err
	}
	runtimeDir := filepath.Join(c.RuntimeRoot, id)
	evidenceDir := filepath.Join(c.EvidenceDir, id)
	if err = safefs.EnsurePrivateDir(evidenceDir); err != nil {
		return nil, err
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, time.Duration(p.Scenario.TimeoutMS)*time.Millisecond)
	defer cancel()
	rec, err := evidence.New(evidenceDir, cancel)
	if err != nil {
		return nil, fmt.Errorf("evidence initialization: %w", err)
	}
	r := &Report{APIVersion: ReportVersion, RunID: id, ScenarioName: p.Scenario.Name, TemporalMode: "guest-wall-clock-step/v1", ExecutionStatus: "ERROR", AssertionStatus: "NOT_EVALUATED", CleanupStatus: "INCOMPLETE", StartedAt: start.UTC(), Stage: "VALIDATING", Steps: []StepResult{}, Assertions: []scenario.AssertionResult{}, Causes: []string{}}
	configBytes, _ := json.Marshal(c)
	m := Manifest{APIVersion: "epoch-run/v1", RunID: id, UID: os.Getuid(), HostBootID: preflight.HostBootID, Stage: r.Stage, StartedAt: r.StartedAt, RuntimeDir: runtimeDir, CreatedPaths: []string{"firecracker.json", "rootfs.ext4.partial", "rootfs.ext4", "vsock.sock"}, ConfigSHA256: artifact.Hash(configBytes), ScenarioSHA256: artifact.Hash(p.Input), ArtifactManifestSHA256: p.Image.ManifestSHA256, Image: p.Image.Manifest, Executable: executable, CleanupStatus: "INCOMPLETE"}
	manifestPath := filepath.Join(evidenceDir, "manifest.json")
	stage := func(s string) error {
		r.Stage = s
		m.Stage = s
		if err := saveManifest(manifestPath, m); err != nil {
			return err
		}
		return rec.Event(s, "stage", nil)
	}
	var proc VMM
	var guest Guest
	var lastHost *hostcheck.Observation
	observe := func(label string) error {
		v, err := deps.observe()
		if err != nil {
			return err
		}
		if err = rec.Clocks(map[string]any{"api_version": "epoch-clocks/v1", "source": "host", "label": label, "observation": v}); err != nil {
			return err
		}
		if lastHost != nil {
			err = hostcheck.Compare(*lastHost, v, time.Duration(c.Policy.HostClockThresholdMS)*time.Millisecond)
		}
		lastHost = &v
		return err
	}
	monitorStop := make(chan struct{})
	var monitorDone chan struct{}
	execute := func() error {
		if err := stage("VALIDATING"); err != nil {
			return err
		}
		if err := rec.SaveRaw("scenario.input.json", p.Input); err != nil {
			return err
		}
		if err := rec.Save("scenario.resolved.json", p.Scenario); err != nil {
			return err
		}
		if err := observe("run.begin"); err != nil {
			return err
		}
		if err := stage("PREPARING"); err != nil {
			return err
		}
		if err := safefs.EnsurePrivateDir(runtimeDir); err != nil {
			return err
		}
		if err := safefs.CheckSpace(runtimeDir, p.Image.Manifest.RootFS.SizeBytes, c.Policy.ReserveBytes); err != nil {
			return err
		}
		if err := artifact.CopyPrivate(ctx, p.Image.Manifest.RootFS, filepath.Join(runtimeDir, "rootfs.ext4")); err != nil {
			return err
		}
		if err := firecracker.WriteConfig(filepath.Join(runtimeDir, "firecracker.json"), firecracker.ConfigOptions{KernelPath: p.Image.Manifest.Kernel.Path, RootFSPath: filepath.Join(runtimeDir, "rootfs.ext4"), SocketPath: filepath.Join(runtimeDir, "vsock.sock"), RunID: id, Nonce: nonce, CID: c.GuestCID, VCPUCount: p.Scenario.Resources.VCPUCount, MemoryMiB: p.Scenario.Resources.MemoryMiB}); err != nil {
			return err
		}
		stdout, err := rec.OpenLog("firecracker.stdout.log")
		if err != nil {
			return err
		}
		stderr, err := rec.OpenLog("firecracker.stderr.log")
		if err != nil {
			return err
		}
		if err = stage("BOOTING"); err != nil {
			return err
		}
		proc, err = deps.start(ctx, firecracker.Options{Executable: executable, ConfigPath: filepath.Join(runtimeDir, "firecracker.json"), Stdout: stdout, Stderr: stderr})
		if err != nil {
			return err
		}
		identity := proc.Identity()
		m.Process = &identity
		if err = saveManifest(manifestPath, m); err != nil {
			return err
		}
		monitorDone = make(chan struct{})
		go func() {
			defer close(monitorDone)
			select {
			case <-proc.Done():
				cancel()
			case <-monitorStop:
			}
		}()
		bootCtx, bootCancel := context.WithTimeout(ctx, time.Duration(p.Scenario.BootTimeoutMS)*time.Millisecond)
		defer bootCancel()
		guest, err = deps.ready(bootCtx, proc, filepath.Join(runtimeDir, "vsock.sock"), c.GuestPort, id, nonce, p.Image)
		if err != nil {
			return err
		}
		if err = stage("READY"); err != nil {
			return err
		}
		if err = stage("RUNNING"); err != nil {
			return err
		}
		observationBytes := 0
		for _, step := range p.Scenario.Steps {
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = observe(step.ID + ".before"); err != nil {
				return err
			}
			if err = rec.Event(r.Stage, "step.begin", map[string]string{"id": step.ID, "op": step.Op}); err != nil {
				return err
			}
			t0 := time.Now()
			raw, stepErr := executeStep(ctx, guest, step)
			afterErr := observe(step.ID + ".after")
			if stepErr != nil || afterErr != nil {
				combined := errors.Join(stepErr, afterErr)
				failure := &protocol.Error{Code: "execution", Message: combined.Error()}
				var typed *protocol.Error
				if errors.As(stepErr, &typed) {
					failure = typed
				}
				failed := StepResult{ID: step.ID, Op: step.Op, ElapsedNS: time.Since(t0).Nanoseconds(), Result: raw, Error: failure}
				r.Steps = append(r.Steps, failed)
				_ = rec.Action(failed)
				_ = rec.Clocks(map[string]any{"api_version": "epoch-clocks/v1", "source": "guest", "step": step.ID, "error": failure})
				return combined
			}
			observationBytes += len(raw)
			if observationBytes > 1<<20 {
				return fmt.Errorf("combined step result quota exceeded (1 MiB report budget)")
			}
			sr := StepResult{ID: step.ID, Op: step.Op, ElapsedNS: time.Since(t0).Nanoseconds(), Result: raw}
			r.Steps = append(r.Steps, sr)
			if err = rec.Action(sr); err != nil {
				return err
			}
			if err = rec.Clocks(map[string]any{"api_version": "epoch-clocks/v1", "source": "guest", "step": step.ID, "op": step.Op, "result": raw}); err != nil {
				return err
			}
			var selected []scenario.Assertion
			for _, a := range p.Scenario.Assertions {
				if a.Step == step.ID {
					selected = append(selected, a)
				}
			}
			results, evalErr := scenario.Evaluate(selected, map[string]json.RawMessage{step.ID: raw})
			for _, result := range results {
				observationBytes += len(result.Actual)
				if observationBytes > 1<<20 {
					return fmt.Errorf("combined observation/assertion detail quota exceeded")
				}
				r.Assertions = append(r.Assertions, result)
			}
			if evalErr != nil {
				return evalErr
			}
			if step.Op == "action.exec" {
				var action protocol.ActionResult
				_ = json.Unmarshal(raw, &action)
				want := 0
				if step.ExpectedExitCode != nil {
					want = *step.ExpectedExitCode
				}
				status := "PASS"
				if action.ExitCode != want {
					status = "FAIL"
				}
				actual, _ := json.Marshal(action.ExitCode)
				r.Assertions = append(r.Assertions, scenario.AssertionResult{ID: "auto:" + step.ID + ":exit_code", Step: step.ID, Status: status, Actual: actual, Message: fmt.Sprintf("expected exit code %d", want)})
			}
			if err = rec.Event(r.Stage, "step.complete", map[string]string{"id": step.ID}); err != nil {
				return err
			}
		}
		if err = observe("run.end"); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = stage("COLLECTING"); err != nil {
			return err
		}
		return rec.Err()
	}
	runErr := execute()
	close(monitorStop)
	if monitorDone != nil {
		<-monitorDone
	}
	if proc != nil {
		select {
		case <-proc.Done():
			runErr = errors.Join(runErr, fmt.Errorf("VMM exited during execution: %v", proc.Err()))
		default:
		}
	}
	if runErr == nil {
		r.ExecutionStatus = "COMPLETED"
		r.AssertionStatus = "PASS"
		for _, a := range r.Assertions {
			if a.Status != "PASS" {
				r.AssertionStatus = "FAIL"
			}
		}
	} else {
		addCause(r, runErr)
		if parent.Err() == context.Canceled {
			r.ExecutionStatus = "CANCELLED"
		}
	}
	// Cleanup is independent of an expired run context and keeps the original cause.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	if err = stage("CLEANING"); err != nil {
		addCause(r, err)
		if r.ExecutionStatus == "COMPLETED" {
			r.ExecutionStatus = "ERROR"
		}
	}
	if guest != nil {
		shutdownCtx, done := context.WithTimeout(cleanupCtx, 2*time.Second)
		var out json.RawMessage
		if e := guest.Call(shutdownCtx, "shutdown", nil, &out); e != nil {
			addCause(r, fmt.Errorf("guest shutdown: %w", e))
			if r.ExecutionStatus == "COMPLETED" {
				r.ExecutionStatus = "ERROR"
			}
		}
		done()
		_ = guest.Close()
	}
	dead := proc == nil
	if proc != nil {
		if e := proc.Stop(cleanupCtx); e != nil {
			addCause(r, fmt.Errorf("VMM cleanup: %w", e))
		} else {
			dead = true
		}
	}
	if e := observe("cleanup.end"); e != nil {
		addCause(r, e)
		if r.ExecutionStatus == "COMPLETED" {
			r.ExecutionStatus = "ERROR"
		}
	}
	if dead {
		if e := removeOwnedRuntime(c, m); e != nil {
			addCause(r, e)
		} else {
			r.CleanupStatus = "COMPLETE"
		}
	}
	m.CleanupStatus = r.CleanupStatus
	if e := stage("FINISHED"); e != nil {
		addCause(r, e)
		if r.ExecutionStatus == "COMPLETED" {
			r.ExecutionStatus = "ERROR"
		}
	}
	if e := rec.Close(); e != nil {
		addCause(r, e)
		if r.ExecutionStatus == "COMPLETED" {
			r.ExecutionStatus = "ERROR"
		}
	}
	if e := rec.Err(); e != nil {
		addCause(r, e)
		if r.ExecutionStatus == "COMPLETED" {
			r.ExecutionStatus = "ERROR"
		}
	}
	if parent.Err() == context.Canceled && r.ExecutionStatus != "CANCELLED" {
		addCause(r, fmt.Errorf("run cancelled during cleanup: %w", parent.Err()))
		r.ExecutionStatus = "CANCELLED"
	}
	r.FinishedAt = time.Now().UTC()
	r.ElapsedNS = time.Since(start).Nanoseconds()
	if e := rec.Final(r); e != nil {
		addCause(r, fmt.Errorf("report not saved: %w", e))
		if r.ExecutionStatus == "COMPLETED" {
			r.ExecutionStatus = "ERROR"
		}
		return r, e
	}
	r.ReportSaved = true
	return r, nil
}

func addCause(r *Report, err error) {
	if err == nil {
		return
	}
	s := err.Error()
	if len(s) > 4096 {
		s = s[:4096] + " [cause truncated]"
	}
	if len(r.Causes) < 64 {
		r.Causes = append(r.Causes, s)
	}
}
func randomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func ready(ctx context.Context, proc VMM, socket string, port uint32, id, nonce string, image *artifact.Prepared) (Guest, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("boot readiness deadline: %w (last dial: %v)", ctx.Err(), last)
		case <-proc.Done():
			return nil, fmt.Errorf("VMM exited before readiness: %v", proc.Err())
		default:
		}
		attempt, done := context.WithTimeout(ctx, time.Second)
		g, err := guestclient.Dial(attempt, socket, port, id, nonce)
		if err == nil {
			var hello protocol.HelloResult
			err = g.Call(attempt, "hello", nil, &hello)
			done()
			if err != nil {
				g.Close()
				return nil, fmt.Errorf("agent hello: %w", err)
			}
			m := image.Manifest
			if hello.AgentVersion != m.GuestAgent.Version || hello.AgentSHA256 != m.GuestAgent.SHA256 || hello.ProtocolVersion != protocol.Version || hello.RunID != id || hello.Nonce != nonce || hello.CID != 3 || hello.WorkloadID != m.Workload.ID || hello.WorkloadVersion != m.Workload.Version || hello.WorkloadSHA256 != m.Workload.ManifestSHA256 || !hello.Ready {
				g.Close()
				return nil, fmt.Errorf("guest image/agent/workload identity mismatch")
			}
			return g, nil
		}
		done()
		last = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("boot readiness deadline: %w", ctx.Err())
		case <-proc.Done():
			return nil, fmt.Errorf("VMM exited before readiness: %v", proc.Err())
		case <-ticker.C:
		}
	}
}
