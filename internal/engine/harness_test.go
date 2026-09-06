package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shanurwan/epoch/internal/protocol"
	"github.com/shanurwan/epoch/internal/scenario"
)

func syntheticHardwareReport(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "scenarios", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := scenario.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	wall := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mono int64 = 1000000000
	sample := func() protocol.ClockObservation {
		wall = wall.Add(time.Millisecond)
		mono += 1000000
		return protocol.ClockObservation{Realtime: wall, MonotonicNS: mono}
	}
	r := Report{APIVersion: ReportVersion, RunID: strings.Repeat("a", 32), ScenarioName: s.Name, TemporalMode: "guest-wall-clock-step/v1", ExecutionStatus: "COMPLETED", AssertionStatus: "PASS", CleanupStatus: "COMPLETE", StartedAt: wall, FinishedAt: wall.Add(time.Second), ElapsedNS: int64(time.Second), Stage: "FINISHED", Steps: []StepResult{}, Assertions: []scenario.AssertionResult{}, Causes: []string{}}
	for _, step := range s.Steps {
		var result any
		switch step.Op {
		case "clock.set":
			before := sample()
			target, err := time.Parse(time.RFC3339Nano, step.At)
			if err != nil {
				t.Fatal(err)
			}
			wall = target
			after := sample()
			result = protocol.ClockSetResult{Target: target, Before: before, After: after, ElapsedNS: after.MonotonicNS - before.MonotonicNS, ToleranceMS: step.ToleranceMS, ReadbackValid: true}
		case "clock.read":
			result = sample()
		case "action.exec":
			before := sample()
			after := sample()
			exit := 0
			if step.ExpectedExitCode != nil {
				exit = *step.ExpectedExitCode
			}
			result = protocol.ActionResult{ExitCode: exit, StdoutJSON: json.RawMessage(`{"fixture":true}`), Before: before, After: after}
		case "service.start", "service.stop":
			result = protocol.ServiceResult{Service: step.Service, Running: step.Op == "service.start", Before: sample(), After: sample()}
		default:
			t.Fatalf("unsupported synthetic harness operation %s", step.Op)
		}
		b, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		r.Steps = append(r.Steps, StepResult{ID: step.ID, Op: step.Op, ElapsedNS: 1000000, Result: b})
		if step.Op == "action.exec" {
			r.Assertions = append(r.Assertions, scenario.AssertionResult{ID: "auto:" + step.ID + ":exit_code", Step: step.ID, Status: "PASS"})
		}
	}
	for _, a := range s.Assertions {
		r.Assertions = append(r.Assertions, scenario.AssertionResult{ID: a.ID, Step: a.Step, Status: "PASS"})
	}
	if strings.HasPrefix(name, "fault-") {
		r.ExecutionStatus = "ERROR"
		r.AssertionStatus = "NOT_EVALUATED"
		r.Assertions = []scenario.AssertionResult{}
		var detail protocol.ActionResult
		if err := json.Unmarshal(r.Steps[1].Result, &detail); err != nil {
			t.Fatal(err)
		}
		detail.StdoutJSON = nil
		failure := &protocol.Error{}
		switch name {
		case "fault-timeout":
			failure.Code = "execution_error"
			failure.Message = "action deadline/cancellation: context deadline exceeded"
			detail.After.Realtime = detail.Before.Realtime.Add(300 * time.Millisecond)
			detail.After.MonotonicNS = detail.Before.MonotonicNS + int64(300*time.Millisecond)
		case "fault-large-output":
			failure.Code = "output_quota"
			failure.Message = "action stdout exceeded 64 KiB"
			detail.StdoutTruncated = true
		case "fault-large-stderr":
			failure.Code = "output_quota"
			failure.Message = "action stderr exceeded 64 KiB"
			detail.StderrTruncated = true
			detail.Stderr = strings.Repeat("x", 65536)
		case "fault-malformed-json":
			failure.Code = "execution_error"
			failure.Message = "action required JSON output: JSON value: invalid character 'm'"
		default:
			t.Fatalf("unknown fault fixture %s", name)
		}
		failure.Details, err = json.Marshal(detail)
		if err != nil {
			t.Fatal(err)
		}
		r.Steps[1].Result = nil
		r.Steps[1].Error = failure
		r.Causes = []string{failure.Error()}
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(b, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func checkSyntheticHardwareReport(t *testing.T, name string, report map[string]any, accepted bool) {
	t.Helper()
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is required for hardware-harness predicate tests")
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(jq, "-e", "--arg", "scenario", name, "--slurpfile", "expected", filepath.Join("..", "..", "scenarios", name+".json"), "-f", filepath.Join("..", "..", "scripts", "check-kvm-report.jq"))
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.CombinedOutput()
	if accepted {
		if err != nil {
			t.Fatalf("valid fixture report rejected: %v: %s", err, out)
		}
		return
	}
	if err == nil {
		t.Fatalf("unrelated failure accepted: %s", out)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || (exit.ExitCode() != 1 && exit.ExitCode() != 4) {
		t.Fatalf("predicate failed to execute: %v: %s", err, out)
	}
}

func TestHardwareHarnessAcceptsPreciseFixtureReports(t *testing.T) {
	requireHarnessDateParsing(t)
	for _, name := range []string{"clock-smoke", "clock-backward", "real-sleep", "expected-nonzero", "live-service", "fault-timeout", "fault-large-output", "fault-large-stderr", "fault-malformed-json"} {
		t.Run(name, func(t *testing.T) { checkSyntheticHardwareReport(t, name, syntheticHardwareReport(t, name), true) })
	}
}

func TestHardwareHarnessRejectsUnrelatedErrors(t *testing.T) {
	requireHarnessDateParsing(t)
	for _, test := range []struct {
		name, fixture string
		change        func(map[string]any)
	}{
		{"boot failure", "fault-timeout", func(r map[string]any) { r["steps"] = []any{} }},
		{"wrong scenario", "fault-timeout", func(r map[string]any) { r["scenario_name"] = "another-experiment" }},
		{"unrelated VM exit", "fault-large-output", func(r map[string]any) {
			r["causes"] = append(r["causes"].([]any), "VMM exited during execution: exit status 1")
		}},
		{"joined VM exit", "fault-large-output", func(r map[string]any) {
			c := r["causes"].([]any)
			c[0] = c[0].(string) + "\nVMM exited during execution: exit status 1"
		}},
		{"cleanup error", "fault-malformed-json", func(r map[string]any) { r["cleanup_status"] = "INCOMPLETE" }},
		{"wrong failed step", "fault-timeout", func(r map[string]any) { r["steps"].([]any)[1].(map[string]any)["id"] = "another-step" }},
		{"wrong operation", "fault-timeout", func(r map[string]any) { r["steps"].([]any)[1].(map[string]any)["op"] = "clock.read" }},
		{"missing clock readback", "fault-timeout", func(r map[string]any) {
			r["steps"].([]any)[0].(map[string]any)["result"].(map[string]any)["readback_valid"] = false
		}},
		{"wrong clock target", "fault-timeout", func(r map[string]any) {
			r["steps"].([]any)[0].(map[string]any)["result"].(map[string]any)["target"] = "1999-01-01T12:00:00Z"
		}},
		{"host request timeout", "fault-timeout", func(r map[string]any) {
			e := harnessFailure(r)
			e["code"] = "execution"
			e["message"] = "guest outcome unknown: read unix: i/o timeout"
			delete(e, "details")
			r["causes"] = []any{e["code"].(string) + ": " + e["message"].(string)}
		}},
		{"missing error details", "fault-timeout", func(r map[string]any) { delete(harnessFailure(r), "details") }},
		{"regressed guest monotonic", "fault-malformed-json", func(r map[string]any) { harnessDetails(r)["after"].(map[string]any)["monotonic_ns"] = float64(0) }},
		{"wrong actual guest time", "fault-malformed-json", func(r map[string]any) {
			harnessDetails(r)["after"].(map[string]any)["realtime"] = "2026-01-01T00:00:00Z"
		}},
		{"stdout quota not observed", "fault-large-output", func(r map[string]any) { harnessDetails(r)["stdout_truncated"] = false }},
		{"wrong quota stream", "fault-large-output", func(r map[string]any) { harnessDetails(r)["stderr_truncated"] = true }},
		{"stderr quota not reached", "fault-large-stderr", func(r map[string]any) { harnessDetails(r)["stderr"] = "short diagnostic" }},
		{"generic execution error", "fault-malformed-json", func(r map[string]any) {
			e := harnessFailure(r)
			e["message"] = "launch workload: permission denied"
			r["causes"] = []any{e["code"].(string) + ": " + e["message"].(string)}
		}},
		{"unexpected evaluated assertion", "fault-timeout", func(r map[string]any) {
			r["assertions"] = []any{map[string]any{"id": "action-exit", "step": "probe", "status": "PASS"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := syntheticHardwareReport(t, test.fixture)
			test.change(r)
			checkSyntheticHardwareReport(t, test.fixture, r, false)
		})
	}
}

func harnessFailure(r map[string]any) map[string]any {
	return r["steps"].([]any)[1].(map[string]any)["error"].(map[string]any)
}
func harnessDetails(r map[string]any) map[string]any {
	return harnessFailure(r)["details"].(map[string]any)
}

func requireHarnessDateParsing(t *testing.T) {
	t.Helper()
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is required for hardware-harness predicate tests")
	}
	cmd := exec.Command(jq, "-e", "fromdateiso8601")
	cmd.Stdin = strings.NewReader(`"2026-01-01T00:00:00Z"`)
	out, err := cmd.CombinedOutput()
	if err != nil && strings.Contains(string(out), "not implemented") {
		t.Skip("jq date parsing is unavailable in this executable")
	}
	if err != nil {
		t.Fatalf("jq date parsing probe failed: %v: %s", err, out)
	}
}
