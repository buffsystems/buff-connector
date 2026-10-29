package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/buffsystems/buff-connector/internal/problem"
)

// A remote MCP server (#351) is spoken to over the published Streamable HTTP
// transport: every message is a POST to one address, the server may answer
// with a JSON body or with an event stream carrying the answer, and the
// session it names in the handshake is quoted on every request after. A
// session the server no longer knows ends the channel — the supervisor then
// does what it does for a process that died: starts over.
//
// What the connector deliberately does not do here is open the server-to-
// client stream (GET): the pipeline only ever asks; nothing a server pushes
// unasked has anywhere to go.

// RemoteSpec is where a remote server is and what to send it.
type RemoteSpec struct {
	URL     string
	Headers map[string]string
}

// channel is what an Instance needs from either transport.
type channel interface {
	Call(ctx context.Context, method string, params, result any) error
	Notify(method string, params any) error
	Close() error
	Closed() bool
	Err() error
	Done() <-chan struct{}
}

// Done reports when the stdio channel ended, for the supervisor.
func (c *Conn) Done() <-chan struct{} { return c.closeCh }

type httpChannel struct {
	spec    RemoteSpec
	client  *http.Client
	handler Handler

	mu      sync.Mutex
	nextID  int64
	session string
	closed  bool
	err     error
	done    chan struct{}
}

func newHTTPChannel(spec RemoteSpec, h Handler) *httpChannel {
	return &httpChannel{spec: spec, client: &http.Client{}, handler: h, done: make(chan struct{})}
}

func (c *httpChannel) Done() <-chan struct{} { return c.done }

func (c *httpChannel) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *httpChannel) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *httpChannel) Close() error { c.fail(nil); return nil }

func (c *httpChannel) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed, c.err = true, err
	close(c.done)
}

// notifyTimeout bounds a notification, which has no call timeout of its own.
const notifyTimeout = 30 * time.Second

func (c *httpChannel) Notify(method string, params any) error {
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	_, err := c.post(ctx, request{JSONRPC: "2.0", Method: method, Params: mustRaw(params)}, "")
	return err
}

func (c *httpChannel) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.closed {
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = problem.New("channel_closed")
		}
		return err
	}
	c.nextID++
	id := json.Number(fmt.Sprint(c.nextID))
	c.mu.Unlock()

	res, err := c.post(ctx, request{JSONRPC: "2.0", ID: &id, Method: method, Params: mustRaw(params)}, id.String())
	if err != nil {
		return err
	}
	if res == nil {
		// 202 to a REQUEST: the server took it and answered nothing. It would
		// arrive on the stream this connector does not open, so it is an error
		// here, not a wait.
		return problem.New("remote_no_answer", method)
	}
	if res.Error != nil {
		return res.Error
	}
	if result != nil && len(res.Result) > 0 {
		return json.Unmarshal(res.Result, result)
	}
	return nil
}

func mustRaw(params any) json.RawMessage {
	if params == nil {
		return nil
	}
	b, _ := json.Marshal(params)
	return b
}

// post sends one message and, when it was a request, waits for its answer in
// whichever form the server chose.
func (c *httpChannel) post(ctx context.Context, msg request, wantID string) (*response, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.spec.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", MCPProtocolVersion)
	for k, v := range c.spec.Headers {
		req.Header.Set(k, v)
	}
	c.mu.Lock()
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	c.mu.Unlock()

	res, err := c.client.Do(req)
	if err != nil {
		// The server cannot be reached at all. That is what a dead process is
		// to the stdio channel: the channel ends, the supervisor backs off and
		// tries the handshake again — and the state says so, instead of a
		// "running" that answers nothing.
		err = problem.Wrap("remote_unreachable", err, msg.Method)
		c.fail(err)
		return nil, err
	}
	defer res.Body.Close()

	if sid := res.Header.Get("Mcp-Session-Id"); sid != "" {
		c.mu.Lock()
		c.session = sid
		c.mu.Unlock()
	}
	switch {
	case res.StatusCode == http.StatusNotFound && msg.Method != "initialize":
		// The session is gone. Nothing on this channel can succeed any more;
		// say so once and let the supervisor start over.
		err := problem.New("remote_session_gone", msg.Method)
		c.fail(err)
		return nil, err
	case res.StatusCode == http.StatusAccepted, res.StatusCode == http.StatusNoContent:
		return nil, nil
	case res.StatusCode < 200 || res.StatusCode > 299:
		tail, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, problem.New("remote_status", msg.Method, res.StatusCode, strings.TrimSpace(string(tail)))
	}
	if wantID == "" {
		return nil, nil
	}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		return c.readStream(res.Body, wantID)
	}
	var out response
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, problem.Wrap("remote_unreadable", err, msg.Method)
	}
	return &out, nil
}

// readStream reads server-sent events until the answer to our request
// arrives. Other messages on the way — a server's own request, a log
// notification — are handled the way the stdio reader handles them: a request
// is answered, a notification is dropped.
func (c *httpChannel) readStream(r io.Reader, wantID string) (*response, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), DefaultMaxFrame)
	var data []string
	flush := func() (*response, bool) {
		if len(data) == 0 {
			return nil, false
		}
		payload := strings.Join(data, "\n")
		data = nil
		var probe struct {
			ID     *json.Number    `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if json.Unmarshal([]byte(payload), &probe) != nil {
			return nil, false
		}
		if probe.Method != "" {
			if probe.ID != nil {
				go c.answer(*probe.ID, probe.Method, probe.Params)
			}
			return nil, false
		}
		if probe.ID != nil && probe.ID.String() == wantID {
			return &response{ID: *probe.ID, Result: probe.Result, Error: probe.Error}, true
		}
		return nil, false
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if res, ok := flush(); ok {
				return res, nil
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if res, ok := flush(); ok {
		return res, nil
	}
	if err := sc.Err(); err != nil {
		return nil, problem.Wrap("remote_event_stream", err)
	}
	return nil, problem.New("remote_stream_ended")
}

// answer replies to a request the server made of us, by POST, as the
// transport requires. Without a handler the answer is "not supported" — the
// same as over stdio, and better than a request left hanging.
func (c *httpChannel) answer(id json.Number, method string, params json.RawMessage) {
	res := response{JSONRPC: "2.0", ID: id}
	if c.handler == nil {
		res.Error = &rpcError{Code: -32601, Message: "method not supported: " + method}
	} else if out, err := c.handler(context.Background(), method, params); err != nil {
		res.Error = &rpcError{Code: -32000, Message: err.Error()}
	} else {
		res.Result, _ = json.Marshal(out)
	}
	body, _ := json.Marshal(res)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.spec.URL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.spec.Headers {
		req.Header.Set(k, v)
	}
	c.mu.Lock()
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	c.mu.Unlock()
	if r, err := c.client.Do(req); err == nil {
		r.Body.Close()
	}
}
