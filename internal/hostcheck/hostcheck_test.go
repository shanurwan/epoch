package hostcheck

import (
	"fmt"
	"testing"
	"time"
)

func TestPrivilegesFailClosed(t *testing.T) {
	base := "Uid:\t1000\t1000\t1000\t1000\nCapPrm:\t%s\nCapEff:\t0000000000000000\nCapAmb:\t0000000000000000\nCapBnd:\tffffffffffffffff\n"
	if err := parsePrivileges(fmt.Sprintf(base, "0000000000000000")); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{fmt.Sprintf(base, "0000000002000000"), "Uid: 1000 0 1000 1000\n", fmt.Sprintf(base, "wat"), ""} {
		if parsePrivileges(s) == nil {
			t.Fatalf("accepted unsafe status %q", s)
		}
	}
}

func TestCompareClockDimensions(t *testing.T) {
	a := Observation{Realtime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), MonotonicNS: 1e9, BoottimeNS: 1e9}
	for _, tc := range []struct {
		name             string
		wall, mono, boot time.Duration
		bad              bool
	}{
		{"normal", time.Second, time.Second, time.Second, false},
		{"ntp slew", time.Second + time.Millisecond, time.Second, time.Second, false},
		{"wall step", 2 * time.Second, time.Second, time.Second, true},
		{"suspend", time.Second, time.Second, 2 * time.Second, true},
		{"backwards monotonic", time.Second, -time.Second, time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := Observation{Realtime: a.Realtime.Add(tc.wall), MonotonicNS: a.MonotonicNS + int64(tc.mono), BoottimeNS: a.BoottimeNS + int64(tc.boot)}
			if (Compare(a, b, 250*time.Millisecond) != nil) != tc.bad {
				t.Fatal("unexpected comparison")
			}
		})
	}
}
