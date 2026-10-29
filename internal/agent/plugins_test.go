package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/plugin"
	"github.com/buffsystems/buff-connector/internal/problem"
)

func stateWithStore(t *testing.T) (string, *plugin.Store) {
	t.Helper()
	root, err := os.MkdirTemp("", "buffagent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := plugin.PrepareStateDir(root); err != nil {
		t.Fatal(err)
	}
	return root, plugin.NewStore(root, nil, nil)
}

func instance(id, slug string, cfg map[string]any) config.PluginInstance {
	return config.PluginInstance{ID: id, Plugin: "demo", Version: "1.0.0", Slug: slug, Config: cfg}
}

// Each instance gets its own user, and keeps it. Sharing one uid would mean two
// instances could read each other through /proc; changing it between restarts
// would orphan the files the instance already wrote.
func TestUIDsArePerInstanceAndStable(t *testing.T) {
	root, _ := stateWithStore(t)
	a := newUIDAllocator(root)

	one, err := a.For("GUID-ONE")
	if err != nil {
		t.Fatal(err)
	}
	two, err := a.For("GUID-TWO")
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("two instances were given the same user")
	}

	// A fresh allocator (a restarted connector) must reach the same answer.
	again, err := newUIDAllocator(root).For("GUID-ONE")
	if err != nil {
		t.Fatal(err)
	}
	if again != one {
		t.Fatalf("uid changed across restart: %d then %d", one, again)
	}
}

// A configuration naming a plugin that is not installed must produce a readable
// reason, not a silent absence.
func TestMissingPluginIsReportedNotIgnored(t *testing.T) {
	root, store := stateWithStore(t)
	m := NewPluginManager(context.Background(), root, store)

	m.Sync(context.Background(), &config.Config{
		Plugins: []config.PluginInstance{instance("7QK3M9XBWZ0FVT2H", "jira_prod", nil)},
	})

	reports := m.Reports()
	if len(reports) != 1 {
		t.Fatalf("reports = %+v", reports)
	}
	if reports[0].Status != "failed" || !strings.Contains(reports[0].LastError, "не установлен") {
		t.Fatalf("report = %+v", reports[0])
	}
	// The platform gets it as a status its own clients write the text of (#718).
	want := &problem.Problem{Code: "plugin_not_installed", Params: map[string]string{"plugin": "demo", "version": "1.0.0"}}
	if !reflect.DeepEqual(reports[0].Problem, want) {
		t.Errorf("problem = %+v", reports[0].Problem)
	}
	if reports[0].Slug != "jira_prod" {
		t.Errorf("the operator has to recognise which connection this is: %+v", reports[0])
	}
}

// Settings that do not match the plugin's schema stop THAT instance with the
// field named, and leave everything else running.
func TestBadSettingsStopOnlyTheirOwnInstance(t *testing.T) {
	root, store := stateWithStore(t)
	writeDemoPlugin(t, store)
	m := NewPluginManager(context.Background(), root, store)

	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "good", map[string]any{"base_url": "https://a.example.com"}),
		instance("J4RD8N1VC6PQY5ZE", "bad", map[string]any{"base_url": 42}),
	}})

	byslug := map[string]plugin.InstanceReport{}
	for _, r := range m.Reports() {
		byslug[r.Slug] = r
	}
	if !strings.Contains(byslug["bad"].LastError, "base_url") {
		t.Fatalf("bad instance report = %+v", byslug["bad"])
	}
	if byslug["good"].Status == "failed" && strings.Contains(byslug["good"].LastError, "base_url") {
		t.Fatalf("a neighbour's bad settings stopped a healthy instance: %+v", byslug["good"])
	}
}

// The resolved allowlist belongs in the report: "can reach a.example.com" is
// what an operator can check, "makes web requests" is not.
func TestReportCarriesTheResolvedEgress(t *testing.T) {
	root, store := stateWithStore(t)
	writeDemoPlugin(t, store)
	m := NewPluginManager(context.Background(), root, store)

	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "jira_prod", map[string]any{"base_url": "https://jira.example.com:8443"}),
	}})

	r := m.Reports()[0]
	if len(r.Egress) != 1 || r.Egress[0] != "jira.example.com:8443" {
		t.Fatalf("egress = %+v", r.Egress)
	}
}

