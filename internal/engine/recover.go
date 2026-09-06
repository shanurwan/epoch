package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/shanurwan/epoch/internal/config"
	"github.com/shanurwan/epoch/internal/firecracker"
	"github.com/shanurwan/epoch/internal/hostcheck"
	"github.com/shanurwan/epoch/internal/jsonutil"
	"github.com/shanurwan/epoch/internal/safefs"
	"github.com/shanurwan/epoch/internal/scenario"
)

var runPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func ValidRunID(id string) bool { return runPattern.MatchString(id) }

func evidencePath(c config.Config, id, name string) (string, error) {
	if !ValidRunID(id) {
		return "", fmt.Errorf("run ID must be 32 lowercase hex characters")
	}
	dir := filepath.Join(c.EvidenceDir, id)
	if err := safefs.ValidatePrivateDir(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := safefs.ValidatePrivateFile(path); err != nil {
		return "", err
	}
	return path, nil
}
func loadManifest(c config.Config, id string) (Manifest, error) {
	var m Manifest
	path, err := evidencePath(c, id, "manifest.json")
	if err != nil {
		return m, err
	}
	b, err := config.ReadBounded(path, 64<<10)
	if err != nil {
		return m, err
	}
	if err = jsonutil.Decode(b, &m); err != nil {
		return m, err
	}
	if m.APIVersion != "epoch-run/v1" || m.RunID != id || m.HostBootID == "" || m.UID != os.Getuid() || m.RuntimeDir != filepath.Join(c.RuntimeRoot, id) {
		return m, fmt.Errorf("ownership manifest does not match configured operator/run roots")
	}
	if m.Process != nil && (m.Process.UID != m.UID || m.Process.HostBootID != m.HostBootID) {
		return m, fmt.Errorf("process identity differs from run owner")
	}
	return m, nil
}

func ReadReport(c config.Config, id string) (*Report, error) {
	path, err := evidencePath(c, id, "report.json")
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		m, e := loadManifest(c, id)
		if e != nil {
			return nil, fmt.Errorf("report absent and ownership evidence unreadable: %w", e)
		}
		name := "unknown-interrupted"
		if scenarioPath, e := evidencePath(c, id, "scenario.resolved.json"); e == nil {
			if b, e := config.ReadBounded(scenarioPath, 1<<20); e == nil {
				var s scenario.Scenario
				if jsonutil.Decode(b, &s) == nil && s.Name != "" {
					name = s.Name
				}
			}
		}
		return &Report{APIVersion: ReportVersion, RunID: id, ScenarioName: name, TemporalMode: "guest-wall-clock-step/v1", ExecutionStatus: "INTERRUPTED", AssertionStatus: "NOT_EVALUATED", CleanupStatus: m.CleanupStatus, StartedAt: m.StartedAt, Stage: m.Stage, Steps: []StepResult{}, Assertions: []scenario.AssertionResult{}, Causes: []string{"final report absent; partial evidence does not establish a completed experiment"}}, nil
	}
	b, err := config.ReadBounded(path, 2<<20)
	if err != nil {
		return nil, err
	}
	var r Report
	if err = jsonutil.Decode(b, &r); err != nil {
		return nil, fmt.Errorf("invalid final report: %w", err)
	}
	if r.APIVersion != ReportVersion || r.RunID != id || r.Stage != "FINISHED" || !validOutcomes(r) {
		return nil, fmt.Errorf("invalid final report identity/outcome")
	}
	r.ReportSaved = true
	return &r, nil
}
func validOutcomes(r Report) bool {
	e := r.ExecutionStatus == "COMPLETED" || r.ExecutionStatus == "ERROR" || r.ExecutionStatus == "CANCELLED" || r.ExecutionStatus == "INTERRUPTED"
	a := r.AssertionStatus == "PASS" || r.AssertionStatus == "FAIL" || r.AssertionStatus == "NOT_EVALUATED"
	c := r.CleanupStatus == "COMPLETE" || r.CleanupStatus == "INCOMPLETE"
	if r.ExecutionStatus == "COMPLETED" && r.AssertionStatus == "PASS" {
		if len(r.Assertions) == 0 {
			return false
		}
		for _, v := range r.Assertions {
			if v.Status != "PASS" {
				return false
			}
		}
	}
	return e && a && c
}

func admit(c config.Config) error {
	entries, err := os.ReadDir(c.RuntimeRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "operator.lock" {
			return fmt.Errorf("runtime contains unresolved entry %q; inspect/recover before another run", entry.Name())
		}
	}
	entries, err = os.ReadDir(c.EvidenceDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !ValidRunID(entry.Name()) || !entry.IsDir() {
			return fmt.Errorf("unexpected evidence-root entry %q requires review", entry.Name())
		}
		r, err := ReadReport(c, entry.Name())
		if err != nil {
			return fmt.Errorf("prior run %s needs inspection: %w", entry.Name(), err)
		}
		if !r.ReportSaved || r.CleanupStatus != "COMPLETE" {
			return fmt.Errorf("prior run %s requires recover before admission", entry.Name())
		}
	}
	return nil
}

