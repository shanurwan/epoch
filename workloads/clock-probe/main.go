// clock-probe is a standalone fixture, not an application clock abstraction.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const markerPath = "marker.json"

func emit(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
func main() {
	code, err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
	os.Exit(code)
}
func run(args []string) (int, error) {
	if len(args) != 1 {
		return 0, errors.New("one declared fixture command is required")
	}
	switch args[0] {
	case "observe":
		realtime, monotonic, err := observe()
		if err != nil {
			return 0, err
		}
		return 0, emit(map[string]any{"realtime": realtime, "monotonic_ns": monotonic, "year_utc": realtime.Year(), "uid": os.Geteuid()})
	case "write-marker":
		b, err := io.ReadAll(io.LimitReader(os.Stdin, (64<<10)+1))
		if err != nil {
			return 0, err
		}
		if len(b) > 64<<10 {
			return 0, errors.New("marker input quota exceeded")
		}
		var input struct {
			Value string `json:"value"`
		}
		d := json.NewDecoder(strings.NewReader(string(b)))
		d.DisallowUnknownFields()
		if err = d.Decode(&input); err != nil {
			return 0, err
		}
		if err = d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return 0, errors.New("trailing marker input")
		}
		if len(input.Value) > 4096 {
			return 0, errors.New("marker value too long")
		}
		data, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		f, err := os.OpenFile(markerPath+".tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return 0, err
		}
		_, writeErr := f.Write(data)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
			return 0, err
		}
		if err = os.Rename(markerPath+".tmp", markerPath); err != nil {
			return 0, err
		}
		dir, err := os.Open(".")
		if err != nil {
			return 0, err
		}
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return 0, err
		}
		return 0, emit(input)
	case "read-marker":
		b, err := os.ReadFile(markerPath)
		if err != nil {
			return 0, err
		}
		if !json.Valid(b) {
			return 0, errors.New("malformed persistent marker")
		}
		return 0, emit(json.RawMessage(b))
	case "sleep":
		time.Sleep(2 * time.Second)
		return 0, emit(map[string]bool{"slept": true})
	case "hang":
		for {
			time.Sleep(time.Hour)
		}
	case "large-output":
		_, err := io.WriteString(os.Stdout, strings.Repeat("x", (64<<10)+1))
		return 0, err
	case "large-stderr":
		_, err := io.WriteString(os.Stderr, strings.Repeat("x", (64<<10)+1))
		if err != nil {
			return 0, err
		}
		return 0, emit(map[string]bool{"emitted": true})
	case "malformed-json":
		_, err := io.WriteString(os.Stdout, "{malformed\n")
		return 0, err
	case "nonzero":
		return 7, emit(map[string]bool{"expected_nonzero": true})
	case "service":
		if err := os.WriteFile("service.ready", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			return 0, err
		}
		defer os.Remove("service.ready")
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
		defer signal.Stop(stop)
		<-stop
		return 0, nil
	case "service-ready":
		_, err := os.Stat("service.ready")
		if err != nil {
			return 1, emit(map[string]bool{"ready": false})
		}
		return 0, emit(map[string]bool{"ready": true})
	default:
		return 0, errors.New("unknown fixture command")
	}
}
