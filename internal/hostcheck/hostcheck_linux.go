//go:build linux && amd64

package hostcheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Every extant thread is inspected because Linux capabilities are per thread.
// Epoch never mutates credentials; subsequently created Go threads inherit them.
func CheckPrivilege() error {
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		return errors.New("host runtime refuses UID 0")
	}
	threads, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return fmt.Errorf("inspect thread privileges: %w", err)
	}
	if len(threads) == 0 {
		return errors.New("no thread privilege records")
	}
	for _, t := range threads {
		b, err := os.ReadFile(filepath.Join("/proc/self/task", t.Name(), "status"))
		if err != nil {
			return fmt.Errorf("inspect thread %s privileges: %w", t.Name(), err)
		}
		if err := parsePrivileges(string(b)); err != nil {
			return fmt.Errorf("thread %s: %w", t.Name(), err)
		}
	}
	return nil
}

func Observe() (Observation, error) {
	var r, m, b unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_REALTIME, &r); err != nil {
		return Observation{}, err
	}
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &m); err != nil {
		return Observation{}, err
	}
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &b); err != nil {
		return Observation{}, err
	}
	return Observation{Realtime: time.Unix(r.Sec, r.Nsec).UTC(), MonotonicNS: m.Nano(), BoottimeNS: b.Nano()}, nil
}

func BootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if len(id) != 36 {
		return "", errors.New("invalid host boot ID")
	}
	return id, nil
}

func doctor(ctx context.Context, path string, probe bool) (Report, error) {
	r := Report{OS: runtime.GOOS, Architecture: runtime.GOARCH, UID: os.Getuid(), FirecrackerPath: path, ProbeRequested: probe, Problems: []string{}, Notes: []string{
		"Read-only inspection does not prove guest boot, vsock, or guest clock isolation.",
		"Direct non-jailer execution and delegated cgroup enforcement are experimental limitations.",
		"Rocky kernel 5.14 is a lab compatibility target outside the Firecracker v1.16.1 upstream validation matrix.",
	}}
	add := func(err error) {
		if err != nil {
			r.Problems = append(r.Problems, err.Error())
		}
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	privErr := CheckPrivilege()
	add(privErr)
	id, err := BootID()
	add(err)
	r.HostBootID = id
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		r.Kernel = strings.TrimSpace(string(b))
	} else {
		add(err)
	}
	b, err := os.ReadFile("/sys/fs/selinux/enforce")
	r.SELinuxEnforcing = err == nil && strings.TrimSpace(string(b)) == "1"
	if !r.SELinuxEnforcing {
		add(errors.New("SELinux enforcement is not confirmed"))
	}
	var stat unix.Statfs_t
	err = unix.Statfs("/sys/fs/cgroup", &stat)
	r.CgroupV2 = err == nil && stat.Type == unix.CGROUP2_SUPER_MAGIC
	if !r.CgroupV2 {
		add(errors.New("cgroup v2 is not confirmed"))
	}
	fi, err := os.Lstat("/dev/kvm")
	if err != nil {
		add(err)
	} else if fi.Mode()&os.ModeCharDevice == 0 || fi.Mode()&os.ModeSymlink != 0 {
		add(errors.New("/dev/kvm is not a real character device"))
	} else {
		err = unix.Access("/dev/kvm", unix.R_OK|unix.W_OK)
		r.KVMAccessible = err == nil
		add(err)
	}
	obs, err := Observe()
	if err == nil {
		r.Observation = &obs
	}
	add(err)
	if probe && len(r.Problems) == 0 {
		err = probeKVM(ctx)
		r.ProbePassed = err == nil
		add(err)
	}
	if len(r.Problems) > 0 {
		return r, errors.New(strings.Join(r.Problems, "; "))
	}
	return r, nil
}

func probeKVM(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Open("/dev/kvm", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	version, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0xae00, 0)
	if errno != 0 {
		return errno
	}
	if version != 12 {
		return fmt.Errorf("unexpected KVM API %d", version)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	vm, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0xae01, 0)
	if errno != 0 {
		return errno
	}
	if err := unix.Close(int(vm)); err != nil {
		return err
	}
	return ctx.Err()
}
