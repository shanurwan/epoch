// Package evidence writes private, bounded records and atomic final reports.
package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/shanurwan/epoch/internal/safefs"
)

const MaxBytes int64 = 8 << 20
const ReportReserve int64 = 2 << 20
const MetadataReserve int64 = 64 << 10
const MaxEvents = 4096

type Recorder struct {
	mu                      sync.Mutex
	dir                     string
	start                   time.Time
	used                    int64
	seq                     int
	err                     error
	cancel                  func()
	files                   []*os.File
	events, clocks, actions io.Writer
}

type Event struct {
	APIVersion string    `json:"api_version"`
	Sequence   int       `json:"sequence"`
	Stage      string    `json:"stage"`
	ElapsedNS  int64     `json:"elapsed_ns"`
	HostUTC    time.Time `json:"host_utc"`
	Kind       string    `json:"kind"`
	Data       any       `json:"data,omitempty"`
}

func New(dir string, cancel func()) (*Recorder, error) {
	r := &Recorder{dir: dir, start: time.Now(), cancel: cancel}
	var err error
	if r.events, err = r.OpenLog("events.ndjson"); err != nil {
		return nil, err
	}
	if r.clocks, err = r.OpenLog("clock-observations.ndjson"); err != nil {
		r.Close()
		return nil, err
	}
	if r.actions, err = r.OpenLog("actions.ndjson"); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func (r *Recorder) failLocked(err error) {
	if r.err == nil {
		r.err = err
		if r.cancel != nil {
			r.cancel()
		}
	}
}
func (r *Recorder) Err() error { r.mu.Lock(); defer r.mu.Unlock(); return r.err }

// Log writers retain bounded bytes, then keep consuming input until termination.
// The error is delivered out of band so a full pipe cannot deadlock VM cleanup.
type drainWriter struct {
	r *Recorder
	f *os.File
}

func (w drainWriter) Write(p []byte) (int, error) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	if w.r.err != nil {
		return len(p), nil
	}
	if w.r.used+int64(len(p)) > MaxBytes-ReportReserve-MetadataReserve {
		w.r.failLocked(fmt.Errorf("evidence quota exceeded"))
		return len(p), nil
	}
	n, err := w.f.Write(p)
	w.r.used += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.r.failLocked(fmt.Errorf("evidence storage: %w", err))
	}
	return len(p), nil
}

func (r *Recorder) OpenLog(name string) (io.Writer, error) {
	if filepath.Base(name) != name {
		return nil, fmt.Errorf("invalid evidence filename")
	}
	f, err := os.OpenFile(filepath.Join(r.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	r.files = append(r.files, f)
	return drainWriter{r, f}, nil
}

func (r *Recorder) Event(stage, kind string, data any) error {
	r.mu.Lock()
	if r.seq >= MaxEvents {
		r.failLocked(fmt.Errorf("event quota exceeded"))
		err := r.err
		r.mu.Unlock()
		return err
	}
	r.seq++
	seq := r.seq
	r.mu.Unlock()
	return r.line(r.events, Event{APIVersion: "epoch-event/v1", Sequence: seq, Stage: stage, Kind: kind, Data: data, ElapsedNS: time.Since(r.start).Nanoseconds(), HostUTC: time.Now().UTC()})
}
func (r *Recorder) Clocks(record any) error { return r.line(r.clocks, record) }
func (r *Recorder) Action(record any) error { return r.line(r.actions, record) }
func (r *Recorder) line(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	if err != nil {
		return err
	}
	return r.Err()
}

func (r *Recorder) Save(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return r.SaveRaw(name, append(b, '\n'))
}
func (r *Recorder) SaveRaw(name string, b []byte) error {
	if filepath.Base(name) != name {
		return fmt.Errorf("invalid evidence filename")
	}
	r.mu.Lock()
	if r.err != nil {
		err := r.err
		r.mu.Unlock()
		return err
	}
	if r.used+int64(len(b)) > MaxBytes-ReportReserve-MetadataReserve {
		r.failLocked(fmt.Errorf("evidence quota exceeded"))
		err := r.err
		r.mu.Unlock()
		return err
	}
	r.used += int64(len(b))
	r.mu.Unlock()
	// JSON files are atomically replaced; raw scenario bytes retain their exact input.
	err := atomicBytes(filepath.Join(r.dir, name), b)
	if err != nil {
		r.mu.Lock()
		r.failLocked(err)
		r.mu.Unlock()
	}
	return err
}

func (r *Recorder) Final(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if int64(len(b)) > ReportReserve {
		return fmt.Errorf("final report reserve exceeded; report not saved")
	}
	return atomicBytes(filepath.Join(r.dir, "report.json"), b)
}

func (r *Recorder) Close() error {
	var errs []error
	for _, f := range r.files {
		if e := f.Sync(); e != nil {
			errs = append(errs, e)
		}
		if e := f.Close(); e != nil {
			errs = append(errs, e)
		}
	}
	r.files = nil
	return errors.Join(errs...)
}

func atomicBytes(path string, b []byte) error {
	if !json.Valid(b) {
		return fmt.Errorf("refusing to publish invalid JSON")
	}
	return safefs.AtomicWrite(path, b)
}

// Quote escapes control sequences from workload content before terminal display.
func Quote(s string) string { return strconv.QuoteToASCII(s) }
