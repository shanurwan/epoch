// Package scenario validates and plans portable temporal experiments.
package scenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/shanurwan/epoch/internal/jsonutil"
)

const (
	APIVersion          = "epoch/v1alpha1"
	MaxFileSize         = 1 << 20
	MaxInputSize        = 64 << 10
	MaxSteps            = 128
	MaxAssertions       = 128
	MaxTimeoutMS  int64 = 600000
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
var workloadIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var timestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

type Resources struct {
	VCPUCount int `json:"vcpu_count"`
	MemoryMiB int `json:"memory_mib"`
}

type Scenario struct {
	APIVersion    string      `json:"api_version"`
	Name          string      `json:"name"`
	Image         string      `json:"image"`
	Workload      string      `json:"workload"`
	Resources     Resources   `json:"resources"`
	TimeoutMS     int64       `json:"timeout_ms"`
	BootTimeoutMS int64       `json:"boot_timeout_ms"`
	Steps         []Step      `json:"steps"`
	Assertions    []Assertion `json:"assertions"`
}

type Step struct {
	ID               string          `json:"id"`
	Op               string          `json:"op"`
	At               string          `json:"at,omitempty"`
	Action           string          `json:"action,omitempty"`
	Service          string          `json:"service,omitempty"`
	Input            json.RawMessage `json:"input,omitempty"`
	TimeoutMS        int64           `json:"timeout_ms,omitempty"`
	DurationMS       int64           `json:"duration_ms,omitempty"`
	AllowLive        bool            `json:"allow_live,omitempty"`
	ToleranceMS      int64           `json:"tolerance_ms,omitempty"`
	ExpectedExitCode *int            `json:"expected_exit_code,omitempty"`
	present          map[string]json.RawMessage
}

type Assertion struct {
	ID      string          `json:"id"`
	Step    string          `json:"step"`
	Pointer string          `json:"pointer"`
	Op      string          `json:"op"`
	Value   json.RawMessage `json:"value,omitempty"`
}

func (s *Step) UnmarshalJSON(data []byte) error {
	type plain Step
	var v plain
	if err := jsonutil.Decode(data, &v); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return fmt.Errorf("step must be an object")
	}
	for k, raw := range fields {
		if k != "input" && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("step field %s cannot be null", k)
		}
	}
	*s = Step(v)
	s.present = fields
	return nil
}

