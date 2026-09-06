package artifact

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateCopyPreservesBase(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	data := bytes.Repeat([]byte("trusted bytes"), 20000)
	if err := os.WriteFile(base, data, 0600); err != nil {
		t.Fatal(err)
	}
	meta := File{Path: base, SizeBytes: int64(len(data)), SHA256: Hash(data)}
	dest := filepath.Join(dir, "private")
	if err := CopyPrivate(context.Background(), meta, dest); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(dest, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte("modified"), 0)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyFile(context.Background(), meta); err != nil {
		t.Fatalf("base modified by private write: %v", err)
	}
	if err = CopyPrivate(context.Background(), meta, dest); err == nil {
		t.Fatal("existing private file replaced")
	}
}
func TestCopyCancellationAndMismatchNeverPublish(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "digest", true: "cancel"}[cancelled], func(t *testing.T) {
			dir := t.TempDir()
			base := filepath.Join(dir, "base")
			if err := os.WriteFile(base, []byte("base"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			dest := filepath.Join(dir, "private")
			err := CopyPrivate(ctx, File{Path: base, SizeBytes: 4, SHA256: Hash([]byte("wrong"))}, dest)
			if err == nil {
				t.Fatal("expected failure")
			}
			if _, err = os.Stat(dest); !os.IsNotExist(err) {
				t.Fatal("partial disk published")
			}
			if _, err = os.Stat(dest + ".partial"); !os.IsNotExist(err) {
				t.Fatal("partial file retained after normal error")
			}
		})
	}
}

type failedWriter struct{ err error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.err }
func TestCopyStorageError(t *testing.T) {
	want := errors.New("ENOSPC fixture")
	_, err := copyContext(context.Background(), failedWriter{want}, bytes.NewReader([]byte("state")))
	if !errors.Is(err, want) {
		t.Fatalf("lost write cause: %v", err)
	}
	_, err = copyContext(context.Background(), shortWriter{}, bytes.NewReader([]byte("state")))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
}

type shortWriter struct{}

func (shortWriter) Write([]byte) (int, error) { return 0, nil }
