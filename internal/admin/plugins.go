package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buffsystems/buff-connector/internal/catalog"
	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/mcpsource"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// The plugin half of the local admin surface: the catalogue, installing, and the
// lifecycle of an instance.
//
// It lives only here — installing code into a customer's network is the
// operator's decision, taken on their own machine. The platform's own UI shows
// what is installed and never changes it.

// PluginHost is what this surface needs from the running agent. A narrow
// interface rather than the manager itself, so the admin package does not depend
// on the agent's internals.
type PluginHost interface {
	Privilege() plugin.Privilege
	Reports() []plugin.InstanceReport
	Store() *plugin.Store
	Check(ctx context.Context, id string) (map[string]any, error)
	Log(id string) []plugin.LogLine
	RuntimeInstalled() bool
	// Runtime is the interpreter plugins run under — a staged one or the
	// machine's own. The page shows it, because "which Python" is the first
	// question when a plugin behaves oddly.
	Runtime() string
	Sync(ctx context.Context, cfg *config.Config)

	// The custom-server half (#351): turning what the operator brought into
	// a draft, and what this machine can run.
	PrepareCustomArchive(ctx context.Context, name string, data []byte) (mcpsource.Draft, error)
	PrepareCustomGitHub(ctx context.Context, url, token string) (mcpsource.Draft, error)
	Toolchain() mcpsource.Toolchain
	CodeFields(id string) []mcpsource.Field
}

// WithPlugins wires the plugin surface. Without it the connector still runs as a
// git bridge — which is what it is out of the box.
func (s *Server) WithPlugins(h PluginHost, cat *catalog.Client) *Server {
	s.plugins = h
	s.catalog = cat
	s.oauth = newOAuthFlow(s.lookupOAuth)
	return s
}

func (s *Server) pluginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/plugins", s.guard(s.auth(s.pluginsState)))
	mux.HandleFunc("/api/plugins/install", s.guard(s.auth(s.pluginInstall)))
	mux.HandleFunc("/api/catalog", s.guard(s.auth(s.catalogList)))
	mux.HandleFunc("/api/instances/save", s.guard(s.auth(s.instanceSave)))
	mux.HandleFunc("/api/instances/upgrade", s.guard(s.auth(s.instanceUpgrade)))
	mux.HandleFunc("/api/instances/delete", s.guard(s.auth(s.instanceDelete)))
	mux.HandleFunc("/api/instances/check", s.guard(s.auth(s.instanceCheck)))
	mux.HandleFunc("/api/instances/log", s.guard(s.auth(s.instanceLog)))
	s.mcpRoutes(mux)
	s.oauthRoutes(mux)
}

// pluginsState is everything the page needs to draw the plugin sections.
func (s *Server) pluginsState(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	priv := s.plugins.Privilege()
	installed := []map[string]any{}
	if list, err := s.plugins.Store().List(); err == nil {
		for _, in := range list {
			installed = append(installed, map[string]any{
				"name": in.Manifest.Name, "version": in.Manifest.Version,
				"summary": in.Manifest.Summary, "publisher": in.Manifest.Publisher,
				"digest": in.Digest[:12], "host_api": in.Manifest.HostAPI,
				"schema": in.Manifest.Schema, "credentials": in.Manifest.Creds,
				"webhook": in.Manifest.Webhook, "signature": in.Manifest.Signature,
				// The plugin's suggested read interval — the form's placeholder,
				// so the operator sees what "leave empty" will mean.
				"poll_seconds": in.Manifest.PollSeconds,
			})
		}
	}
	cfg, _ := config.LoadLenient(s.cfgPath)
	if cfg == nil {
		cfg = &config.Config{}
	}
	writeJSON(w, 200, map[string]any{
		"enabled": true,
		// Where plugins come from is a security fact and belongs on the page:
		// an operator approving an install should see the source without
		// reading a process's environment, and an audit needs to tell our
		// registry from a mirror.
		"catalog_url": s.catalogURL(),
		// Where the connector listens, so the page can say "the receiver is off"
		// instead of offering a switch that quietly does nothing.
		"webhooks": map[string]any{"listen": cfg.Webhooks.Listen, "host": cfg.Webhooks.Host},
		// The operator has to know BEFORE installing anything whether this
		// connector can isolate a plugin at all.
		"isolation": map[string]any{"available": priv.CanSwitchUID, "reason": priv.Reason},
		"runtime":   map[string]any{"installed": s.plugins.RuntimeInstalled(), "path": s.plugins.Runtime()},
		"installed": installed,
		"instances": s.plugins.Reports(),
		"config":    s.withCustom(cfg, instanceConfigs(cfg)),
		// What a server the operator brings could run on, as the connection's
		// user would see it — so the page can say "no Node.js here" up front.
		"toolchain": s.plugins.Toolchain(),
	})
}

