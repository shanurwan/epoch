//go:build linux

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shanurwan/epoch/internal/artifact"
	"github.com/shanurwan/epoch/internal/config"
	"github.com/shanurwan/epoch/internal/evidence"
	"github.com/shanurwan/epoch/internal/firecracker"
	"github.com/shanurwan/epoch/internal/hostcheck"
	"github.com/shanurwan/epoch/internal/protocol"
	"github.com/shanurwan/epoch/internal/safefs"
	"github.com/shanurwan/epoch/internal/scenario"
)

// These tests copy ordinary bytes, never boot a guest or call a clock setter.
// The fake process identity is recorded only; recovery tests use no PID at all.
type lifecycleFixture struct {
	t           *testing.T
	root        string
	config      config.Config
	path        string
	input       []byte
	base        []byte
	image       artifact.Manifest
	scenario    scenario.Scenario
	bootID      string
	deps        runtimeDeps
	process     *lifecycleVMM
	guest       *lifecycleGuest
	starts      atomic.Int32
	privatePath string
}

type lifecycleVMM struct {
	done           chan struct{}
	once           sync.Once
	identity       firecracker.ProcessIdentity
	stopErr        error
	stops          atomic.Int32
	cleanupExpired atomic.Bool
}

func (p *lifecycleVMM) Done() <-chan struct{}                 { return p.done }
func (p *lifecycleVMM) Err() error                            { return nil }
func (p *lifecycleVMM) Identity() firecracker.ProcessIdentity { return p.identity }
func (p *lifecycleVMM) Stop(ctx context.Context) error {
	p.stops.Add(1)
	if ctx.Err() != nil {
		p.cleanupExpired.Store(true)
		return ctx.Err()
	}
	if p.stopErr != nil {
		return p.stopErr
	}
	p.once.Do(func() { close(p.done) })
	return nil
}

type lifecycleGuest struct {
	now       time.Time
	mono      int64
	actions   int
	shutdowns int
	closed    bool
	onAction  func(context.Context, int) error
}

