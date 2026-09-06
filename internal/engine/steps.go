package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shanurwan/epoch/internal/jsonutil"
	"github.com/shanurwan/epoch/internal/protocol"
	"github.com/shanurwan/epoch/internal/scenario"
)

// Guest is the request boundary shared by real transport and sequencing tests.
type Guest interface {
	Call(context.Context, string, any, any) error
	Close() error
}

const responseAllowance = 4 * time.Second

func executeStep(ctx context.Context, g Guest, s scenario.Step) (json.RawMessage, error) {
	timeout := 5 * time.Second
	if s.TimeoutMS > 0 {
		timeout = time.Duration(s.TimeoutMS) * time.Millisecond
	}
	requestTimeoutMS := timeout.Milliseconds()
	if s.Op == "action.exec" || s.Op == "service.start" || s.Op == "service.stop" {
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining < time.Millisecond {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				return nil, context.DeadlineExceeded
			}
			if remaining < timeout {
				timeout = remaining.Truncate(time.Millisecond)
				requestTimeoutMS = timeout.Milliseconds()
			}
		}
		// The workload budget excludes bounded guest termination and response time.
		// The parent still bounds the complete operation, including this allowance.
		timeout += responseAllowance
	}
	if s.Op == "wait" {
		timeout = time.Duration(s.DurationMS)*time.Millisecond + 5*time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var result any
	switch s.Op {
	case "clock.read":
		var v protocol.ClockObservation
		if err := g.Call(ctx, s.Op, nil, &v); err != nil {
			return nil, err
		}
		if err := validClock(v); err != nil {
			return nil, err
		}
		result = v
	case "clock.set":
		var v protocol.ClockSetResult
		if err := g.Call(ctx, s.Op, protocol.ClockSetRequest{At: s.At, ToleranceMS: s.ToleranceMS, AllowLive: s.AllowLive}, &v); err != nil {
			return nil, err
		}
		target, err := time.Parse(time.RFC3339Nano, s.At)
		if err != nil {
			return nil, err
		}
		elapsed := v.After.MonotonicNS - v.Before.MonotonicNS
		if err := validClock(v.Before); err != nil {
			return nil, err
		}
		if err := validClock(v.After); err != nil {
			return nil, err
		}
		tolerance := time.Duration(s.ToleranceMS) * time.Millisecond
		if !v.ReadbackValid || !v.Target.Equal(target) || v.ToleranceMS != s.ToleranceMS || elapsed < 0 || elapsed != v.ElapsedNS ||
			v.After.Realtime.Before(target.Add(-tolerance)) || v.After.Realtime.After(target.Add(time.Duration(elapsed)+tolerance)) {
			return nil, fmt.Errorf("guest clock readback failed independent host validation")
		}
		result = v
	case "action.exec":
		var v protocol.ActionResult
		if err := g.Call(ctx, s.Op, protocol.ActionRequest{Action: s.Action, Input: s.Input, TimeoutMS: requestTimeoutMS}, &v); err != nil {
			return nil, err
		}
		if v.ExitCode < 0 || v.ExitCode > 255 {
			return nil, fmt.Errorf("action did not complete with a normal exit code")
		}
		if v.StdoutTruncated || v.StderrTruncated || len(v.StdoutJSON) > protocol.MaxOutput || len(v.Stderr) > protocol.MaxOutput {
			return nil, fmt.Errorf("action output quota exceeded")
		}
		var observation any
		if len(v.StdoutJSON) == 0 {
			return nil, fmt.Errorf("action required output missing")
		}
		if err := jsonutil.Decode(v.StdoutJSON, &observation); err != nil {
			return nil, fmt.Errorf("action required output malformed: %w", err)
		}
		if err := validInterval(v.Before, v.After); err != nil {
			return nil, err
		}
		result = v
	case "service.start", "service.stop":
		var v protocol.ServiceResult
		if err := g.Call(ctx, s.Op, protocol.ServiceRequest{Service: s.Service, Input: s.Input, TimeoutMS: requestTimeoutMS}, &v); err != nil {
			return nil, err
		}
		if v.Service != s.Service || v.Running != (s.Op == "service.start") {
			return nil, fmt.Errorf("invalid service lifecycle observation")
		}
		if len(v.Stdout) > protocol.MaxOutput || len(v.Stderr) > protocol.MaxOutput {
			return nil, fmt.Errorf("service diagnostic quota exceeded")
		}
		if err := validInterval(v.Before, v.After); err != nil {
			return nil, err
		}
		result = v
	case "wait":
		var before, after protocol.ClockObservation
		if err := g.Call(ctx, "clock.read", nil, &before); err != nil {
			return nil, err
		}
		t := time.NewTimer(time.Duration(s.DurationMS) * time.Millisecond)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
		if err := g.Call(ctx, "clock.read", nil, &after); err != nil {
			return nil, err
		}
		if err := validInterval(before, after); err != nil {
			return nil, err
		}
		result = struct {
			Before     protocol.ClockObservation `json:"before"`
			After      protocol.ClockObservation `json:"after"`
			DurationMS int64                     `json:"duration_ms"`
		}{before, after, s.DurationMS}
	default:
		return nil, fmt.Errorf("unsupported planned operation")
	}
	return json.Marshal(result)
}

func validClock(c protocol.ClockObservation) error {
	if c.Realtime.IsZero() || c.MonotonicNS < 0 {
		return fmt.Errorf("missing or invalid guest clock observation")
	}
	return nil
}
func validInterval(before, after protocol.ClockObservation) error {
	if err := validClock(before); err != nil {
		return err
	}
	if err := validClock(after); err != nil {
		return err
	}
	if after.MonotonicNS < before.MonotonicNS {
		return fmt.Errorf("guest monotonic clock regressed")
	}
	return nil
}
