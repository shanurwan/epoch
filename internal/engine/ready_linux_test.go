//go:build linux

package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shanurwan/epoch/internal/artifact"
	"github.com/shanurwan/epoch/internal/firecracker"
	"github.com/shanurwan/epoch/internal/jsonutil"
	"github.com/shanurwan/epoch/internal/protocol"
	"github.com/shanurwan/epoch/internal/safefs"
)

func readinessTestRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "epoch-ready-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if filepath.Dir(dir) != "/tmp" || !strings.HasPrefix(filepath.Base(dir), "epoch-ready-") {
			t.Errorf("unsafe readiness fixture path %q", dir)
			return
		}
		if err := safefs.ValidatePrivateDir(dir); err != nil {
			t.Errorf("readiness fixture ownership: %v", err)
			return
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("readiness fixture cleanup: %v", err)
		}
	})
	return dir
}

func TestReadinessUsesActualWireHello(t *testing.T) {
	for _, test := range []struct {
		name           string
		isReady        bool
		wrongAgentHash bool
		wantError      bool
	}{
		{name: "ready guest accepted", isReady: true},
		{name: "unready guest rejected", isReady: false, wantError: true},
		{name: "different agent rejected", isReady: true, wrongAgentHash: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := readinessTestRoot(t)
			socket := filepath.Join(dir, "vsock.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			id := strings.Repeat("a", 32)
			nonce := strings.Repeat("b", 64)
			agentHash := artifact.Hash([]byte("verified test agent"))
			workloadHash := artifact.Hash([]byte("verified test workload"))
			image := &artifact.Prepared{Manifest: artifact.Manifest{GuestAgent: artifact.Agent{Version: "test-v1", ProtocolVersion: protocol.Version, SHA256: agentHash}, Workload: artifact.Workload{ID: "clock-probe", Version: "test-v1", ManifestSHA256: workloadHash}}}
			hello := protocol.HelloResult{AgentVersion: "test-v1", AgentSHA256: agentHash, ProtocolVersion: protocol.Version, RunID: id, Nonce: nonce, CID: 3, WorkloadID: "clock-probe", WorkloadVersion: "test-v1", WorkloadSHA256: workloadHash, Ready: test.isReady}
			if test.wrongAgentHash {
				hello.AgentSHA256 = artifact.Hash([]byte("unexpected agent"))
			}
			serverDone := make(chan error, 1)
			go func() { serverDone <- serveReadinessHello(listener, hello) }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			process := &lifecycleVMM{done: make(chan struct{}), identity: firecracker.ProcessIdentity{UID: os.Getuid()}}
			guest, err := ready(ctx, process, socket, protocol.GuestPort, id, nonce, image)
			if guest != nil {
				_ = guest.Close()
			}
			if (err != nil) != test.wantError {
				t.Errorf("ready returned guest=%v error=%v; wantError=%v", guest != nil, err, test.wantError)
			}
			if !test.wantError && guest == nil {
				t.Error("valid wire hello did not return a guest client")
			}
			if test.wantError && err != nil && !strings.Contains(err.Error(), "identity mismatch") {
				t.Errorf("identity failure misclassified: %v", err)
			}
			select {
			case serverErr := <-serverDone:
				if serverErr != nil {
					t.Errorf("wire server: %v", serverErr)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("wire server did not finish")
			}
			if process.stops.Load() != 0 {
				t.Error("readiness inspection changed process lifecycle")
			}
		})
	}
}

// The test server implements the Firecracker CONNECT transition and reads the
// actual client request before echoing its envelope. It imports no guest agent.
func serveReadinessHello(listener *net.UnixListener, hello protocol.HelloResult) error {
	conn, err := listener.AcceptUnix()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	connect, err := protocol.ReadLine(reader, 128)
	if err != nil {
		return err
	}
	if string(connect) != "CONNECT 7000" {
		return fmt.Errorf("invalid CONNECT request %q", connect)
	}
	// The returned value is a host-assigned port and deliberately differs from 7000.
	if _, err := io.WriteString(conn, "OK 49152\n"); err != nil {
		return err
	}
	frame, err := protocol.ReadLine(reader, protocol.MaxFrame)
	if err != nil {
		return err
	}
	var request protocol.Request
	if err := jsonutil.Decode(frame, &request); err != nil {
		return err
	}
	if request.Version != protocol.Version || request.Op != "hello" || request.RunID != hello.RunID || request.Nonce != hello.Nonce {
		return fmt.Errorf("unexpected hello request %+v", request)
	}
	if !bytesEqualEmptyObject(request.Payload) {
		return fmt.Errorf("unexpected hello payload %s", request.Payload)
	}
	result, err := json.Marshal(hello)
	if err != nil {
		return err
	}
	response := protocol.Response{Version: protocol.Version, ID: request.ID, Op: request.Op, RunID: request.RunID, Nonce: request.Nonce, Result: result}
	if err := protocol.WriteFrame(conn, response); err != nil {
		return err
	}
	var trailing [1]byte
	_, err = reader.Read(trailing[:])
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("unexpected request after one-shot readiness test")
}

func bytesEqualEmptyObject(raw json.RawMessage) bool {
	var payload map[string]json.RawMessage
	return jsonutil.Decode(raw, &payload) == nil && payload != nil && len(payload) == 0
}

func TestReadinessRejectsExitedVMMBeforeDial(t *testing.T) {
	dir := readinessTestRoot(t)
	process := &lifecycleVMM{done: make(chan struct{})}
	close(process.done)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	guest, err := ready(ctx, process, filepath.Join(dir, "absent.sock"), 7000, strings.Repeat("a", 32), strings.Repeat("b", 64), &artifact.Prepared{})
	if guest != nil || err == nil || !strings.Contains(err.Error(), "VMM exited before readiness") {
		t.Fatalf("unexpected exited-VM result: guest=%v error=%v", guest, err)
	}
}
