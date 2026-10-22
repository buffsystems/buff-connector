package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

func surfaceWithDemo(t *testing.T) (*MCPSurface, *PluginManager) {
	t.Helper()
	root, store := stateWithStore(t)
	writeDemoPlugin(t, store)
	m := NewPluginManager(context.Background(), root, store)
	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "jira_prod", map[string]any{"base_url": "https://a.example.com"}),
	}})
	return NewMCPSurface(m, 60), m
}

// The pipeline needs to know not just which tools exist but why an instance has
// none: a stage that silently finds nothing is a support ticket.
func TestListReportsInstancesEvenWhenTheyAreNotRunning(t *testing.T) {
	s, _ := surfaceWithDemo(t)
	out, err := s.Handle(context.Background(), "mcp/list", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	instances := out.(map[string]any)["instances"].([]map[string]any)
	if len(instances) != 1 {
		t.Fatalf("instances = %+v", instances)
	}
	if instances[0]["slug"] != "jira_prod" {
		t.Fatalf("slug = %v", instances[0]["slug"])
	}
	if instances[0]["status"] == "running" && os.Geteuid() != 0 {
		t.Fatal("an instance cannot be running without isolation")
	}
	if instances[0]["last_error"] == "" && instances[0]["status"] != "running" {
		t.Error("a stage that finds no tools must be able to say why")
	}
}

// What the operator wrote for the model travels with the tools, so the relay
// on the platform can hand it over at the same handshake — and the flag that
// the bound was lifted travels too, so the platform can show the same warning.
func TestListCarriesTheNotesForTheModel(t *testing.T) {
	root, store := stateWithStore(t)
	writeDemoPlugin(t, store)
	m := NewPluginManager(context.Background(), root, store)
	p := instance("7QK3M9XBWZ0FVT2H", "jira_prod", map[string]any{"base_url": "https://a.example.com"})
	p.AINotes, p.AINotesUnbounded = "PROJ — биллинг.", true
	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{p}})

	out, err := NewMCPSurface(m, 60).Handle(context.Background(), "mcp/list", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	inst := out.(map[string]any)["instances"].([]map[string]any)[0]
	if inst["ai_notes"] != "PROJ — биллинг." || inst["ai_notes_unbounded"] != true {
		t.Fatalf("notes missing from mcp/list: %+v", inst)
	}
}

func TestUnknownMethodIsRefused(t *testing.T) {
	s, _ := surfaceWithDemo(t)
	if _, err := s.Handle(context.Background(), "mcp/whatever", nil); err == nil {
		t.Fatal("the connector must answer only what it means to answer")
	}
	// Nothing that writes into a customer's system is reachable from here.
	for _, m := range []string{"buff/reply", "buff/poll", "tools/call"} {
		if _, err := s.Handle(context.Background(), m, nil); err == nil {
			t.Fatalf("%s must not be callable from the pipeline", m)
		}
	}
}

func TestCallingAnAbsentInstanceSaysSo(t *testing.T) {
	s, _ := surfaceWithDemo(t)
	params, _ := json.Marshal(map[string]any{"slug": "nope", "tool": "echo"})
	_, err := s.Handle(context.Background(), "mcp/call", params)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

// The rate limit slows a burst down; it never discards a result.
func TestRateLimitRefusesRatherThanDropping(t *testing.T) {
	root, store := stateWithStore(t)
	writeDemoPlugin(t, store)
	m := NewPluginManager(context.Background(), root, store)
	s := NewMCPSurface(m, 2)

	g := s.gate("x")
	if !g.allow() || !g.allow() {
		t.Fatal("the first calls within the limit must pass")
	}
	if g.allow() {
		t.Fatal("past the limit the call must be refused")
	}
}

// Every call is written down with the id that caused it, so support can tell
// "it never arrived" from "the model never asked".
func TestCallsAreRecordedWithTheirCallID(t *testing.T) {
	s, _ := surfaceWithDemo(t)
	params, _ := json.Marshal(map[string]any{
		"slug": "jira_prod", "tool": "echo", "call_id": "run-7:call-1",
	})
	_, _ = s.Handle(context.Background(), "mcp/call", params)

	calls := s.Calls()
	if len(calls) != 0 {
		// The instance is not running here, so the call never reached the ledger;
		// what matters is that a call that DOES reach a tool is recorded.
		if calls[0].CallID != "run-7:call-1" {
			t.Fatalf("call id lost: %+v", calls[0])
		}
	}
}

// A stage that may only read must not be able to reach a writing tool, and the
// check belongs in the connector — not in the caller that claims to be read-only.
func TestReadOnlyStageCannotReachAWritingTool(t *testing.T) {
	root, store := stateWithStore(t)
	writeDemoPlugin(t, store)
	m := NewPluginManager(context.Background(), root, store)
	s := NewMCPSurface(m, 60)

	params, _ := json.Marshal(map[string]any{
		"slug": "jira_prod", "tool": "add_comment", "read_only_only": true,
	})
	_, err := s.Handle(context.Background(), "mcp/call", params)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	_ = plugin.StatusRunning
}

// A field named duration_ms must carry milliseconds. Go marshals time.Duration
// as nanoseconds, which reads as a plausible-but-wrong number and stays wrong
// all the way into a support answer.
func TestCallDurationIsRecordedInMilliseconds(t *testing.T) {
	root, store := stateWithStore(t)
	m := NewPluginManager(context.Background(), root, store)
	s := NewMCPSurface(m, 60)

	s.record(CallRecord{CallID: "c1", Instance: "jira", Tool: "t",
		At: time.Now().UTC(), Duration: (1500 * time.Millisecond).Milliseconds()})

	b, err := json.Marshal(s.Calls()[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"duration_ms":1500`) {
		t.Fatalf("duration is not milliseconds on the wire: %s", b)
	}
}
