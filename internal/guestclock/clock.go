// Package guestclock is guest-only. Host packages must never import it.
package guestclock

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shanurwan/epoch/internal/protocol"
)

type Identity struct {
	RunID, Nonce string
	CID          uint32
}

func ParseIdentity(cmdline string) (Identity, error) {
	values := map[string]string{}
	for _, part := range strings.Fields(cmdline) {
		k, v, ok := strings.Cut(part, "=")
		if !strings.HasPrefix(k, "epoch.") {
			continue
		}
		if !ok {
			return Identity{}, errors.New("malformed Epoch boot marker")
		}
		if _, exists := values[k]; exists {
			return Identity{}, errors.New("duplicate Epoch boot marker")
		}
		values[k] = v
	}
	run, nonce := values["epoch.run"], values["epoch.nonce"]
	cid, err := strconv.ParseUint(values["epoch.cid"], 10, 32)
	decoded, hexErr := hex.DecodeString(nonce)
	if err != nil || cid != uint64(protocol.GuestCID) || !protocol.ValidID(run) || hexErr != nil || len(decoded) != 32 {
		return Identity{}, errors.New("refusing clock control without explicit valid Epoch guest boot identity")
	}
	return Identity{RunID: run, Nonce: nonce, CID: uint32(cid)}, nil
}
func (i Identity) Verify(observedCID uint32) error {
	parsed, err := ParseIdentity(fmt.Sprintf("epoch.run=%s epoch.nonce=%s epoch.cid=%d", i.RunID, i.Nonce, i.CID))
	if err != nil {
		return err
	}
	if observedCID <= 2 || observedCID != parsed.CID {
		return errors.New("refusing clock control: actual vsock CID does not match non-host guest identity")
	}
	return nil
}

// Boundary permits unit tests to supply a fake. Tests must never use NewSystem.
type Boundary interface {
	Sample() (protocol.ClockObservation, error)
	Set(time.Time) error
}
type Controller struct{ boundary Boundary }

func New(identity Identity, observedCID uint32, b Boundary) (*Controller, error) {
	if err := identity.Verify(observedCID); err != nil {
		return nil, err
	}
	if b == nil {
		return nil, errors.New("missing guest clock boundary")
	}
	return &Controller{boundary: b}, nil
}
func (c *Controller) Read() (protocol.ClockObservation, error) { return c.boundary.Sample() }
func (c *Controller) Set(req protocol.ClockSetRequest) (protocol.ClockSetResult, error) {
	var out protocol.ClockSetResult
	target, err := time.Parse(time.RFC3339Nano, req.At)
	if err != nil {
		return out, fmt.Errorf("invalid absolute time: %w", err)
	}
	target = target.UTC()
	if target.Before(time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)) || !target.Before(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)) {
		return out, errors.New("clock target outside supported interval")
	}
	if req.ToleranceMS == 0 {
		req.ToleranceMS = 1000
	}
	if req.ToleranceMS < 1 || req.ToleranceMS > 10000 {
		return out, errors.New("clock tolerance outside 1..10000 ms")
	}
	out.Target = target
	out.ToleranceMS = req.ToleranceMS
	if out.Before, err = c.boundary.Sample(); err != nil {
		return out, err
	}
	if err = c.boundary.Set(target); err != nil {
		return out, err
	}
	if out.After, err = c.boundary.Sample(); err != nil {
		return out, err
	}
	out.ElapsedNS = out.After.MonotonicNS - out.Before.MonotonicNS
	tolerance := time.Duration(req.ToleranceMS) * time.Millisecond
	delta := out.After.Realtime.Sub(target)
	out.ReadbackValid = out.ElapsedNS >= 0 && delta >= -tolerance && delta <= time.Duration(out.ElapsedNS)+tolerance
	return out, nil
}