func (g *lifecycleGuest) sample() protocol.ClockObservation {
	g.mono += 1000
	return protocol.ClockObservation{Realtime: g.now, MonotonicNS: g.mono}
}
func (g *lifecycleGuest) Call(ctx context.Context, op string, payload, result any) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("engine request is missing a deadline")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var value any
	switch op {
	case "clock.set":
		request, ok := payload.(protocol.ClockSetRequest)
		if !ok {
			return fmt.Errorf("unexpected clock request %T", payload)
		}
		target, err := time.Parse(time.RFC3339Nano, request.At)
		if err != nil {
			return err
		}
		before := g.sample()
		g.now = target
		after := g.sample()
		value = protocol.ClockSetResult{Target: target, Before: before, After: after, ElapsedNS: after.MonotonicNS - before.MonotonicNS, ToleranceMS: request.ToleranceMS, ReadbackValid: true}
	case "clock.read":
		value = g.sample()
	case "action.exec":
		g.actions++
		if g.onAction != nil {
			if err := g.onAction(ctx, g.actions); err != nil {
				return err
			}
		}
		before := g.sample()
		after := g.sample()
		observation, _ := json.Marshal(map[string]any{"year_utc": g.now.Year(), "value": "persistent-observation"})
		value = protocol.ActionResult{ExitCode: 0, StdoutJSON: observation, Before: before, After: after}
	case "shutdown":
		g.shutdowns++
		value = map[string]bool{"stopped": true}
	default:
		return fmt.Errorf("unexpected guest operation %s", op)
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, result)
}
func (g *lifecycleGuest) Close() error { g.closed = true; return nil }

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("engine lifecycle tests require an ordinary user")
	}
	if err := hostcheck.CheckPrivilege(); err != nil {
		t.Skipf("ordinary-user safety profile unavailable: %v", err)
	}
	root, err := os.MkdirTemp("/tmp", "epoch-engine-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Verify the exact test-created directory before recursive fixture cleanup.
		if filepath.Dir(root) != "/tmp" || !strings.HasPrefix(filepath.Base(root), "epoch-engine-") {
			t.Errorf("unsafe fixture cleanup path %q", root)
			return
		}
		if err := safefs.ValidatePrivateDir(root); err != nil {
			t.Errorf("fixture cleanup ownership: %v", err)
			return
		}
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("fixture cleanup: %v", err)
		}
	})
	if err := safefs.CheckSpace(root, 64, 5<<30); err != nil {
		t.Skipf("test requires runtime's conservative 5 GiB reserve: %v", err)
	}
	boot, err := hostcheck.BootID()
	if err != nil {
		t.Fatal(err)
	}
	f := &lifecycleFixture{t: t, root: root, bootID: boot, base: []byte("prepared private-copy fixture\n")}
	f.config = config.Defaults()
	f.config.FirecrackerPath = filepath.Join(root, "fake-firecracker")
	f.config.ArtifactDir = filepath.Join(root, "images")
	f.config.RuntimeRoot = filepath.Join(root, "r")
	f.config.EvidenceDir = filepath.Join(root, "e")
	for _, path := range []string{f.config.ArtifactDir, f.config.RuntimeRoot, f.config.EvidenceDir} {
		if err := safefs.EnsurePrivateDir(path); err != nil {
			t.Fatal(err)
		}
	}
	writeLifecycleBytes(t, f.config.FirecrackerPath, []byte("fake identity; never executed\n"))
	kernel := []byte("kernel fixture; never booted\n")
	writeLifecycleBytes(t, filepath.Join(f.config.ArtifactDir, "kernel"), kernel)
	writeLifecycleBytes(t, filepath.Join(f.config.ArtifactDir, "base.ext4"), f.base)
	workload := protocol.WorkloadManifest{APIVersion: protocol.WorkloadVersion, ID: "clock-probe", Version: "test-v1", UID: 1000, GID: 1000, WorkingDir: "/var/lib/epoch", Environment: map[string]string{}, Actions: map[string]protocol.Command{"observe": {Argv: []string{"/usr/local/bin/clock-probe", "observe"}}}, Services: map[string]protocol.Service{}}
	w := writeLifecycleJSON(t, filepath.Join(f.config.ArtifactDir, "workload.json"), workload)
	f.image = artifact.Manifest{APIVersion: artifact.Version, ID: "image-v1", Architecture: "x86_64", FirecrackerVersion: "1.16.1",
		Kernel:     artifact.File{Path: "kernel", SHA256: artifact.Hash(kernel), SizeBytes: int64(len(kernel)), SourceIdentity: "test-only kernel bytes", ChecksumOrigin: "locally-observed"},
		RootFS:     artifact.File{Path: "base.ext4", SHA256: artifact.Hash(f.base), SizeBytes: int64(len(f.base)), SourceIdentity: "test-only filesystem bytes", ChecksumOrigin: "locally-observed"},
		GuestAgent: artifact.Agent{Version: "test-v1", ProtocolVersion: protocol.Version, SHA256: artifact.Hash([]byte("fake agent"))},
		Workload:   artifact.Workload{ID: workload.ID, Version: workload.Version, ManifestPath: "workload.json", ManifestSHA256: artifact.Hash(w)},
		Recipe:     artifact.Recipe{Revision: "test-only-v1", Transformations: []string{"generated inert bytes for lifecycle tests"}},
	}
	writeLifecycleJSON(t, filepath.Join(f.config.ArtifactDir, "image-v1.json"), f.image)
	f.scenario = scenario.Scenario{APIVersion: scenario.APIVersion, Name: "lifecycle-test", Image: "image-v1", Workload: workload.ID, Resources: scenario.Resources{VCPUCount: 1, MemoryMiB: 512}, TimeoutMS: 10000, BootTimeoutMS: 2000,
		Steps:      []scenario.Step{{ID: "initial", Op: "clock.set", At: "2000-01-01T12:00:00Z"}, {ID: "first", Op: "action.exec", Action: "observe", TimeoutMS: 2000}, {ID: "second", Op: "action.exec", Action: "observe", TimeoutMS: 2000}},
		Assertions: []scenario.Assertion{{ID: "first-year", Step: "first", Pointer: "/stdout_json/year_utc", Op: "eq", Value: json.RawMessage(`2000`)}, {ID: "second-year", Step: "second", Pointer: "/stdout_json/year_utc", Op: "eq", Value: json.RawMessage(`2000`)}},
	}
	f.path = filepath.Join(root, "scenario.json")
	f.saveScenario()
	f.process = &lifecycleVMM{done: make(chan struct{}), identity: firecracker.ProcessIdentity{PID: 0, UID: os.Getuid(), HostBootID: boot, StartTicks: 1}}
	f.guest = &lifecycleGuest{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), mono: 1000000}
	f.deps = runtimeDeps{
		doctor: func(_ context.Context, _ string, probe bool) (hostcheck.Report, error) {
			if probe {
				return hostcheck.Report{}, errors.New("unexpected KVM probe")
			}
			return hostcheck.Report{UID: os.Getuid(), HostBootID: boot}, nil
		},
		executable: func(_ context.Context, path, version string) (firecracker.Identity, error) {
			return firecracker.Identity{Path: path, Version: version, SHA256: artifact.Hash([]byte("fake executable"))}, nil
		},
		start: func(_ context.Context, options firecracker.Options) (VMM, error) {
			f.starts.Add(1)
			if options.Executable.Path != f.config.FirecrackerPath {
				return nil, errors.New("wrong executable identity")
			}
			f.privatePath = filepath.Join(filepath.Dir(options.ConfigPath), "rootfs.ext4")
			private, err := os.ReadFile(f.privatePath)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(private, f.base) {
				return nil, errors.New("VMM received an incomplete private copy")
			}
			baseInfo, err := os.Stat(filepath.Join(f.config.ArtifactDir, "base.ext4"))
			if err != nil {
				return nil, err
			}
			privateInfo, err := os.Stat(f.privatePath)
			if err != nil {
				return nil, err
			}
			if os.SameFile(baseInfo, privateInfo) {
				return nil, errors.New("base was hard-linked instead of copied")
			}
			if err := os.WriteFile(f.privatePath, []byte("guest-private mutation\n"), 0600); err != nil {
				return nil, err
			}
			_, _ = fmt.Fprintln(options.Stdout, "fake VMM diagnostic")
			return f.process, nil
		},
		ready: func(_ context.Context, _ VMM, _ string, port uint32, id, nonce string, image *artifact.Prepared) (Guest, error) {
			if port != 7000 || !ValidRunID(id) || len(nonce) != 64 || image.Manifest.ID != f.image.ID {
				return nil, errors.New("invalid readiness identity")
			}
			return f.guest, nil
		},
	}
	return f
}

func writeLifecycleBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func writeLifecycleJSON(t *testing.T, path string, value any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	writeLifecycleBytes(t, path, b)
	return b
}
func (f *lifecycleFixture) saveScenario() {
	f.t.Helper()
	f.input = writeLifecycleJSON(f.t, f.path, f.scenario)
}
func (f *lifecycleFixture) run(ctx context.Context) *Report {
	f.t.Helper()
	r, err := run(ctx, f.config, f.path, f.deps)
	if err != nil {
		f.t.Fatalf("run: %v (report %+v)", err, r)
	}
	if r == nil {
		f.t.Fatal("missing report")
	}
	return r
}

func assertLifecycleOutcome(t *testing.T, r *Report, execution, assertion, cleanup string, code int) {
	t.Helper()
	if r.ExecutionStatus != execution || r.AssertionStatus != assertion || r.CleanupStatus != cleanup || r.ExitCode() != code || r.Stage != "FINISHED" || !r.ReportSaved {
		t.Fatalf("unexpected outcome: %+v, exit %d", r, r.ExitCode())
	}
}
func (f *lifecycleFixture) assertBaseUnchanged() {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.config.ArtifactDir, "base.ext4"))
	if err != nil {
		f.t.Fatal(err)
	}
	if !bytes.Equal(b, f.base) {
		f.t.Fatal("prepared base was modified")
	}
}
func (f *lifecycleFixture) assertClean(r *Report) {
	f.t.Helper()
	if _, err := os.Lstat(filepath.Join(f.config.RuntimeRoot, r.RunID)); !errors.Is(err, os.ErrNotExist) {
		f.t.Fatalf("private runtime remains: %v", err)
	}
	if f.process.stops.Load() != 1 || f.process.cleanupExpired.Load() {
		f.t.Fatalf("cleanup did not receive one independent live context: stops=%d expired=%v", f.process.stops.Load(), f.process.cleanupExpired.Load())
	}
	f.assertBaseUnchanged()
}

