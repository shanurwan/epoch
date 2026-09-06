//go:build linux

package guestagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shanurwan/epoch/internal/jsonutil"
	"github.com/shanurwan/epoch/internal/protocol"
	"golang.org/x/sys/unix"
)

type boundedCapture struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
	quota    chan struct{}
}

func newCapture() *boundedCapture { return &boundedCapture{quota: make(chan struct{})} }
func (b *boundedCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	space := protocol.MaxOutput - len(b.data)
	if len(p) > space {
		p = p[:space]
		if !b.overflow {
			b.overflow = true
			close(b.quota)
		}
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *boundedCapture) snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.data), b.overflow
}

type ownedProcess struct {
	cmd         *exec.Cmd
	out, stderr *boundedCapture
	done        chan struct{}
	err         error
	expected    bool
	signalMu    sync.Mutex
	reaping     bool
	cleanupErr  error
}
type Manager struct {
	manifest                 protocol.WorkloadManifest
	manifestPath, executable string
	clock                    Clock
	mu                       sync.Mutex
	services                 map[string]*ownedProcess
	startOverride            func(string, string, []byte) (*ownedProcess, error)
}

func NewManager(m protocol.WorkloadManifest, manifestPath string, clock Clock) (*Manager, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &Manager{manifest: m, manifestPath: manifestPath, executable: exe, clock: clock, services: map[string]*ownedProcess{}}, nil
}
func (m *Manager) launch(kind, id string, input []byte) (*ownedProcess, error) {
	if m.startOverride != nil {
		return m.startOverride(kind, id, input)
	}
	p := &ownedProcess{out: newCapture(), stderr: newCapture(), done: make(chan struct{})}
	cmd := exec.Command(m.executable, "--workload-exec", m.manifestPath, kind, id)
	cmd.Dir = m.manifest.WorkingDir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + m.manifest.WorkingDir, "TZ=UTC", "LANG=C"}
	keys := make([]string, 0, len(m.manifest.Environment))
	for k := range m.manifest.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+m.manifest.Environment[k])
	}
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = p.out
	cmd.Stderr = p.stderr
	vsockDevice, err := os.Open("/dev/vsock")
	if err != nil {
		return nil, err
	}
	defer vsockDevice.Close()
	cmd.ExtraFiles = []*os.File{vsockDevice}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL, Credential: &syscall.Credential{Uid: m.manifest.UID, Gid: m.manifest.GID, Groups: []uint32{}}}
	p.cmd = cmd
	return startOwned(p)
}
func startOwned(p *ownedProcess) (*ownedProcess, error) {
	cmd := p.cmd
	cmd.WaitDelay = 500 * time.Millisecond
	started := make(chan error, 1)
	// Keep the creating OS thread alive through Wait for Linux Pdeathsig semantics.
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		err := cmd.Start()
		started <- err
		if err == nil {
			var info unix.Siginfo
			for {
				err = unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
				if !errors.Is(err, unix.EINTR) {
					break
				}
			}
			p.signalMu.Lock()
			if err == nil {
				// WNOWAIT retains the leader's PID until descendants have been
				// signalled. No group signal can race with the subsequent reap.
				p.cleanupErr = killGroupAndWait(cmd.Process.Pid)
			} else {
				p.cleanupErr = fmt.Errorf("observe owned leader before reap: %w", err)
				_ = cmd.Process.Kill()
			}
			p.reaping = true
			p.signalMu.Unlock()
			p.err = cmd.Wait()
			if errors.Is(p.err, exec.ErrWaitDelay) {
				p.cleanupErr = errors.Join(p.cleanupErr, p.err)
			}
		} else {
			p.err = err
		}
		close(p.done)
	}()
	if err := <-started; err != nil {
		return nil, fmt.Errorf("launch workload: %w", err)
	}
	return p, nil
}

