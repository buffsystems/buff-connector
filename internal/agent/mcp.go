package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/buffsystems/buff-connector/internal/admin"
	"github.com/buffsystems/buff-connector/internal/devgate"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// The connector's answering surface: what the pipeline may ask it for.
//
// Two methods, deliberately. `mcp/list` says which instances exist and what
// tools they offer; `mcp/call` runs one. Everything else a plugin can do —
// posting a reply, polling for events — is driven by the connector itself, and
// exposing it here would hand a pipeline stage a way to write into a customer's
// tracker that the read-only rule is meant to prevent.

// ToolCallLimit bounds how often one instance may be called. It is a rate, not a
// cap on results: a burst is slowed down, nothing is discarded, and the operator
// can see it happening.
type ToolCallLimit struct {
	PerMinute int
}

type rateGate struct {
	mu     sync.Mutex
	limit  int
	window time.Time
	count  int
}

func (g *rateGate) allow() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now().Truncate(time.Minute)
	if now.After(g.window) {
		g.window, g.count = now, 0
	}
	if g.limit > 0 && g.count >= g.limit {
		return false
	}
	g.count++
	return true
}

// CallRecord is one tool call, for the report and for support.
//
// The duration is milliseconds as an integer, not a time.Duration: Go marshals
// a Duration as NANOSECONDS, and a field named duration_ms carrying nanoseconds
// is the kind of quiet wrongness nobody notices until a support answer is off by
// six orders of magnitude.
type CallRecord struct {
	CallID string `json:"call_id"`
	// Caller: the developer whose Buff Code made the call (#368); empty for the platform's runs.
	Caller   string    `json:"caller,omitempty"`
	Instance string    `json:"instance"`
	Tool     string    `json:"tool"`
	At       time.Time `json:"at"`
	Duration int64     `json:"duration_ms"`
	Error    string    `json:"error,omitempty"`
}

// MCPSurface answers the pipeline on behalf of the installed instances.
type MCPSurface struct {
	pm    *PluginManager
	limit int

	mu    sync.Mutex
	gates map[string]*rateGate
	calls []CallRecord
	// byDeveloper counts Buff Code's calls per developer for the operator's page (#368);
	// unlike calls, it is not handed away with the report.
	byDeveloper map[string]*admin.DeveloperCalls
}

func NewMCPSurface(pm *PluginManager, perMinute int) *MCPSurface {
	return &MCPSurface{pm: pm, limit: perMinute, gates: map[string]*rateGate{}, byDeveloper: map[string]*admin.DeveloperCalls{}}
}

// Handle is the entry point for a request arriving over the relay.
func (s *MCPSurface) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "mcp/list":
		return s.list(), nil
	case "mcp/call":
		return s.call(ctx, params)
	}
	return nil, fmt.Errorf("connector does not answer %q", method)
}

// list describes every instance the pipeline may use. Instances that are not
// running are listed too, with their status: a stage that finds no tools should
// be able to say why.
func (s *MCPSurface) list() map[string]any {
	reports := s.pm.Reports()
	out := make([]map[string]any, 0, len(reports))
	for _, r := range reports {
		inst := s.pm.Instance(r.Slug)
		tools := []map[string]any{}
		if inst != nil {
			for _, t := range inst.Tools() {
				tools = append(tools, map[string]any{
					"name": t.Name, "description": t.Description,
					"inputSchema": t.InputSchema, "read_only": t.ReadOnly, "vision": t.Vision,
				})
			}
		}
		out = append(out, map[string]any{
			"instance": r.ID, "slug": r.Slug, "alias": r.Alias,
			"plugin": r.Plugin, "version": r.Version, "status": r.Status,
			"dev_project": r.DevProject, "last_error": r.LastError, "tools": tools,
			"ai_notes": r.AINotes, "ai_notes_unbounded": r.AINotesUnbounded,
			"custom": r.Custom,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["slug"].(string) < out[j]["slug"].(string)
	})
	return map[string]any{"instances": out}
}

type callRequest struct {
	Slug     string          `json:"slug"`
	Tool     string          `json:"tool"`
	Args     json.RawMessage `json:"arguments"`
	CallID   string          `json:"call_id"`
	ReadOnly bool            `json:"read_only_only"`
}

func (s *MCPSurface) call(ctx context.Context, params json.RawMessage) (any, error) {
	var req callRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("bad call: %w", err)
	}
	return s.invoke(ctx, req, "")
}