// Decode performs strict structural and semantic validation, then applies defaults.
func Decode(data []byte) (*Scenario, error) {
	if len(data) == 0 || len(data) > MaxFileSize {
		return nil, fmt.Errorf("scenario must be 1..%d bytes", MaxFileSize)
	}
	var s Scenario
	if err := jsonutil.Decode(data, &s); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("scenario must be an object")
	}
	for k, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, fmt.Errorf("scenario field %s cannot be null", k)
		}
	}
	if raw, exists := fields["resources"]; exists {
		var resources map[string]json.RawMessage
		if err := json.Unmarshal(raw, &resources); err != nil {
			return nil, err
		}
		for _, key := range []string{"vcpu_count", "memory_mib"} {
			if value, ok := resources[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, fmt.Errorf("resources.%s is required and cannot be null", key)
			}
		}
		if s.Resources.VCPUCount == 0 || s.Resources.MemoryMiB == 0 {
			return nil, fmt.Errorf("resource values must be positive")
		}
	}
	for _, key := range []string{"timeout_ms", "boot_timeout_ms"} {
		if raw, ok := fields[key]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("0")) {
			return nil, fmt.Errorf("%s must be positive", key)
		}
	}
	var assertions []map[string]json.RawMessage
	if err := json.Unmarshal(fields["assertions"], &assertions); err != nil {
		return nil, fmt.Errorf("assertions: %w", err)
	}
	for _, a := range assertions {
		if a == nil {
			return nil, fmt.Errorf("assertion must be an object")
		}
		for _, key := range []string{"id", "step", "pointer", "op"} {
			if raw, ok := a[key]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return nil, fmt.Errorf("assertion.%s is required and cannot be null", key)
			}
		}
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Validate checks the complete plan before any runtime resources are created.
// It also normalizes timestamps to UTC and fills omitted policy defaults.
func (s *Scenario) Validate() error {
	if s == nil {
		return fmt.Errorf("scenario is nil")
	}
	if s.APIVersion != APIVersion {
		return fmt.Errorf("unsupported api_version %q", s.APIVersion)
	}
	for key, value := range map[string]string{"name": s.Name, "image": s.Image, "workload": s.Workload} {
		if !identifier.MatchString(value) {
			return fmt.Errorf("%s must be a safe identifier of 1..64 characters", key)
		}
	}
	if !workloadIdentifier.MatchString(s.Image) || !workloadIdentifier.MatchString(s.Workload) {
		return fmt.Errorf("image and workload identifiers cannot contain dots")
	}
	if s.Resources == (Resources{}) {
		s.Resources = Resources{VCPUCount: 1, MemoryMiB: 512}
	}
	if s.Resources.VCPUCount < 1 || s.Resources.VCPUCount > 2 || s.Resources.MemoryMiB < 128 || s.Resources.MemoryMiB > 2048 {
		return fmt.Errorf("resources require 1..2 vCPUs and 128..2048 MiB")
	}
	if s.TimeoutMS == 0 {
		s.TimeoutMS = 120000
	}
	if s.BootTimeoutMS == 0 {
		s.BootTimeoutMS = 30000
	}
	if s.TimeoutMS < 1 || s.TimeoutMS > MaxTimeoutMS || s.BootTimeoutMS < 1 || s.BootTimeoutMS > s.TimeoutMS {
		return fmt.Errorf("invalid scenario/boot timeout interval")
	}
	if len(s.Steps) < 1 || len(s.Steps) > MaxSteps {
		return fmt.Errorf("scenario requires 1..%d steps", MaxSteps)
	}
	if len(s.Assertions) < 1 || len(s.Assertions) > MaxAssertions {
		return fmt.Errorf("scenario requires 1..%d explicit assertions", MaxAssertions)
	}
	steps := make(map[string]Step)
	services := make(map[string]bool)
	clockSet := false
	var waits int64
	for i := range s.Steps {
		step := &s.Steps[i]
		if !identifier.MatchString(step.ID) {
			return fmt.Errorf("step %d has invalid ID", i)
		}
		if _, exists := steps[step.ID]; exists {
			return fmt.Errorf("duplicate step ID %q", step.ID)
		}
		if err := step.validate(s.TimeoutMS); err != nil {
			return fmt.Errorf("step %q: %w", step.ID, err)
		}
		switch step.Op {
		case "clock.set":
			if len(services) > 0 && !step.AllowLive {
				return fmt.Errorf("step %q requires allow_live=true while services are running", step.ID)
			}
			clockSet = true
		case "action.exec", "service.start":
			if !clockSet {
				return fmt.Errorf("step %q requires an earlier clock.set readiness barrier", step.ID)
			}
		}
		switch step.Op {
		case "service.start":
			if services[step.Service] {
				return fmt.Errorf("service %q already running", step.Service)
			}
			services[step.Service] = true
			if len(services) > 4 {
				return fmt.Errorf("at most four simultaneous services are allowed")
			}
		case "service.stop":
			if !services[step.Service] {
				return fmt.Errorf("service %q has not been started", step.Service)
			}
			delete(services, step.Service)
		case "wait":
			waits += step.DurationMS
		}
		steps[step.ID] = *step
	}
	if waits >= s.TimeoutMS {
		return fmt.Errorf("combined waits must leave time in the scenario budget")
	}
	seen := make(map[string]bool)
	for _, a := range s.Assertions {
		if !identifier.MatchString(a.ID) || seen[a.ID] {
			return fmt.Errorf("invalid or duplicate assertion ID %q", a.ID)
		}
		seen[a.ID] = true
		step, ok := steps[a.Step]
		if !ok {
			return fmt.Errorf("assertion %q references unknown step %q", a.ID, a.Step)
		}
		if step.Op == "wait" {
			return fmt.Errorf("assertion %q references wait step without guest observation", a.ID)
		}
		if err := availablePointer(step.Op, a.Pointer); err != nil {
			return fmt.Errorf("assertion %q: %w", a.ID, err)
		}
		switch a.Op {
		case "exists", "absent":
			if len(a.Value) > 0 {
				return fmt.Errorf("assertion %q operation %s does not accept value", a.ID, a.Op)
			}
		case "eq", "ne", "gt", "gte", "lt", "lte":
			if len(a.Value) == 0 {
				return fmt.Errorf("assertion %q requires value", a.ID)
			}
			var value any
			if err := jsonutil.Decode(a.Value, &value); err != nil {
				return fmt.Errorf("assertion %q value: %w", a.ID, err)
			}
			if a.Op != "eq" && a.Op != "ne" {
				if _, ok := value.(json.Number); !ok {
					return fmt.Errorf("assertion %q ordering requires a number", a.ID)
				}
			}
		default:
			return fmt.Errorf("assertion %q has unsupported operation %q", a.ID, a.Op)
		}
	}
	return nil
}

