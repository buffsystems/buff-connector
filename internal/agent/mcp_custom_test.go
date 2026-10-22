package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buffsystems/buff-connector/internal/catalog"
	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// fakeMCPBinary puts a copy of this test binary where the instance's own user
// can execute it — the build cache the runner keeps it in is not traversable by
// anyone else.
func fakeMCPBinary(t *testing.T, root string) string {
	t.Helper()
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst := filepath.Join(root, "plugins", "fake-mcp")
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(f, src); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return dst
}

func customInstance(command string) config.PluginInstance {
	return config.PluginInstance{
		ID: "7QK3M9XBWZ0FVT2H", Slug: "weather", Alias: "Погода",
		MCP: &config.CustomMCP{
			Source: config.MCPFromCommand, Command: command,
			Args: []string{"-test.run=TestNothingMatchesThis"},
			// The bundle's way: settings and secrets reach the process through
			// placeholders, never under a name the server did not ask for.
			Env: map[string]string{"BUFF_FAKE_MCP": "1", "WEATHER_UNITS": "${user_config.units}",
				"WEATHER_API_KEY": "${user_config.api_key}"},
			Accepted: true,
		},
		Config:      map[string]any{"units": "metric"},
		Credentials: []config.InstanceCredential{{Name: "api_key", Kind: config.CredentialSecret, Token: "k-123"}},
	}
}