// Removing an instance from the config takes it out of the running set.
func TestRemovingAnInstanceStopsIt(t *testing.T) {
	root, store := stateWithStore(t)
	writeDemoPlugin(t, store)
	m := NewPluginManager(context.Background(), root, store)
	cfg := &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "jira_prod", map[string]any{"base_url": "https://a.example.com"}),
	}}
	m.Sync(context.Background(), cfg)

	m.Sync(context.Background(), &config.Config{})
	if got := len(m.Reports()); got != 0 {
		t.Fatalf("reports = %d after removing the instance", got)
	}
}

// writeDemoPlugin installs a plugin whose entry point does not need to run for
// these tests — what is under test is the reconciliation around it.
func writeDemoPlugin(t *testing.T, store *plugin.Store) {
	t.Helper()
	dir := filepath.Join(storeRoot(store), "0000000000000000000000000000000000000000000000000000000000000000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	man := `
name: demo
version: 1.0.0
host_api: 1
entry: main.py
egress:
  - from_config: base_url
config_schema:
  type: object
  required: [base_url]
  properties:
    base_url: {type: string, format: uri}
`
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := store.Resolve("demo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(in); err != nil {
		t.Fatal(err)
	}
}

func storeRoot(s *plugin.Store) string { return s.Root() }

// A plugin process must outlive the request that started it. Starting one from
// an HTTP handler's context killed it the moment the handler returned: the
// operator saw "running" and the process was already gone.
func TestInstancesOutliveTheRequestThatStartedThem(t *testing.T) {
	root, store := stateWithStore(t)
	writeDemoPlugin(t, store)
	m := NewPluginManager(context.Background(), root, store)

	reqCtx, cancel := context.WithCancel(context.Background())
	m.Sync(reqCtx, &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "jira_prod", map[string]any{"base_url": "https://a.example.com"}),
	}})
	cancel() // the handler returns

	// The manager still holds it — the request's end is not the instance's end.
	if len(m.Reports()) != 1 {
		t.Fatalf("the instance vanished with its request: %+v", m.Reports())
	}
}

// An instance whose process has gone must come back even when nothing about its
// configuration changed. Comparing fingerprints alone kept a dead connection
// dead for ever.
func TestAnUnchangedButDeadInstanceIsRestarted(t *testing.T) {
	root, store := stateWithStore(t)
	writeDemoPlugin(t, store)
	m := NewPluginManager(context.Background(), root, store)
	cfg := &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "jira_prod", map[string]any{"base_url": "https://a.example.com"}),
	}}
	m.Sync(context.Background(), cfg)

	// Kill it the way a crash would, leaving the configuration untouched.
	m.mu.Lock()
	run := m.running["7QK3M9XBWZ0FVT2H"]
	m.mu.Unlock()
	if run == nil {
		t.Skip("instance never started in this environment")
	}
	run.inst.Stop()

	m.Sync(context.Background(), cfg)
	m.mu.Lock()
	after := m.running["7QK3M9XBWZ0FVT2H"]
	m.mu.Unlock()
	if after == run {
		t.Fatal("a dead instance with an unchanged fingerprint was left dead")
	}
}

// Renaming a connection must not restart it — the alias is what the operator
// calls it, not part of how it runs. But the new name still has to reach the
// UI: a rename that only lands in the file leaves the page showing the old one
// until something unrelated makes the process restart.
func TestRenamingAConnectionShowsUpWithoutRestartingIt(t *testing.T) {
	root, store := stateWithStore(t)
	writeDemoPlugin(t, store)
	m := NewPluginManager(context.Background(), root, store)

	p := instance("7QK3M9XBWZ0FVT2H", "jira_prod", map[string]any{"base_url": "https://a.example.com"})
	p.Alias = "Jira прод"
	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{p}})

	m.mu.Lock()
	before := m.running[p.ID]
	m.mu.Unlock()
	if before == nil {
		t.Skip("instance never started in this environment")
	}

	p.Alias = "Jira тест"
	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{p}})

	m.mu.Lock()
	after := m.running[p.ID]
	m.mu.Unlock()
	if after != before {
		t.Error("a rename restarted the connection — the alias does not change how it runs")
	}
	reports := m.Reports()
	if len(reports) != 1 || reports[0].Alias != "Jira тест" {
		t.Fatalf("the new name never reached the UI: %+v", reports)
	}
}

// The notes for the model are read by the model, not by the plugin: editing
// them must be applied — they have to reach the report, the MCP surface and
// the platform — and must NOT restart the connection.
func TestEditingTheNotesForTheModelIsAChangeWithoutARestart(t *testing.T) {
	before := &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "jira", map[string]any{"base_url": "https://a"}),
	}}
	after := &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "jira", map[string]any{"base_url": "https://a"}),
	}}
	after.Plugins[0].AINotes = "PROJ — биллинг, OPS — эксплуатация."

	if pluginsSig(before) == pluginsSig(after) {
		t.Fatal("new notes for the model were not seen as a change")
	}
	if before.Plugins[0].Fingerprint() != after.Plugins[0].Fingerprint() {
		t.Error("new notes for the model restarted the plugin")
	}
	lifted := *after
	lifted.Plugins = []config.PluginInstance{after.Plugins[0]}
	lifted.Plugins[0].AINotesUnbounded = true
	if pluginsSig(after) == pluginsSig(&lifted) {
		t.Fatal("lifting the bound was not seen as a change — the warning would never reach the list")
	}
}

// Like the alias, the notes must reach the live report without a restart.
func TestEditedNotesReachTheReportWithoutARestart(t *testing.T) {
	root, store := stateWithStore(t)
	writeDemoPlugin(t, store)
	m := NewPluginManager(context.Background(), root, store)

	p := instance("7QK3M9XBWZ0FVT2H", "jira_prod", map[string]any{"base_url": "https://a.example.com"})
	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{p}})

	p.AINotes, p.AINotesUnbounded = "PROJ — биллинг.", true
	m.Sync(context.Background(), &config.Config{Plugins: []config.PluginInstance{p}})

	reports := m.Reports()
	if len(reports) != 1 || reports[0].AINotes != "PROJ — биллинг." || !reports[0].AINotesUnbounded {
		t.Fatalf("the notes never reached the report: %+v", reports)
	}
}

// Turning a webhook on must make the connector REBIND, even though the plugin
// process itself is unaffected. Leaving the listener settings out of the reload
// signature made the change look applied — the file was reloaded, the page
// showed the URL — while nothing was listening on the port.
func TestTurningAWebhookOnIsAChangeWorthApplying(t *testing.T) {
	before := &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "jira", map[string]any{"base_url": "https://a"}),
	}}
	after := &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "jira", map[string]any{"base_url": "https://a"}),
	}}
	on := true
	after.Webhooks = config.Webhooks{Listen: "0.0.0.0:8766"}
	after.Plugins[0].Webhook = &on
	after.Plugins[0].WebhookSecret = "HOOK"

	if pluginsSig(before) == pluginsSig(after) {
		t.Fatal("switching the listener on was not seen as a change")
	}
	// …and it must NOT restart the plugin: the process does not care about ports.
	if before.Plugins[0].Fingerprint() != after.Plugins[0].Fingerprint() {
		t.Error("moving a listener restarted the plugin")
	}
}

// How often a connection is read is a setting the loop reads live: changing it
// must be seen as a change to apply, and must NOT restart the plugin — the
// process does not care how often it is asked.
func TestChangingThePollIntervalIsAChangeWithoutARestart(t *testing.T) {
	before := &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "notion", map[string]any{"roots": []any{}}),
	}}
	after := &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "notion", map[string]any{"roots": []any{}}),
	}}
	after.Plugins[0].PollSeconds = 5

	if pluginsSig(before) == pluginsSig(after) {
		t.Fatal("a new poll interval was not seen as a change")
	}
	if before.Plugins[0].Fingerprint() != after.Plugins[0].Fingerprint() {
		t.Error("a new poll interval restarted the plugin")
	}
}

// Who may ask the bot is read by the connector when an event is forwarded, not by
// the plugin (#476): a new list must be applied — or a person just removed from it
// keeps asking until the next restart — and must NOT restart the connection.
// Found on the stand: the edit was saved, the page showed it, events were judged
// by the old list.
func TestChangingWhoMayAskIsAChangeWithoutARestart(t *testing.T) {
	before := &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "gitea", map[string]any{"base_url": "https://a"}),
	}}
	after := &config.Config{Plugins: []config.PluginInstance{
		instance("7QK3M9XBWZ0FVT2H", "gitea", map[string]any{"base_url": "https://a"}),
	}}
	after.Plugins[0].Askers = []string{"petrov"}
	if pluginsSig(before) == pluginsSig(after) {
		t.Fatal("a new list of who may ask was not seen as a change")
	}
	members := &config.Config{Plugins: []config.PluginInstance{after.Plugins[0]}}
	members.Plugins[0].AskersMembers = true
	if pluginsSig(after) == pluginsSig(members) {
		t.Fatal("letting the org's members ask was not seen as a change")
	}
	if before.Plugins[0].Fingerprint() != members.Plugins[0].Fingerprint() {
		t.Error("changing who may ask restarted the plugin")
	}
}
