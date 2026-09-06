//go:build linux && amd64

package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shanurwan/epoch/internal/hostcheck"
	"github.com/shanurwan/epoch/internal/safefs"
	"golang.org/x/sys/unix"
)

func openExecutable(ctx context.Context, path string) (*os.File, Identity, error) {
	if err := safefs.ValidateNoSymlinks(path); err != nil {
		return nil, Identity{}, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, Identity{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	fail := func(err error) (*os.File, Identity, error) { f.Close(); return nil, Identity{}, err }
	fi, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0111 == 0 || fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return fail(errors.New("VMM must be a regular executable without setuid/setgid"))
	}
	if fi.Size() <= 0 || fi.Size() > 256<<20 {
		return fail(errors.New("VMM executable exceeds 256 MiB profile limit or is empty"))
	}
	_, err = unix.Fgetxattr(fd, "security.capability", nil)
	if err == nil {
		return fail(errors.New("VMM executable has file capabilities"))
	}
	if !errors.Is(err, unix.ENODATA) && !errors.Is(err, unix.ENOTSUP) {
		return fail(fmt.Errorf("cannot inspect VMM file capabilities: %w", err))
	}
	h := sha256.New()
	buf := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		n, e := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return fail(e)
		}
	}
	if _, err = f.Seek(0, 0); err != nil {
		return fail(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fail(errors.New("VMM identity unavailable"))
	}
	return f, Identity{Path: path, SHA256: hex.EncodeToString(h.Sum(nil)), Size: fi.Size(), Device: st.Dev, Inode: st.Ino}, nil
}

func ValidateExecutable(ctx context.Context, path, version string) (Identity, error) {
	if err := hostcheck.CheckPrivilege(); err != nil {
		return Identity{}, err
	}
	f, id, err := openExecutable(ctx, path)
	if err != nil {
		return Identity{}, err
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, diag := &cappedWriter{limit: 4096}, &cappedWriter{limit: 4096}
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", "--version")
	cmd.Args[0] = path
	cmd.ExtraFiles = []*os.File{f}
	cmd.Env = []string{"LANG=C", "LC_ALL=C"}
	cmd.Stdout = out
	cmd.Stderr = diag
	cmd.WaitDelay = time.Second
	// Keep the creating thread until Wait even for this short identity probe.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Run(); err != nil {
		return Identity{}, fmt.Errorf("VMM version probe: %w", err)
	}
	if out.overflow || diag.overflow {
		return Identity{}, errors.New("VMM version output exceeded quota")
	}
	id.Version, err = parseVersionOutput(out.dst, version)
	if err != nil {
		return Identity{}, err
	}
	// Firecracker v1.16.1 emits shutdown diagnostics after its version line.
	// Retain bounded stdout/stderr as evidence; only the first line is identity.
	id.VersionStdout = string(out.dst)
	id.VersionStderr = string(diag.dst)
	return id, nil
}

func Start(ctx context.Context, o Options) (*Process, error) {
	if err := hostcheck.CheckPrivilege(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := safefs.ValidatePrivateFile(o.ConfigPath); err != nil {
		return nil, err
	}
	f, id, err := openExecutable(ctx, o.Executable.Path)
	if err != nil {
		return nil, err
	}
	if id.SHA256 != o.Executable.SHA256 || id.Size != o.Executable.Size || id.Device != o.Executable.Device || id.Inode != o.Executable.Inode {
		f.Close()
		return nil, errors.New("VMM executable identity changed before launch")
	}
	cmd := exec.Command("/proc/self/fd/3", "--no-api", "--config-file", o.ConfigPath)
	cmd.Args[0] = o.Executable.Path
	cmd.ExtraFiles = []*os.File{f}
	cmd.Env = []string{"LANG=C", "LC_ALL=C"}
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	cmd.WaitDelay = 2 * time.Second
	return startOwned(ctx, cmd, f)
}

// Go's fork/exec implementation installs PR_SET_PDEATHSIG and then verifies
// getppid against the pre-fork parent PID, closing the parent-death setup race.
// This goroutine pins the creating OS thread until the sole Wait has finished.
func startOwned(ctx context.Context, cmd *exec.Cmd, opened *os.File) (*Process, error) {
	type result struct {
		p   *Process
		err error
	}
	ready := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if opened != nil {
			defer opened.Close()
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
		if err := ctx.Err(); err != nil {
			ready <- result{err: err}
			return
		}
		if err := cmd.Start(); err != nil {
			ready <- result{err: err}
			return
		}
		id, _, err := readIdentity(cmd.Process.Pid)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			ready <- result{err: fmt.Errorf("inspect newly owned process: %w", err)}
			return
		}
		p := &Process{identity: id, done: make(chan struct{}), stopDone: make(chan struct{})}
		p.stop = func(stopCtx context.Context) error {
			select {
			case <-p.done:
				return nil
			default:
			}
			// os.Process owns the live child handle. Go uses pidfds where available
			// and prevents signals through a released handle after Wait.
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return err
			}
			timer := time.NewTimer(stopGrace)
			defer timer.Stop()
			select {
			case <-p.done:
				return nil
			case <-timer.C:
			case <-stopCtx.Done():
			}
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return err
			}
			select {
			case <-p.done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		}
		ready <- result{p: p}
		p.err = cmd.Wait()
		close(p.done)
	}()
	r := <-ready
	if r.err != nil {
		return nil, r.err
	}
	go func() {
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = r.p.Stop(stopCtx)
		case <-r.p.done:
		}
	}()
	return r.p, nil
}