func waitRunning(t *testing.T, m *PluginManager) plugin.InstanceReport {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r := m.Reports(); len(r) == 1 && r[0].Status == string(plugin.StatusRunning) && len(r[0].Tools) > 0 {
			return r[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the custom server did not come up: %+v", m.Reports())
	return plugin.InstanceReport{}
}

// A server the operator brought needs no plugin in the store and no Python
// runtime: the connector runs the command, under the connection's own user,
// with the environment and secrets the operator gave it. It reports itself as
// custom, its tools with the server's hints — and no tool as read-only until
// the operator marks it, which takes effect without a restart.
func TestACustomServerRunsWithoutAPluginAndTheOperatorMarksWhatReads(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("custom servers run under their own uid, which needs privilege")
	}
	root, store := stateWithStore(t)
	m := NewPluginManager(context.Background(), root, store)
	cfg := &config.Config{Plugins: []config.PluginInstance{customInstance(fakeMCPBinary(t, root))}}
	m.Sync(context.Background(), cfg)
	t.Cleanup(m.StopAll)

	r := waitRunning(t, m)
	if !r.Custom || r.Source != config.MCPFromCommand {
		t.Fatalf("the report must say this is the operator's own server: %+v", r)
	}
	if r.Plugin != "weather" || r.Version != "0.3.0" {
		t.Errorf("the running server's own name and version must show: %+v", r)
	}
	if strings.Join(r.Tools, ",") != "lookup,set_home" {
		t.Errorf("tools = %v", r.Tools)
	}
	if len(r.ReadOnly) != 0 {
		t.Errorf("nothing is read-only before the operator says so: %v", r.ReadOnly)
	}
	if strings.Join(r.ReadOnlyHint, ",") != "lookup" {
		t.Errorf("the server's hint must reach the operator: %v", r.ReadOnlyHint)
	}

	// The process got the operator's environment AND the secret, by name.
	surface := NewMCPSurface(m, 0)
	res, err := surface.Handle(context.Background(), "mcp/call", json.RawMessage(
		`{"slug":"weather","tool":"lookup","arguments":{},"call_id":"c1"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(mustJSON(t, res)), "Perm: metric k-123") {
		t.Fatalf("result = %s", mustJSON(t, res))
	}
	if strings.Contains(string(mustJSON(t, res)), "api_key=") {
		t.Fatal("a placeholder-only secret leaked into the environment under its own name")
	}

	// A reading stage may not touch it yet.
	if _, err := surface.Handle(context.Background(), "mcp/call", json.RawMessage(
		`{"slug":"weather","tool":"lookup","arguments":{},"call_id":"c2","read_only_only":true}`)); err == nil {
		t.Fatal("an unmarked tool reached a reading stage")
	}

	cfg.Plugins[0].MCP.ReadOnly = []string{"lookup"}
	m.Sync(context.Background(), cfg)
	r = waitRunning(t, m)
	if strings.Join(r.ReadOnly, ",") != "lookup" {
		t.Fatalf("the operator's mark did not reach the report: %+v", r)
	}
	if r.Restarts != 0 {
		t.Errorf("marking a tool read-only restarted the server (%d restarts)", r.Restarts)
	}
	if _, err := surface.Handle(context.Background(), "mcp/call", json.RawMessage(
		`{"slug":"weather","tool":"lookup","arguments":{},"call_id":"c3","read_only_only":true}`)); err != nil {
		t.Fatalf("a marked tool must be callable from a reading stage: %v", err)
	}
	if _, err := surface.Handle(context.Background(), "mcp/call", json.RawMessage(
		`{"slug":"weather","tool":"set_home","arguments":{},"call_id":"c4","read_only_only":true}`)); err == nil {
		t.Fatal("set_home was never marked and must stay out of a reading stage")
	}
}

// A setting the command refers to and nobody filled in is a readable failure
// naming the setting — not an empty string handed to the server.
func TestAnUnfilledSettingIsReportedByName(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("custom servers run under their own uid, which needs privilege")
	}
	root, store := stateWithStore(t)
	m := NewPluginManager(context.Background(), root, store)
	inst := customInstance(fakeMCPBinary(t, root))
	inst.Config = nil
	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{inst}})
	t.Cleanup(m.StopAll)
	r := m.Reports()
	if len(r) != 1 || r[0].Status != "failed" || !strings.Contains(r[0].LastError, "user_config.units") {
		t.Fatalf("reports = %+v", r)
	}
}

// A command that is not on this machine is a readable failure, not a crash
// loop the operator has to decode from a log.
func TestAMissingCommandIsReportedInTheOperatorsTerms(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("custom servers run under their own uid, which needs privilege")
	}
	root, store := stateWithStore(t)
	m := NewPluginManager(context.Background(), root, store)
	inst := customInstance("definitely-not-a-command-on-this-machine")
	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{inst}})
	t.Cleanup(m.StopAll)

	r := m.Reports()
	if len(r) != 1 || r[0].Status != "failed" || !strings.Contains(r[0].LastError, "definitely-not-a-command") {
		t.Fatalf("reports = %+v", r)
	}
	if !r[0].Custom {
		t.Error("even a failed custom server is reported as custom")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A remote server needs no process at all — and so no privilege: it is reached
// at its address with the operator's header, and reports where it reaches.
func TestARemoteServerIsReachedWithTheOperatorsHeader(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		var msg struct {
			ID     *json.Number `json:"id"`
			Method string       `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &msg)
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		result := map[string]any{"tools": []any{map[string]any{"name": "lookup", "inputSchema": map[string]any{"type": "object"}}}}
		if msg.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "s1")
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
				"serverInfo": map[string]any{"name": "remote-weather", "version": "2.0.0"}}
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}))
	defer srv.Close()

	root, store := stateWithStore(t)
	m := NewPluginManager(context.Background(), root, store)
	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{{
		ID: "7QK3M9XBWZ0FVT2H", Slug: "remote", Alias: "Remote weather",
		MCP:         &config.CustomMCP{Source: config.MCPFromURL, URL: srv.URL + "/mcp", Accepted: true},
		Credentials: []config.InstanceCredential{{Name: "authorization", Kind: "header", Token: "Bearer t-9"}},
	}}})
	t.Cleanup(m.StopAll)

	r := waitRunning(t, m)
	if !r.Custom || r.Source != config.MCPFromURL || r.Plugin != "remote-weather" {
		t.Fatalf("report = %+v", r)
	}
	if len(r.Egress) != 1 || r.Egress[0] != srv.URL+"/mcp" {
		t.Errorf("the report must say where the connection reaches: %v", r.Egress)
	}
	if auth != "Bearer t-9" {
		t.Errorf("Authorization = %q", auth)
	}
}

// A custom server is not in the catalogue, whatever it calls itself: no
// "newer version" is ever offered for it.
func TestACustomServerIsNeverOfferedACatalogueUpdate(t *testing.T) {
	root, store := stateWithStore(t)
	m := NewPluginManager(context.Background(), root, store).WithCatalogue(func() *catalog.Index {
		return &catalog.Index{Plugins: []catalog.Entry{{Name: "jira", Versions: []catalog.Version{{Version: "9.9.9", HostAPI: 1}}}}}
	})
	inst := customInstance("definitely-not-a-command-on-this-machine")
	inst.MCP.Name, inst.MCP.Version = "jira", "1.0.0"
	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{inst}})
	t.Cleanup(m.StopAll)
	r := m.Reports()
	if len(r) != 1 || r[0].Latest != "" {
		t.Fatalf("reports = %+v", r)
	}
}
