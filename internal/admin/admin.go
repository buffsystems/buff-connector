// Package admin is the connector's LOCAL setup interface: a tiny web app the
// agent serves on loopback (127.0.0.1) so an operator configures providers,
// tokens and repos through forms instead of hand-editing YAML — and NOTHING
// leaves their machine. The token stays local (written to a 0600 secret file);
// repo lists and access checks go straight from the agent to THEIR provider.
//
// Saving writes the config file, which the running agent hot-reloads (plug-and-
// play: add a repo / change a token with no restart).
package admin

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/buffsystems/buff-connector/internal/catalog"
	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/i18n"
	"github.com/buffsystems/buff-connector/internal/identity"
	"github.com/buffsystems/buff-connector/internal/provider"
)

//go:embed index.html
var indexHTML []byte

// The page's look — fonts, tokens, base and the composite ui-* classes — is the Buff
// design system's plain CSS (#561), vendored by `make ui` from the pinned @buff/ui and
// embedded rather than fetched: the machine this runs on may have no route to the
// internet at all, and a font or style CDN is precisely what a firewalled perimeter
// cannot reach. The typeface (Geist) ships inside it, at /ui/fonts/.
//
//go:embed ui
var uiFS embed.FS

// The product's mark, restated here for the same two reasons as the design
// system's CSS: this page ships inside a Go binary with no build step, and
// modules do not read each other's files. Its source of truth is the app's
// frontend/public/favicon.svg and the site's components/logo.tsx — the lucide
// "brain" glyph in #2563eb. Do not invent one here; a mark that matches nothing
// the customer has seen reads as somebody else's page.
//
//go:embed brand
var brandFS embed.FS

// PairFunc redeems a pairing code (injected to avoid an import cycle with agent).
type PairFunc func(cfg *config.Config, code, label, version string) error

// Server serves the local setup UI + API.
type Server struct {
	cfgPath string
	version string
	pair    PairFunc
	// token is the local credential; see auth.go for why it lives in memory.
	token string
	// plugins and catalog are wired only when the agent is running alongside;
	// the setup UI on its own still configures git providers.
	plugins PluginHost
	catalog *catalog.Client
	// oauth is the sign-in of oauth2 credentials; see oauth.go.
	oauth *oauthFlow
	// ide is who uses the company's tools from Buff Code (#368); see ide.go.
	ide IDEJournal
}

func New(cfgPath, version string, pair PairFunc) *Server {
	return &Server{cfgPath: cfgPath, version: version, pair: pair, token: newToken()}
}

// guard blocks two browser-based attacks on a local server: DNS rebinding (a
// hostile domain pointed at 127.0.0.1 — rejected unless the Host is loopback) and
// CSRF (a hostile page POSTing to the local API — every state-changing call must
// carry a custom header, which a cross-origin request cannot set without a CORS
// preflight that we never grant).
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	loopback := func(h string) bool {
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
		return h == "127.0.0.1" || h == "localhost" || h == "::1"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !loopback(r.Host) {
			http.Error(w, i18n.ForRequest(r).T("чужой адрес"), http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && r.Header.Get("X-Buff-Local") != "1" {
			http.Error(w, i18n.ForRequest(r).T("нет локального заголовка"), http.StatusForbidden)
			return
		}
		next(withLang(w, r), r)
	}
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	// guard is the browser-facing lock (host + CSRF header); auth is the
	// credential every caller must carry, browser or not. Both, always.
	// The design system's CSS (with its fonts) and the brand mark are the only
	// unauthenticated routes: they carry nothing, and gating them would leave the
	// 401 page itself unstyled and unbranded — which is the page a stranger is most
	// likely to reach.
	mux.Handle("/ui/", http.StripPrefix("/", http.FileServer(http.FS(uiFS))))
	mux.Handle("/brand/", http.StripPrefix("/", http.FileServer(http.FS(brandFS))))
	mux.HandleFunc("/", s.guard(s.root))
	mux.HandleFunc("/api/state", s.guard(s.auth(s.state)))
	mux.HandleFunc("/api/validate", s.guard(s.auth(s.validate)))
	mux.HandleFunc("/api/save", s.guard(s.auth(s.save)))
	mux.HandleFunc("/api/pair", s.guard(s.auth(s.pairHandler)))
	s.pluginRoutes(mux)
	mux.HandleFunc("/api/ide", s.guard(s.auth(s.ideState)))
	return mux
}

// Serve runs the admin server on addr until ctx is done. addr should be a
// loopback address (e.g. 127.0.0.1:8765).
func (s *Server) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	if l := langOf(w); l != i18n.RU {
		v = localizeJSON(l, v)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadLenient(s.cfgPath)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	paired := false
	if cfg.StateDir != "" {
		if _, e := identity.Load(cfg.StateDir); e == nil {
			paired = true
		}
	}
	provs := make([]map[string]any, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		provs = append(provs, map[string]any{"kind": p.Kind, "base_url": p.BaseURL, "repos": p.Repos})
	}
	// The relay is baked in; surface the default so the field is pre-filled and the
	// operator never has to copy it (they only touch it for a self-hosted override).
	mw := cfg.MiddlewareURL
	if mw == "" {
		mw = config.DefaultRelay
	}
	// Sections this build does not read are doing nothing. The page says so —
	// a retired one with what replaced it, so an operator whose file still
	// carries it is not left guessing why their integration went quiet.
	ignored := make([]map[string]any, 0, 1)
	for _, name := range cfg.UnknownSections() {
		note, retired := config.RetiredSection(name)
		if !retired {
			note = "этот раздел появился в более новой версии коннектора — обновите его."
		}
		ignored = append(ignored, map[string]any{"name": name, "note": note})
	}
	fp, _ := config.Fingerprint(s.cfgPath)
	writeJSON(w, 200, map[string]any{
		"ignored": ignored,
		"version": s.version,
		// The connector we publish today, when it is newer than this one. Read
		// from the catalogue this connector already verifies, so saying "update"
		// needs nothing new to trust (#302).
		"core_latest":    s.coreUpdate(),
		"fingerprint":    fp,
		"middleware_url": mw,
		"state_dir":      cfg.StateDir,
		"webhooks":       map[string]any{"listen": cfg.Webhooks.Listen, "host": cfg.Webhooks.Host},
		"paired":         paired,
		"providers":      provs,
	})
}

// coreUpdate is the newer connector version to move to, or "" — including when
// this build runs without a catalogue at all (the setup UI on its own).
func (s *Server) coreUpdate() string {
	if s.catalog == nil {
		return ""
	}
	idx, err := s.catalog.Cached()
	if err != nil {
		return ""
	}
	return idx.CoreUpdate(s.version)
}

type validateReq struct {
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}

// validate checks the token against THEIR provider and lists the repos it can
// access — so the UI shows a checklist. The token never leaves this machine.
func (s *Server) validate(w http.ResponseWriter, r *http.Request) {
	var req validateReq
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "неверный запрос"})
		return
	}
	p, err := provider.New(provider.Kind(req.Kind), req.BaseURL, "_/_", nil)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	repos, err := p.ListRepos(ctx, req.Token)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "repos": repos})
}