func killGroupAndWait(leader int) error {
	deadline := time.Now().Add(time.Second)
	for {
		if err := syscall.Kill(-leader, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		active, err := activeGroupDescendants(leader)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("owned group descendants did not stop before cleanup deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func activeGroupDescendants(leader int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == leader {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect group membership: %w", err)
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			return false, errors.New("invalid process stat while verifying owned group")
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 3 {
			return false, errors.New("incomplete process stat while verifying owned group")
		}
		group, err := strconv.Atoi(fields[2])
		if err != nil {
			return false, err
		}
		if group == leader && fields[0] != "Z" && fields[0] != "X" {
			return true, nil
		}
	}
	return false, nil
}
func (p *ownedProcess) signalGroup(signal syscall.Signal) error {
	p.signalMu.Lock()
	defer p.signalMu.Unlock()
	if p.reaping {
		return nil
	}
	if err := syscall.Kill(-p.cmd.Process.Pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
func terminate(ctx context.Context, p *ownedProcess) error {
	select {
	case <-p.done:
		return p.cleanupErr
	default:
	}
	if err := p.signalGroup(syscall.SIGTERM); err != nil {
		return err
	}
	t := time.NewTimer(250 * time.Millisecond)
	defer t.Stop()
	select {
	case <-p.done:
		return p.cleanupErr
	case <-t.C:
	case <-ctx.Done():
	}
	if err := p.signalGroup(syscall.SIGKILL); err != nil {
		return err
	}
	select {
	case <-p.done:
		return p.cleanupErr
	case <-ctx.Done():
		return fmt.Errorf("workload reap: %w", ctx.Err())
	}
}
func (m *Manager) Action(ctx context.Context, req protocol.ActionRequest) (result protocol.ActionResult, resultErr error) {
	var err error
	if _, ok := m.manifest.Actions[req.Action]; !ok {
		return result, errors.New("undeclared action")
	}
	if result.Before, err = m.clock.Read(); err != nil {
		return result, err
	}
	defer func() {
		var readErr error
		result.After, readErr = m.clock.Read()
		resultErr = errors.Join(resultErr, readErr)
	}()
	input := req.Input
	if len(input) == 0 {
		input = []byte("{}")
	}
	input = append(bytes.Clone(input), '\n')
	p, err := m.launch("action", req.Action, input)
	if err != nil {
		return result, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, terminate(cleanup, p))
		_, result.StdoutTruncated = p.out.snapshot()
		stderr, truncated := p.stderr.snapshot()
		result.Stderr = string(stderr)
		result.StderrTruncated = truncated
	}()
	check := time.NewTicker(25 * time.Millisecond)
	defer check.Stop()
wait:
	for {
		select {
		case <-p.done:
			break wait
		case <-ctx.Done():
			return result, fmt.Errorf("action deadline/cancellation: %w", ctx.Err())
		case <-p.out.quota:
			return result, &protocol.Error{Code: "output_quota", Message: "action stdout exceeded 64 KiB"}
		case <-p.stderr.quota:
			return result, &protocol.Error{Code: "output_quota", Message: "action stderr exceeded 64 KiB"}
		case <-check.C:
			if err = m.Health(); err != nil {
				return result, err
			}
		}
	}
	out, outTruncated := p.out.snapshot()
	stderr, errTruncated := p.stderr.snapshot()
	result.StdoutTruncated = outTruncated
	result.StderrTruncated = errTruncated
	result.Stderr = string(stderr)
	if outTruncated || errTruncated {
		return result, &protocol.Error{Code: "output_quota", Message: "action output exceeded quota"}
	}
	if p.err != nil {
		var exit *exec.ExitError
		if !errors.As(p.err, &exit) {
			return result, p.err
		}
	}
	result.ExitCode = p.cmd.ProcessState.ExitCode()
	if result.ExitCode < 0 {
		return result, errors.New("action terminated by signal instead of a normal process exit")
	}
	var value any
	if err = jsonutil.Decode(out, &value); err != nil {
		return result, fmt.Errorf("action required JSON output: %w", err)
	}
	result.StdoutJSON = bytes.TrimSpace(out)
	return result, nil
}
func (m *Manager) Start(ctx context.Context, req protocol.ServiceRequest) (result protocol.ServiceResult, resultErr error) {
	result.Service = req.Service
	before, err := m.clock.Read()
	if err != nil {
		return result, err
	}
	result.Before = before
	defer func() {
		var readErr error
		result.After, readErr = m.clock.Read()
		resultErr = errors.Join(resultErr, readErr)
	}()
	spec, ok := m.manifest.Services[req.Service]
	if !ok {
		return result, errors.New("undeclared service")
	}
	m.mu.Lock()
	if _, ok = m.services[req.Service]; ok || len(m.services) >= 4 {
		m.mu.Unlock()
		return result, errors.New("service already started or service limit reached")
	}
	m.mu.Unlock()
	input := req.Input
	if len(input) == 0 {
		input = []byte("{}")
	}
	input = append(bytes.Clone(input), '\n')
	p, err := m.launch("service", req.Service, input)
	if err != nil {
		return result, err
	}
	m.mu.Lock()
	m.services[req.Service] = p
	m.mu.Unlock()
	for {
		if err = m.Health(); err != nil {
			return result, err
		}
		ready, readyErr := m.Action(ctx, protocol.ActionRequest{Action: spec.ReadinessAction})
		if readyErr != nil {
			return result, fmt.Errorf("service readiness execution: %w", readyErr)
		}
		if ready.ExitCode == 0 {
			if err = m.Health(); err != nil {
				return result, err
			}
			result.Running = true
			return result, nil
		}
		t := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return result, ctx.Err()
		case <-t.C:
		}
	}
}
func (m *Manager) Stop(ctx context.Context, req protocol.ServiceRequest) (result protocol.ServiceResult, resultErr error) {
	result.Service = req.Service
	var err error
	if result.Before, err = m.clock.Read(); err != nil {
		return result, err
	}
	defer func() {
		var readErr error
		result.After, readErr = m.clock.Read()
		resultErr = errors.Join(resultErr, readErr)
	}()
	m.mu.Lock()
	p, ok := m.services[req.Service]
	if ok {
		select {
		case <-p.done:
			m.mu.Unlock()
			return result, errors.New("service exited before explicit stop")
		default:
		}
		p.expected = true
	}
	m.mu.Unlock()
	if !ok {
		return result, errors.New("service is not running")
	}
	if err = terminate(ctx, p); err != nil {
		return result, err
	}
	m.mu.Lock()
	delete(m.services, req.Service)
	m.mu.Unlock()
	out, truncOut := p.out.snapshot()
	stderr, truncErr := p.stderr.snapshot()
	if truncOut || truncErr {
		return result, errors.New("service output quota exceeded")
	}
	result.Stdout = string(out)
	result.Stderr = string(stderr)
	return result, nil
}
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	processes := make(map[string]*ownedProcess, len(m.services))
	for id, p := range m.services {
		p.expected = true
		processes[id] = p
	}
	m.mu.Unlock()
	var all error
	for id, p := range processes {
		if err := terminate(ctx, p); err != nil {
			all = errors.Join(all, err)
			continue
		}
		m.mu.Lock()
		delete(m.services, id)
		m.mu.Unlock()
		out, truncOut := p.out.snapshot()
		stderr, truncErr := p.stderr.snapshot()
		fmt.Fprintf(os.Stderr, "epoch service %q cleanup stdout=%q stderr=%q stdout_truncated=%t stderr_truncated=%t\n", id, string(out), string(stderr), truncOut, truncErr)
		if truncOut || truncErr {
			all = errors.Join(all, fmt.Errorf("service %s output quota exceeded", id))
		}
	}
	return all
}
func (m *Manager) Active() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.services) }
func (m *Manager) Health() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, p := range m.services {
		if p.expected {
			continue
		}
		select {
		case <-p.out.quota:
			return fmt.Errorf("service %s stdout quota exceeded", id)
		case <-p.stderr.quota:
			return fmt.Errorf("service %s stderr quota exceeded", id)
		case <-p.done:
			return fmt.Errorf("service %s exited unexpectedly: %v", id, p.err)
		default:
		}
	}
	return nil
}