// Only direct, recognized, owned runtime entries are removed, with the VMM known
// dead by the caller. Unknown files are retained for inspection, never recursed.
func removeOwnedRuntime(c config.Config, m Manifest) error {
	if !ValidRunID(m.RunID) || m.RuntimeDir != filepath.Join(c.RuntimeRoot, m.RunID) || m.UID != os.Getuid() {
		return fmt.Errorf("runtime ownership mismatch; refusing cleanup")
	}
	if _, err := os.Lstat(m.RuntimeDir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := safefs.ValidatePrivateDir(m.RuntimeDir); err != nil {
		return err
	}
	allowed := map[string]bool{"rootfs.ext4": true, "rootfs.ext4.partial": true, "firecracker.json": true, "vsock.sock": true}
	entries, err := os.ReadDir(m.RuntimeDir)
	if err != nil {
		return err
	}
	var paths []string
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("unrecognized runtime entry %q retained for review", entry.Name())
		}
		path, err := safefs.OwnedChild(m.RuntimeDir, entry.Name())
		if err != nil {
			return err
		}
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if st.IsDir() || (!st.Mode().IsRegular() && st.Mode()&os.ModeSocket == 0) {
			return fmt.Errorf("unexpected runtime entry type")
		}
		paths = append(paths, path)
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return os.Remove(m.RuntimeDir)
}

type Recovery struct {
	RunID        string  `json:"run_id"`
	Apply        bool    `json:"apply"`
	ProcessAlive bool    `json:"process_alive"`
	CanClean     bool    `json:"can_clean"`
	Message      string  `json:"message"`
	Report       *Report `json:"report,omitempty"`
}

func Recover(ctx context.Context, c config.Config, id string, apply bool) (*Recovery, error) {
	if err := hostcheck.CheckPrivilege(); err != nil {
		return nil, err
	}
	m, err := loadManifest(c, id)
	if err != nil {
		return nil, err
	}
	boot, err := hostcheck.BootID()
	if err != nil {
		return nil, err
	}
	// A reboot may clear an ephemeral runtime root. Read-only inspection can
	// still use persistent ownership evidence without creating anything.
	rootErr := safefs.ValidatePrivateDir(c.RuntimeRoot)
	if rootErr != nil && (!errors.Is(rootErr, os.ErrNotExist) || m.HostBootID == boot) {
		return nil, rootErr
	}
	if rootErr != nil && apply {
		if err = safefs.EnsurePrivateDir(c.RuntimeRoot); err != nil {
			return nil, err
		}
		rootErr = nil
	}
	if rootErr == nil {
		lockErr := safefs.ValidatePrivateFile(filepath.Join(c.RuntimeRoot, "operator.lock"))
		if lockErr != nil && (!errors.Is(lockErr, os.ErrNotExist) || m.HostBootID == boot) {
			return nil, lockErr
		}
		if lockErr == nil || apply {
			lock, e := safefs.AcquireLock(c.RuntimeRoot)
			if e != nil {
				return nil, e
			}
			defer lock.Close()
		}
	}
	out := &Recovery{RunID: id, Apply: apply}
	if m.Process != nil {
		state, err := firecracker.InspectProcess(*m.Process)
		if err != nil {
			return out, err
		}
		out.ProcessAlive = state.Alive
		out.Message = state.Reason
		out.CanClean = true
	} else if m.HostBootID != boot || m.Stage == "VALIDATING" || m.Stage == "PREPARING" || m.CleanupStatus == "COMPLETE" {
		out.CanClean = true
		out.Message = "no live VMM ownership ambiguity"
	} else {
		out.Message = "launch may have occurred before process identity was persisted; manual review required"
		return out, nil
	}
	if !apply {
		return out, nil
	}
	if out.ProcessAlive {
		stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = firecracker.StopOwned(stopCtx, *m.Process)
		cancel()
		if err != nil {
			return out, err
		}
		state, err := firecracker.InspectProcess(*m.Process)
		if err != nil {
			return out, err
		}
		if state.Alive {
			return out, fmt.Errorf("VMM still alive; disk retained")
		}
		out.ProcessAlive = false
	}
	if err = removeOwnedRuntime(c, m); err != nil {
		return out, err
	}
	r, err := ReadReport(c, id)
	if err != nil {
		return out, err
	}
	if !r.ReportSaved {
		r.ExecutionStatus = "INTERRUPTED"
		r.AssertionStatus = "NOT_EVALUATED"
	}
	r.CleanupStatus = "COMPLETE"
	r.Stage = "FINISHED"
	r.FinishedAt = time.Now().UTC()
	r.ReportSaved = false
	m.CleanupStatus = "COMPLETE"
	m.Stage = "FINISHED"
	if err = saveManifest(filepath.Join(c.EvidenceDir, id, "manifest.json"), m); err != nil {
		return out, err
	}
	b, marshalErr := json.Marshal(r)
	if marshalErr != nil {
		return out, marshalErr
	}
	if len(b) > 2<<20 {
		return out, fmt.Errorf("recovered report exceeds reserved size")
	}
	if err = safefs.AtomicWrite(filepath.Join(c.EvidenceDir, id, "report.json"), append(b, '\n')); err != nil {
		return out, err
	}
	r.ReportSaved = true
	out.Report = r
	out.Message = "owned runtime resources cleaned; no workload action replayed"
	return out, nil
}

func saveManifest(path string, m Manifest) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > 64<<10 {
		return fmt.Errorf("ownership metadata exceeds reserved 64 KiB")
	}
	return safefs.AtomicWrite(path, b)
}
