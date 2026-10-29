package plugin

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

// A service account and an OAuth account hold no bearer at rest: the host mints
// one from what they hold — a private key, a refresh token — and the plugin
// sees a bearer on the wire exactly as for a static token. What Google issues
// lives an hour; caching and renewal are the host's job, invisible to the
// plugin.

func testRSAKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	return key, pemKey
}

// upstreamAndGate is the customer's API behind a gate with one credential.
func upstreamAndGate(t *testing.T, cred Credential) (*httptest.Server, *HTTPGate, *string) {
	t.Helper()
	var seen string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(up.Close)
	u, _ := url.Parse(up.URL)
	gate := newGate(t, []ResolvedEgress{{Host: u.Host}}, map[string]Credential{"api": cred})
	return up, gate, &seen
}

func TestServiceAccountMintsABearerFromASignedJWT(t *testing.T) {
	key, pemKey := testRSAKey(t)
	var calls int32
	var assertion string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		assertion = r.Form.Get("assertion")
		_, _ = w.Write([]byte(`{"access_token":"MINTED","expires_in":3600}`))
	}))
	defer tokenSrv.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "sa.json")
	sa, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "bot@proj.iam", "private_key": pemKey})
	_ = os.WriteFile(file, sa, 0o600)

	spec := &OAuthSpec{TokenURI: tokenSrv.URL, Scopes: []string{"https://example/auth/drive"}}
	up, gate, seen := upstreamAndGate(t, Credential{Kind: KindServiceAccount, File: file, OAuth: spec})

	for i := 0; i < 2; i++ {
		if _, err := gate.Do(context.Background(), HTTPRequest{Credential: "api", URL: up.URL + "/v3/files"}); err != nil {
			t.Fatalf("do: %v", err)
		}
	}
	if *seen != "Bearer MINTED" {
		t.Fatalf("upstream saw %q", *seen)
	}
	if calls != 1 {
		t.Fatalf("token endpoint called %d times for two requests; the bearer must be cached", calls)
	}

	// The assertion is a real RS256 JWT over the claims Google expects.
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion is not a JWT: %q", assertion)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature does not verify with the key's public half: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	if claims["iss"] != "bot@proj.iam" || claims["aud"] != tokenSrv.URL || claims["scope"] != "https://example/auth/drive" {
		t.Fatalf("claims = %v", claims)
	}
}

