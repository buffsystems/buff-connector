package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/config"
)

// Buff Code gets the same catalogue the platform's runs use (#368): each tool named
// with its connection so two sources never collide, the administrator's notes for the
// model at the handshake, and every call journaled under the developer who made it.
func TestBuffCodeSeesTheToolsAndCallsAsTheDeveloper(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("custom servers run under their own uid, which needs privilege")
	}
	root, store := stateWithStore(t)
	m := NewPluginManager(context.Background(), root, store)
	inst := customInstance(fakeMCPBinary(t, root))
	inst.AINotes = "Погода для офисов компании."
	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{inst}})
	t.Cleanup(m.StopAll)
	waitRunning(t, m)

	s := NewMCPSurface(m, 0)
	names := []string{}
	for _, tool := range s.Catalog() {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "weather__lookup,weather__set_home" {
		t.Fatalf("catalog = %v", names)
	}
	if !strings.Contains(s.Instructions(), "Погода для офисов компании.") || !strings.Contains(s.Instructions(), "weather") {
		t.Fatalf("instructions = %q", s.Instructions())
	}

	out, err := s.Call(context.Background(), "weather__lookup", json.RawMessage(`{}`), "ivan")
	if err != nil || !strings.Contains(string(out), "Perm: metric") {
		t.Fatalf("call: %s %v", out, err)
	}
	calls := s.Calls()
	if len(calls) != 1 || calls[0].Caller != "ivan" || calls[0].Instance != "weather" || calls[0].Tool != "lookup" {
		t.Fatalf("journal = %+v", calls)
	}
	if _, err := s.Call(context.Background(), "nothing-here", json.RawMessage(`{}`), "ivan"); err == nil {
		t.Fatal("a name without its connection must be refused")
	}
}

// The operator's page counts calls per developer (#368), and the count survives the
// journal being handed to the platform with each report.
func TestDeveloperCallsAreCountedPerDeveloper(t *testing.T) {
	root, store := stateWithStore(t)
	s := NewMCPSurface(NewPluginManager(context.Background(), root, store), 60)
	s.record(CallRecord{CallID: "a", Caller: "ivan", Instance: "handbook", Tool: "read"})
	s.record(CallRecord{CallID: "b", Caller: "ivan", Instance: "handbook", Tool: "read", Error: "boom"})
	s.record(CallRecord{CallID: "c", Caller: "", Instance: "handbook", Tool: "read"})
	s.record(CallRecord{CallID: "d", Caller: "olga", Instance: "handbook", Tool: "search"})
	_ = s.TakeCalls()
	rows := s.DeveloperCalls()
	if len(rows) != 2 || rows[0].Developer != "ivan" || rows[0].Calls != 2 || rows[0].Errors != 1 ||
		rows[1].Developer != "olga" || rows[1].Calls != 1 {
		t.Fatalf("rows = %+v", rows)
	}
}
