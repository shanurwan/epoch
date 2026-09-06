package guestclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/shanurwan/epoch/internal/protocol"
)

func pipeClient(t *testing.T) (*Client, net.Conn) {
	t.Helper()
	host, guest := net.Pipe()
	c := &Client{conn: host, reader: bufio.NewReader(host), runID: "run-test", nonce: strings.Repeat("ab", 32)}
	t.Cleanup(func() { host.Close(); guest.Close() })
	return c, guest
}
func TestLostActionResponseIsNeverRetried(t *testing.T) {
	c, guest := pipeClient(t)
	seen := make(chan string, 1)
	go func() {
		b, _ := protocol.ReadLine(bufio.NewReader(guest), protocol.MaxFrame)
		seen <- string(b)
		guest.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := c.Call(ctx, "action.exec", protocol.ActionRequest{Action: "observe"}, nil)
	var unknown *OutcomeUnknown
	if !errors.As(err, &unknown) {
		t.Fatalf("want unknown, got %v", err)
	}
	if !strings.Contains(<-seen, "action.exec") {
		t.Fatal("missing action request")
	}
	if err = c.Call(ctx, "action.exec", nil, nil); err == nil {
		t.Fatal("broken connection reused")
	}
}
func TestResponseIdentityAndStrictJSON(t *testing.T) {
	for _, tc := range []struct{ name, body string }{{"correct", `{"ok":true}`}, {"duplicate", `{"ok":true,"ok":false}`}} {
		t.Run(tc.name, func(t *testing.T) {
			c, guest := pipeClient(t)
			go func() {
				b, _ := protocol.ReadLine(bufio.NewReader(guest), protocol.MaxFrame)
				var req protocol.Request
				_ = json.Unmarshal(b, &req)
				_ = protocol.WriteFrame(guest, protocol.Response{Version: protocol.Version, ID: req.ID, Op: req.Op, RunID: req.RunID, Nonce: req.Nonce, Result: json.RawMessage(tc.body)})
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var out struct {
				OK bool `json:"ok"`
			}
			err := c.Call(ctx, "hello", nil, &out)
			if (err == nil) != (tc.name == "correct") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
func TestCancelledReadTerminates(t *testing.T) {
	c, guest := pipeClient(t)
	go func() { _, _ = protocol.ReadLine(bufio.NewReader(guest), protocol.MaxFrame) }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.Call(ctx, "hello", nil, nil); err == nil {
		t.Fatal("missing deadline error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("deadline was not enforced")
	}
}
