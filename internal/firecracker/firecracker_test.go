package firecracker

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionLineWithFirecrackerExitDiagnostic(t *testing.T) {
	// The pinned Rocky probe prints a version line followed by an exit log.
	output := "Firecracker v1.16.1\n\n2026-09-07T00:01:02.123456789 [anonymous-instance:main:INFO] Firecracker exiting successfully. exit_code=0\n"
	for _, expected := range []string{"1.16.1", "v1.16.1", ""} {
		version, err := parseVersionOutput([]byte(output), expected)
		if err != nil || version != "Firecracker v1.16.1" {
			t.Fatalf("expected=%q version=%q error=%v", expected, version, err)
		}
	}
	if _, err := parseVersionOutput([]byte("Firecracker v1.16.1\n"), "1.16.1"); err != nil {
		t.Fatal(err)
	}
}

func TestVersionOutputRejectsNoiseAndWrongVersion(t *testing.T) {
	for _, output := range []string{
		"Firecracker v1.15.0\n\nFirecracker v1.16.1\n",
		"diagnostic first\nFirecracker v1.16.1\n",
		"\nFirecracker v1.16.1\n",
		" Firecracker v1.16.1\n",
		"Firecracker v1.16.1 \n",
		"Firecracker v1.16.1",
		"Firecracker v1.16.1 extra\n",
		"Firecracker vnot-a-version\n",
	} {
		if _, err := parseVersionOutput([]byte(output), "1.16.1"); err == nil {
			t.Fatalf("accepted %q", output)
		}
	}
}

func TestVersionCaptureContinuesDrainingAfterQuota(t *testing.T) {
	w := &cappedWriter{limit: 4096}
	for _, chunk := range []string{"Firecracker v1.16.1\n", strings.Repeat("x", 8192), "final log\n"} {
		n, err := w.Write([]byte(chunk))
		if err != nil || n != len(chunk) {
			t.Fatalf("capture stopped draining: n=%d err=%v", n, err)
		}
	}
	if !w.overflow || len(w.dst) != w.limit {
		t.Fatal("version capture quota was not enforced")
	}
}

func TestConfigHasOneDiskAndNoNetwork(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	o := ConfigOptions{KernelPath: filepath.Join(root, "k"), RootFSPath: filepath.Join(root, "d"), SocketPath: filepath.Join(root, "s"), RunID: "run-123", Nonce: "nonce_456", CID: 3, VCPUCount: 1, MemoryMiB: 512}
	c, err := BuildConfig(o)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "network") || strings.Contains(string(b), "no-seccomp") {
		t.Fatal(string(b))
	}
	if len(c.Drives) != 1 || c.Drives[0].ReadOnly || !strings.Contains(c.BootSource.BootArgs, "systemd.unit=epoch.target epoch.run=run-123 epoch.nonce=nonce_456 epoch.cid=3") {
		t.Fatal(c)
	}
	o.Nonce = "x init=/bin/sh"
	if _, err := BuildConfig(o); err == nil {
		t.Fatal("accepted boot argument injection")
	}
}