func TestOAuth2RefreshesWithTheStoredRefreshTokenAndRenewsOnExpiry(t *testing.T) {
	var calls int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "R1" ||
			r.Form.Get("client_id") != "cid" || r.Form.Get("client_secret") != "csec" {
			t.Errorf("form = %v", r.Form)
		}
		_, _ = w.Write([]byte(`{"access_token":"T` + string(rune('0'+n)) + `","expires_in":3600}`))
	}))
	defer tokenSrv.Close()

	file := filepath.Join(t.TempDir(), "oauth.json")
	_ = SaveOAuthSecret(file, OAuthSecret{ClientID: "cid", ClientSecret: "csec", RefreshToken: "R1"})
	spec := &OAuthSpec{TokenURI: tokenSrv.URL, AuthURI: "https://auth", Scopes: []string{"s"}}
	up, gate, seen := upstreamAndGate(t, Credential{Kind: KindOAuth2, File: file, OAuth: spec})

	now := time.Now()
	gate.tokens.now = func() time.Time { return now }
	if _, err := gate.Do(context.Background(), HTTPRequest{Credential: "api", URL: up.URL + "/"}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if *seen != "Bearer T1" {
		t.Fatalf("upstream saw %q", *seen)
	}
	// Fifty-nine minutes later the token has a minute left — inside the early
	// margin, so it is renewed before the request rather than dying in flight.
	now = now.Add(59 * time.Minute)
	if _, err := gate.Do(context.Background(), HTTPRequest{Credential: "api", URL: up.URL + "/"}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if *seen != "Bearer T2" || calls != 2 {
		t.Fatalf("upstream saw %q after %d mints; want a renewed bearer", *seen, calls)
	}
}

// A sign-in finishes while the instance runs. The refresh token lands in the
// credential's file, and the next request must work without a restart.
func TestOAuth2NotSignedInSaysSoUntilTheFileGainsARefreshToken(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"OK","expires_in":3600}`))
	}))
	defer tokenSrv.Close()
	file := filepath.Join(t.TempDir(), "oauth.json")
	_ = SaveOAuthSecret(file, OAuthSecret{ClientID: "cid", ClientSecret: "csec"})
	spec := &OAuthSpec{TokenURI: tokenSrv.URL, AuthURI: "https://auth", Scopes: []string{"s"}}
	up, gate, seen := upstreamAndGate(t, Credential{Kind: KindOAuth2, File: file, OAuth: spec})

	_, err := gate.Do(context.Background(), HTTPRequest{Credential: "api", URL: up.URL + "/"})
	if err == nil || !strings.Contains(i18n.LocalizeError(i18n.EN, err), "not signed in") {
		t.Fatalf("err = %v, want a clear 'not signed in'", err)
	}
	_ = SaveOAuthSecret(file, OAuthSecret{ClientID: "cid", ClientSecret: "csec", RefreshToken: "R"})
	if _, err := gate.Do(context.Background(), HTTPRequest{Credential: "api", URL: up.URL + "/"}); err != nil {
		t.Fatalf("after sign-in: %v", err)
	}
	if *seen != "Bearer OK" {
		t.Fatalf("upstream saw %q", *seen)
	}
}

// A sign-in repeated while the instance runs — the first consent left Drive
// unticked, say — writes a new refresh token. The bearer minted from the old
// one is good for an hour by its own clock, and must NOT be served past the
// secret it came from: the live run met exactly that 403 for an hour.
func TestARepeatedSignInReplacesTheCachedBearer(t *testing.T) {
	var calls int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		n := atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"access_token":"FROM-` + r.Form.Get("refresh_token") + `-` + string(rune('0'+n)) + `","expires_in":3600}`))
	}))
	defer tokenSrv.Close()
	file := filepath.Join(t.TempDir(), "oauth.json")
	_ = SaveOAuthSecret(file, OAuthSecret{ClientID: "cid", ClientSecret: "csec", RefreshToken: "OLD"})
	spec := &OAuthSpec{TokenURI: tokenSrv.URL, AuthURI: "https://auth", Scopes: []string{"s"}}
	up, gate, seen := upstreamAndGate(t, Credential{Kind: KindOAuth2, File: file, OAuth: spec})

	if _, err := gate.Do(context.Background(), HTTPRequest{Credential: "api", URL: up.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	if *seen != "Bearer FROM-OLD-1" {
		t.Fatalf("upstream saw %q", *seen)
	}
	_ = SaveOAuthSecret(file, OAuthSecret{ClientID: "cid", ClientSecret: "csec", RefreshToken: "NEW"})
	if _, err := gate.Do(context.Background(), HTTPRequest{Credential: "api", URL: up.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	if *seen != "Bearer FROM-NEW-2" {
		t.Fatalf("upstream saw %q after the new sign-in; the old bearer was served", *seen)
	}
	// …and the new one is cached like any other.
	if _, err := gate.Do(context.Background(), HTTPRequest{Credential: "api", URL: up.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("token endpoint called %d times, want 2", calls)
	}
}

func TestTokenEndpointRefusalIsReportedInItsOwnWords(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
	}))
	defer tokenSrv.Close()
	file := filepath.Join(t.TempDir(), "oauth.json")
	_ = SaveOAuthSecret(file, OAuthSecret{ClientID: "cid", ClientSecret: "csec", RefreshToken: "dead"})
	spec := &OAuthSpec{TokenURI: tokenSrv.URL, AuthURI: "https://auth", Scopes: []string{"s"}}
	up, gate, _ := upstreamAndGate(t, Credential{Kind: KindOAuth2, File: file, OAuth: spec})

	_, err := gate.Do(context.Background(), HTTPRequest{Credential: "api", URL: up.URL + "/"})
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("err = %v, want the endpoint's own reason", err)
	}
}