// invoke is the one road to a tool — the platform's runs and Buff Code alike: the same
// rate limit, the same read-only check, the same journal.
func (s *MCPSurface) invoke(ctx context.Context, req callRequest, caller string) (json.RawMessage, error) {
	inst := s.pm.Instance(req.Slug)
	if inst == nil {
		return nil, fmt.Errorf("no connection named %q is running here", req.Slug)
	}
	if !s.gate(req.Slug).allow() {
		return nil, fmt.Errorf("connection %q is being called more than %d times a minute; "+
			"slow down rather than dropping calls", req.Slug, s.limit)
	}
	// The caller says it wants only read-only tools; the connector enforces it,
	// because the decision must not depend on the caller getting it right.
	if req.ReadOnly {
		ok := false
		for _, t := range inst.Tools() {
			if t.Name == req.Tool && t.ReadOnly {
				ok = true
			}
		}
		if !ok {
			return nil, fmt.Errorf("%s is not a read-only tool and this stage may only read", req.Tool)
		}
	}

	started := time.Now()
	ctx = plugin.WithCallID(ctx, req.CallID)
	var out json.RawMessage
	err := inst.CallTool(ctx, req.Tool, decodeArgs(req.Args), &out)

	rec := CallRecord{CallID: req.CallID, Caller: caller, Instance: req.Slug, Tool: req.Tool,
		At: started.UTC(), Duration: time.Since(started).Milliseconds()}
	if err != nil {
		rec.Error = err.Error()
	}
	s.record(rec)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeveloperCalls is Buff Code's use of the tools, per developer, for the operator's page.
func (s *MCPSurface) DeveloperCalls() []admin.DeveloperCalls {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]admin.DeveloperCalls, 0, len(s.byDeveloper))
	for _, d := range s.byDeveloper {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Developer < out[j].Developer })
	return out
}

// toolSep joins a connection and its tool in the names Buff Code sees (#368).
const toolSep = "__"

// Catalog is every running connection's tools for Buff Code (#368), named with the
// connection so two sources never collide.
func (s *MCPSurface) Catalog() []devgate.Tool {
	out := []devgate.Tool{}
	for _, r := range s.pm.Reports() {
		inst := s.pm.Instance(r.Slug)
		if inst == nil {
			continue
		}
		for _, t := range inst.Tools() {
			schema, _ := json.Marshal(t.InputSchema)
			out = append(out, devgate.Tool{
				Name: r.Slug + toolSep + t.Name, Description: t.Description,
				InputSchema: schema, ReadOnly: t.ReadOnly,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Instructions tells the model what each connection is, with the administrator's notes.
func (s *MCPSurface) Instructions() string {
	var b strings.Builder
	b.WriteString("Инструменты пространства компании — через коннектор Buff. Имя инструмента: <подключение>" + toolSep + "<инструмент>.\n")
	for _, r := range s.pm.Reports() {
		fmt.Fprintf(&b, "\n%s — %s", r.Slug, r.Alias)
		if r.AINotes != "" {
			fmt.Fprintf(&b, ": %s", r.AINotes)
		}
	}
	return b.String()
}

// Call runs one of the catalogue's tools for the developer who asked (#368).
func (s *MCPSurface) Call(ctx context.Context, name string, args json.RawMessage, caller string) (json.RawMessage, error) {
	slug, tool, ok := strings.Cut(name, toolSep)
	if !ok || slug == "" || tool == "" {
		return nil, fmt.Errorf("инструмента %q нет: имя — <подключение>%s<инструмент>", name, toolSep)
	}
	id := make([]byte, 6)
	_, _ = rand.Read(id)
	return s.invoke(ctx, callRequest{Slug: slug, Tool: tool, Args: args,
		CallID: "ide:" + caller + ":" + hex.EncodeToString(id)}, caller)
}

func (s *MCPSurface) gate(slug string) *rateGate {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.gates[slug]
	if !ok {
		g = &rateGate{limit: s.limit}
		s.gates[slug] = g
	}
	return g
}

// record keeps the recent calls so support can answer "did it even arrive?"
// without asking the customer to find logs.
func (s *MCPSurface) record(r CallRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Caller != "" {
		d, ok := s.byDeveloper[r.Caller]
		if !ok {
			d = &admin.DeveloperCalls{Developer: r.Caller}
			s.byDeveloper[r.Caller] = d
		}
		d.Calls++
		if r.Error != "" {
			d.Errors++
		}
		if r.At.After(d.Last) {
			d.Last = r.At
		}
	}
	s.calls = append(s.calls, r)
	if len(s.calls) > 500 {
		s.calls = s.calls[len(s.calls)-500:]
	}
}

// Calls is the recent tool-call ledger.
func (s *MCPSurface) Calls() []CallRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CallRecord(nil), s.calls...)
}

func decodeArgs(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return map[string]any{}
	}
	return v
}

// TakeCalls hands over the calls made since the last time it was asked and
// clears them.
//
// Handing them over rather than copying is deliberate: the ledger travels to the
// platform, where it belongs — an action inside a customer's system has to be
// traceable from OUR side, not only from a buffer in their network that goes
// away with the process.
func (s *MCPSurface) TakeCalls() []CallRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.calls
	s.calls = nil
	return out
}
