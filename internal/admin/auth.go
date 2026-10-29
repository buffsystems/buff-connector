package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"os"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

// The local admin surface can install plugin code, accept a bundle from a file
// and write secrets. Loopback checks and the custom header in guard() defend
// against a BROWSER being used to reach it (DNS rebinding, CSRF) — they defend
// against nothing that already runs on the machine: any local process, or anyone
// with `docker exec`, can send the request directly. So the surface carries a
// credential of its own.
//
// The token lives in MEMORY for the life of the process and is printed with the
// URL the operator opens. Nothing is written to disk on purpose: there is then no
// secret at rest to leak, no file mode to get wrong, and it works when the config
// directory is mounted read-only. An operator who needs a stable token (scripts,
// a service that restarts) pins one through the environment.

const (
	cookieName = "buff_admin"
	tokenEnv   = "BUFF_ADMIN_TOKEN"
)

// newToken returns the pinned token if the operator set one, else a fresh random
// one. 24 bytes is far past guessing range for a surface that answers only on
// loopback.
func newToken() string {
	if v := os.Getenv(tokenEnv); v != "" {
		return v
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not recoverable: refusing to serve is the only
		// safe answer, and a panic here happens at startup, not mid-session.
		panic("connector: cannot generate admin token: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Token is the credential this server accepts.
func (s *Server) Token() string { return s.token }

// URL is the address to hand the operator. It carries the token once so the
// browser can trade it for a cookie; nobody pastes a header by hand.
func (s *Server) URL(addr string) string { return "http://" + addr + "/?t=" + s.token }

// tokenFrom pulls the credential out of a request: the header (scripts, CLI) or
// the cookie the browser got when it first opened the printed URL.
func tokenFrom(r *http.Request) string {
	if v := r.Header.Get("X-Buff-Token"); v != "" {
		return v
	}
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value
	}
	return ""
}

// authorized compares in constant time so the token cannot be recovered by
// timing the comparison one byte at a time.
func (s *Server) authorized(r *http.Request) bool {
	got := tokenFrom(r)
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// auth wraps a handler so it answers only to a request carrying the credential.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			unauthorized(w, r)
			return
		}
		next(w, r)
	}
}

// unauthorized tells the operator WHERE the URL comes from, because the usual
// reason for landing here is opening the bare address instead of the printed one.
// It answers in the request's language: nothing else on this page has loaded yet.
func unauthorized(w http.ResponseWriter, r *http.Request) {
	http.Error(w, i18n.ForRequest(r).T("нет доступа — откройте адрес, который коннектор напечатал при запуске "+
		"(в нём одноразовый токен), или передайте токен в заголовке X-Buff-Token"), http.StatusUnauthorized)
}

// root serves the setup page. A token in the query is traded for a cookie and
// redirected away, so the credential stops travelling in URLs — out of browser
// history, out of referrers, out of any proxy log.
func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if t := r.URL.Query().Get("t"); t != "" {
		if subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) != 1 {
			unauthorized(w, r)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    s.token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !s.authorized(r) {
		unauthorized(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(pageHTML)
}