func TestAServiceAccountFileThatIsNotOneIsNamedAsSuch(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sa.json")
	_ = os.WriteFile(file, []byte(`{"client_id":"this is an oauth client"}`), 0o600)
	spec := &OAuthSpec{TokenURI: "http://127.0.0.1:1/token", Scopes: []string{"s"}}
	up, gate, _ := upstreamAndGate(t, Credential{Kind: KindServiceAccount, File: file, OAuth: spec})
	_, err := gate.Do(context.Background(), HTTPRequest{Credential: "api", URL: up.URL + "/"})
	if err == nil || !strings.Contains(i18n.LocalizeError(i18n.EN, err), "service account JSON key") {
		t.Fatalf("err = %v", err)
	}
}

// ---- the sign-in ----------------------------------------------------------

func TestAuthorizeURLAsksForOfflineAccessWithPKCE(t *testing.T) {
	spec := OAuthSpec{AuthURI: "https://accounts.example/o/auth", Scopes: []string{"a", "b"}}
	verifier := NewVerifier()
	raw := AuthorizeURL(spec, "cid", "http://127.0.0.1:8123/oauth/callback", "st8", verifier)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	want := map[string]string{
		"client_id": "cid", "redirect_uri": "http://127.0.0.1:8123/oauth/callback", "response_type": "code",
		"scope": "a b openid email", "access_type": "offline", "prompt": "consent", "state": "st8",
		"code_challenge_method": "S256", "code_challenge": challenge(verifier),
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
}

func TestCodeFromReturnReadsAPastedAddressOrABareCode(t *testing.T) {
	code, state := CodeFromReturn("  http://127.0.0.1:8123/oauth/callback?state=st8&code=4%2FabcDEF&scope=x ")
	if code != "4/abcDEF" || state != "st8" {
		t.Fatalf("got %q %q", code, state)
	}
	code, state = CodeFromReturn("4/abcDEF")
	if code != "4/abcDEF" || state != "" {
		t.Fatalf("got %q %q", code, state)
	}
}

func TestExchangeCodeStoresTheRefreshTokenBesideTheClient(t *testing.T) {
	idToken := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"me@gmail.com"}`)) + ".s"
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "CODE" ||
			r.Form.Get("code_verifier") != "VER" || r.Form.Get("redirect_uri") != "http://127.0.0.1:1/cb" ||
			r.Form.Get("client_id") != "cid" || r.Form.Get("client_secret") != "csec" {
			t.Errorf("form = %v", r.Form)
		}
		_, _ = w.Write([]byte(`{"access_token":"A","refresh_token":"REFRESH","id_token":"` + idToken + `"}`))
	}))
	defer tokenSrv.Close()
	file := filepath.Join(t.TempDir(), "oauth.json")
	_ = SaveOAuthSecret(file, OAuthSecret{ClientID: "cid", ClientSecret: "csec"})

	got, err := ExchangeCode(context.Background(), OAuthSpec{TokenURI: tokenSrv.URL}, file, "CODE", "http://127.0.0.1:1/cb", "VER")
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != "REFRESH" || got.Account != "me@gmail.com" {
		t.Fatalf("got %+v", got)
	}
	ok, who := SignedIn(file)
	if !ok || who != "me@gmail.com" {
		t.Fatalf("file says signed in=%v as %q", ok, who)
	}
	saved, _ := LoadOAuthSecret(file)
	if saved.ClientID != "cid" || saved.ClientSecret != "csec" {
		t.Fatalf("the client must survive the exchange: %+v", saved)
	}
}

// The file Google's console hands out wraps the client in `installed`; pasted
// whole, it is the client. After a sign-in the record is written back flat,
// with the refresh token beside the client.
func TestTheConsolesClientFileIsAcceptedWhole(t *testing.T) {
	file := filepath.Join(t.TempDir(), "oauth.json")
	_ = os.WriteFile(file, []byte(`{"installed":{"client_id":"cid","project_id":"p","auth_uri":"https://a","token_uri":"https://t","client_secret":"csec","redirect_uris":["http://localhost"]}}`), 0o600)
	s, err := LoadOAuthSecret(file)
	if err != nil || s.ClientID != "cid" || s.ClientSecret != "csec" {
		t.Fatalf("parsed %+v, %v", s, err)
	}
	if ok, _ := SignedIn(file); ok {
		t.Fatal("a fresh client file is not a sign-in")
	}
	form, err := refreshGrant([]byte(`{"web":{"client_id":"cid","client_secret":"csec","refresh_token":"R"}}`))
	if err != nil || form.Get("client_id") != "cid" || form.Get("refresh_token") != "R" {
		t.Fatalf("refresh grant from a wrapped record: %v %v", form, err)
	}
	if _, err := ParseOAuthSecret([]byte(`{"type":"service_account"}`)); err == nil {
		t.Fatal("a service account key is not an OAuth client")
	}
}

func TestExchangeCodeFailureNamesTheReason(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Bad Request"}`))
	}))
	defer tokenSrv.Close()
	file := filepath.Join(t.TempDir(), "oauth.json")
	_ = SaveOAuthSecret(file, OAuthSecret{ClientID: "cid", ClientSecret: "csec"})
	_, err := ExchangeCode(context.Background(), OAuthSpec{TokenURI: tokenSrv.URL}, file, "x", "r", "v")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v", err)
	}
	if ok, _ := SignedIn(file); ok {
		t.Fatal("a failed exchange must not mark the credential signed in")
	}
}

