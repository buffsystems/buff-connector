package admin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The local admin surface installs code, accepts bundles from a file and writes
// secrets. Loopback + a custom header stop a BROWSER from reaching it, but they
// stop nothing that runs on the machine itself — any local process or anyone with
// `docker exec` can just send the request. So the surface needs a credential of
// its own, and these tests are the contract for it.

func newAuthed(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := New(filepath.Join(t.TempDir(), "c.yaml"), "test", nil)
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return s, srv
}

func TestAPIRejectsRequestWithoutToken(t *testing.T) {
	_, srv := newAuthed(t)

	res, err := http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

func TestAPIAcceptsTokenHeader(t *testing.T) {
	s, srv := newAuthed(t)

	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.Header.Set("X-Buff-Token", s.Token())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
}

func TestAPIRejectsWrongToken(t *testing.T) {
	_, srv := newAuthed(t)

	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.Header.Set("X-Buff-Token", "not-the-token")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

// The operator opens the URL the agent printed, which carries the token once.
// The page then keeps it in a cookie so the token stops travelling in URLs (and
// stops landing in browser history and referrers).
func TestRootWithQueryTokenSetsCookieAndRedirects(t *testing.T) {
	s, srv := newAuthed(t)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := client.Get(srv.URL + "/?t=" + s.Token())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/" {
		t.Fatalf("Location = %q, want /", loc)
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == cookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie set")
	}
	if cookie.Value != s.Token() {
		t.Fatal("cookie does not carry the token")
	}
	if !cookie.HttpOnly {
		t.Error("cookie must be HttpOnly so page scripts cannot read it")
	}

	// The cookie alone now authorises the API.
	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.AddCookie(cookie)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("with cookie status = %d, want 200", res2.StatusCode)
	}
}

func TestRootWithoutTokenExplainsInsteadOfServingTheApp(t *testing.T) {
	_, srv := newAuthed(t)

	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
	buf := make([]byte, 512)
	n, _ := res.Body.Read(buf)
	// No language asked for: the default, Russian.
	if !strings.Contains(string(buf[:n]), "коннектор") {
		t.Errorf("401 body should tell the operator where the URL comes from, got %q", buf[:n])
	}
}

// A pinned token lets an operator script the surface (and survives a restart).
func TestTokenCanBePinnedByEnv(t *testing.T) {
	t.Setenv(tokenEnv, "pinned-token-value")
	s := New(filepath.Join(t.TempDir(), "c.yaml"), "test", nil)
	if s.Token() != "pinned-token-value" {
		t.Fatalf("Token() = %q, want the pinned value", s.Token())
	}
}

func TestGeneratedTokensDiffer(t *testing.T) {
	t.Setenv(tokenEnv, "") // no pin: exercise generation
	a := New("a.yaml", "test", nil)
	b := New("b.yaml", "test", nil)
	if a.Token() == b.Token() {
		t.Fatal("two servers generated the same token")
	}
	if len(a.Token()) < 20 {
		t.Fatalf("token too short to resist guessing: %q", a.Token())
	}
}

// The URL the operator is told to open must carry the token, or the credential
// is useless in practice — nobody will paste a header by hand.
func TestURLCarriesToken(t *testing.T) {
	s := New("c.yaml", "test", nil)
	got := s.URL("127.0.0.1:8765")
	want := "http://127.0.0.1:8765/?t=" + s.Token()
	if got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}
