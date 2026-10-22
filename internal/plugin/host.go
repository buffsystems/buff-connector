package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// An Instance is one configured connection to one customer system: a plugin
// process, the channel to it, and the supervision that keeps it alive.
//
// Supervision has one rule that is not negotiable: a failing instance backs off
// but is NEVER switched off. Something that fails is retried, not removed — an
// instance quietly disabled at 3am is a customer whose tickets stop being
// answered with nobody knowing why. The backoff has a ceiling instead, so two
// crash-looping instances cannot burn a small machine.

type Status string

const (
	StatusStopped     Status = "stopped"
	StatusStarting    Status = "starting"
	StatusRunning     Status = "running"
	StatusBackoff     Status = "backoff"
	StatusNoIsolation Status = "no-isolation"
)

// Tool is one capability the plugin offers. ReadOnly decides whether it may be
// exposed to a pipeline stage: a stage that only reads must not be handed
// "create ticket" or "change status". Vision marks a tool that answers with a
// PICTURE: a model that cannot see must not be handed it — it would get a blob
// and answer as if it had read it.
//
// ReadOnlyHint is what the SERVER said (MCP's annotations.readOnlyHint). For a
// catalogue plugin — code we signed — the hint is the decision. For a server the
// operator brought, the hint is shown and the operator decides: unverified code
// claiming to be harmless is exactly the claim the read-only rule exists to
// distrust.
type Tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	ReadOnly     bool            `json:"-"`
	ReadOnlyHint bool            `json:"-"`
	Vision       bool            `json:"-"`
}

// State is everything an operator or the report needs to know about an instance.
type State struct {
	Status    Status        `json:"status"`
	Restarts  int           `json:"restarts"`
	Backoff   time.Duration `json:"backoff_ms"`
	LastError string        `json:"last_error,omitempty"`
	Since     time.Time     `json:"since"`
	Tools     int           `json:"tools"`
}

// InstanceConfig is the fixed description of an instance; nothing here changes
// while it runs.
type InstanceConfig struct {
	GUID string
	Slug string
	Spec Spec

	// PlainMCP marks a server the operator brought (#351): the handshake is the
	// published MCP one, no buff/* method is ever sent, and a tool is read-only
	// only when ReadOnlyTools names it. The list can change while the server
	// runs — see SetReadOnlyTools — so it is a mark, not part of the process.
	PlainMCP      bool
	ReadOnlyTools []string

	// Remote, when set, means there is no process: the server is reached at
	// an address over Streamable HTTP. Spec is then unused.
	Remote *RemoteSpec

	StartBackoff time.Duration
	MaxBackoff   time.Duration
	CallTimeout  time.Duration
}

func (c *InstanceConfig) defaults() {
	if c.StartBackoff == 0 {
		c.StartBackoff = time.Second
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = 2 * time.Minute
	}
	if c.CallTimeout == 0 {
		c.CallTimeout = 30 * time.Second
	}
}

type Instance struct {
	cfg     InstanceConfig
	starter *Starter
	handler Handler

	mu      sync.RWMutex
	state   State
	conn    channel
	proc    *Process
	tools   []Tool
	marks   map[string]bool
	info    ServerInfo
	logs    LogRing
	stopped bool
	cancel  context.CancelFunc
}

// ServerInfo is what the plugin says about itself during the handshake.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	HostAPI int    `json:"-"`

	// What the plugin offers beyond tools, taken from the handshake. The
	// connector has to know BEFORE it asks: polling a plugin that reports
	// nothing is a call that fails on every tick, and routing an answer to one
	// that cannot write it back loses the answer somewhere quiet.
	Polls   bool `json:"-"`
	Replies bool `json:"-"`
	Checks  bool `json:"-"`
}

func NewInstance(s *Starter, cfg InstanceConfig, h Handler) *Instance {
	cfg.defaults()
	in := &Instance{
		cfg: cfg, starter: s, handler: h,
		state: State{Status: StatusStopped, Since: time.Now()},
	}
	in.SetReadOnlyTools(cfg.ReadOnlyTools)
	return in
}

// SetReadOnlyTools records the operator's marks for a PlainMCP instance. It
// takes effect on the next Tools() — no restart, the server does not care.
func (i *Instance) SetReadOnlyTools(names []string) {
	marks := make(map[string]bool, len(names))
	for _, n := range names {
		marks[n] = true
	}
	i.mu.Lock()
	i.marks = marks
	i.mu.Unlock()
}

