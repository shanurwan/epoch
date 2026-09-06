package safefs

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWritePreservesBytes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scenario.input.json")
	input := []byte("{\n  \"x\" : 1 }\n\n")
	if err := AtomicWrite(path, input); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, input) {
		t.Fatalf("bytes changed: %q", got)
	}
}

func TestAtomicPublicationAndTraversal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "report.json")
	if err := AtomicJSON(path, map[string]string{"status": "first"}); err != nil {
		t.Fatal(err)
	}
	if err := AtomicJSON(path, map[string]string{"status": "complete"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "complete" {
		t.Fatal(got)
	}
	for _, name := range []string{"../escape", "..", "a/b", "a\\b", ""} {
		if _, err := OwnedChild(dir, name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}

func TestRejectSymlinkComponents(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ValidateNoSymlinks(link); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := EnsurePrivateDir(filepath.Join(link, "run")); err == nil {
		t.Fatal("created through symlink")
	}
}
