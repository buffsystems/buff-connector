package admin

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// Signing an oauth2 credential in, from the local page.
//
// Start hands the browser the source's consent URL; the source returns the
// browser to THIS server's loopback address with a code; the code is traded for
// a refresh token that lands in the credential's file. When the browser is not
// on the connector's machine the return lands on nothing — the operator pastes
// the address of that failed page into the form and Finish runs the same trade.
//
// The return route carries no admin credential on purpose: the browser arrives
// from another site, where the admin cookie (SameSite=Strict) does not travel.
// What protects it is the state: random, single-use, and tied to a sign-in an
// AUTHENTICATED request started minutes ago. A code arriving with a state this
// connector never issued is dropped.

const signInTTL = 10 * time.Minute

type pendingSignIn struct {
	id, name string
	spec     plugin.OAuthSpec
	file     string
	redirect string
	verifier string
	started  time.Time
}

type oauthFlow struct {
	mu      sync.Mutex
	pending map[string]pendingSignIn
	// lookup finds the credential's minting spec and secret file. Injected so
	// the flow is testable without a signed bundle on disk.
	lookup func(id, name string) (plugin.OAuthSpec, string, error)
	now    func() time.Time
}

func newOAuthFlow(lookup func(id, name string) (plugin.OAuthSpec, string, error)) *oauthFlow {
	return &oauthFlow{pending: map[string]pendingSignIn{}, lookup: lookup, now: time.Now}
}

// lookupOAuth reads the instance and its plugin's manifest for one credential.
func (s *Server) lookupOAuth(id, name string) (plugin.OAuthSpec, string, error) {
	cfg, err := config.LoadLenient(s.cfgPath)
	if err != nil {
		return plugin.OAuthSpec{}, "", err
	}
	inst := findInstance(cfg, id)
	if inst == nil {
		return plugin.OAuthSpec{}, "", fmt.Errorf("подключение %s не найдено", id)
	}
	cred, ok := credentialOf(inst, name)
	if !ok || cred.Kind != plugin.KindOAuth2 {
		return plugin.OAuthSpec{}, "", fmt.Errorf("у подключения нет учётных данных %q вида oauth2 — сначала сохраните подключение с client_id и client_secret", name)
	}
	installed, err := s.plugins.Store().Resolve(inst.Plugin, inst.Version)
	if err != nil {
		return plugin.OAuthSpec{}, "", err
	}
	spec := installed.Manifest.OAuthFor(name, inst.Config)
	if spec == nil {
		return plugin.OAuthSpec{}, "", fmt.Errorf("плагин не объявляет oauth для %q", name)
	}
	return *spec, cred.TokenFile, nil
}

func (s *Server) oauthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/instances/oauth/start", s.guard(s.auth(s.oauthStart)))
	mux.HandleFunc("/api/instances/oauth/finish", s.guard(s.auth(s.oauthFinish)))
	mux.HandleFunc("/oauth/callback", s.guard(s.oauthCallback))
}

// redirectFor is where the source sends the browser back: the loopback address
// with the port the browser reached this page on — through an SSH tunnel that
// is the tunnel's local port, and the return travels the same way.
func redirectFor(r *http.Request) string {
	host := "127.0.0.1"
	if _, port, err := net.SplitHostPort(r.Host); err == nil && port != "" {
		host = net.JoinHostPort("127.0.0.1", port)
	}
	return "http://" + host + "/oauth/callback"
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || s.oauth == nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	spec, file, err := s.oauth.lookup(req.ID, req.Name)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	secret, err := plugin.LoadOAuthSecret(file)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	p := pendingSignIn{
		id: req.ID, name: req.Name, spec: spec, file: file,
		redirect: redirectFor(r), verifier: plugin.NewVerifier(), started: s.oauth.now(),
	}
	state := s.oauth.remember(p)
	writeJSON(w, 200, map[string]any{
		"ok": true, "url": plugin.AuthorizeURL(spec, secret.ClientID, p.redirect, state, p.verifier),
		"redirect_uri": p.redirect,
	})
}

func (f *oauthFlow) remember(p pendingSignIn) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for st, old := range f.pending {
		if f.now().Sub(old.started) > signInTTL {
			delete(f.pending, st)
		}
	}
	state := plugin.NewState()
	f.pending[state] = p
	return state
}

// take claims a pending sign-in: by state when the return carries one, else
// the latest started for that credential (a bare code pasted by hand).
func (f *oauthFlow) take(state, id, name string) (pendingSignIn, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if state != "" {
		p, ok := f.pending[state]
		if ok {
			delete(f.pending, state)
		}
		return p, ok && f.now().Sub(p.started) <= signInTTL
	}
	var best pendingSignIn
	bestState := ""
	for st, p := range f.pending {
		if p.id == id && p.name == name && p.started.After(best.started) {
			best, bestState = p, st
		}
	}
	if bestState == "" {
		return pendingSignIn{}, false
	}
	delete(f.pending, bestState)
	return best, f.now().Sub(best.started) <= signInTTL
}

func (f *oauthFlow) finish(ctx context.Context, p pendingSignIn, code string) (plugin.OAuthSecret, error) {
	return plugin.ExchangeCode(ctx, p.spec, p.file, code, p.redirect, p.verifier)
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if s.oauth == nil {
		http.Error(w, "no plugins", 404)
		return
	}
	if e := q.Get("error"); e != "" {
		w.WriteHeader(400)
		fmt.Fprint(w, returnPage("Вход не выполнен", "Источник ответил: "+e))
		return
	}
	p, ok := s.oauth.take(q.Get("state"), "", "")
	if !ok || q.Get("code") == "" {
		w.WriteHeader(400)
		fmt.Fprint(w, returnPage("Вход не выполнен",
			"Этот возврат не относится к входу, начатому на странице коннектора, или вход начат слишком давно. Начните заново."))
		return
	}
	got, err := s.oauth.finish(r.Context(), p, q.Get("code"))
	if err != nil {
		w.WriteHeader(400)
		fmt.Fprint(w, returnPage("Вход не выполнен", err.Error()))
		return
	}
	fmt.Fprint(w, returnPage("Вход выполнен", "Подключение работает от имени "+orUnknown(got.Account)+
		". Вернитесь на страницу коннектора — эту вкладку можно закрыть."))
}

func (s *Server) oauthFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Pasted string `json:"pasted"`
	}
	if err := readJSON(r, &req); err != nil || s.oauth == nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	code, state := plugin.CodeFromReturn(req.Pasted)
	if code == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "вставьте адрес страницы, на которую вернул источник, или код из него"})
		return
	}
	p, ok := s.oauth.take(state, req.ID, req.Name)
	if !ok {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "вход не был начат или начат слишком давно — нажмите «Войти» ещё раз"})
		return
	}
	got, err := s.oauth.finish(r.Context(), p, code)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "account": got.Account})
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "выбранной учётной записи"
	}
	return s
}

// returnPage is the whole page the browser lands on after consent: one
// sentence, no script, nothing to load. The page it belongs to is the one
// already open in another tab.
func returnPage(title, text string) string {
	return `<!doctype html><meta charset="utf-8"><title>` + html.EscapeString(title) + `</title>` +
		`<body style="font:16px system-ui;margin:3rem auto;max-width:36rem;padding:0 1rem;color:#1a1a1a">` +
		`<h1 style="font-size:1.3rem">` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(text) + `</p></body>`
}
