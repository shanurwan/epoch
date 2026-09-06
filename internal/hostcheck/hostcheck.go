// Package hostcheck observes host state; it never changes clocks or host policy.
package hostcheck

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var ErrUnsupported = errors.New("host runtime requires Linux x86_64")

type Observation struct {
	Realtime    time.Time `json:"realtime_utc"`
	MonotonicNS int64     `json:"monotonic_ns"`
	BoottimeNS  int64     `json:"boottime_ns"`
}

type Report struct {
	OS               string       `json:"os"`
	Architecture     string       `json:"architecture"`
	UID              int          `json:"uid"`
	Kernel           string       `json:"kernel,omitempty"`
	HostBootID       string       `json:"host_boot_id,omitempty"`
	SELinuxEnforcing bool         `json:"selinux_enforcing"`
	CgroupV2         bool         `json:"cgroup_v2"`
	KVMAccessible    bool         `json:"kvm_accessible"`
	ProbeRequested   bool         `json:"probe_requested"`
	ProbePassed      bool         `json:"probe_passed"`
	FirecrackerPath  string       `json:"firecracker_path"`
	Observation      *Observation `json:"observation,omitempty"`
	Problems         []string     `json:"problems"`
	Notes            []string     `json:"notes"`
}

// Compare detects sampled wall-clock changes and suspend, not their cause.
func Compare(a, b Observation, threshold time.Duration) error {
	if threshold <= 0 {
		return errors.New("host clock threshold must be positive")
	}
	mono := b.MonotonicNS - a.MonotonicNS
	boot := b.BoottimeNS - a.BoottimeNS
	if mono < 0 || boot < 0 {
		return errors.New("host monotonic clocks regressed")
	}
	wall := b.Realtime.Sub(a.Realtime)
	drift := wall - time.Duration(mono)
	if drift > threshold || drift < -threshold {
		return fmt.Errorf("environmental clock discontinuity: realtime-minus-monotonic delta %s exceeds %s", drift, threshold)
	}
	suspend := time.Duration(boot - mono)
	if suspend > threshold || suspend < -threshold {
		return fmt.Errorf("environmental suspend/clock anomaly: boottime-minus-monotonic delta %s exceeds %s", suspend, threshold)
	}
	return nil
}

func parsePrivileges(status string) error {
	seen := map[string]bool{}
	s := bufio.NewScanner(strings.NewReader(status))
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "Uid:":
			if seen[fields[0]] || len(fields) != 5 {
				return errors.New("invalid UID status")
			}
			seen[fields[0]] = true
			for _, v := range fields[1:] {
				n, err := strconv.ParseUint(v, 10, 32)
				if err != nil || n == 0 {
					return errors.New("host runtime refuses root UID in any credential set")
				}
			}
		case "CapPrm:", "CapEff:", "CapAmb:":
			if seen[fields[0]] || len(fields) != 2 {
				return errors.New("invalid capability status")
			}
			seen[fields[0]] = true
			n, err := strconv.ParseUint(fields[1], 16, 64)
			if err != nil {
				return fmt.Errorf("parse %s: %w", fields[0], err)
			}
			if n&(1<<25) != 0 {
				return fmt.Errorf("host runtime refuses CAP_SYS_TIME in %s", fields[0])
			}
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	if len(seen) != 4 {
		return errors.New("incomplete privilege inspection")
	}
	return nil
}

func Doctor(ctx context.Context, firecrackerPath string, probe bool) (Report, error) {
	return doctor(ctx, firecrackerPath, probe)
}
