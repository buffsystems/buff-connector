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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Two credential kinds hold no bearer token at rest — they hold what a token is
// MINTED from, and the host mints it. A service account (Google's JSON key) is a
// private key: the host signs a short-lived JWT and trades it for an access
// token. An OAuth account is a client and a refresh token: the host trades the
// refresh token for an access token. Either way the plugin names the credential
// and sees a bearer on the wire, exactly as it does for a static token; the
// hour-long life of what Google issues is the host's problem, not the plugin's.
//
// The secret is read from its FILE at mint time, not once at start: an OAuth
// sign-in completes while the instance runs and adds the refresh token to that
// file, and the next request must simply work — no restart, no second save.

// OAuthSpec is how a plugin's credential is minted, from the manifest: where a
// sign-in starts, where tokens are issued, and the access asked for. Either
// address may be written down or taken from a SETTING of the connection
// (`*_from` names a config field, usually one with the real address as its
// default) — so a stand can point the sign-in at a stand-in without a plugin
// built for the stand.
type OAuthSpec struct {
	AuthURI      string   `yaml:"auth_uri" json:"auth_uri,omitempty"`
	TokenURI     string   `yaml:"token_uri" json:"token_uri,omitempty"`
	AuthURIFrom  string   `yaml:"auth_uri_from" json:"auth_uri_from,omitempty"`
	TokenURIFrom string   `yaml:"token_uri_from" json:"token_uri_from,omitempty"`
	Scopes       []string `yaml:"scopes" json:"scopes"`
}

// Resolve fills the addresses from the connection's settings. The result is
// what the host mints and signs in with; the manifest's own is never used
// where a setting was named.
func (o OAuthSpec) Resolve(cfg map[string]any) OAuthSpec {
	out := o
	if o.AuthURIFrom != "" {
		out.AuthURI, _ = cfg[o.AuthURIFrom].(string)
	}
	if o.TokenURIFrom != "" {
		out.TokenURI, _ = cfg[o.TokenURIFrom].(string)
	}
	return out
}

const (
	KindServiceAccount = "service_account"
	KindOAuth2         = "oauth2"
	// KindPassword is an account and a password the plugin RECEIVES, through
	// buff/credential: the one exception to "the plugin never sees a secret",
	// for a protocol the host does not speak on its behalf (SMB). The
	// manifest declares it, so the catalogue shows it before install.
	KindPassword = "password"
)

// KnownKinds are the credential kinds a manifest may declare.
var KnownKinds = []string{"bearer", "basic", "header", KindServiceAccount, KindOAuth2, KindPassword}

// Handed reports whether a kind's secret is given to the plugin itself.
func Handed(kind string) bool { return kind == KindPassword }

// Minted reports whether a kind's bearer is issued at request time.
func Minted(kind string) bool { return kind == KindServiceAccount || kind == KindOAuth2 }

// serviceAccountKey is the part of Google's JSON key the host needs.
type serviceAccountKey struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

// OAuthSecret is what an oauth2 credential's file holds: the operator's own
// client, and — once they have signed in — the refresh token.
type OAuthSecret struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// Account is who signed in, when the token endpoint said (the id_token's
	// email). Shown on the page so the operator sees WHICH account the
	// connection acts as; never used for auth.
	Account string `json:"account,omitempty"`
}

type mintedToken struct {
	token   string
	expires time.Time
	// from is what the bearer was minted from. A sign-in repeated while the
	// instance runs writes a new secret to the file; a bearer minted from the
	// old one — with the old consent, say — must not outlive it.
	from string
}

// tokenMinter issues and caches bearers per credential name.
type tokenMinter struct {
	mu     sync.Mutex
	cache  map[string]mintedToken
	client *http.Client
	now    func() time.Time
	// early is how long before expiry a token is treated as expired, so a
	// request never starts with a bearer that dies in flight.
	early time.Duration
}

func newTokenMinter() *tokenMinter {
	return &tokenMinter{
		cache:  map[string]mintedToken{},
		client: &http.Client{Timeout: 30 * time.Second},
		now:    time.Now,
		early:  time.Minute,
	}
}

