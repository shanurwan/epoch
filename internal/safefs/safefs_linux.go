//go:build linux

package safefs

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"syscall"
)

func checkOwned(fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("file ownership unavailable")
	}
	if st.Uid != uint32(os.Geteuid()) {
		return errors.New("path is not owned by the operator")
	}
	return nil
}
func checkTrusted(fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("file ownership unavailable")
	}
	if st.Uid != 0 && st.Uid != uint32(os.Geteuid()) {
		return errors.New("path owned by another UID")
	}
	if fi.Mode().Perm()&0022 != 0 {
		// A root-owned sticky temporary ancestor is allowed; final private roots
		// still require exact operator ownership and 0700 mode.
		if !(fi.IsDir() && st.Uid == 0 && fi.Mode()&os.ModeSticky != 0) {
			return errors.New("path is group/other writable")
		}
	}
	return nil
}
func checkPrivate(fi os.FileInfo, dir bool) error {
	if err := checkOwned(fi); err != nil {
		return err
	}
	want := os.FileMode(0600)
	if dir {
		want = 0700
	}
	if fi.Mode().Perm() != want {
		return fmt.Errorf("private path mode is %04o, require %04o", fi.Mode().Perm(), want)
	}
	return nil
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

type Lock struct{ f *os.File }

func AcquireLock(root string) (*Lock, error) {
	if err := ValidatePrivateDir(root); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "operator.lock")
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err := ValidatePrivateFile(path); err != nil {
		f.Close()
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another operator run holds runtime lock: %w", err)
	}
	return &Lock{f: f}, nil
}
func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
func CheckSpace(path string, copyBytes, reserveBytes int64) error {
	if copyBytes < 0 || reserveBytes < 0 {
		return errors.New("negative space budget")
	}
	if err := ValidatePrivateDir(path); err != nil {
		return err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return err
	}
	if st.Bsize <= 0 {
		return errors.New("invalid filesystem block size")
	}
	available := uint64(st.Bavail) * uint64(st.Bsize)
	required := uint64(copyBytes) + uint64(reserveBytes)
	if available < required {
		return fmt.Errorf("insufficient free space: available %d bytes, require %d bytes including reserve", available, required)
	}
	return nil
}
