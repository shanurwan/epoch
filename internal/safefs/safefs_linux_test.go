//go:build linux

package safefs

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnedSocketUnderPrivateRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vsock.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := os.Chmod(path, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := OwnedChild(dir, "vsock.sock"); err != nil {
		t.Fatal(err)
	}
}

func TestExclusiveOperatorLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := AcquireLock(dir); err == nil {
		second.Close()
		t.Fatal("admitted concurrent lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}

func TestRejectPublicRuntimeRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(dir); err == nil {
		t.Fatal("accepted public runtime root")
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0755 {
		t.Fatal("changed existing directory mode")
	}
}