// State is a snapshot, safe to read from anywhere.
func (i *Instance) State() State {
	i.mu.RLock()
	defer i.mu.RUnlock()
	st := i.state
	st.Tools = len(i.tools)
	return st
}

// Tools is what the plugin declared at the last successful handshake, with
// ReadOnly decided: by the hint for a catalogue plugin, by the operator's marks
// for a server they brought.
func (i *Instance) Tools() []Tool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := append([]Tool(nil), i.tools...)
	for n := range out {
		if i.cfg.PlainMCP {
			out[n].ReadOnly = i.marks[out[n].Name]
		} else {
			out[n].ReadOnly = out[n].ReadOnlyHint
		}
	}
	return out
}

// Log is the tail of what this instance wrote to stderr.
func (i *Instance) Log() []LogLine { return i.logs.Lines() }

// Info identifies the running plugin.
func (i *Instance) Info() ServerInfo {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.info
}

// Start brings the instance up and keeps it up. It returns the FIRST attempt's
// error so a caller acting on an operator's request gets a straight answer; the
// supervisor carries on regardless, because a system that is down now may be up
// in a minute.
func (i *Instance) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	i.mu.Lock()
	i.stopped = false
	i.cancel = cancel
	i.mu.Unlock()

	err := i.attempt(ctx)
	if errors.Is(err, ErrNoPrivilege) {
		// Nothing to retry: this will not change until the operator changes how
		// the connector itself is run.
		i.setState(StatusNoIsolation, err)
		cancel()
		return err
	}
	if err != nil {
		i.setState(StatusBackoff, err)
	}
	go i.supervise(ctx)
	return err
}

// supervise restarts the instance for as long as it is meant to be running.
func (i *Instance) supervise(ctx context.Context) {
	backoff := i.cfg.StartBackoff
	for {
		i.mu.RLock()
		conn, stopped := i.conn, i.stopped
		i.mu.RUnlock()
		if stopped || ctx.Err() != nil {
			return
		}

		if conn != nil && !conn.Closed() {
			select {
			case <-ctx.Done():
				return
			case <-conn.Done():
			}
			i.setState(StatusBackoff, conn.Err())
		}

		i.mu.Lock()
		i.state.Restarts++
		i.state.Backoff = backoff
		i.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		if err := i.attempt(ctx); err != nil {
			i.setState(StatusBackoff, err)
			// Grow towards the ceiling, then stay there. Never stop trying.
			if backoff = backoff * 2; backoff > i.cfg.MaxBackoff {
				backoff = i.cfg.MaxBackoff
			}
			continue
		}
		backoff = i.cfg.StartBackoff
	}
}

// attempt starts the process and completes the handshake once.
func (i *Instance) attempt(ctx context.Context) error {
	i.setState(StatusStarting, nil)

	conn, proc, err := i.open(ctx)
	if err != nil {
		return err
	}

	hctx, cancel := context.WithTimeout(ctx, i.cfg.CallTimeout)
	defer cancel()

	var init struct {
		ProtocolVersion string              `json:"protocolVersion"`
		HostAPI         int                 `json:"hostApi"`
		ServerInfo      ServerInfo          `json:"serverInfo"`
		Capabilities    map[string]struct{} `json:"capabilities"`
	}
	abandon := func() {
		conn.Close()
		if proc != nil {
			proc.Stop()
		}
	}
	if err := conn.Call(hctx, "initialize", i.initializeParams(), &init); err != nil {
		abandon()
		// "the plugin closed the channel" tells an operator nothing; what it
		// printed on the way out tells them everything.
		if tail := i.logs.Last(); tail != "" {
			return fmt.Errorf("initialize: %w (plugin said: %s)", err, tail)
		}
		return fmt.Errorf("initialize: %w", err)
	}
	if init.HostAPI > SupportedHostAPI {
		abandon()
		return fmt.Errorf("plugin needs host API %d, this connector speaks %d — update the connector",
			init.HostAPI, SupportedHostAPI)
	}
	if i.cfg.PlainMCP {
		// The published protocol: nothing may be asked until the client has
		// said the handshake is over.
		if err := conn.Notify("notifications/initialized", map[string]any{}); err != nil {
			abandon()
			return fmt.Errorf("initialized: %w", err)
		}
	}

	tools, err := listTools(hctx, conn)
	if err != nil {
		abandon()
		return err
	}

	i.mu.Lock()
	i.conn, i.proc, i.tools = conn, proc, tools
	i.info = init.ServerInfo
	i.info.HostAPI = init.HostAPI
	_, i.info.Polls = init.Capabilities["buff/poll"]
	_, i.info.Replies = init.Capabilities["buff/reply"]
	_, i.info.Checks = init.Capabilities["buff/check"]
	i.state.Status = StatusRunning
	i.state.LastError = ""
	i.state.Since = time.Now()
	i.mu.Unlock()
	return nil
}

