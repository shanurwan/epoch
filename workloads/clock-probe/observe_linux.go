//go:build linux

package main

import (
	"golang.org/x/sys/unix"
	"time"
)

func observe() (time.Time, int64, error) {
	var rt, mono unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_REALTIME, &rt); err != nil {
		return time.Time{}, 0, err
	}
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &mono); err != nil {
		return time.Time{}, 0, err
	}
	return time.Unix(rt.Sec, rt.Nsec).UTC(), mono.Nano(), nil
}
