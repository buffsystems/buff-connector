// Package plugin runs connector plugins as separate processes and speaks to them
// over stdio.
//
// The wire format is JSON-RPC 2.0, one message per line — the same framing MCP
// uses for stdio servers, so a plugin is an MCP server with a few extra methods
// in the buff/* namespace rather than a shape of its own.
//
// Both directions carry requests. The host calls the plugin (initialize,
// tools/list, tools/call, buff/poll …); the plugin calls the host (buff/http,
// buff/log). That second direction is what lets a plugin reach a customer system
// without ever holding the credential: it asks the host to make the request.
package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/buffsystems/buff-connector/internal/problem"
)

// DefaultMaxFrame bounds a single message. A tool result can be large (a ticket
// with attachments), so the ceiling is generous; past it we treat the peer as
// broken and fail loudly rather than dropping or truncating anything.
const DefaultMaxFrame = 8 << 20

// Handler answers a request that came FROM the peer. Returning an error sends a
// JSON-RPC error back; it does not break the channel.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.Number    `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.Number     `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	// local is why the channel failed a call ITSELF, kept as the error it is
	// so its code survives to the report; Error carries the same as text.
	local error
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

// Conn is one JSON-RPC channel. It is safe for concurrent use.
type Conn struct {
	w        io.Writer
	handler  Handler
	maxFrame int

	mu      sync.Mutex
	writeMu sync.Mutex
	nextID  int64
	pending map[string]chan response
	closed  bool
	closeCh chan struct{}
	err     error
}

// NewConn starts serving the channel immediately: the reader runs until the
// stream ends, the peer sends something unparseable at the frame level, or Close
// is called.
func NewConn(r io.Reader, w io.Writer, h Handler) *Conn {
	return newConn(r, w, h, DefaultMaxFrame)
}

// newConn exists so tests can drive the frame limit without exporting a knob
// nobody outside this package needs.
func newConn(r io.Reader, w io.Writer, h Handler, maxFrame int) *Conn {
	c := &Conn{
		w:        w,
		handler:  h,
		maxFrame: maxFrame,
		pending:  make(map[string]chan response),
		closeCh:  make(chan struct{}),
	}
	go c.read(r)
	return c
}

// Closed reports whether the channel has failed or been closed.
func (c *Conn) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Err is the reason the channel ended, if it ended badly.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the channel and releases everyone waiting on it.
func (c *Conn) Close() error { c.fail(nil); return nil }

// fail tears the channel down once, releasing every pending call with the reason.
func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed, c.err = true, err
	pending := c.pending
	c.pending = map[string]chan response{}
	close(c.closeCh)
	c.mu.Unlock()

	if err == nil {
		err = problem.New("channel_closed")
	}
	for id, ch := range pending {
		ch <- response{ID: json.Number(id), Error: &rpcError{Code: -32000, Message: err.Error()}, local: err}
	}
}

// read consumes whole lines. A line that is not a JSON-RPC message is IGNORED on
// purpose: plugins log to stdout, and that noise must not cost us the channel. A
// line past the frame limit is different — that is a broken or hostile peer, and
// we end the channel so the supervisor restarts it.
func (c *Conn) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	// Scanner takes the LARGER of max and the starting buffer's capacity, so the
	// starting buffer must never exceed the limit we mean to enforce.
	start := 64 << 10
	if c.maxFrame < start {
		start = c.maxFrame
	}
	sc.Buffer(make([]byte, 0, start), c.maxFrame)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue // log output, banners, blank lines
		}
		var probe struct {
			ID     *json.Number    `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			continue // looked like JSON, was not: still just noise
		}
		switch {
		case probe.Method != "":
			c.serve(probe.ID, probe.Method, probe.Params)
		case probe.ID != nil:
			c.deliver(response{ID: *probe.ID, Result: probe.Result, Error: probe.Error})
		}
	}
	err := sc.Err()
	if errors.Is(err, bufio.ErrTooLong) {
		err = problem.New("frame_too_large", c.maxFrame)
	} else if err == nil {
		err = problem.New("plugin_closed")
	}
	c.fail(err)
}

// deliver hands a response to whoever is waiting for that id.
func (c *Conn) deliver(res response) {
	c.mu.Lock()
	ch, ok := c.pending[res.ID.String()]
	if ok {
		delete(c.pending, res.ID.String())
	}
	c.mu.Unlock()
	if ok {
		ch <- res
	}
}

// serve answers a request from the peer. Each one runs on its own goroutine so a
// slow host handler cannot stall the reader.
func (c *Conn) serve(id *json.Number, method string, params json.RawMessage) {
	go func() {
		if c.handler == nil {
			if id != nil {
				c.writeMessage(response{JSONRPC: "2.0", ID: *id,
					Error: &rpcError{Code: -32601, Message: "method not supported: " + method}})
			}
			return
		}
		result, err := c.handler(context.Background(), method, params)
		if id == nil {
			return // notification: no reply expected
		}
		if err != nil {
			c.writeMessage(response{JSONRPC: "2.0", ID: *id,
				Error: &rpcError{Code: -32000, Message: err.Error()}})
			return
		}
		raw, mErr := json.Marshal(result)
		if mErr != nil {
			c.writeMessage(response{JSONRPC: "2.0", ID: *id,
				Error: &rpcError{Code: -32603, Message: "host result not encodable: " + mErr.Error()}})
			return
		}
		c.writeMessage(response{JSONRPC: "2.0", ID: *id, Result: raw})
	}()
}

func (c *Conn) writeMessage(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, _ = c.w.Write(append(b, '\n'))
}

// Notify sends a message that expects no answer. MCP requires one after the
// handshake (notifications/initialized); a server built on the published SDKs
// refuses every request until it arrives.
func (c *Conn) Notify(method string, params any) error {
	c.mu.Lock()
	closed, err := c.closed, c.err
	c.mu.Unlock()
	if closed {
		if err == nil {
			err = problem.New("channel_closed")
		}
		return err
	}
	var raw json.RawMessage
	if params != nil {
		b, mErr := json.Marshal(params)
		if mErr != nil {
			return problem.Wrap("encode_params", mErr)
		}
		raw = b
	}
	c.writeMessage(request{JSONRPC: "2.0", Method: method, Params: raw})
	return nil
}

// Call sends a request and waits for its answer, the context bounding the wait.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	return c.call(ctx, id, method, params, result)
}

func (c *Conn) call(ctx context.Context, id int64, method string, params, result any) error {
	key := json.Number(fmt.Sprint(id))
	ch := make(chan response, 1)

	c.mu.Lock()
	if c.closed {
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = problem.New("channel_closed")
		}
		return err
	}
	c.pending[key.String()] = ch
	c.mu.Unlock()

	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return problem.Wrap("encode_params", err)
		}
		raw = b
	}
	c.writeMessage(request{JSONRPC: "2.0", ID: &key, Method: method, Params: raw})

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, key.String())
		c.mu.Unlock()
		return ctx.Err()
	case res := <-ch:
		if res.local != nil {
			return res.local
		}
		if res.Error != nil {
			return res.Error
		}
		if result == nil || len(res.Result) == 0 {
			return nil
		}
		return json.Unmarshal(res.Result, result)
	}
}
