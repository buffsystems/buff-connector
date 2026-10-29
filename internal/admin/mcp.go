package admin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buffsystems/buff-connector/internal/config"
)

// The custom-server half of the local surface (#351). Like installing a
// plugin, bringing a server of one's own happens HERE, on the operator's own
// machine: the archive is dropped on this page, the repository is fetched
// from this machine, and the consent is given here. The platform's UI only
// ever sees the result, marked as the operator's own.

func (s *Server) mcpRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/mcp/upload", s.guard(s.auth(s.mcpUpload)))
	mux.HandleFunc("/api/mcp/github", s.guard(s.auth(s.mcpGitHub)))
	mux.HandleFunc("/api/mcp/save", s.guard(s.auth(s.mcpSave)))
	mux.HandleFunc("/api/mcp/marks", s.guard(s.auth(s.mcpMarks)))
}

// prepareTimeout bounds an install. npm on a slow line is minutes; a limit
// that fails an honest install is worse than one that lets a hung one run.
const prepareTimeout = 30 * time.Minute

// mcpUpload takes the dropped archive (multipart field "archive") and answers
// with the draft.
func (s *Server) mcpUpload(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "плагины не включены"})
		return
	}
	file, header, err := r.FormFile("archive")
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "в запросе нет архива"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), prepareTimeout)
	defer cancel()
	draft, err := s.plugins.PrepareCustomArchive(ctx, header.Filename, data)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "draft": draft})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "draft": draft})
}

func (s *Server) mcpGitHub(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil || s.plugins == nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "неверный запрос"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), prepareTimeout)
	defer cancel()
	draft, err := s.plugins.PrepareCustomGitHub(ctx, req.URL, strings.TrimSpace(req.Token))
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "draft": draft})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "draft": draft})
}

type mcpSaveReq struct {
	ID      string            `json:"id"`
	Source  string            `json:"source"`
	Origin  string            `json:"origin"`
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`

	Alias      string         `json:"alias"`
	DevProject string         `json:"dev_project"`
	Enabled    *bool          `json:"enabled"`
	Config     map[string]any `json:"config"`
	// Credentials by name: the kind (env, secret, header) and the value. A
	// blank value means "unchanged" — the page never receives the current one.
	Credentials map[string]struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	} `json:"credentials"`
	ReadOnly         []string `json:"read_only"`
	Accepted         bool     `json:"accepted"`
	AINotes          string   `json:"ai_notes"`
	AINotesUnbounded bool     `json:"ai_notes_unbounded"`
	Fingerprint      string   `json:"fingerprint"`
}

// mcpSave writes the connection. Secrets go to 0600 files as for any other
// connection; the consent is written into the config, where the connector
// checks it before running anything.
func (s *Server) mcpSave(w http.ResponseWriter, r *http.Request) {
	var req mcpSaveReq
	if err := readJSON(r, &req); err != nil || s.plugins == nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "неверный запрос"})
		return
	}
	cfg, err := config.LoadLenient(s.cfgPath)
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	inst := config.PluginInstance{
		ID: req.ID, Alias: req.Alias, DevProject: req.DevProject, Enabled: req.Enabled, Config: req.Config,
		AINotes: req.AINotes, AINotesUnbounded: req.AINotesUnbounded,
		MCP: &config.CustomMCP{
			Source: req.Source, Origin: req.Origin, Name: req.Name, Version: req.Version,
			Command: strings.TrimSpace(req.Command), Args: req.Args, Env: req.Env, URL: strings.TrimSpace(req.URL),
			ReadOnly: req.ReadOnly, Accepted: req.Accepted,
		},
	}
	if inst.ID == "" {
		inst.ID = config.NewInstanceID()
	}
	// The slug is what the model sees in every tool name. An alias with no
	// letters reduces to nothing, so the packaging's own name is used then —
	// and for a remote server, which has no packaging, the host it lives on.
	name := inst.Alias
	if name == "" || config.SlugFor(name) == "i" {
		name = req.Name
	}
	if name == "" && inst.MCP.URL != "" {
		if u, err := url.Parse(inst.MCP.URL); err == nil {
			name = "mcp " + u.Hostname()
		}
	}
	taken := map[string]bool{}
	for _, other := range cfg.Plugins {
		if other.ID != inst.ID {
			taken[other.Slug] = true
		}
	}
	inst.Slug = config.UniqueSlugFor(name, taken)

	secretsDir := filepath.Join(cfg.StateDir, "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	prev := findInstance(cfg, inst.ID)
	for credName, c := range req.Credentials {
		if !config.MCPCredentialKinds[c.Kind] || !config.IsEnvName(credName) {
			writeJSON(w, 400, map[string]any{"ok": false,
				"error": fmt.Sprintf("учётные данные «%s»: имя должно быть идентификатором, вид — env, secret или header", credName)})
			return
		}
		if c.Value == "" {
			if old, ok := credentialOf(prev, credName); ok {
				inst.Credentials = append(inst.Credentials, old)
			}
			continue
		}
		file := filepath.Join(secretsDir, fmt.Sprintf("mcp-%s-%s.token", inst.ID, credName))
		if err := os.WriteFile(file, []byte(c.Value), 0o600); err != nil {
			writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		cred := config.InstanceCredential{Name: credName, Kind: c.Kind, TokenFile: file}
		if c.Kind == "header" {
			cred.Header = "Authorization"
		}
		inst.Credentials = append(inst.Credentials, cred)
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

// mcpMarks records which tools the operator marked as only reading. Its own
// call, because the tools are only known once the server runs — and ticking a
// box must not go through the whole form.
func (s *Server) mcpMarks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string   `json:"id"`
		ReadOnly    []string `json:"read_only"`
		Fingerprint string   `json:"fingerprint"`
	}
	if err := readJSON(r, &req); err != nil || s.plugins == nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "неверный запрос"})
		return
	}
	cfg, err := config.LoadLenient(s.cfgPath)
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	inst := findInstance(cfg, req.ID)
	if inst == nil || inst.MCP == nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "нет своего сервера с таким id"})
		return
	}
	inst.MCP.ReadOnly = req.ReadOnly
	if err := config.SaveIf(s.cfgPath, cfg, req.Fingerprint); err != nil {
		writeJSON(w, 409, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.applyPlugins()
	writeJSON(w, 200, map[string]any{"ok": true})
}

// withCustom adds, to each custom connection's config entry, what the page
// needs to draw its form: the mcp block and the fields its packaging asks.
func (s *Server) withCustom(cfg *config.Config, entries []map[string]any) []map[string]any {
	for i := range cfg.Plugins {
		p := &cfg.Plugins[i]
		if p.MCP == nil || i >= len(entries) {
			continue
		}
		entries[i]["mcp"] = map[string]any{
			"source": p.MCP.Source, "origin": p.MCP.Origin, "name": p.MCP.Name, "version": p.MCP.Version,
			"command": p.MCP.Command, "args": p.MCP.Args, "env": p.MCP.Env, "url": p.MCP.URL,
			"read_only": p.MCP.ReadOnly, "accepted": p.MCP.Accepted,
			"fields": s.plugins.CodeFields(p.ID),
		}
	}
	return entries
}
