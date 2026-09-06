//go:build !linux

package safefs

import (
	"errors"
	"os"
)

// Portable validation/build tests do not provide Linux runtime assurances.
func checkOwned(_ os.FileInfo) error           { return nil }
func checkTrusted(_ os.FileInfo) error         { return nil }
func checkPrivate(_ os.FileInfo, _ bool) error { return nil }
func syncDir(_ string) error                   { return nil }

type Lock struct{}

func AcquireLock(_ string) (*Lock, error) { return nil, errors.New("runtime locks require Linux") }
func (l *Lock) Close() error              { return nil }
func CheckSpace(_ string, _, _ int64) error {
	return errors.New("runtime free-space inspection requires Linux")
}