// instanceConfigs is what the page needs to draw the settings form, including
// the webhook URL to paste into the customer's tracker. The URL is rendered
// here, from the GUID, so the page never has to build one out of parts.
func instanceConfigs(cfg *config.Config) []map[string]any {
	out := make([]map[string]any, 0, len(cfg.Plugins))
	for i := range cfg.Plugins {
		p := &cfg.Plugins[i]
		out = append(out, map[string]any{
			"id": p.ID, "plugin": p.Plugin, "version": p.Version, "alias": p.Alias,
			"slug": p.Slug, "dev_project": p.DevProject, "enabled": p.Enabled,
			"config": p.Config,
			// The account a credential belongs to is not a secret, and showing
			// it back is what lets a connection be re-saved without retyping it.
			"credential_users": credentialUsers(p),
			// Which of the plugin's kinds each credential IS (a service
			// account or a signed-in person), and for a sign-in, whether it
			// has happened — the page draws the "sign in" button from these.
			"credential_kinds": credentialKinds(p),
			"oauth":            oauthState(p),
			"webhook":          p.WebhookEnabled(),
			"webhook_url":      cfg.WebhookURL(p),
			"poll_seconds":     p.PollSeconds,
			// What the operator wrote for the model, and whether they lifted the
			// bound to write it — the page draws the risk card from the flag.
			"ai_notes":           p.AINotes,
			"ai_notes_unbounded": p.AINotesUnbounded,
			"askers":             p.Askers,
			"askers_members":     p.AskersMembers,
			// A signing secret the SOURCE introduced (Notion's verification
			// token): shown so the operator can paste it back into the source
			// and confirm the subscription; a pending one waits for acceptance.
			"signing_secret":         plugin.LoadSigningSecret(cfg.StateDir, p.ID),
			"signing_secret_pending": plugin.PendingSigningSecret(cfg.StateDir, p.ID),
		})
	}
	return out
}

// credentialUsers is the account named beside each credential, by credential.
func credentialUsers(p *config.PluginInstance) map[string]string {
	out := map[string]string{}
	for _, c := range p.Credentials {
		if c.Username != "" {
			out[c.Name] = c.Username
		}
	}
	return out
}

// credentialKinds is the kind each credential was saved as, by credential.
func credentialKinds(p *config.PluginInstance) map[string]string {
	out := map[string]string{}
	for _, c := range p.Credentials {
		out[c.Name] = c.Kind
	}
	return out
}

// oauthState says, for each oauth2 credential, whether a refresh token is on
// file and who it belongs to.
func oauthState(p *config.PluginInstance) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, c := range p.Credentials {
		if c.Kind != plugin.KindOAuth2 {
			continue
		}
		ok, who := plugin.SignedIn(c.TokenFile)
		out[c.Name] = map[string]any{"signed_in": ok, "account": who}
	}
	return out
}

// catalogURL is the address plugins are fetched from, or empty when this
// connector has no catalogue wired at all.
func (s *Server) catalogURL() string {
	if s.catalog == nil {
		return ""
	}
	return s.catalog.BaseURL
}

