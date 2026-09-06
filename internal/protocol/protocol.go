// Package protocol contains the bounded, versioned guest wire contract. It has
// no dependency on guest clock-setting code.
package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	Version                = "epoch-guest/v1"
	WorkloadVersion        = "epoch-workload/v1"
	MaxFrame               = 1 << 20
	MaxInput               = 64 << 10
	MaxOutput              = 64 << 10
	GuestCID        uint32 = 3
	GuestPort       uint32 = 7000
)

type Request struct {
	Version string          `json:"version"`
	ID      string          `json:"id"`
	Op      string          `json:"op"`
	RunID   string          `json:"run_id"`
	Nonce   string          `json:"nonce"`
	Payload json.RawMessage `json:"payload"`
}
type Response struct {
	Version string          `json:"version"`
	ID      string          `json:"id"`
	Op      string          `json:"op"`
	RunID   string          `json:"run_id"`
	Nonce   string          `json:"nonce"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}
type Error struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

type HelloResult struct {
	AgentVersion    string `json:"agent_version"`
	AgentSHA256     string `json:"agent_sha256"`
	ProtocolVersion string `json:"protocol_version"`
	RunID           string `json:"run_id"`
	Nonce           string `json:"nonce"`
	CID             uint32 `json:"cid"`
	WorkloadID      string `json:"workload_id"`
	WorkloadVersion string `json:"workload_version"`
	WorkloadSHA256  string `json:"workload_sha256"`
	Ready           bool   `json:"ready"`
}
type ClockObservation struct {
	Realtime    time.Time `json:"realtime"`
	MonotonicNS int64     `json:"monotonic_ns"`
}
type ClockSetRequest struct {
	At          string `json:"at"`
	ToleranceMS int64  `json:"tolerance_ms"`
	AllowLive   bool   `json:"allow_live"`
}
type ClockSetResult struct {
	Target        time.Time        `json:"target"`
	Before        ClockObservation `json:"before"`
	After         ClockObservation `json:"after"`
	ElapsedNS     int64            `json:"elapsed_ns"`
	ToleranceMS   int64            `json:"tolerance_ms"`
	ReadbackValid bool             `json:"readback_valid"`
}
type ActionRequest struct {
	Action    string          `json:"action"`
	Input     json.RawMessage `json:"input,omitempty"`
	TimeoutMS int64           `json:"timeout_ms"`
}
type ActionResult struct {
	ExitCode        int              `json:"exit_code"`
	StdoutJSON      json.RawMessage  `json:"stdout_json"`
	Stderr          string           `json:"stderr"`
	StdoutTruncated bool             `json:"stdout_truncated"`
	StderrTruncated bool             `json:"stderr_truncated"`
	Before          ClockObservation `json:"before"`
	After           ClockObservation `json:"after"`
}
type ServiceRequest struct {
	Service   string          `json:"service"`
	TimeoutMS int64           `json:"timeout_ms"`
	Input     json.RawMessage `json:"input,omitempty"`
}
type ServiceResult struct {
	Service string           `json:"service"`
	Running bool             `json:"running"`
	Before  ClockObservation `json:"before"`
	After   ClockObservation `json:"after"`
	Stdout  string           `json:"stdout,omitempty"`
	Stderr  string           `json:"stderr,omitempty"`
}
type Command struct {
	Argv []string `json:"argv"`
}
type Service struct {
	Argv            []string `json:"argv"`
	ReadinessAction string   `json:"readiness_action"`
}
type WorkloadManifest struct {
	APIVersion  string             `json:"api_version"`
	ID          string             `json:"id"`
	Version     string             `json:"version"`
	UID         uint32             `json:"uid"`
	GID         uint32             `json:"gid"`
	WorkingDir  string             `json:"working_dir"`
	Environment map[string]string  `json:"environment"`
	Actions     map[string]Command `json:"actions"`
	Services    map[string]Service `json:"services"`
}

var safeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var safeEnv = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func ValidID(s string) bool { return safeID.MatchString(s) }
func (m WorkloadManifest) Validate() error {
	if m.APIVersion != WorkloadVersion || !ValidID(m.ID) || m.Version == "" || m.UID == 0 || m.GID == 0 {
		return errors.New("invalid workload identity/version or root workload identity")
	}
	if !path.IsAbs(m.WorkingDir) || path.Clean(m.WorkingDir) != m.WorkingDir {
		return errors.New("workload working_dir must be a clean absolute guest path")
	}
	if len(m.Actions) == 0 || len(m.Actions) > 128 || len(m.Services) > 4 {
		return errors.New("invalid workload action/service count")
	}
	check := func(id string, argv []string) error {
		if !ValidID(id) || len(argv) == 0 || len(argv) > 64 || !path.IsAbs(argv[0]) || path.Clean(argv[0]) != argv[0] {
			return fmt.Errorf("invalid command %q", id)
		}
		switch path.Base(argv[0]) {
		case "sh", "bash", "dash", "zsh", "sudo", "su":
			return fmt.Errorf("shell/privilege launcher forbidden: %s", argv[0])
		}
		for _, arg := range argv {
			if strings.ContainsRune(arg, 0) || len(arg) > 4096 {
				return errors.New("invalid command argument")
			}
		}
		return nil
	}
	for id, c := range m.Actions {
		if err := check(id, c.Argv); err != nil {
			return err
		}
	}
	for id, s := range m.Services {
		if err := check(id, s.Argv); err != nil {
			return err
		}
		if _, ok := m.Actions[s.ReadinessAction]; !ok {
			return fmt.Errorf("service %s has no declared readiness action", id)
		}
	}
	if len(m.Environment) > 64 {
		return errors.New("too many environment entries")
	}
	for k, v := range m.Environment {
		if !safeEnv.MatchString(k) || strings.ContainsRune(v, 0) || len(v) > 4096 || strings.HasPrefix(k, "LD_") || strings.HasPrefix(k, "DYLD_") {
			return fmt.Errorf("invalid environment key/value %q", k)
		}
	}
	return nil
}

// ReadLine preserves bytes buffered beyond a frame, including after CONNECT.
func ReadLine(r *bufio.Reader, limit int) ([]byte, error) {
	var out []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(out)+len(part) > limit {
			return nil, errors.New("protocol frame quota exceeded")
		}
		out = append(out, part...)
		if err == nil {
			return out[:len(out)-1], nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(out) > 0 {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
}
func WriteFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b)+1 > MaxFrame {
		return errors.New("protocol frame quota exceeded")
	}
	b = append(b, '\n')
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
