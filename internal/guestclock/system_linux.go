//go:build linux

package guestclock

import (
	"errors"
	"os"
	"time"

	"github.com/mdlayher/vsock"
	"github.com/shanurwan/epoch/internal/protocol"
	"golang.org/x/sys/unix"
)

type systemBoundary struct{}

func NewSystem() (Identity, *Controller, error) {
	if os.Getpid() == 1 {
		return Identity{}, nil, errors.New("epoch-agent requires a real guest init; refusing PID 1")
	}
	if os.Geteuid() != 0 {
		return Identity{}, nil, errors.New("guest agent must run as guest root")
	}
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return Identity{}, nil, err
	}
	identity, err := ParseIdentity(string(cmdline))
	if err != nil {
		return identity, nil, err
	}
	cid, err := vsock.ContextID()
	if err != nil {
		return identity, nil, err
	}
	controller, err := New(identity, cid, systemBoundary{})
	return identity, controller, err
}
func (systemBoundary) Sample() (protocol.ClockObservation, error) {
	var realtime, monotonic unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_REALTIME, &realtime); err != nil {
		return protocol.ClockObservation{}, err
	}
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &monotonic); err != nil {
		return protocol.ClockObservation{}, err
	}
	return protocol.ClockObservation{Realtime: time.Unix(realtime.Sec, realtime.Nsec).UTC(), MonotonicNS: monotonic.Nano()}, nil
}
func (systemBoundary) Set(at time.Time) error {
	ts := unix.NsecToTimespec(at.UnixNano())
	return unix.ClockSettime(unix.CLOCK_REALTIME, &ts)
}
