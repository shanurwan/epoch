//go:build !linux || !amd64

package firecracker

import "context"

func ValidateExecutable(context.Context, string, string) (Identity, error) {
	return Identity{}, ErrUnsupported
}
func Start(context.Context, Options) (*Process, error)     { return nil, ErrUnsupported }
func InspectProcess(ProcessIdentity) (ProcessState, error) { return ProcessState{}, ErrUnsupported }
func StopOwned(context.Context, ProcessIdentity) error     { return ErrUnsupported }
