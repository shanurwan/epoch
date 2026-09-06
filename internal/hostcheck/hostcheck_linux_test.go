//go:build linux && amd64

package hostcheck

import (
	"os"
	"testing"
)

func TestLivePrivilegeInspectionAndReadOnlyClocks(t *testing.T) {
	err := CheckPrivilege()
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		if err == nil {
			t.Fatal("accepted root runtime")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	observation, err := Observe()
	if err != nil {
		t.Fatal(err)
	}
	if observation.Realtime.IsZero() || observation.MonotonicNS <= 0 || observation.BoottimeNS <= 0 {
		t.Fatalf("missing host clock observation: %+v", observation)
	}
}
