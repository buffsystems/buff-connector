package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/buffsystems/buff-connector/internal/catalog"
	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// PluginManager turns the plugins: section of the config into running
// processes, and keeps the two in step.
//
// Reconciliation is per instance, and that is the whole point: editing one
// connection restarts one process. The old behaviour — one fingerprint over the
// entire config — meant changing a Jira project key also bounced every other
// integration the operator ran.

type runningInstance struct {
	inst        *plugin.Instance
	fingerprint string
	report      plugin.InstanceReport
	// pollSuggested is the manifest's poll_seconds, kept here so the loop's
	// clock does not read the plugin store off disk once a second.
	pollSuggested time.Duration
}

type PluginManager struct {
	// base outlives any single request. A plugin process is tied to the context
	// it was started with, so starting one from an HTTP handler's context killed
	// it the moment the handler returned — the operator saw "running", and the
	// process was already gone.
	base     context.Context
	stateDir string
	store    *plugin.Store
	starter  *plugin.Starter
	uids     *uidAllocator
	runtime  string

	// What the catalogue says exists. A function rather than a client: the
	// manager reports versions, it does not fetch catalogues, and a connector
	// that has never reached one simply claims nothing.
	catalogue func() *catalog.Index

	// github is where repositories are downloaded from; empty means GitHub.
	// Set by tests to a stand-in.
	github string

	mu      sync.Mutex
	running map[string]*runningInstance
	failed  map[string]plugin.InstanceReport
}

func NewPluginManager(base context.Context, stateDir string, store *plugin.Store) *PluginManager {
	if base == nil {
		base = context.Background()
	}
	return &PluginManager{
		base:     base,
		stateDir: stateDir,
		store:    store,
		starter:  &plugin.Starter{Priv: plugin.CheckPrivilege()},
		uids:     newUIDAllocator(stateDir),
		runtime:  filepath.Join(stateDir, "runtime", "python", "bin", "python3"),
		running:  map[string]*runningInstance{},
		failed:   map[string]plugin.InstanceReport{},
	}
}

// Privilege is what the connector can actually do about isolation, so the UI and
// the report can say so before an operator installs anything.
func (m *PluginManager) Privilege() plugin.Privilege { return m.starter.Priv }

// Sync brings the running set in line with the configuration.
func (m *PluginManager) Sync(ctx context.Context, cfg *config.Config) {
	// Connections that cannot run at all — a slug or id used twice. They are
	// reported, not started, and their neighbours are untouched.
	problems := cfg.InstanceProblems()

	wanted := map[string]*config.PluginInstance{}
	for i := range cfg.Plugins {
		p := &cfg.Plugins[i]
		if p.IsEnabled() {
			wanted[p.ID] = p
		}
	}

	m.mu.Lock()
	for id, run := range m.running {
		if _, keep := wanted[id]; !keep {
			run.inst.Stop()
			delete(m.running, id)
		}
	}
	// Instances that never came up are tracked separately; they have to be
	// forgotten too, or removing a broken connection leaves its error on screen
	// for ever.
	for id := range m.failed {
		if _, keep := wanted[id]; !keep {
			delete(m.failed, id)
		}
	}
	m.mu.Unlock()

	ids := make([]string, 0, len(wanted))
	for id := range wanted {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		p := wanted[id]
		fp := p.Fingerprint()

		m.mu.Lock()
		run, up := m.running[id]
		m.mu.Unlock()
		// Unchanged AND alive: leave it be. Alive matters as much as unchanged —
		// an instance whose process has gone must come back even when its
		// configuration is identical, or a fingerprint match keeps a dead
		// connection dead for ever.
		if reason, broken := problems[id]; broken {
			m.mu.Lock()
			if run, was := m.running[id]; was {
				run.inst.Stop()
				delete(m.running, id)
			}
			broken := plugin.InstanceReport{
				ID: p.ID, Slug: p.Slug, Alias: p.Alias, Plugin: p.Plugin,
				Version: p.Version, DevProject: p.DevProject,
				AINotes: p.AINotes, AINotesUnbounded: p.AINotesUnbounded,
				Status: "failed", LastError: reason,
			}
			if p.IsCustom() {
				broken.Custom, broken.Source = true, p.MCP.Source
				broken.Plugin, broken.Version = p.MCP.Name, p.MCP.Version
			}
			m.failed[id] = broken
			m.mu.Unlock()
			continue
		}
		if up && run.fingerprint == fp && alive(run.inst) {
			// The alias is display-only, and deliberately outside the
			// fingerprint: renaming a connection must not restart it. It still
			// has to reach the UI, though — otherwise a rename lands in the file
			// and the page keeps showing the old name until something unrelated
			// makes the process restart.
			// The notes for the model are the same kind of thing: the model
			// reads them, the process does not, and an edit must show up.
			// The read-only marks of a custom server are the same kind of
			// thing again: the operator's decision, applied live.
			m.mu.Lock()
			run.report.Alias = p.Alias
			run.report.AINotes, run.report.AINotesUnbounded = p.AINotes, p.AINotesUnbounded
			if p.IsCustom() {
				run.inst.SetReadOnlyTools(p.MCP.ReadOnly)
			}
			m.mu.Unlock()
			continue
		}
		if up {
			run.inst.Stop()
			m.mu.Lock()
			delete(m.running, id)
			m.mu.Unlock()
		}
		m.start(p, fp)
	}
}