func (s *Step) validate(runTimeout int64) error {
	allowed := map[string]bool{"id": true, "op": true}
	fields := func(names ...string) {
		for _, name := range names {
			allowed[name] = true
		}
	}
	switch s.Op {
	case "clock.read":
	case "clock.set":
		fields("at", "allow_live", "tolerance_ms")
	case "action.exec":
		fields("action", "input", "timeout_ms", "expected_exit_code")
	case "service.start":
		fields("service", "input", "timeout_ms")
	case "service.stop":
		fields("service", "timeout_ms")
	case "wait":
		fields("duration_ms")
	default:
		return fmt.Errorf("unsupported operation %q", s.Op)
	}
	present := make(map[string]json.RawMessage)
	for key, value := range s.present {
		present[key] = value
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var current map[string]json.RawMessage
	if err := json.Unmarshal(raw, &current); err != nil {
		return err
	}
	for key, value := range current {
		present[key] = value
	}
	for key := range present {
		if !allowed[key] {
			return fmt.Errorf("field %q is invalid for %s", key, s.Op)
		}
	}
	for _, key := range []string{"timeout_ms", "duration_ms", "tolerance_ms"} {
		if raw, ok := present[key]; ok {
			var n int64
			if err := json.Unmarshal(raw, &n); err != nil || n <= 0 {
				return fmt.Errorf("%s must be positive", key)
			}
		}
	}
	if len(s.Input) > MaxInputSize {
		return fmt.Errorf("action input exceeds %d bytes", MaxInputSize)
	}
	if len(s.Input) > 0 {
		var v any
		if err := jsonutil.Decode(s.Input, &v); err != nil {
			return fmt.Errorf("input: %w", err)
		}
	}
	switch s.Op {
	case "clock.set":
		target, err := time.Parse(time.RFC3339Nano, s.At)
		if err != nil || !timestamp.MatchString(s.At) {
			return fmt.Errorf("at must be RFC3339 with an explicit offset")
		}
		if target.Before(time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)) || !target.Before(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)) {
			return fmt.Errorf("at must be >=1990-01-01 and <2100-01-01 UTC")
		}
		s.At = target.UTC().Format(time.RFC3339Nano)
		if s.ToleranceMS == 0 {
			s.ToleranceMS = 1000
		}
		if s.ToleranceMS > 10000 {
			return fmt.Errorf("tolerance_ms exceeds 10000")
		}
	case "action.exec":
		if !workloadIdentifier.MatchString(s.Action) {
			return fmt.Errorf("action must be a valid identifier")
		}
		if s.ExpectedExitCode != nil && (*s.ExpectedExitCode < 0 || *s.ExpectedExitCode > 255) {
			return fmt.Errorf("expected_exit_code must be 0..255")
		}
	case "service.start", "service.stop":
		if !workloadIdentifier.MatchString(s.Service) {
			return fmt.Errorf("service must be a valid identifier")
		}
	case "wait":
		if s.DurationMS <= 0 || s.DurationMS >= runTimeout {
			return fmt.Errorf("duration_ms must be positive and less than the run timeout")
		}
	}
	if allowed["timeout_ms"] {
		if s.TimeoutMS == 0 {
			s.TimeoutMS = 5000
			if s.TimeoutMS > runTimeout {
				s.TimeoutMS = runTimeout
			}
		}
		if s.TimeoutMS < 1 || s.TimeoutMS > runTimeout {
			return fmt.Errorf("timeout_ms must fit within the run timeout")
		}
	}
	return nil
}
