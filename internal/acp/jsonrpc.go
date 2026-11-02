// Package acp is a client for the Agent Client Protocol, version 1
// (JSON-RPC 2.0 over newline-delimited stdio), as specified by
// github.com/agentclientprotocol (schema/v1/schema.json in
// @agentclientprotocol/sdk 2.x). Spine is the client; the agent (for
// example Claude Code through @agentclientprotocol/claude-agent-acp) is the
// server for initialize/session/* and the client for session/update
// notifications and session/request_permission requests.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// JSON-RPC error codes used here.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("acp: rpc error %d: %s", e.Code, e.Message) }

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Handler answers requests and receives notifications from the peer.
type Handler interface {
	// Request returns a result or an *RPCError.
	Request(ctx context.Context, method string, params json.RawMessage) (any, *RPCError)
	Notify(ctx context.Context, method string, params json.RawMessage)
}

// ErrClosed is returned for calls on a connection whose peer went away.
var ErrClosed = errors.New("acp: connection closed")

// MaxLine bounds one JSON-RPC message.
const MaxLine = 64 << 20

// Conn is a bidirectional JSON-RPC connection.
type Conn struct {
	w       io.Writer
	wmu     sync.Mutex
	h       Handler
	mu      sync.Mutex
	next    int64
	pending map[string]chan message
	closed  chan struct{}
	err     error
	// OnActivity is called for every message received (stall detection).
	OnActivity func()
}

// NewConn creates a connection; call Serve to start reading.
func NewConn(w io.Writer, h Handler) *Conn {
	return &Conn{w: w, h: h, pending: map[string]chan message{}, closed: make(chan struct{})}
}

// Serve reads messages until r ends. Requests from the peer are handled
// concurrently; notifications are delivered in order.
func (c *Conn) Serve(ctx context.Context, r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxLine)
	var err error
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if c.OnActivity != nil {
			c.OnActivity()
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			continue // not a protocol line (adapters may log to stdout); ignore
		}
		switch {
		case m.Method != "" && len(m.ID) > 0: // request
			go c.answer(ctx, m)
		case m.Method != "": // notification
			c.h.Notify(ctx, m.Method, m.Params)
		case len(m.ID) > 0: // response
			c.mu.Lock()
			ch := c.pending[string(m.ID)]
			delete(c.pending, string(m.ID))
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	err = sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.mu.Lock()
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.closed)
	return err
}

// Done is closed when the peer's output ends.
func (c *Conn) Done() <-chan struct{} { return c.closed }

func (c *Conn) answer(ctx context.Context, m message) {
	res, rerr := c.h.Request(ctx, m.Method, m.Params)
	out := message{JSONRPC: "2.0", ID: m.ID}
	if rerr != nil {
		out.Error = rerr
	} else {
		b, err := json.Marshal(res)
		if err != nil {
			out.Error = &RPCError{Code: CodeInternal, Message: err.Error()}
		} else {
			out.Result = b
		}
	}
	c.send(out)
}

func (c *Conn) send(m message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(b)
	return err
}

// Call sends a request and waits for its response.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return ErrClosed
	}
	c.next++
	id := json.RawMessage(strconv.FormatInt(c.next, 10))
	ch := make(chan message, 1)
	c.pending[string(id)] = ch
	c.mu.Unlock()
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := c.send(message{ID: id, Method: method, Params: p}); err != nil {
		c.mu.Lock()
		delete(c.pending, string(id))
		c.mu.Unlock()
		return fmt.Errorf("acp: send %s: %w", method, err)
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if m.Error != nil {
			return m.Error
		}
		if result != nil && len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, result); err != nil {
				return fmt.Errorf("acp: %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, string(id))
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.send(message{Method: method, Params: p})
}