// start brings one instance up, recording a readable reason when it cannot.
func (m *PluginManager) start(p *config.PluginInstance, fp string) {
	// From this moment the connection is answerable for what happens in the
	// customer's tracker. Leaving the mark to the first poll loses everything
	// written in between.
	seedInstanceWatermark(m.stateDir, p.ID)
	report := plugin.InstanceReport{
		ID: p.ID, Slug: p.Slug, Alias: p.Alias, Plugin: p.Plugin,
		Version: p.Version, DevProject: p.DevProject,
		AINotes: p.AINotes, AINotesUnbounded: p.AINotesUnbounded,
	}
	fail := func(err error) {
		report.Status = "failed"
		report.LastError = err.Error()
		m.mu.Lock()
		m.failed[p.ID] = report
		m.mu.Unlock()
	}
	if p.IsCustom() {
		m.startCustom(p, fp, report, func(err error) {
			report.Custom, report.Source = true, p.MCP.Source
			fail(err)
		})
		return
	}

	installed, err := m.store.Resolve(p.Plugin, p.Version)
	if err != nil {
		fail(err)
		return
	}
	if err := m.store.CheckIntegrity(installed); err != nil {
		fail(err)
		return
	}
	if reason, blocked := m.store.Blocked(installed.Manifest); blocked {
		report.Withdrawn = reason
		if m.store.IsCritical(installed.Manifest) {
			fail(fmt.Errorf("version withdrawn: %s", reason))
			return
		}
	}
	// Defaults are filled once, here, so the egress rules and the plugin read the
	// same value the form showed.
	cfg := installed.Manifest.WithDefaults(p.Config)
	if err := installed.Manifest.ValidateConfig(cfg); err != nil {
		fail(fmt.Errorf("settings: %w", err))
		return
	}
	egress, err := installed.Manifest.ResolveEgress(cfg)
	if err != nil {
		fail(err)
		return
	}
	for _, e := range egress {
		report.Egress = append(report.Egress, e.Label())
	}
	report.Egress = append(report.Egress, installed.Manifest.ResolveNetwork(cfg)...)
	runtime := m.interpreter()
	if runtime == "" {
		fail(fmt.Errorf("плагину не на чем работать: нужен Python %d.%d или новее, доступный "+
			"пользователю подключения (интеграция работает под собственным пользователем ОС, "+
			"поэтому интерпретатор в домашнем каталоге root ей не подходит). Поставьте его "+
			"пакетным менеджером или разверните среду в %s",
			minPython[0], minPython[1], m.RuntimeDir()))
		return
	}

	uid, err := m.uids.For(p.ID)
	if err != nil {
		fail(err)
		return
	}
	dir, err := plugin.PrepareInstanceDir(m.stateDir, p.ID, uid, uid)
	if err != nil {
		fail(err)
		return
	}

	creds := map[string]plugin.Credential{}
	for _, c := range p.Credentials {
		cred := plugin.Credential{
			Kind: c.Kind, Header: c.Header, Token: c.Token,
			Username: c.Username, Password: c.Password,
		}
		// A minted kind is issued from its file at request time, by the
		// endpoints the MANIFEST names — never by ones the secret names.
		if plugin.Minted(c.Kind) {
			cred.File = c.TokenFile
			cred.OAuth = installed.Manifest.OAuthFor(c.Name, cfg)
		}
		creds[c.Name] = cred
	}
	gate := plugin.NewHTTPGate(egress, creds, nil)

	cfgJSON, _ := json.Marshal(cfg)
	spec := plugin.Spec{
		Path: runtime,
		Args: []string{filepath.Join(installed.Dir, installed.Manifest.Entry)},
		Dir:  dir,
		UID:  uid, GID: uid,
		Env: []string{
			"PYTHONUNBUFFERED=1",
			"PYTHONDONTWRITEBYTECODE=1",
			"HOME=" + dir,
			"TMPDIR=" + dir,
			"PYTHONPATH=" + installed.Dir,
			"BUFF_INSTANCE=" + p.ID,
			"BUFF_SLUG=" + p.Slug,
			"BUFF_CONFIG=" + string(cfgJSON),
		},
	}

	inst := plugin.NewInstance(m.starter, plugin.InstanceConfig{
		GUID: p.ID, Slug: p.Slug, Spec: spec,
	}, gate.Handler(nil))

	if err := inst.Start(m.base); err != nil {
		// Not fatal on its own: the supervisor keeps trying, and the state says
		// what happened. The exception is having no way to isolate the plugin,
		// which Start reports and will not retry.
		report.Status = string(inst.State().Status)
		report.LastError = err.Error()
	} else {
		report.Status = string(plugin.StatusRunning)
	}

	m.mu.Lock()
	delete(m.failed, p.ID)
	m.running[p.ID] = &runningInstance{
		inst: inst, fingerprint: fp, report: report,
		pollSuggested: time.Duration(installed.Manifest.PollSeconds) * time.Second,
	}
	m.mu.Unlock()
}

