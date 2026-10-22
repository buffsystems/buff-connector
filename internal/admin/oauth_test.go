package admin

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buffsystems/buff-connector/internal/plugin"
)

// The sign-in of an oauth2 credential: the page starts it, the source returns
// the browser to the loopback address with a code, the code becomes a refresh
// token in the credential's file. The return carries no admin credential — the
// state is what ties it to a sign-in this connector started.

func signInStand(t *testing.T) (*httptest.Server, *Server, string) {
	t.Helper()
	idToken := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"me@gmail.com"}`)) + ".s"
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "GOODCODE" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Malformed auth code."}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"A","refresh_token":"REFRESH","id_token":"` + idToken + `"}`))
	}))
	t.Cleanup(tokenSrv.Close)

	file := filepath.Join(t.TempDir(), "google.token")
	if err := plugin.SaveOAuthSecret(file, plugin.OAuthSecret{ClientID: "cid", ClientSecret: "csec"}); err != nil {
		t.Fatal(err)
	}
	spec := plugin.OAuthSpec{AuthURI: "https://accounts.example/auth", TokenURI: tokenSrv.URL, Scopes: []string{"drive"}}
	s := New(filepath.Join(t.TempDir(), "connector.yaml"), "test", nil)
	s.oauth = newOAuthFlow(func(id, name string) (plugin.OAuthSpec, string, error) {
		return spec, file, nil
	})
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return srv, s, file
}

func startSignIn(t *testing.T, srv *httptest.Server) (authURL *url.URL) {
	t.Helper()
	r := post(t, srv, "/api/instances/oauth/start", map[string]any{"id": "I1", "name": "google"})
	if r["ok"] != true {
		t.Fatalf("start: %v", r)
	}
	u, err := url.Parse(r["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSignInReturnsThroughLoopbackAndStoresTheRefreshToken(t *testing.T) {
	srv, _, file := signInStand(t)
	u := startSignIn(t, srv)
	q := u.Query()
	if u.Host != "accounts.example" || q.Get("client_id") != "cid" || q.Get("state") == "" {
		t.Fatalf("auth url = %s", u)
	}
	// The return address is the loopback port the browser reached the page on.
	srvURL, _ := url.Parse(srv.URL)
	wantRedirect := "http://" + srvURL.Host + "/oauth/callback"
	if q.Get("redirect_uri") != wantRedirect {
		t.Fatalf("redirect_uri = %q, want %q", q.Get("redirect_uri"), wantRedirect)
	}

	// The browser comes back from the source: no admin header, no cookie.
	res, err := http.Get(wantRedirect + "?state=" + url.QueryEscape(q.Get("state")) + "&code=GOODCODE&scope=drive")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "me@gmail.com") {
		t.Fatalf("callback: %d %s", res.StatusCode, body)
	}
	if ok, who := plugin.SignedIn(file); !ok || who != "me@gmail.com" {
		t.Fatalf("file: signed_in=%v as %q", ok, who)
	}
	// The state is single-use.
	res, _ = http.Get(wantRedirect + "?state=" + url.QueryEscape(q.Get("state")) + "&code=GOODCODE")
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("a replayed return answered %d, want 400", res.StatusCode)
	}
}

func TestAReturnWithAStateThisConnectorNeverIssuedIsDropped(t *testing.T) {
	srv, _, file := signInStand(t)
	res, _ := http.Get(srv.URL + "/oauth/callback?state=forged&code=GOODCODE")
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("answered %d, want 400", res.StatusCode)
	}
	if ok, _ := plugin.SignedIn(file); ok {
		t.Fatal("a forged return must not sign the credential in")
	}
}

// The browser was on another machine: the return landed nowhere, and the
// operator pastes its address into the form.
func TestAPastedReturnAddressFinishesTheSignIn(t *testing.T) {
	srv, _, file := signInStand(t)
	u := startSignIn(t, srv)
	pasted := "http://127.0.0.1:8765/oauth/callback?state=" + u.Query().Get("state") + "&code=GOODCODE&scope=drive"
	r := post(t, srv, "/api/instances/oauth/finish", map[string]any{"id": "I1", "name": "google", "pasted": pasted})
	if r["ok"] != true || r["account"] != "me@gmail.com" {
		t.Fatalf("finish: %v", r)
	}
	if ok, _ := plugin.SignedIn(file); !ok {
		t.Fatal("not signed in after a pasted return")
	}
}

// A bare code (no state) is matched to the latest sign-in started for that
// credential; a code the source refuses is reported in the source's words.
func TestABareCodeGoesToTheLatestSignInAndARefusalIsNamed(t *testing.T) {
	srv, _, _ := signInStand(t)
	startSignIn(t, srv)
	r := post(t, srv, "/api/instances/oauth/finish", map[string]any{"id": "I1", "name": "google", "pasted": "BADCODE"})
	if r["ok"] == true || !strings.Contains(r["error"].(string), "invalid_grant") {
		t.Fatalf("finish: %v", r)
	}
	// Nothing pending any more: the attempt consumed it.
	r = post(t, srv, "/api/instances/oauth/finish", map[string]any{"id": "I1", "name": "google", "pasted": "GOODCODE"})
	if r["ok"] == true {
		t.Fatalf("a second finish with nothing pending must fail: %v", r)
	}
}

func TestAStaleSignInIsNotHonoured(t *testing.T) {
	srv, s, _ := signInStand(t)
	now := time.Now()
	s.oauth.now = func() time.Time { return now }
	u := startSignIn(t, srv)
	now = now.Add(signInTTL + time.Second)
	res, _ := http.Get(srv.URL + "/oauth/callback?state=" + url.QueryEscape(u.Query().Get("state")) + "&code=GOODCODE")
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 400 || !strings.Contains(string(body), "слишком давно") {
		t.Fatalf("stale return: %d %s", res.StatusCode, body)
	}
}

func TestStartRefusesWhenTheCredentialIsNotAnOAuthClient(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "connector.yaml"), "test", nil)
	s.oauth = newOAuthFlow(func(id, name string) (plugin.OAuthSpec, string, error) {
		return plugin.OAuthSpec{}, "", errNotOAuth
	})
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	r := post(t, srv, "/api/instances/oauth/start", map[string]any{"id": "I1", "name": "google"})
	if r["ok"] == true || !strings.Contains(r["error"].(string), errNotOAuth.Error()) {
		t.Fatalf("start: %v", r)
	}
}

var errNotOAuth = errors.New("у подключения нет учётных данных вида oauth2")