// ---- the manifest side ------------------------------------------------------

func TestManifestRefusesAMintedKindWithoutItsEndpoints(t *testing.T) {
	base := "name: g\nversion: 1.0.0\nhost_api: 4\nentry: main.py\n"
	cases := map[string]string{
		"unknown kind":            base + "credentials:\n  - name: api\n    kinds: [magic]\n",
		"service account, no uri": base + "credentials:\n  - name: api\n    kinds: [service_account]\n",
		"oauth2 without auth_uri": base + "credentials:\n  - name: api\n    kinds: [oauth2]\n    oauth:\n      token_uri: https://t\n      scopes: [s]\n",
	}
	for name, src := range cases {
		if _, err := ParseManifest([]byte(src)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := base + "credentials:\n  - name: api\n    kinds: [service_account, oauth2]\n    oauth:\n      auth_uri: https://a\n      token_uri: https://t\n      scopes: [s]\n"
	m, err := ParseManifest([]byte(good))
	if err != nil {
		t.Fatalf("good manifest refused: %v", err)
	}
	if got := m.OAuthFor("api", nil); got == nil || got.AuthURI != "https://a" || got.TokenURI != "https://t" {
		t.Fatalf("oauth block lost: %+v", got)
	}
}

// The addresses may come from the connection's settings — a stand points the
// sign-in at a stand-in that way — with the real address as the default.
func TestOAuthAddressesCanComeFromSettings(t *testing.T) {
	src := "name: g\nversion: 1.0.0\nhost_api: 4\nentry: main.py\n" +
		"config_schema:\n  type: object\n  properties:\n    token_uri:\n      type: string\n      default: https://oauth2.example/token\n" +
		"    auth_uri:\n      type: string\n      default: https://accounts.example/auth\n" +
		"credentials:\n  - name: api\n    kinds: [oauth2]\n    oauth:\n      auth_uri_from: auth_uri\n      token_uri_from: token_uri\n      scopes: [s]\n"
	m, err := ParseManifest([]byte(src))
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	got := m.OAuthFor("api", map[string]any{"token_uri": "http://127.0.0.1:8810/token"})
	if got.TokenURI != "http://127.0.0.1:8810/token" || got.AuthURI != "https://accounts.example/auth" {
		t.Fatalf("resolved = %+v", got)
	}
	bad := strings.Replace(src, "token_uri_from: token_uri", "token_uri_from: nowhere", 1)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("a *_from naming no string setting was accepted")
	}
	both := strings.Replace(src, "token_uri_from: token_uri", "token_uri_from: token_uri\n      token_uri: https://t", 1)
	if _, err := ParseManifest([]byte(both)); err == nil {
		t.Fatal("an address both written down and taken from a setting was accepted")
	}
}