// catalogList reads the published catalogue, falling back to the cached copy so
// the list is still there without a network — clearly marked with its date.
func (s *Server) catalogList(w http.ResponseWriter, r *http.Request) {
	if s.catalog == nil {
		writeJSON(w, 200, map[string]any{"available": false,
			"error": "no catalogue is configured for this connector"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	idx, err := s.catalog.Fetch(ctx)
	if err == nil {
		writeJSON(w, 200, map[string]any{"available": true, "live": true, "index": idx})
		return
	}
	if cached, cErr := s.catalog.Cached(); cErr == nil {
		writeJSON(w, 200, map[string]any{
			"available": true, "live": false, "index": cached,
			"error": "showing the last catalogue read: " + err.Error(),
		})
		return
	}
	writeJSON(w, 200, map[string]any{"available": false, "error": err.Error()})
}

func (s *Server) pluginInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := readJSON(r, &req); err != nil || s.catalog == nil || s.plugins == nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	idx, err := s.catalog.Fetch(ctx)
	if err != nil {
		writeJSON(w, 502, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	entry, version, ok := idx.Find(req.Name, req.Version)
	if !ok {
		writeJSON(w, 404, map[string]any{"ok": false,
			"error": fmt.Sprintf("%s %s is not in the catalogue", req.Name, req.Version)})
		return
	}
	bundle, sig, err := s.catalog.Download(ctx, entry.Name, version)
	if err != nil {
		writeJSON(w, 502, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	in, err := s.plugins.Store().Install(bundle, sig)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := s.plugins.Store().Record(in); err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "name": in.Manifest.Name,
		"version": in.Manifest.Version, "digest": in.Digest[:12]})
}

type instanceReq struct {
	ID          string            `json:"id"`
	Plugin      string            `json:"plugin"`
	Version     string            `json:"version"`
	Alias       string            `json:"alias"`
	DevProject  string            `json:"dev_project"`
	Enabled     *bool             `json:"enabled"`
	Config      map[string]any    `json:"config"`
	Credentials map[string]string `json:"credentials"`

	// CredentialUsers names the account a credential belongs to, for the
	// trackers whose authentication is an account AND a token (Jira Cloud,
	// Bitbucket Cloud). Empty for the rest.
	CredentialUsers map[string]string `json:"credential_users"`

	// CredentialKinds is the operator's choice where the plugin offers several
	// kinds (a service account OR a signed-in person). Empty means the
	// plugin's first kind.
	CredentialKinds map[string]string `json:"credential_kinds"`

	// Webhook says this connection accepts callbacks. WHERE the connector
	// listens is not asked here: that is one address for the whole machine.
	Webhook *bool `json:"webhook"`

	// PollSeconds is how often this connection is read; zero leaves it to the
	// plugin's suggestion or the connector's default.
	PollSeconds int `json:"poll_seconds"`

	// AINotes is what the operator tells the model about this connection;
	// AINotesUnbounded is the operator's word that they accepted the warning
	// about a long text and lifted the length bound.
	AINotes          string `json:"ai_notes"`
	AINotesUnbounded bool   `json:"ai_notes_unbounded"`

	// Askers is who may ask the bot from a comment, one rule per entry, and
	// AskersMembers adds the organisation's members; both empty — anyone.
	Askers        []string `json:"askers"`
	AskersMembers bool     `json:"askers_members"`

	// SigningSecret is the source's own signing secret, entered or accepted by
	// the operator. Empty means unchanged — the connector normally learns it
	// from the source's handshake by itself.
	SigningSecret string `json:"signing_secret"`

	Fingerprint string `json:"fingerprint"`
}

// instanceSave creates or updates one connection. Secrets go to 0600 files and
// the YAML keeps only a reference, so a config file can be copied, diffed or
// committed without carrying a customer's token.
func (s *Server) instanceSave(w http.ResponseWriter, r *http.Request) {
	var req instanceReq
	if err := readJSON(r, &req); err != nil || s.plugins == nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	cfg, err := config.LoadLenient(s.cfgPath)
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	installed, err := s.plugins.Store().Resolve(req.Plugin, req.Version)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := installed.Manifest.ValidateConfig(req.Config); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := config.ValidateAskers(req.Askers); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	inst := config.PluginInstance{
		ID: req.ID, Plugin: req.Plugin, Version: req.Version, Alias: req.Alias,
		DevProject: req.DevProject, Enabled: req.Enabled, Config: req.Config,
		Webhook: req.Webhook, PollSeconds: req.PollSeconds,
		AINotes: req.AINotes, AINotesUnbounded: req.AINotesUnbounded,
		Askers: liveAskers(req.Askers), AskersMembers: req.AskersMembers,
	}
	if inst.ID == "" {
		inst.ID = config.NewInstanceID()
	}
	name := inst.Alias
	if name == "" {
		name = inst.Plugin
	}
	// Two connections an operator calls "Jira прод" and "Jira тест" reduce to the
	// same identifier; sorting that out is ours, not something they should meet
	// as an error.
	taken := map[string]bool{}
	for _, other := range cfg.Plugins {
		if other.ID != inst.ID {
			taken[other.Slug] = true
		}
	}
	inst.Slug = config.SlugOnSave(findInstance(cfg, inst.ID), name, taken)

	secretsDir := filepath.Join(cfg.StateDir, "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	prev := findInstance(cfg, inst.ID)
	// The secret is MINTED, never typed: it is the credential in a URL, and an
	// operator asked to invent one invents a weak one. It survives a rename and
	// a re-save, because it is already pasted into the customer's tracker.
	if inst.Webhook != nil && *inst.Webhook {
		if prev != nil && prev.WebhookSecret != "" {
			inst.WebhookSecret = prev.WebhookSecret
		} else {
			inst.WebhookSecret = config.NewInstanceID()
		}
	}
	// The plugin decides whether it can receive callbacks at all; a route for a
	// plugin that cannot is an open door onto nothing.
	if installed.Manifest.Webhook == plugin.WebhookNone {
		inst.Webhook, inst.WebhookSecret = nil, ""
	}
	if token := strings.TrimSpace(req.SigningSecret); token != "" {
		if err := plugin.SaveSigningSecret(cfg.StateDir, inst.ID, token); err != nil {
			writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	for _, c := range installed.Manifest.Creds {
		value := req.Credentials[c.Name]
		account := strings.TrimSpace(req.CredentialUsers[c.Name])
		kind := req.CredentialKinds[c.Name]
		file := filepath.Join(secretsDir, fmt.Sprintf("%s-%s-%s.token", inst.Plugin, inst.ID, c.Name))
		if value == "" {
			// Left blank means "unchanged" — the page never receives the current
			// value, so it cannot echo it back. Changing the KIND without a new
			// value is not "unchanged": the old secret is of the other kind.
			if old, ok := credentialOf(prev, c.Name); ok {
				if kind != "" && kind != old.Kind {
					writeJSON(w, 400, map[string]any{"ok": false,
						"error": fmt.Sprintf("способ входа для «%s» изменён — введите учётные данные заново", c.Name)})
					return
				}
				inst.Credentials = append(inst.Credentials, config.ReuseCredential(old, account))
				continue
			}
			continue
		}
		if err := os.WriteFile(file, []byte(value), 0o600); err != nil {
			writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		inst.Credentials = append(inst.Credentials,
			config.CredentialOfKind(c.Name, kind, c.Kinds, c.Header, account, file))
	}

	replaced := false
	for i := range cfg.Plugins {
		if cfg.Plugins[i].ID == inst.ID {
			cfg.Plugins[i] = inst
			replaced = true
		}
	}
	if !replaced {
		cfg.Plugins = append(cfg.Plugins, inst)
	}
	// The platform's half of the checks (the notes bound among them) is a
	// mistake in the FORM, so it answers 400 with the reason — not the 409 the
	// write would give, which reads as "somebody else changed the file".
	if err := cfg.CheckWritable(); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := config.SaveIf(s.cfgPath, cfg, req.Fingerprint); err != nil {
		writeJSON(w, 409, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.applyPlugins()
	writeJSON(w, 200, map[string]any{"ok": true, "id": inst.ID, "slug": inst.Slug})
}

func (s *Server) instanceDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string `json:"id"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := readJSON(r, &req); err != nil || s.plugins == nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	cfg, err := config.LoadLenient(s.cfgPath)
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	kept := cfg.Plugins[:0]
	for _, p := range cfg.Plugins {
		if p.ID != req.ID {
			kept = append(kept, p)
		}
	}
	cfg.Plugins = kept
	if err := config.SaveIf(s.cfgPath, cfg, req.Fingerprint); err != nil {
		writeJSON(w, 409, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.applyPlugins()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) instanceCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil || s.plugins == nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.plugins.Check(ctx, req.ID)
	writeJSON(w, 200, checkAnswer(out, err))
}

// checkAnswer is what the page and the CLI colour by. A check the plugin itself
// reports as failed — «доступно 1 из 3 корней» — is a failure, not a green line
// with bad news in it: the plugin's verdict becomes the answer's, and its
// detail is the error the page shows.
func checkAnswer(out map[string]any, err error) map[string]any {
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if verdict, said := out["ok"].(bool); said && !verdict {
		detail, _ := out["detail"].(string)
		if detail == "" {
			detail = "проверка не прошла"
		}
		return map[string]any{"ok": false, "error": detail, "result": out}
	}
	return map[string]any{"ok": true, "result": out}
}

func (s *Server) instanceLog(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeJSON(w, 200, map[string]any{"lines": []any{}})
		return
	}
	writeJSON(w, 200, map[string]any{"lines": s.plugins.Log(r.URL.Query().Get("id"))})
}

// applyPlugins reloads the config and reconciles. The running agent watches the
// file too; doing it here as well means the operator sees the result of their
// own click immediately rather than after the next poll.
func (s *Server) applyPlugins() {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.plugins.Sync(ctx, cfg)
}

func findInstance(cfg *config.Config, id string) *config.PluginInstance {
	for i := range cfg.Plugins {
		if cfg.Plugins[i].ID == id {
			return &cfg.Plugins[i]
		}
	}
	return nil
}

func credentialOf(p *config.PluginInstance, name string) (config.InstanceCredential, bool) {
	if p == nil {
		return config.InstanceCredential{}, false
	}
	for _, c := range p.Credentials {
		if c.Name == name {
			return c, true
		}
	}
	return config.InstanceCredential{}, false
}

var _ = json.Marshal

// liveAskers keeps the rules the operator actually wrote: a blank line in the
// form is not a rule and must not reach the file.
func liveAskers(rules []string) []string {
	var out []string
	for _, r := range rules {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}
