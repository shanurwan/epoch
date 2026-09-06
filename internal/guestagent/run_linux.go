//go:build linux

package guestagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/mdlayher/vsock"
	"github.com/shanurwan/epoch/internal/guestclock"
	"github.com/shanurwan/epoch/internal/protocol"
	"golang.org/x/sys/unix"
)

func Run(ctx context.Context, manifestPath, version string) error {
	identity, clock, err := guestclock.NewSystem()
	if err != nil {
		return err
	}
	if err = trustedGuestFile(manifestPath); err != nil {
		return err
	}
	m, digest, err := LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	manager, err := NewManager(m, manifestPath, clock)
	if err != nil {
		return err
	}
	exe, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	st, err := exe.Stat()
	if err != nil {
		exe.Close()
		return err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || st.Mode().Perm()&0022 != 0 {
		exe.Close()
		return errors.New("agent executable must be root owned and not group/world writable")
	}
	h := sha256.New()
	_, err = io.Copy(h, exe)
	_ = exe.Close()
	if err != nil {
		return err
	}
	agentHash := hex.EncodeToString(h.Sum(nil))
	listener, err := vsock.ListenContextID(identity.CID, protocol.GuestPort, nil)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &Server{Identity: identity, Manifest: m, ManifestSHA256: digest, AgentVersion: version, AgentSHA256: agentHash, Clock: clock, Runner: manager}
	return server.Serve(ctx, listener)
}

// WorkloadExec runs only in a credential-dropped child of the agent. Locking this
// thread ensures PR_SET_NO_NEW_PRIVS applies to the exact thread calling execve.
func WorkloadExec(args []string) error {
	if len(args) != 3 {
		return errors.New("invalid internal workload invocation")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return err
	}
	identity, err := guestclock.ParseIdentity(string(cmdline))
	if err != nil {
		return err
	}
	cid, err := unix.IoctlGetUint32(3, unix.IOCTL_VM_SOCKETS_GET_LOCAL_CID)
	if err != nil {
		return err
	}
	if err = identity.Verify(cid); err != nil {
		return err
	}
	if err = unix.Close(3); err != nil {
		return err
	}
	m, _, err := LoadManifest(args[0])
	if err != nil {
		return err
	}
	if err = trustedGuestFile(args[0]); err != nil {
		return err
	}
	if os.Getuid() != int(m.UID) || os.Geteuid() != int(m.UID) || os.Getgid() != int(m.GID) || os.Getegid() != int(m.GID) {
		return errors.New("workload identity mismatch")
	}
	groups, err := os.Getgroups()
	if err != nil {
		return err
	}
	if len(groups) != 0 {
		return errors.New("unexpected workload supplementary groups")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	checked := 0
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "CapEff:", "CapPrm:", "CapAmb:":
			caps, e := strconv.ParseUint(fields[1], 16, 64)
			if e != nil || caps != 0 {
				return errors.New("workload active capabilities must be empty")
			}
			checked++
		}
	}
	if checked != 3 {
		return errors.New("cannot inspect workload capabilities")
	}
	if err = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	var argv []string
	switch args[1] {
	case "action":
		command, ok := m.Actions[args[2]]
		if !ok {
			return errors.New("undeclared action")
		}
		argv = command.Argv
	case "service":
		service, ok := m.Services[args[2]]
		if !ok {
			return errors.New("undeclared service")
		}
		argv = service.Argv
	default:
		return errors.New("invalid workload kind")
	}
	if err = os.Chdir(m.WorkingDir); err != nil {
		return err
	}
	if err = unix.Exec(argv[0], argv, os.Environ()); err != nil {
		return fmt.Errorf("exec declared workload: %w", err)
	}
	return nil
}

func trustedGuestFile(filename string) error {
	if !filepath.IsAbs(filename) || filepath.Clean(filename) != filename {
		return errors.New("guest manifest path must be absolute and clean")
	}
	for p := filename; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || st.Mode().Perm()&0022 != 0 || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("guest manifest path is not trusted root-owned content: %s", p)
		}
		if p == filename && !st.Mode().IsRegular() {
			return errors.New("guest manifest is not a regular file")
		}
		if p == "/" {
			return nil
		}
	}
}