type saveProvider struct {
	Kind    string   `json:"kind"`
	BaseURL string   `json:"base_url"`
	Token   string   `json:"token"`
	Repos   []string `json:"repos"`
}
type saveReq struct {
	MiddlewareURL string         `json:"middleware_url"`
	StateDir      string         `json:"state_dir"`
	Providers     []saveProvider `json:"providers"`
	// Webhooks is where THIS CONNECTOR listens — one address for every
	// connection, because the connector is the listener.
	Webhooks *config.Webhooks `json:"webhooks"`
	// Fingerprint is what the page saw when it loaded. The save is refused if the
	// file changed since — the CLI and the operator's editor write the same file.
	Fingerprint string `json:"fingerprint"`
}

// save writes each token to a 0600 secret file (never into the YAML) and writes
// the config; the running agent hot-reloads it.
func (s *Server) save(w http.ResponseWriter, r *http.Request) {
	var req saveReq
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "неверный запрос"})
		return
	}
	if req.StateDir == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "нужен state_dir"})
		return
	}
	secretsDir := filepath.Join(req.StateDir, "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Start from what is on disk, not from a blank config: this handler rebuilds
	// the provider list, but the file also holds plugin instances and whatever
	// sections a NEWER connector wrote. Rebuilding from scratch would delete
	// them, so an operator's integrations would vanish because somebody opened
	// the settings page.
	existing, _ := config.LoadLenient(s.cfgPath)
	if existing == nil {
		existing = &config.Config{}
	}
	cfg := &config.Config{
		MiddlewareURL: req.MiddlewareURL,
		StateDir:      req.StateDir,
		Plugins:       existing.Plugins,
		Webhooks:      existing.Webhooks,
		Unknown:       existing.Unknown,
	}
	// A page that does not render the listener must not erase it: the CLI and a
	// hand-edited file write the same config.
	if req.Webhooks != nil {
		cfg.Webhooks = *req.Webhooks
	}
	for i, p := range req.Providers {
		tokenFile := filepath.Join(secretsDir, fmt.Sprintf("provider-%d.token", i))
		if p.Token != "" {
			if err := os.WriteFile(tokenFile, []byte(p.Token), 0o600); err != nil {
				writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
				return
			}
		} else if existing != nil && i < len(existing.Providers) && existing.Providers[i].TokenFile != "" {
			tokenFile = existing.Providers[i].TokenFile
		}
		cfg.Providers = append(cfg.Providers, config.Provider{
			Kind: p.Kind, BaseURL: p.BaseURL, Repos: p.Repos, TokenFile: tokenFile,
		})
	}
	// Validate before writing: a config the agent would refuse to load must be
	// rejected here, with the reason, rather than saved and silently ignored.
	if err := cfg.Validate(); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := config.SaveIf(s.cfgPath, cfg, req.Fingerprint); err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) pairHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil || req.Code == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "нужен код"})
		return
	}
	cfg, err := config.Load(s.cfgPath) // strict: providers must be configured + valid first
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": fmt.Sprintf("сначала настройте провайдера и сохраните: %v", err)})
		return
	}
	label, _ := os.Hostname()
	if err := s.pair(cfg, req.Code, label, s.version); err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
