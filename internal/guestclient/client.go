package guestclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shanurwan/epoch/internal/jsonutil"
	"github.com/shanurwan/epoch/internal/protocol"
)

type Client struct {
	conn         net.Conn
	reader       *bufio.Reader
	runID, nonce string
	mu           sync.Mutex
	next         uint64
	broken       bool
}

// OutcomeUnknown means a state-changing request may have executed. Retrying it
// would risk duplicating a workload effect.
type OutcomeUnknown struct{ Err error }

func (e *OutcomeUnknown) Error() string { return "guest outcome unknown: " + e.Err.Error() }
func (e *OutcomeUnknown) Unwrap() error { return e.Err }
func Dial(ctx context.Context, socket string, port uint32, runID, nonce string) (*Client, error) {
	if !protocol.ValidID(runID) || len(nonce) < 32 || port == 0 {
		return nil, errors.New("invalid guest connection identity")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, reader: bufio.NewReader(conn), runID: runID, nonce: nonce}
	finish := c.deadline(ctx)
	defer finish()
	if _, err = fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, err
	}
	line, err := protocol.ReadLine(c.reader, 128)
	if err != nil {
		conn.Close()
		return nil, err
	}
	parts := strings.Split(string(line), " ")
	if len(parts) != 2 || parts[0] != "OK" {
		conn.Close()
		return nil, errors.New("invalid Firecracker vsock handshake")
	}
	assigned, e := strconv.ParseUint(parts[1], 10, 32)
	for _, r := range parts[1] {
		if r < '0' || r > '9' {
			e = errors.New("host port must contain decimal digits")
		}
	}
	if e != nil || assigned == 0 {
		conn.Close()
		return nil, errors.New("invalid assigned vsock host port")
	}
	return c, nil
}
func (c *Client) Close() error { return c.conn.Close() }
func (c *Client) deadline(ctx context.Context) func() {
	d := time.Now().Add(30 * time.Second)
	if v, ok := ctx.Deadline(); ok {
		d = v
	}
	_ = c.conn.SetDeadline(d)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetDeadline(time.Now()); close(done) })
	return func() {
		if !stop() {
			<-done
		}
		_ = c.conn.SetDeadline(time.Time{})
	}
}
func (c *Client) Call(ctx context.Context, op string, payload any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return errors.New("guest connection unusable after protocol failure")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch op {
	case "hello", "clock.read", "clock.set", "action.exec", "service.start", "service.stop", "shutdown":
	default:
		return errors.New("unsupported guest operation")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if payload == nil {
		b = []byte("{}")
	}
	c.next++
	id := strconv.FormatUint(c.next, 10)
	req := protocol.Request{Version: protocol.Version, ID: id, Op: op, RunID: c.runID, Nonce: c.nonce, Payload: b}
	finish := c.deadline(ctx)
	defer finish()
	fail := func(e error) error {
		c.broken = true
		_ = c.conn.Close()
		if op != "hello" && op != "clock.read" {
			return &OutcomeUnknown{e}
		}
		return e
	}
	if err = protocol.WriteFrame(c.conn, req); err != nil {
		return fail(err)
	}
	frame, err := protocol.ReadLine(c.reader, protocol.MaxFrame)
	if err != nil {
		return fail(err)
	}
	var resp protocol.Response
	if err = jsonutil.Decode(frame, &resp); err != nil {
		return fail(fmt.Errorf("decode response: %w", err))
	}
	if resp.Version != protocol.Version || resp.ID != id || resp.Op != op || resp.RunID != c.runID || resp.Nonce != c.nonce {
		return fail(errors.New("guest response identity mismatch"))
	}
	if (resp.Error == nil) == (len(resp.Result) == 0) {
		return fail(errors.New("guest response must contain exactly one result or error"))
	}
	if string(resp.Result) == "null" {
		return fail(errors.New("guest result must not be null"))
	}
	if resp.Error != nil {
		if resp.Error.Code == "" || resp.Error.Message == "" {
			return fail(errors.New("invalid guest error envelope"))
		}
		return resp.Error
	}
	if result != nil {
		if err = jsonutil.Decode(resp.Result, result); err != nil {
			return fail(fmt.Errorf("decode result: %w", err))
		}
	}
	return nil
}
