package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestBoundedFrames(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader("OK 12345\n{\"value\":1}\n"), 64)
	first, err := ReadLine(r, 128)
	if err != nil || string(first) != "OK 12345" {
		t.Fatalf("handshake=%q err=%v", first, err)
	}
	second, err := ReadLine(r, MaxFrame)
	if err != nil || string(second) != "{\"value\":1}" {
		t.Fatalf("buffered response=%q err=%v", second, err)
	}
	if _, err = ReadLine(bufio.NewReader(strings.NewReader("12345\n")), 5); err == nil {
		t.Fatal("oversized line accepted")
	}
	if _, err = ReadLine(bufio.NewReader(strings.NewReader("partial")), MaxFrame); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	if _, err = ReadLine(bufio.NewReader(bytes.NewReader(nil)), MaxFrame); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}
func TestManifestRejectsPrivilegeAndShells(t *testing.T) {
	m := WorkloadManifest{APIVersion: WorkloadVersion, ID: "probe", Version: "1", UID: 10001, GID: 10001, WorkingDir: "/var/lib/probe", Actions: map[string]Command{"observe": {Argv: []string{"/usr/bin/probe"}}}}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	m.UID = 0
	if err := m.Validate(); err == nil {
		t.Fatal("root accepted")
	}
	m.UID = 10001
	m.Actions["observe"] = Command{Argv: []string{"/bin/sh", "-c", "echo x"}}
	if err := m.Validate(); err == nil {
		t.Fatal("shell accepted")
	}
}