// bearer returns a live access token for the credential, minting one when the
// cached one is missing or about to expire.
func (m *tokenMinter) bearer(ctx context.Context, name string, c Credential) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.OAuth == nil || c.OAuth.TokenURI == "" {
		return "", fmt.Errorf("учётные данные %q — в манифесте нет адреса выдачи токена для вида %s", name, c.Kind)
	}
	secret, err := secretOf(c)
	if err != nil {
		return "", fmt.Errorf("учётные данные %q: %w", name, err)
	}
	if t, ok := m.cache[name]; ok && t.from == string(secret) && m.now().Add(m.early).Before(t.expires) {
		return t.token, nil
	}
	var form url.Values
	switch c.Kind {
	case KindServiceAccount:
		form, err = serviceAccountGrant(secret, c.OAuth, m.now())
	case KindOAuth2:
		form, err = refreshGrant(secret)
	default:
		return "", fmt.Errorf("учётные данные %q — вид %s не выпускается", name, c.Kind)
	}
	if err != nil {
		return "", fmt.Errorf("учётные данные %q: %w", name, err)
	}
	tok, ttl, err := postTokenForm(ctx, m.client, c.OAuth.TokenURI, form)
	if err != nil {
		return "", fmt.Errorf("учётные данные %q: %w", name, err)
	}
	m.cache[name] = mintedToken{token: tok, expires: m.now().Add(ttl), from: string(secret)}
	return tok, nil
}

// secretOf reads the credential's material: the file, when it has one, because
// that is where a sign-in lands while the instance runs; else what was
// resolved at start.
func secretOf(c Credential) ([]byte, error) {
	if c.File != "" {
		b, err := readSecretFile(c.File)
		if err != nil {
			return nil, err
		}
		return b, nil
	}
	if c.Token == "" {
		return nil, fmt.Errorf("секрет не задан")
	}
	return []byte(c.Token), nil
}

func serviceAccountGrant(secret []byte, spec *OAuthSpec, now time.Time) (url.Values, error) {
	var key serviceAccountKey
	if err := json.Unmarshal(secret, &key); err != nil || key.ClientEmail == "" || key.PrivateKey == "" {
		return nil, fmt.Errorf("секрет — не JSON-ключ сервисного аккаунта (нужны client_email и private_key)")
	}
	block, _ := pem.Decode([]byte(key.PrivateKey))
	if block == nil {
		return nil, fmt.Errorf("private_key — не PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("private_key: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private_key — не ключ RSA")
	}
	claims := map[string]any{
		"iss":   key.ClientEmail,
		"scope": strings.Join(spec.Scopes, " "),
		"aud":   spec.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	assertion, err := signJWT(rsaKey, claims)
	if err != nil {
		return nil, err
	}
	return url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}, nil
}

func refreshGrant(secret []byte) (url.Values, error) {
	s, err := ParseOAuthSecret(secret)
	if err != nil {
		return nil, err
	}
	if s.RefreshToken == "" {
		return nil, fmt.Errorf("вход ещё не выполнен — сначала войдите на странице подключения")
	}
	return url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {s.ClientID},
		"client_secret": {s.ClientSecret},
		"refresh_token": {s.RefreshToken},
	}, nil
}

// signJWT builds an RS256 JWT — the only algorithm the jwt-bearer grant of the
// service-account kind uses.
func signJWT(key *rsa.PrivateKey, claims map[string]any) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := header + "." + enc.EncodeToString(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// postTokenForm is one call to a token endpoint. A failure carries the
// endpoint's own words: "invalid_grant" from Google names the real problem
// (a revoked sign-in, a wrong client) far better than anything we could say.
func postTokenForm(ctx context.Context, client *http.Client, tokenURI string, form url.Values) (string, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("адрес выдачи токена: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, fmt.Errorf("адрес выдачи токена: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return "", 0, fmt.Errorf("адрес выдачи токена ответил %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return "", 0, fmt.Errorf("адрес выдачи токена ответил без access_token")
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	return out.AccessToken, ttl, nil
}
