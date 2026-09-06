package guestclock

import (
	"strings"
	"testing"
	"time"

	"github.com/shanurwan/epoch/internal/protocol"
)

type fakeBoundary struct {
	samples []protocol.ClockObservation
	sets    int
}

func (f *fakeBoundary) Sample() (protocol.ClockObservation, error) {
	s := f.samples[0]
	f.samples = f.samples[1:]
	return s, nil
}
func (f *fakeBoundary) Set(time.Time) error { f.sets++; return nil }
func TestGuard(t *testing.T) {
	valid := "epoch.run=run-123 epoch.nonce=" + strings.Repeat("ab", 32) + " epoch.cid=3"
	i, err := ParseIdentity(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, cid := range []uint32{0, 1, 2, 4} {
		if i.Verify(cid) == nil {
			t.Fatalf("accepted CID %d", cid)
		}
	}
	if i.Verify(3) != nil {
		t.Fatal("valid guest rejected")
	}
	for _, s := range []string{"", valid + " epoch.cid=3", strings.Replace(valid, "epoch.run=run-123", "epoch.run=../bad", 1), strings.Replace(valid, "epoch.cid=3", "epoch.cid=2", 1)} {
		if _, err := ParseIdentity(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
func TestReadbackUsesLocalElapsedAndFakeSetter(t *testing.T) {
	target := time.Date(1999, 12, 31, 23, 59, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		delta time.Duration
		valid bool
	}{{"elapsed accepted", 1500 * time.Millisecond, true}, {"wrong clock", 3 * time.Second, false}, {"too early", -2 * time.Second, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeBoundary{samples: []protocol.ClockObservation{{Realtime: time.Now(), MonotonicNS: 10}, {Realtime: target.Add(tc.delta), MonotonicNS: 1_000_000_010}}}
			c, err := New(Identity{RunID: "run-1", Nonce: strings.Repeat("ab", 32), CID: 3}, 3, f)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Set(protocol.ClockSetRequest{At: target.Format(time.RFC3339), ToleranceMS: 1000})
			if err != nil {
				t.Fatal(err)
			}
			if got.ReadbackValid != tc.valid || f.sets != 1 {
				t.Fatalf("result=%+v sets=%d", got, f.sets)
			}
		})
	}
}
func TestOutOfRangeNeverCallsSetter(t *testing.T) {
	f := &fakeBoundary{}
	c, _ := New(Identity{RunID: "run-1", Nonce: strings.Repeat("ab", 32), CID: 3}, 3, f)
	for _, at := range []string{"1989-12-31T23:59:59Z", "2100-01-01T00:00:00Z", "1999-01-01T00:00:00"} {
		if _, err := c.Set(protocol.ClockSetRequest{At: at}); err == nil {
			t.Fatal("accepted", at)
		}
	}
	if f.sets != 0 {
		t.Fatal("setter invoked")
	}
}