func readIdentity(pid int) (ProcessIdentity, string, error) {
	if pid <= 0 {
		return ProcessIdentity{}, "", errors.New("invalid process PID")
	}
	boot, err := hostcheck.BootID()
	if err != nil {
		return ProcessIdentity{}, "", err
	}
	base := "/proc/" + strconv.Itoa(pid)
	info, err := os.Stat(base)
	if err != nil {
		return ProcessIdentity{}, "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ProcessIdentity{}, "", errors.New("process UID unavailable")
	}
	b, err := os.ReadFile(base + "/stat")
	if err != nil {
		return ProcessIdentity{}, "", err
	}
	start, state, err := parseStat(string(b))
	if err != nil {
		return ProcessIdentity{}, "", err
	}
	return ProcessIdentity{PID: pid, UID: int(st.Uid), StartTicks: start, HostBootID: boot}, state, nil
}

func parseStat(s string) (uint64, string, error) {
	// comm may contain spaces and closing parentheses; the final ')' is its end.
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return 0, "", errors.New("malformed proc stat")
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0, "", errors.New("short proc stat")
	}
	n, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return 0, "", err
	}
	return n, f[0], nil
}

func InspectProcess(id ProcessIdentity) (ProcessState, error) {
	if id.PID <= 0 || id.StartTicks == 0 || id.HostBootID == "" || id.UID != os.Getuid() {
		return ProcessState{}, errors.New("invalid or foreign process ownership identity")
	}
	boot, err := hostcheck.BootID()
	if err != nil {
		return ProcessState{}, err
	}
	if boot != id.HostBootID {
		return ProcessState{Reason: "host rebooted; recorded process cannot survive"}, nil
	}
	actual, state, err := readIdentity(id.PID)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
		return ProcessState{Reason: "process absent"}, nil
	}
	if err != nil {
		return ProcessState{}, err
	}
	if actual != id {
		return ProcessState{}, errors.New("PID identity differs; manual review required, no signal sent")
	}
	if state == "Z" || state == "X" {
		return ProcessState{Reason: "process exited"}, nil
	}
	return ProcessState{Alive: true, Reason: "verified live owned process"}, nil
}

func StopOwned(ctx context.Context, id ProcessIdentity) error {
	if err := hostcheck.CheckPrivilege(); err != nil {
		return err
	}
	state, err := InspectProcess(id)
	if err != nil || !state.Alive {
		return err
	}
	fd, err := unix.PidfdOpen(id.PID, 0)
	if err != nil {
		return fmt.Errorf("cannot acquire stable process handle; manual review required: %w", err)
	}
	defer unix.Close(fd)
	// Recheck after pidfd_open. No signal is sent through a numeric PID.
	state, err = InspectProcess(id)
	if err != nil || !state.Alive {
		return err
	}
	signal := func(sig unix.Signal) error {
		err := unix.PidfdSendSignal(fd, sig, nil, 0)
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		return err
	}
	if err := signal(unix.SIGTERM); err != nil {
		return err
	}
	grace := time.NewTimer(stopGrace)
	defer grace.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	killed := false
	for {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if n, err := unix.Poll(fds, 0); err != nil && !errors.Is(err, unix.EINTR) {
			return err
		} else if n > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			if !killed {
				_ = signal(unix.SIGKILL)
			}
			return ctx.Err()
		case <-grace.C:
			if err := signal(unix.SIGKILL); err != nil {
				return err
			}
			killed = true
		case <-tick.C:
		}
	}
}
