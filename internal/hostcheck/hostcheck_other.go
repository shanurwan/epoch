//go:build !linux || !amd64

package hostcheck

import (
	"context"
	"runtime"
)

func CheckPrivilege() error         { return ErrUnsupported }
func Observe() (Observation, error) { return Observation{}, ErrUnsupported }
func BootID() (string, error)       { return "", ErrUnsupported }
func doctor(_ context.Context, path string, probe bool) (Report, error) {
	return Report{OS: runtime.GOOS, Architecture: runtime.GOARCH, UID: -1, FirecrackerPath: path, ProbeRequested: probe, Problems: []string{ErrUnsupported.Error()}}, ErrUnsupported
}