// PollSuggested is how often the running instance's plugin suggests being read;
// zero for a plugin that says nothing, and for a connection that is not up.
func (m *PluginManager) PollSuggested(id string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run, ok := m.running[id]; ok {
		return run.pollSuggested
	}
	return 0
}

// alive reports whether an instance is actually able to answer. A status of
// "running" recorded at start is not the same as a process that is still there.
func alive(inst *plugin.Instance) bool {
	st := inst.State()
	return st.Status == plugin.StatusRunning || st.Status == plugin.StatusStarting ||
		st.Status == plugin.StatusBackoff
}

// Reports is the current picture, live state folded in.
func (m *PluginManager) Reports() []plugin.InstanceReport {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]plugin.InstanceReport, 0, len(m.running)+len(m.failed))
	for _, run := range m.running {
		r := run.report
		st := run.inst.State()
		r.Status = string(st.Status)
		r.Restarts = st.Restarts
		if st.LastError != "" {
			r.LastError = st.LastError
		}
		for _, t := range run.inst.Tools() {
			r.Tools = append(r.Tools, t.Name)
			if t.ReadOnly {
				r.ReadOnly = append(r.ReadOnly, t.Name)
			}
			if t.ReadOnlyHint {
				r.ReadOnlyHint = append(r.ReadOnlyHint, t.Name)
			}
			if t.Vision {
				r.Vision = append(r.Vision, t.Name)
			}
		}
		// A custom server names itself in the handshake; that beats whatever
		// the archive or the repository was called.
		if r.Custom {
			if info := run.inst.Info(); info.Name != "" {
				r.Plugin, r.Version = info.Name, info.Version
			}
		}
		out = append(out, r)
	}
	for _, r := range m.failed {
		out = append(out, r)
	}
	// Said once, here, so the local page, the MCP surface and the report to the
	// platform cannot disagree about whether an update is out.
	// A custom server is not in the catalogue, whatever it happens to be
	// called; an "update" offered for it would be somebody else's plugin.
	if idx := m.index(); idx != nil {
		for i := range out {
			if !out[i].Custom {
				out[i].Latest = idx.Update(out[i].Plugin, out[i].Version)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// index is the catalogue as this connector last verified it, or nil.
func (m *PluginManager) index() *catalog.Index {
	if m.catalogue == nil {
		return nil
	}
	return m.catalogue()
}

// WithCatalogue tells the manager where to read what versions exist.
func (m *PluginManager) WithCatalogue(f func() *catalog.Index) *PluginManager {
	m.catalogue = f
	return m
}

// Instance hands back a running instance by slug, for the MCP relay.
func (m *PluginManager) Instance(slug string) *plugin.Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, run := range m.running {
		if run.report.Slug == slug {
			return run.inst
		}
	}
	return nil
}

// StopAll ends every instance.
func (m *PluginManager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, run := range m.running {
		run.inst.Stop()
		delete(m.running, id)
	}
}

// uidAllocator hands each instance its own OS user id and remembers the choice,
// so an instance's files stay owned by the same user across restarts.
type uidAllocator struct {
	path string
	base uint32
	mu   sync.Mutex
}

func newUIDAllocator(stateDir string) *uidAllocator {
	return &uidAllocator{path: filepath.Join(stateDir, "instances", "uids.json"), base: 61000}
}

func (a *uidAllocator) For(id string) (uint32, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	assigned := map[string]uint32{}
	if b, err := os.ReadFile(a.path); err == nil {
		_ = json.Unmarshal(b, &assigned)
	}
	if uid, ok := assigned[id]; ok {
		return uid, nil
	}
	next := a.base
	used := map[uint32]bool{}
	for _, u := range assigned {
		used[u] = true
	}
	for used[next] {
		next++
	}
	assigned[id] = next

	b, err := json.Marshal(assigned)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o711); err != nil {
		return 0, err
	}
	if err := os.WriteFile(a.path, b, 0o600); err != nil {
		return 0, fmt.Errorf("record instance uid: %w", err)
	}
	return next, nil
}

// Check runs the plugin's own connection test for one instance — the "does this
// actually reach your Jira" button in setup. Without it an operator fills in a
// form and learns the answer only from whether events start arriving.
func (m *PluginManager) Check(ctx context.Context, id string) (map[string]any, error) {
	m.mu.Lock()
	run, ok := m.running[id]
	failed, wasFailed := m.failed[id]
	m.mu.Unlock()
	if !ok {
		if wasFailed {
			return nil, fmt.Errorf("%s", failed.LastError)
		}
		return nil, fmt.Errorf("instance %s is not running", id)
	}
	// A server the operator brought has no buff/check; what it does have is a
	// handshake that either happened or did not, and a list of tools.
	if run.report.Custom {
		// A round trip, not a glance at the state: the server has to answer NOW.
		var listed struct {
			Tools []struct{ Name string } `json:"tools"`
		}
		if err := run.inst.Call(ctx, "tools/list", map[string]any{}, &listed); err != nil {
			return nil, err
		}
		info := run.inst.Info()
		return map[string]any{"ok": true, "detail": fmt.Sprintf("сервер %s %s отвечает, инструментов: %d",
			info.Name, info.Version, len(listed.Tools))}, nil
	}
	var out map[string]any
	if err := run.inst.Call(ctx, "buff/check", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Log is the tail of one instance's output.
func (m *PluginManager) Log(id string) []plugin.LogLine {
	m.mu.Lock()
	run, ok := m.running[id]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return run.inst.Log()
}

// Store exposes the installed set for the local admin surface.
func (m *PluginManager) Store() *plugin.Store { return m.store }

// RuntimeInstalled reports whether plugins have an interpreter to run under —
// the staged one or a suitable system Python. The local page says which.
func (m *PluginManager) RuntimeInstalled() bool { return m.interpreter() != "" }

// Runtime is the interpreter in use, for the local page and the report. Empty
// when the machine has none.
func (m *PluginManager) Runtime() string { return m.interpreter() }
