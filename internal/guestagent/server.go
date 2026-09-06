package guestagent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/shanurwan/epoch/internal/guestclock"
	"github.com/shanurwan/epoch/internal/jsonutil"
	"github.com/shanurwan/epoch/internal/protocol"
)

type Clock interface {
	Read() (protocol.ClockObservation, error)
	Set(protocol.ClockSetRequest) (protocol.ClockSetResult, error)
}
type Runner interface {
	Action(context.Context, protocol.ActionRequest) (protocol.ActionResult, error)
	Start(context.Context, protocol.ServiceRequest) (protocol.ServiceResult, error)
	Stop(context.Context, protocol.ServiceRequest) (protocol.ServiceResult, error)
	StopAll(context.Context) error
	Active() int
	Health() error
}
type Server struct {
	Identity                                  guestclock.Identity
	Manifest                                  protocol.WorkloadManifest
	ManifestSHA256, AgentVersion, AgentSHA256 string
	Clock                                     Clock
	Runner                                    Runner
	clockReady                                bool
}

func LoadManifest(filename string) (protocol.WorkloadManifest, string, error) {
	var m protocol.WorkloadManifest
	f, err := os.Open(filename)
	if err != nil {
		return m, "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return m, "", err
	}
	if !st.Mode().IsRegular() || st.Size() > protocol.MaxFrame {
		return m, "", errors.New("invalid workload manifest file")
	}
	b, err := io.ReadAll(io.LimitReader(f, protocol.MaxFrame+1))
	if err != nil {
		return m, "", err
	}
	if len(b) > protocol.MaxFrame {
		return m, "", errors.New("workload manifest quota exceeded")
	}
	if err = jsonutil.Decode(b, &m); err != nil {
		return m, "", err
	}
	if err = m.Validate(); err != nil {
		return m, "", err
	}
	sum := sha256.Sum256(b)
	return m, hex.EncodeToString(sum[:]), nil
}
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Runner.StopAll(cleanup)
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		shutdown, err := s.serveConn(ctx, conn)
		_ = conn.Close()
		if shutdown {
			return err
		}
		if err != nil {
			return err
		}
	}
}
func (s *Server) serveConn(parent context.Context, conn net.Conn) (bool, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	type incoming struct {
		frame []byte
		err   error
	}
	frames := make(chan incoming, 1)
	go func() {
		r := bufio.NewReader(conn)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
			b, err := protocol.ReadLine(r, protocol.MaxFrame)
			select {
			case frames <- incoming{b, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()
	seen := map[string]bool{}
	for {
		var in incoming
		select {
		case in = <-frames:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		if in.err != nil {
			return false, in.err
		}
		var req protocol.Request
		if err := jsonutil.Decode(in.frame, &req); err != nil {
			return false, fmt.Errorf("invalid guest request: %w", err)
		}
		if req.Version != protocol.Version || req.RunID != s.Identity.RunID || req.Nonce != s.Identity.Nonce || !protocol.ValidID(req.ID) {
			return false, errors.New("guest request version/identity mismatch")
		}
		if seen[req.ID] || len(seen) >= 1024 {
			return false, errors.New("duplicate request identity or session request quota")
		}
		seen[req.ID] = true
		result, err := s.handle(ctx, req)
		resp := protocol.Response{Version: protocol.Version, ID: req.ID, Op: req.Op, RunID: req.RunID, Nonce: req.Nonce}
		if err != nil {
			var typed *protocol.Error
			if errors.As(err, &typed) {
				resp.Error = typed
			} else {
				resp.Error = &protocol.Error{Code: "execution_error", Message: err.Error()}
			}
			if result != nil {
				details, marshalErr := json.Marshal(result)
				if marshalErr != nil {
					return false, marshalErr
				}
				resp.Error.Details = details
			}
		} else {
			resp.Result, err = json.Marshal(result)
			if err != nil {
				return false, err
			}
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err = protocol.WriteFrame(conn, resp); err != nil {
			return false, err
		}
		if req.Op == "shutdown" {
			return true, nil
		}
	}
}
func requestTimeout(ms int64) (time.Duration, error) {
	if ms == 0 {
		ms = 5000
	}
	if ms < 1 || ms > 600000 {
		return 0, errors.New("request timeout outside 1..600000 ms")
	}
	return time.Duration(ms) * time.Millisecond, nil
}
func (s *Server) handle(ctx context.Context, req protocol.Request) (any, error) {
	if req.Op != "shutdown" && req.Op != "service.stop" {
		if err := s.Runner.Health(); err != nil {
			return nil, &protocol.Error{Code: "service_exited", Message: err.Error()}
		}
	}
	decode := func(v any) error {
		if len(req.Payload) == 0 || string(req.Payload) == "null" {
			return errors.New("missing request payload")
		}
		return jsonutil.Decode(req.Payload, v)
	}
	switch req.Op {
	case "hello":
		if err := decode(&struct{}{}); err != nil {
			return nil, err
		}
		return protocol.HelloResult{AgentVersion: s.AgentVersion, AgentSHA256: s.AgentSHA256, ProtocolVersion: protocol.Version, RunID: s.Identity.RunID, Nonce: s.Identity.Nonce, CID: s.Identity.CID, WorkloadID: s.Manifest.ID, WorkloadVersion: s.Manifest.Version, WorkloadSHA256: s.ManifestSHA256, Ready: true}, nil
	case "clock.read":
		if err := decode(&struct{}{}); err != nil {
			return nil, err
		}
		return s.Clock.Read()
	case "clock.set":
		var p protocol.ClockSetRequest
		if err := decode(&p); err != nil {
			return nil, err
		}
		if s.Runner.Active() > 0 && !p.AllowLive {
			return nil, &protocol.Error{Code: "live_clock_denied", Message: "running services require allow_live=true"}
		}
		result, err := s.Clock.Set(p)
		s.clockReady = err == nil && result.ReadbackValid
		return result, err
	case "action.exec":
		if !s.clockReady {
			return nil, &protocol.Error{Code: "clock_not_ready", Message: "successful initial clock readback required before workloads"}
		}
		var p protocol.ActionRequest
		if err := decode(&p); err != nil {
			return nil, err
		}
		if _, ok := s.Manifest.Actions[p.Action]; !ok {
			return nil, errors.New("undeclared action")
		}
		if len(p.Input) > protocol.MaxInput {
			return nil, errors.New("action input quota exceeded")
		}
		if len(p.Input) > 0 {
			var v any
			if err := jsonutil.Decode(p.Input, &v); err != nil {
				return nil, err
			}
		}
		d, err := requestTimeout(p.TimeoutMS)
		if err != nil {
			return nil, err
		}
		actCtx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return s.Runner.Action(actCtx, p)
	case "service.start", "service.stop":
		if req.Op == "service.start" && !s.clockReady {
			return nil, &protocol.Error{Code: "clock_not_ready", Message: "initial clock must be established before services"}
		}
		var p protocol.ServiceRequest
		if err := decode(&p); err != nil {
			return nil, err
		}
		if _, ok := s.Manifest.Services[p.Service]; !ok {
			return nil, errors.New("undeclared service")
		}
		if len(p.Input) > protocol.MaxInput {
			return nil, errors.New("service input quota exceeded")
		}
		if req.Op == "service.stop" && len(p.Input) > 0 {
			return nil, errors.New("service.stop does not accept input")
		}
		if len(p.Input) > 0 {
			var v any
			if err := jsonutil.Decode(p.Input, &v); err != nil {
				return nil, err
			}
		}
		d, err := requestTimeout(p.TimeoutMS)
		if err != nil {
			return nil, err
		}
		svcCtx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		if req.Op == "service.start" {
			return s.Runner.Start(svcCtx, p)
		}
		return s.Runner.Stop(svcCtx, p)
	case "shutdown":
		if err := decode(&struct{}{}); err != nil {
			return nil, err
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := errors.Join(s.Runner.Health(), s.Runner.StopAll(cleanup)); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	default:
		return nil, &protocol.Error{Code: "unsupported_operation", Message: "unsupported guest operation"}
	}
}
