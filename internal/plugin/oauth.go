package plugin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// The sign-in of an oauth2 credential: the operator's browser goes to the
// source, consents, and comes back with a code the connector trades for a
// refresh token. That token is then added to the credential's file, where the
// minter (token.go) finds it on the next request.
//
// The client is the OPERATOR'S own (a "desktop" client in Google's terms),
// which is why the redirect is the loopback address the local page is served
// on: a desktop client may return to any port of 127.0.0.1, and the page is
// only ever reached there. When the browser is not on the connector's machine
// the return lands nowhere — the operator then pastes the address bar of that
// failed page (it carries the code) into the form, and the same exchange runs.
//
// PKCE is used because it costs nothing and turns an intercepted code into a
// useless one; `state` ties the return to a sign-in this connector started.

// LoadOAuthSecret reads an oauth2 credential's file.
func LoadOAuthSecret(file string) (OAuthSecret, error) {
	b, err := readSecretFile(file)
	if err != nil {
		return OAuthSecret{}, err
	}
	return ParseOAuthSecret(b)
}

// ParseOAuthSecret reads the client out of what the operator pasted: our own
// record, or the file Google's console hands out — the same fields wrapped in
// `installed` (a desktop client) or `web`. Pasting that file whole must work;
// asking the operator to pick two fields out of it is a chore dressed as a
// contract.
func ParseOAuthSecret(b []byte) (OAuthSecret, error) {
	var s OAuthSecret
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("the secret is not an OAuth client (client_id and client_secret are needed)")
	}
	if s.ClientID == "" {
		var wrapped struct {
			Installed *OAuthSecret `json:"installed"`
			Web       *OAuthSecret `json:"web"`
		}
		_ = json.Unmarshal(b, &wrapped)
		switch {
		case wrapped.Installed != nil && wrapped.Installed.ClientID != "":
			s = *wrapped.Installed
		case wrapped.Web != nil && wrapped.Web.ClientID != "":
			s = *wrapped.Web
		default:
			return s, fmt.Errorf("the secret is not an OAuth client (client_id and client_secret are needed)")
		}
	}
	return s, nil
}

// SaveOAuthSecret writes it back, privately.
func SaveOAuthSecret(file string, s OAuthSecret) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writePrivate(file, string(b))
}

// SignedIn reports whether an oauth2 credential's file already holds a refresh
// token, and who it belongs to.
func SignedIn(file string) (bool, string) {
	s, err := LoadOAuthSecret(file)
	if err != nil {
		return false, ""
	}
	return s.RefreshToken != "", s.Account
}

func readSecretFile(file string) ([]byte, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read secret: %w", err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return nil, fmt.Errorf("the secret file is empty")
	}
	return b, nil
}

// NewVerifier mints a PKCE verifier; NewState a sign-in identifier.
func NewVerifier() string { return randomToken(32) }
func NewState() string    { return randomToken(16) }

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("connector: cannot read random bytes: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL is where the browser is sent. Offline access with forced consent
// is what makes the source issue a refresh token at all — without it a second
// sign-in returns only an access token, and the connection dies in an hour.
func AuthorizeURL(spec OAuthSpec, clientID, redirect, state, verifier string) string {
	// The identity scopes ride along so the token endpoint says WHO signed in
	// (an id_token with the email) — shown on the page, never decided on.
	scopes := append([]string{}, spec.Scopes...)
	for _, s := range []string{"openid", "email"} {
		if !containsFold(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {strings.Join(scopes, " ")},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"state":                 {state},
		"code_challenge":        {challenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(spec.AuthURI, "?") {
		sep = "&"
	}
	return spec.AuthURI + sep + q.Encode()
}

// CodeFromReturn reads the authorization code out of what the operator pasted:
// the whole address of the page the source returned them to, or the bare code.
func CodeFromReturn(pasted string) (code, state string) {
	pasted = strings.TrimSpace(pasted)
	if u, err := url.Parse(pasted); err == nil && u.Query().Get("code") != "" {
		return u.Query().Get("code"), u.Query().Get("state")
	}
	return pasted, ""
}

// ExchangeCode trades the code for a refresh token and records it in the
// credential's file, keeping the client beside it.
func ExchangeCode(ctx context.Context, spec OAuthSpec, file, code, redirect, verifier string) (OAuthSecret, error) {
	s, err := LoadOAuthSecret(file)
	if err != nil {
		return s, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {s.ClientID},
		"client_secret": {s.ClientSecret},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	}
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, spec.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return s, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return s, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return s, fmt.Errorf("token endpoint answered %d without JSON", resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 || out.RefreshToken == "" {
		reason := strings.TrimSpace(out.Error + " " + out.Description)
		if reason == "" {
			reason = fmt.Sprintf("status %d, no refresh_token", resp.StatusCode)
		}
		return s, fmt.Errorf("sign-in failed: %s", reason)
	}
	s.RefreshToken = out.RefreshToken
	s.Account = emailOf(out.IDToken)
	if err := SaveOAuthSecret(file, s); err != nil {
		return s, err
	}
	return s, nil
}

// emailOf reads the email claim of an id_token WITHOUT verifying it. It is
// only displayed: the connector obtained the token itself from the token
// endpoint over TLS, and nothing is decided on it.
func emailOf(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(raw, &claims)
	return claims.Email
}
