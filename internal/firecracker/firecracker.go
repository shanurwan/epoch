// Package firecracker owns one direct, non-jailer Firecracker process.
package firecracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/shanurwan/epoch/internal/safefs"
)

var ErrUnsupported = errors.New("Firecracker runtime requires Linux x86_64")

type Identity struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	Version       string `json:"version"`
	VersionStdout string `json:"version_stdout,omitempty"`
	VersionStderr string `json:"version_stderr,omitempty"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
}
type ProcessIdentity struct {
	PID        int    `json:"pid"`
	UID        int    `json:"uid"`
	StartTicks uint64 `json:"start_ticks"`
	HostBootID string `json:"host_boot_id"`
}
type ProcessState struct {
	Alive  bool   `json:"alive"`
	Reason string `json:"reason"`
}
type Options struct {
	Executable     Identity
	ConfigPath     string
	Stdout, Stderr io.Writer
}
type Process struct {
	identity ProcessIdentity
	done     chan struct{}
	err      error
	stopOnce sync.Once
	stopDone chan struct{}
	stopErr  error
	stop     func(context.Context) error
}

func (p *Process) Done() <-chan struct{}     { return p.done }
func (p *Process) Identity() ProcessIdentity { return p.identity }
func (p *Process) Err() error {
	select {
	case <-p.done:
		return p.err
	default:
		return nil
	}
}
func (p *Process) Stop(ctx context.Context) error {
	select {
	case <-p.done:
		return nil
	default:
	}
	p.stopOnce.Do(func() { go func() { p.stopErr = p.stop(ctx); close(p.stopDone) }() })
	select {
	case <-p.stopDone:
		return p.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

type ConfigOptions struct {
	KernelPath, RootFSPath, SocketPath, RunID, Nonce string
	CID                                              uint32
	VCPUCount, MemoryMiB                             int
}
type Config struct {
	BootSource BootSource `json:"boot-source"`
	Drives     []Drive    `json:"drives"`
	Machine    Machine    `json:"machine-config"`
	Vsock      Vsock      `json:"vsock"`
}
type BootSource struct {
	KernelPath string `json:"kernel_image_path"`
	BootArgs   string `json:"boot_args"`
}
type Drive struct {
	ID       string `json:"drive_id"`
	Path     string `json:"path_on_host"`
	Root     bool   `json:"is_root_device"`
	ReadOnly bool   `json:"is_read_only"`
}
type Machine struct {
	VCPUs     int  `json:"vcpu_count"`
	MemoryMiB int  `json:"mem_size_mib"`
	SMT       bool `json:"smt"`
}
type Vsock struct {
	CID  uint32 `json:"guest_cid"`
	Path string `json:"uds_path"`
}

var safeToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

var versionLine = regexp.MustCompile(`^Firecracker v[0-9]+\.[0-9]+\.[0-9]+$`)

func parseVersionOutput(stdout []byte, expected string) (string, error) {
	first, _, complete := strings.Cut(string(stdout), "\n")
	if !complete || !versionLine.MatchString(first) {
		return "", errors.New("VMM stdout must begin with a complete Firecracker version line")
	}
	if expected != "" && first != "Firecracker v"+strings.TrimPrefix(expected, "v") {
		return "", fmt.Errorf("VMM version %q does not match %q", first, expected)
	}
	return first, nil
}

func BuildConfig(o ConfigOptions) (Config, error) {
	if o.CID != 3 {
		return Config{}, errors.New("single-VM profile requires guest CID 3")
	}
	if o.VCPUCount < 1 || o.VCPUCount > 2 || o.MemoryMiB < 128 || o.MemoryMiB > 2048 {
		return Config{}, errors.New("machine resources outside conservative profile")
	}
	if !safeToken.MatchString(o.RunID) || !safeToken.MatchString(o.Nonce) {
		return Config{}, errors.New("invalid run marker or nonce")
	}
	for _, p := range []string{o.KernelPath, o.RootFSPath, o.SocketPath} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsRune(p, 0) {
			return Config{}, errors.New("VMM paths must be absolute and clean")
		}
	}
	// Firecracker creates additional port-suffixed sockets. Keep conservative
	// headroom beneath Linux sockaddr_un's 108-byte buffer.
	if len(o.SocketPath) > 90 {
		return Config{}, errors.New("vsock path exceeds 90-byte profile limit")
	}
	args := fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw init=/usr/lib/systemd/systemd systemd.unit=epoch.target epoch.run=%s epoch.nonce=%s epoch.cid=%d", o.RunID, o.Nonce, o.CID)
	return Config{BootSource: BootSource{KernelPath: o.KernelPath, BootArgs: args}, Drives: []Drive{{ID: "rootfs", Path: o.RootFSPath, Root: true, ReadOnly: false}}, Machine: Machine{VCPUs: o.VCPUCount, MemoryMiB: o.MemoryMiB, SMT: false}, Vsock: Vsock{CID: o.CID, Path: o.SocketPath}}, nil
}
func WriteConfig(path string, o ConfigOptions) error {
	c, err := BuildConfig(o)
	if err != nil {
		return err
	}
	return safefs.AtomicJSON(path, c)
}

type cappedWriter struct {
	dst      []byte
	limit    int
	overflow bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - len(w.dst)
	if len(p) > remaining {
		w.overflow = true
		p = p[:remaining]
	}
	w.dst = append(w.dst, p...)
	return n, nil
}

const stopGrace = time.Second