// open makes the channel: a process and its pipes, or an HTTP session.
func (i *Instance) open(ctx context.Context) (channel, *Process, error) {
	if i.cfg.Remote != nil {
		return newHTTPChannel(*i.cfg.Remote, i.handler), nil, nil
	}
	proc, err := i.starter.Start(ctx, i.cfg.Spec)
	if err != nil {
		return nil, nil, err
	}
	// Drain stderr from the moment the process exists: a plugin that dies during
	// the handshake writes its traceback there, and that traceback is the whole
	// diagnosis.
	go i.logs.drain(proc.Stderr)
	return NewConn(proc.Stdout, proc.Stdin, i.handler), proc, nil
}

// initializeParams is the handshake for the kind of peer this is. Our plugins
// get the instance they serve; a server the operator brought gets exactly what
// the MCP specification says and nothing it would not recognise.
func (i *Instance) initializeParams() map[string]any {
	if i.cfg.PlainMCP {
		return map[string]any{
			"protocolVersion": MCPProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "buff-connector", "version": ProtocolVersion},
		}
	}
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"clientInfo":      map[string]string{"name": "buff-connector"},
		"instance":        map[string]string{"guid": i.cfg.GUID, "slug": i.cfg.Slug},
	}
}

// listTools reads the plugin's catalogue, every page of it: MCP paginates with
// nextCursor, and a list cut at the first page is a tool the model never hears
// of. The read-only nature of a tool arrives in annotations.readOnlyHint and is
// kept as the HINT; whether it decides anything is Tools()' business. Unmarked
// is a hint of "writing", because guessing the other way hands a reading stage
// a destructive tool. visionHint is ours: unmarked means "answers in text",
// which every model reads.
func listTools(ctx context.Context, conn channel) ([]Tool, error) {
	var out []Tool
	params := map[string]any{}
	seen := map[string]bool{}
	for {
		var res struct {
			Tools []struct {
				Tool
				Annotations struct {
					ReadOnlyHint bool `json:"readOnlyHint"`
					VisionHint   bool `json:"visionHint"`
				} `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := conn.Call(ctx, "tools/list", params, &res); err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		for _, t := range res.Tools {
			tool := t.Tool
			tool.ReadOnlyHint = t.Annotations.ReadOnlyHint
			tool.Vision = t.Annotations.VisionHint
			out = append(out, tool)
		}
		if res.NextCursor == "" {
			return out, nil
		}
		if seen[res.NextCursor] {
			return nil, fmt.Errorf("tools/list: the server repeats cursor %q — a list that never ends", res.NextCursor)
		}
		seen[res.NextCursor] = true
		params = map[string]any{"cursor": res.NextCursor}
	}
}

// CallTool invokes one of the plugin's tools.
func (i *Instance) CallTool(ctx context.Context, name string, args, out any) error {
	i.mu.RLock()
	conn := i.conn
	i.mu.RUnlock()
	if conn == nil || conn.Closed() {
		return fmt.Errorf("instance %s is not running", i.cfg.Slug)
	}
	ctx, cancel := context.WithTimeout(ctx, i.cfg.CallTimeout)
	defer cancel()
	return conn.Call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, out)
}

// Call sends any method to the plugin — used for the buff/* surface.
func (i *Instance) Call(ctx context.Context, method string, params, out any) error {
	i.mu.RLock()
	conn := i.conn
	i.mu.RUnlock()
	if conn == nil || conn.Closed() {
		return fmt.Errorf("instance %s is not running", i.cfg.Slug)
	}
	ctx, cancel := context.WithTimeout(ctx, i.cfg.CallTimeout)
	defer cancel()
	return conn.Call(ctx, method, params, out)
}

// Stop ends the instance and its supervision.
func (i *Instance) Stop() {
	i.mu.Lock()
	i.stopped = true
	conn, proc, cancel := i.conn, i.proc, i.cancel
	i.conn, i.proc = nil, nil
	i.state.Status = StatusStopped
	i.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if conn != nil {
		conn.Close()
	}
	if proc != nil {
		proc.Stop()
	}
}

func (i *Instance) setState(s Status, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.state.Status = s
	if err != nil {
		i.state.LastError = err.Error()
	}
}