func TestLifecycleCompletedPassAndEvidence(t *testing.T) {
	f := newLifecycleFixture(t)
	r := f.run(context.Background())
	assertLifecycleOutcome(t, r, "COMPLETED", "PASS", "COMPLETE", 0)
	f.assertClean(r)
	if len(r.Assertions) != 4 || len(r.Steps) != 3 || f.guest.actions != 2 || f.guest.shutdowns != 1 || !f.guest.closed {
		t.Fatalf("incomplete experiment/cleanup: report=%+v guest=%+v", r, f.guest)
	}
	dir := filepath.Join(f.config.EvidenceDir, r.RunID)
	input, err := os.ReadFile(filepath.Join(dir, "scenario.input.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(input, f.input) {
		t.Fatal("saved input differs byte-for-byte")
	}
	saved, err := ReadReport(f.config, r.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertLifecycleOutcome(t, saved, "COMPLETED", "PASS", "COMPLETE", 0)
	events, err := os.Open(filepath.Join(dir, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	records, partial, err := evidence.ReadPartial(events)
	_ = events.Close()
	if err != nil || partial {
		t.Fatalf("partial events: %v %v", partial, err)
	}
	var stages []string
	for i, raw := range records {
		var event evidence.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Sequence != i+1 || event.ElapsedNS < 0 {
			t.Fatalf("invalid event sequence: %+v", event)
		}
		if event.Kind == "stage" {
			stages = append(stages, event.Stage)
		}
	}
	want := []string{"VALIDATING", "PREPARING", "BOOTING", "READY", "RUNNING", "COLLECTING", "CLEANING", "FINISHED"}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages %v, want %v", stages, want)
	}
	for _, name := range []string{"scenario.input.json", "scenario.resolved.json", "manifest.json", "events.ndjson", "clock-observations.ndjson", "actions.ndjson", "report.json", "firecracker.stdout.log", "firecracker.stderr.log"} {
		if err := safefs.ValidatePrivateFile(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLifecycleAssertionFailureIsCompleted(t *testing.T) {
	f := newLifecycleFixture(t)
	f.scenario.Assertions[1].Value = json.RawMessage(`1999`)
	f.saveScenario()
	r := f.run(context.Background())
	assertLifecycleOutcome(t, r, "COMPLETED", "FAIL", "COMPLETE", 1)
	f.assertClean(r)
	if f.guest.actions != 2 {
		t.Fatal("assertion failure interrupted declared execution")
	}
	found := false
	for _, a := range r.Assertions {
		if a.ID == "second-year" && a.Status == "FAIL" {
			found = true
		}
	}
	if !found {
		t.Fatal("explicit failing assertion not preserved")
	}
}

func TestLifecycleLostActionKeepsEarlierAssertions(t *testing.T) {
	f := newLifecycleFixture(t)
	f.guest.onAction = func(_ context.Context, n int) error {
		if n == 2 {
			return errors.New("lost response; action outcome unknown")
		}
		return nil
	}
	r := f.run(context.Background())
	assertLifecycleOutcome(t, r, "ERROR", "NOT_EVALUATED", "COMPLETE", 3)
	f.assertClean(r)
	if f.guest.actions != 2 {
		t.Fatalf("state-changing action was retried: %d calls", f.guest.actions)
	}
	if len(r.Assertions) != 2 || r.Assertions[0].ID != "first-year" || r.Assertions[0].Status != "PASS" {
		t.Fatalf("earlier assertions lost: %+v", r.Assertions)
	}
	if len(r.Steps) != 3 || r.Steps[2].Error == nil || len(r.Steps[2].Result) != 0 {
		t.Fatalf("unknown outcome missing typed execution failure: %+v", r.Steps)
	}
}

func TestLifecycleBootFailureCleansPrivateDisk(t *testing.T) {
	f := newLifecycleFixture(t)
	f.deps.ready = func(context.Context, VMM, string, uint32, string, string, *artifact.Prepared) (Guest, error) {
		return nil, errors.New("agent did not become ready")
	}
	r := f.run(context.Background())
	assertLifecycleOutcome(t, r, "ERROR", "NOT_EVALUATED", "COMPLETE", 3)
	f.assertClean(r)
	if f.guest.actions != 0 || len(r.Assertions) != 0 || len(r.Steps) != 0 {
		t.Fatal("boot failure produced workload results")
	}
}

func TestLifecycleCancellationUsesSeparateCleanupBudget(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.guest.onAction = func(actionCtx context.Context, n int) error {
		if n == 2 {
			cancel()
			<-actionCtx.Done()
			return actionCtx.Err()
		}
		return nil
	}
	r := f.run(ctx)
	assertLifecycleOutcome(t, r, "CANCELLED", "NOT_EVALUATED", "COMPLETE", 130)
	f.assertClean(r)
	if f.guest.shutdowns != 1 || !f.guest.closed {
		t.Fatal("cancelled context prevented guest cleanup")
	}
}

func TestLifecycleIncompleteCleanupRetainsDiskAndBlocksAdmission(t *testing.T) {
	f := newLifecycleFixture(t)
	f.process.stopErr = errors.New("fake owned VMM death could not be established")
	r := f.run(context.Background())
	assertLifecycleOutcome(t, r, "COMPLETED", "PASS", "INCOMPLETE", 4)
	f.assertBaseUnchanged()
	if _, err := os.Stat(filepath.Join(f.config.RuntimeRoot, r.RunID, "rootfs.ext4")); err != nil {
		t.Fatalf("uncertain-live disk was removed: %v", err)
	}
	if _, err := run(context.Background(), f.config, f.path, f.deps); err == nil {
		t.Fatal("new run admitted before cleanup recovery")
	}
	if f.starts.Load() != 1 {
		t.Fatal("admission failure started another VMM")
	}
}

func TestLifecycleConcurrentRunRejectedByLock(t *testing.T) {
	f := newLifecycleFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.guest.onAction = func(actionCtx context.Context, n int) error {
		if n == 1 {
			close(entered)
			select {
			case <-release:
				return nil
			case <-actionCtx.Done():
				return actionCtx.Err()
			}
		}
		return nil
	}
	type answer struct {
		report *Report
		err    error
	}
	finished := make(chan answer, 1)
	go func() { r, err := run(ctx, f.config, f.path, f.deps); finished <- answer{r, err} }()
	select {
	case <-entered:
	case result := <-finished:
		t.Fatalf("first run ended before lock check: %+v", result)
	case <-time.After(5 * time.Second):
		t.Fatal("first run did not reach workload")
	}
	_, err := run(context.Background(), f.config, f.path, f.deps)
	if err == nil || !strings.Contains(err.Error(), "runtime lock") {
		t.Errorf("concurrent run not rejected by lock: %v", err)
	}
	close(release)
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatal(result.err)
		}
		assertLifecycleOutcome(t, result.report, "COMPLETED", "PASS", "COMPLETE", 0)
		f.assertClean(result.report)
	case <-time.After(5 * time.Second):
		t.Fatal("first run did not finish")
	}
	if f.starts.Load() != 1 {
		t.Fatal("concurrent admission launched another VMM")
	}
}

func TestLifecycleInterruptedLaunchAmbiguityIsReadOnly(t *testing.T) {
	f := newLifecycleFixture(t)
	id := strings.Repeat("a", 32)
	dir := filepath.Join(f.config.RuntimeRoot, id)
	ev := filepath.Join(f.config.EvidenceDir, id)
	for _, path := range []string{dir, ev} {
		if err := safefs.EnsurePrivateDir(path); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := safefs.AcquireLock(f.config.RuntimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(dir, "rootfs.ext4")
	writeLifecycleBytes(t, disk, []byte("retain unknown-live disk"))
	m := Manifest{APIVersion: "epoch-run/v1", RunID: id, HostBootID: f.bootID, UID: os.Getuid(), Stage: "BOOTING", StartedAt: time.Now().UTC(), RuntimeDir: dir, CreatedPaths: []string{"rootfs.ext4"}, CleanupStatus: "INCOMPLETE"}
	manifest := writeLifecycleJSON(t, filepath.Join(ev, "manifest.json"), m)
	writeLifecycleBytes(t, filepath.Join(ev, "scenario.resolved.json"), f.input)
	r, err := ReadReport(f.config, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.ExecutionStatus != "INTERRUPTED" || r.AssertionStatus != "NOT_EVALUATED" || r.ReportSaved {
		t.Fatalf("partial evidence treated as final: %+v", r)
	}
	for _, apply := range []bool{false, true} {
		out, err := Recover(context.Background(), f.config, id, apply)
		if err != nil {
			t.Fatal(err)
		}
		if out.CanClean || out.ProcessAlive || !strings.Contains(out.Message, "manual review") {
			t.Fatalf("ambiguous launch permitted cleanup: %+v", out)
		}
	}
	if _, err := os.Stat(disk); err != nil {
		t.Fatalf("ambiguous disk removed: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(ev, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, manifest) {
		t.Fatal("ambiguous recovery mutated ownership evidence")
	}
	if _, err := os.Stat(filepath.Join(ev, "report.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ambiguous recovery published final report: %v", err)
	}
	if _, err := run(context.Background(), f.config, f.path, f.deps); err == nil {
		t.Fatal("interrupted experiment admitted a new run")
	}
	if f.starts.Load() != 0 {
		t.Fatal("interrupted admission started a VMM")
	}
}

func TestLifecycleRecoverPreLaunchInterruptionWithoutReplay(t *testing.T) {
	f := newLifecycleFixture(t)
	id := strings.Repeat("b", 32)
	dir := filepath.Join(f.config.RuntimeRoot, id)
	ev := filepath.Join(f.config.EvidenceDir, id)
	for _, path := range []string{dir, ev} {
		if err := safefs.EnsurePrivateDir(path); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := safefs.AcquireLock(f.config.RuntimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	writeLifecycleBytes(t, filepath.Join(dir, "rootfs.ext4.partial"), []byte("interrupted private copy"))
	m := Manifest{APIVersion: "epoch-run/v1", RunID: id, HostBootID: f.bootID, UID: os.Getuid(), Stage: "PREPARING", StartedAt: time.Now().UTC(), RuntimeDir: dir, CreatedPaths: []string{"rootfs.ext4.partial"}, CleanupStatus: "INCOMPLETE"}
	writeLifecycleJSON(t, filepath.Join(ev, "manifest.json"), m)
	writeLifecycleBytes(t, filepath.Join(ev, "scenario.resolved.json"), f.input)
	inspection, err := Recover(context.Background(), f.config, id, false)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.CanClean || inspection.ProcessAlive {
		t.Fatalf("pre-launch interruption not cleanable: %+v", inspection)
	}
	if _, err := os.Stat(filepath.Join(dir, "rootfs.ext4.partial")); err != nil {
		t.Fatalf("dry-run modified disk: %v", err)
	}
	out, err := Recover(context.Background(), f.config, id, true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Report == nil {
		t.Fatal("recovery did not publish interrupted report")
	}
	assertLifecycleOutcome(t, out.Report, "INTERRUPTED", "NOT_EVALUATED", "COMPLETE", 3)
	if out.Report.ScenarioName != f.scenario.Name || out.Report.Steps == nil || out.Report.Assertions == nil {
		t.Fatalf("recovered report lost scenario identity or result arrays: %+v", out.Report)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recover did not remove owned preparation files: %v", err)
	}
	if f.starts.Load() != 0 || f.guest.actions != 0 {
		t.Fatal("recovery replayed workload or launched a VM")
	}
	if err := admit(f.config); err != nil {
		t.Fatalf("completed recovery did not clear admission: %v", err)
	}
	f.assertBaseUnchanged()
}

func TestLifecycleHostClockDiscontinuityRetainsCompletedObservation(t *testing.T) {
	f := newLifecycleFixture(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var elapsed time.Duration
	f.deps.observe = func() (hostcheck.Observation, error) {
		elapsed += time.Millisecond
		wall := base.Add(elapsed)
		if f.guest.actions >= 1 {
			wall = wall.Add(2 * time.Second)
		}
		return hostcheck.Observation{Realtime: wall, MonotonicNS: elapsed.Nanoseconds(), BoottimeNS: elapsed.Nanoseconds()}, nil
	}
	r := f.run(context.Background())
	assertLifecycleOutcome(t, r, "ERROR", "NOT_EVALUATED", "COMPLETE", 3)
	f.assertClean(r)
	if f.guest.actions != 1 || len(r.Steps) != 2 {
		t.Fatalf("environmental error did not stop subsequent work: actions=%d steps=%+v", f.guest.actions, r.Steps)
	}
	completed := r.Steps[1]
	if completed.ID != "first" || completed.Error == nil || len(completed.Result) == 0 {
		t.Fatalf("completed observation lost when host sampling detected drift: %+v", completed)
	}
	var action protocol.ActionResult
	if err := json.Unmarshal(completed.Result, &action); err != nil || action.ExitCode != 0 || len(action.StdoutJSON) == 0 {
		t.Fatalf("retained action observation invalid: %+v error=%v", action, err)
	}
	if !strings.Contains(strings.Join(r.Causes, "\n"), "environmental clock discontinuity") {
		t.Fatalf("missing environmental classification: %v", r.Causes)
	}
}

func TestLifecycleLogQuotaDrainsAndStillPublishesError(t *testing.T) {
	f := newLifecycleFixture(t)
	start := f.deps.start
	var drained int
	f.deps.start = func(ctx context.Context, options firecracker.Options) (VMM, error) {
		process, err := start(ctx, options)
		if err != nil {
			return process, err
		}
		large := bytes.Repeat([]byte("x"), int(evidence.MaxBytes)+1)
		n, err := options.Stdout.Write(large)
		if err != nil || n != len(large) {
			return process, fmt.Errorf("quota writer did not drain: n=%d error=%v", n, err)
		}
		drained += n
		tail := []byte("stderr after quota must still drain\n")
		n, err = options.Stderr.Write(tail)
		if err != nil || n != len(tail) {
			return process, fmt.Errorf("post-quota writer did not drain: n=%d error=%v", n, err)
		}
		drained += n
		return process, nil
	}
	r := f.run(context.Background())
	assertLifecycleOutcome(t, r, "ERROR", "NOT_EVALUATED", "COMPLETE", 3)
	f.assertClean(r)
	if int64(drained) <= evidence.MaxBytes || f.guest.actions != 0 {
		t.Fatalf("quota failed to drain/stop execution: drained=%d actions=%d", drained, f.guest.actions)
	}
	if !strings.Contains(strings.Join(r.Causes, "\n"), "evidence quota exceeded") {
		t.Fatalf("quota error omitted from final evidence: %v", r.Causes)
	}
	dir := filepath.Join(f.config.EvidenceDir, r.RunID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var written int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("unexpected evidence entry %s", entry.Name())
		}
		written += info.Size()
	}
	if written > evidence.MaxBytes {
		t.Fatalf("evidence reserve exceeded: %d bytes", written)
	}
	saved, err := ReadReport(f.config, r.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertLifecycleOutcome(t, saved, "ERROR", "NOT_EVALUATED", "COMPLETE", 3)
}

func TestLifecycleReportPublicationFailureNeverClaimsSaved(t *testing.T) {
	f := newLifecycleFixture(t)
	f.guest.onAction = func(_ context.Context, n int) error {
		if n != 1 {
			return nil
		}
		id := filepath.Base(filepath.Dir(f.privatePath))
		if !ValidRunID(id) {
			return errors.New("invalid owned run identity in publication fault fixture")
		}
		return os.Mkdir(filepath.Join(f.config.EvidenceDir, id, "report.json"), 0700)
	}
	r, err := run(context.Background(), f.config, f.path, f.deps)
	if err == nil || r == nil {
		t.Fatalf("publication fault did not return an error and report: report=%+v error=%v", r, err)
	}
	if r.ReportSaved || r.ExecutionStatus != "ERROR" || r.CleanupStatus != "COMPLETE" || r.ExitCode() != 3 || r.Stage != "FINISHED" {
		t.Fatalf("publication failure claimed a successful saved experiment: %+v", r)
	}
	f.assertClean(r)
	if !strings.Contains(strings.Join(r.Causes, "\n"), "report not saved") {
		t.Fatalf("missing publication failure cause: %v", r.Causes)
	}
	path := filepath.Join(f.config.EvidenceDir, r.RunID, "report.json")
	info, statErr := os.Lstat(path)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("publication overwrote the conflicting path: %v", statErr)
	}
	if _, readErr := ReadReport(f.config, r.RunID); readErr == nil {
		t.Fatal("conflicting directory was accepted as a final report")
	}
}
