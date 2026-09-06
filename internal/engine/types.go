// Package engine owns run sequencing, outcomes, cancellation and recovery.
package engine

import (
	"encoding/json"
	"time"

	"github.com/shanurwan/epoch/internal/artifact"
	"github.com/shanurwan/epoch/internal/firecracker"
	"github.com/shanurwan/epoch/internal/protocol"
	"github.com/shanurwan/epoch/internal/scenario"
)

const ReportVersion = "epoch-result/v1"

type Manifest struct {
	APIVersion             string                       `json:"api_version"`
	RunID                  string                       `json:"run_id"`
	HostBootID             string                       `json:"host_boot_id"`
	UID                    int                          `json:"uid"`
	Stage                  string                       `json:"stage"`
	StartedAt              time.Time                    `json:"started_at"`
	RuntimeDir             string                       `json:"runtime_dir"`
	CreatedPaths           []string                     `json:"created_paths"`
	ConfigSHA256           string                       `json:"config_sha256"`
	ScenarioSHA256         string                       `json:"scenario_sha256"`
	ArtifactManifestSHA256 string                       `json:"artifact_manifest_sha256"`
	Image                  artifact.Manifest            `json:"image"`
	Executable             firecracker.Identity         `json:"executable"`
	Process                *firecracker.ProcessIdentity `json:"process,omitempty"`
	CleanupStatus          string                       `json:"cleanup_status"`
}
type StepResult struct {
	ID        string          `json:"id"`
	Op        string          `json:"op"`
	ElapsedNS int64           `json:"elapsed_ns"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *protocol.Error `json:"error,omitempty"`
}
type Report struct {
	APIVersion      string                     `json:"api_version"`
	RunID           string                     `json:"run_id"`
	ScenarioName    string                     `json:"scenario_name"`
	TemporalMode    string                     `json:"temporal_mode"`
	ExecutionStatus string                     `json:"execution_status"`
	AssertionStatus string                     `json:"assertion_status"`
	CleanupStatus   string                     `json:"cleanup_status"`
	StartedAt       time.Time                  `json:"started_at"`
	FinishedAt      time.Time                  `json:"finished_at"`
	ElapsedNS       int64                      `json:"elapsed_ns"`
	Stage           string                     `json:"stage"`
	Steps           []StepResult               `json:"steps"`
	Assertions      []scenario.AssertionResult `json:"assertions"`
	Causes          []string                   `json:"causes"`
	ReportSaved     bool                       `json:"-"`
}

func (r *Report) ExitCode() int {
	if r.CleanupStatus != "COMPLETE" {
		return 4
	}
	if r.ExecutionStatus == "CANCELLED" {
		return 130
	}
	if r.ExecutionStatus != "COMPLETED" {
		return 3
	}
	if r.AssertionStatus == "FAIL" {
		return 1
	}
	if r.AssertionStatus != "PASS" {
		return 3
	}
	return 0
}

type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }
