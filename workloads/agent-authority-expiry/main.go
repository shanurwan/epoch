// agent-authority-expiry is a deterministic reference workload for Epoch.
// It is application code, not part of the Epoch scenario engine.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/shanurwan/epoch/internal/jsonutil"
)

const (
	maxInputBytes = 64 << 10
	pendingFile   = "pending-request.json"
	workerFile    = "worker-state.json"
)

type authorization string

const (
	authorized               authorization = "AUTHORIZED"
	denyNotYetValid          authorization = "DENY_NOT_YET_VALID"
	denyExpired              authorization = "DENY_EXPIRED"
	denyCapabilityMismatch   authorization = "DENY_CAPABILITY_MISMATCH"
	denyResourceMismatch     authorization = "DENY_RESOURCE_MISMATCH"
	denyMalformedAuthority   authorization = "DENY_MALFORMED_AUTHORITY"
	denyPrincipalMismatch    authorization = "DENY_PRINCIPAL_MISMATCH"
	denyMalformedRequest     authorization = "DENY_MALFORMED_REQUEST"
	denyUnsupportedOperation authorization = "DENY_UNSUPPORTED_OPERATION"
)

type authority struct {
	Principal  string `json:"principal"`
	Capability string `json:"capability"`
	Resource   string `json:"resource"`
	NotBefore  string `json:"not_before"`
	ExpiresAt  string `json:"expires_at"`
}

type operationRequest struct {
	RequestID string    `json:"request_id"`
	AgentID   string    `json:"agent_id"`
	Operation string    `json:"operation"`
	Resource  string    `json:"resource"`
	Authority authority `json:"authority"`
}

type pendingRequest struct {
	Request              operationRequest `json:"request"`
	InitialAuthorization authorization    `json:"initial_authorization"`
	InitialDecisionTime  time.Time        `json:"initial_decision_time"`
}

type workerState struct {
	Resource      string `json:"resource"`
	Status        string `json:"status"`
	RestartCount  int    `json:"restart_count"`
	LastRequestID string `json:"last_request_id,omitempty"`
}

type outcome struct {
	RequestID              string        `json:"request_id"`
	Operation              string        `json:"operation"`
	Resource               string        `json:"resource"`
	InitialAuthorization   authorization `json:"initial_authorization"`
	InitialDecisionTime    time.Time     `json:"initial_decision_time"`
	ExecutionAuthorization authorization `json:"execution_authorization,omitempty"`
	ExecutionDecisionTime  *time.Time    `json:"execution_decision_time,omitempty"`
	RequestAccepted        bool          `json:"request_accepted"`
	SideEffectPerformed    bool          `json:"side_effect_performed"`
	WorkerState            *workerState  `json:"worker_state,omitempty"`
}

type stateRequest struct {
	Resource string `json:"resource"`
}

type application struct {
	now      func() time.Time
	stateDir string
}

var safeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var explicitTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func main() {
	app := application{now: time.Now, stateDir: "."}
	if err := app.run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
}

func (a application) run(args []string, input io.Reader, output io.Writer) error {
	if len(args) != 1 {
		return errors.New("one declared authority workload command is required")
	}
	var result any
	var err error
	switch args[0] {
	case "attempt":
		var request operationRequest
		if err = decodeInput(input, &request); err == nil {
			result, err = a.attempt(request)
		}
	case "request":
		var request operationRequest
		if err = decodeInput(input, &request); err == nil {
			result, err = a.request(request)
		}
	case "execute-pending":
		if err = requireEmptyInput(input); err == nil {
			result, err = a.executePending()
		}
	case "worker-state":
		var request stateRequest
		if err = decodeInput(input, &request); err == nil {
			result, err = a.readWorkerState(request.Resource)
		}
	default:
		return errors.New("unknown authority workload command")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func decodeInput(r io.Reader, dst any) error {
	b, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return err
	}
	if len(b) > maxInputBytes {
		return errors.New("workload input quota exceeded")
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return errors.New("workload input is required")
	}
	return jsonutil.Decode(b, dst)
}

func requireEmptyInput(r io.Reader) error {
	b, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return err
	}
	if len(b) > maxInputBytes {
		return errors.New("workload input quota exceeded")
	}
	if strings.TrimSpace(string(b)) != "" {
		return errors.New("execute-pending does not accept input")
	}
	return nil
}

func authorize(request operationRequest, now time.Time) authorization {
	if !safeID.MatchString(request.RequestID) || !safeID.MatchString(request.AgentID) || !safeID.MatchString(request.Resource) {
		return denyMalformedRequest
	}
	if request.Operation != "worker.restart" {
		return denyUnsupportedOperation
	}
	notBefore, expiresAt, ok := parseAuthority(request.Authority)
	if !ok {
		return denyMalformedAuthority
	}
	if request.Authority.Principal != "agent://"+request.AgentID {
		return denyPrincipalMismatch
	}
	if request.Authority.Capability != request.Operation {
		return denyCapabilityMismatch
	}
	if request.Authority.Resource != request.Resource {
		return denyResourceMismatch
	}
	now = now.UTC()
	if now.Before(notBefore) {
		return denyNotYetValid
	}
	if !now.Before(expiresAt) {
		return denyExpired
	}
	return authorized
}

func parseAuthority(value authority) (time.Time, time.Time, bool) {
	if value.Principal == "" || value.Capability == "" || value.Resource == "" ||
		len(value.Principal) > 256 || len(value.Capability) > 128 || len(value.Resource) > 128 ||
		!explicitTimestamp.MatchString(value.NotBefore) || !explicitTimestamp.MatchString(value.ExpiresAt) {
		return time.Time{}, time.Time{}, false
	}
	notBefore, err := time.Parse(time.RFC3339Nano, value.NotBefore)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, value.ExpiresAt)
	if err != nil || !notBefore.Before(expiresAt) {
		return time.Time{}, time.Time{}, false
	}
	return notBefore.UTC(), expiresAt.UTC(), true
}

func (a application) attempt(request operationRequest) (outcome, error) {
	initialTime := a.now().UTC()
	initial := authorize(request, initialTime)
	result := outcome{
		RequestID:            request.RequestID,
		Operation:            request.Operation,
		Resource:             request.Resource,
		InitialAuthorization: initial,
		InitialDecisionTime:  initialTime,
		RequestAccepted:      initial == authorized,
	}
	if initial != authorized {
		return result, nil
	}
	executionTime := a.now().UTC()
	result.ExecutionDecisionTime = &executionTime
	result.ExecutionAuthorization = authorize(request, executionTime)
	if result.ExecutionAuthorization != authorized {
		return result, nil
	}
	state, err := a.restartWorker(request)
	if err != nil {
		return outcome{}, err
	}
	result.SideEffectPerformed = true
	result.WorkerState = &state
	return result, nil
}

func (a application) request(request operationRequest) (outcome, error) {
	decisionTime := a.now().UTC()
	decision := authorize(request, decisionTime)
	result := outcome{
		RequestID:            request.RequestID,
		Operation:            request.Operation,
		Resource:             request.Resource,
		InitialAuthorization: decision,
		InitialDecisionTime:  decisionTime,
		RequestAccepted:      decision == authorized,
	}
	if decision != authorized {
		return result, nil
	}
	path := filepath.Join(a.stateDir, pendingFile)
	if _, err := os.Lstat(path); err == nil {
		return outcome{}, errors.New("a pending request already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return outcome{}, err
	}
	err := writeJSON(path, pendingRequest{Request: request, InitialAuthorization: decision, InitialDecisionTime: decisionTime})
	return result, err
}

func (a application) executePending() (outcome, error) {
	path := filepath.Join(a.stateDir, pendingFile)
	var pending pendingRequest
	b, err := os.ReadFile(path)
	if err != nil {
		return outcome{}, err
	}
	if err = jsonutil.Decode(b, &pending); err != nil {
		return outcome{}, fmt.Errorf("invalid pending request: %w", err)
	}
	if pending.InitialAuthorization != authorized {
		return outcome{}, errors.New("pending request was not initially authorized")
	}
	// Consume before execution. A crash can lose this safe simulation request, but
	// cannot cause Epoch recovery to replay it or this workload to execute it twice.
	if err = os.Remove(path); err != nil {
		return outcome{}, err
	}
	if err = syncDirectory(a.stateDir); err != nil {
		return outcome{}, err
	}
	executionTime := a.now().UTC()
	execution := authorize(pending.Request, executionTime)
	result := outcome{
		RequestID:              pending.Request.RequestID,
		Operation:              pending.Request.Operation,
		Resource:               pending.Request.Resource,
		InitialAuthorization:   pending.InitialAuthorization,
		InitialDecisionTime:    pending.InitialDecisionTime,
		ExecutionAuthorization: execution,
		ExecutionDecisionTime:  &executionTime,
		RequestAccepted:        true,
	}
	if execution != authorized {
		return result, nil
	}
	state, err := a.restartWorker(pending.Request)
	if err != nil {
		return outcome{}, err
	}
	result.SideEffectPerformed = true
	result.WorkerState = &state
	return result, nil
}

func (a application) restartWorker(request operationRequest) (workerState, error) {
	state, err := a.readWorkerState(request.Resource)
	if err != nil {
		return workerState{}, err
	}
	state.RestartCount++
	state.LastRequestID = request.RequestID
	if err = writeJSON(filepath.Join(a.stateDir, workerFile), state); err != nil {
		return workerState{}, err
	}
	return state, nil
}

func (a application) readWorkerState(resource string) (workerState, error) {
	if !safeID.MatchString(resource) {
		return workerState{}, errors.New("worker resource must be a safe identifier")
	}
	path := filepath.Join(a.stateDir, workerFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return workerState{Resource: resource, Status: "running"}, nil
	}
	if err != nil {
		return workerState{}, err
	}
	var state workerState
	if err = jsonutil.Decode(b, &state); err != nil {
		return workerState{}, fmt.Errorf("invalid worker state: %w", err)
	}
	if state.Resource != resource || state.Status != "running" || state.RestartCount < 0 || !safeID.MatchString(state.Resource) {
		return workerState{}, errors.New("worker state identity mismatch")
	}
	return state, nil
}

func writeJSON(path string, value any) (err error) {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
