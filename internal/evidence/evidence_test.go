package evidence

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRawInputAndQuotaDrain(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cancelled := false
	r, err := New(dir, func() { cancelled = true })
	if err != nil {
		t.Fatal(err)
	}
	input := []byte("{ \"exact\": [1, 2] }\n")
	if err = r.SaveRaw("scenario.input.json", input); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "scenario.input.json"))
	if err != nil || !bytes.Equal(got, input) {
		t.Fatalf("input changed: %q %v", got, err)
	}
	w, err := r.OpenLog("vmm.log")
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("a"), 1<<20)
	for i := 0; i < 10; i++ {
		n, err := w.Write(chunk)
		if err != nil || n != len(chunk) {
			t.Fatalf("reader would stop draining: %d %v", n, err)
		}
	}
	if !cancelled || r.Err() == nil {
		t.Fatal("overflow did not cancel")
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	if err = r.Final(map[string]string{"execution_status": "ERROR", "cleanup_status": "COMPLETE"}); err != nil {
		t.Fatalf("error report reserve unusable: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	var total int64
	for _, entry := range entries {
		st, _ := entry.Info()
		total += st.Size()
	}
	if total > MaxBytes {
		t.Fatalf("disk quota exceeded: %d", total)
	}
}
func TestPartialEventsNeverComplete(t *testing.T) {
	for _, tail := range []string{"{\"sequence\":2}", "{\"sequence\":"} {
		r, partial, err := ReadPartial(strings.NewReader("{\"sequence\":1}\n" + tail))
		if err != nil || !partial || len(r) != 1 {
			t.Fatalf("%d %t %v", len(r), partial, err)
		}
	}
	r, p, e := ReadPartial(strings.NewReader("{\"sequence\":1}\n"))
	if e != nil || p || len(r) != 1 {
		t.Fatal("complete event stream misread")
	}
}
func TestTextEscapesTerminalControl(t *testing.T) {
	if strings.ContainsRune(Quote("\x1b[31mred\n"), '\x1b') {
		t.Fatal("terminal escape not escaped")
	}
}
