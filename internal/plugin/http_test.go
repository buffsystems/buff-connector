package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// buff/http is the only way a plugin reaches anything. The plugin never holds a
// credential: it names one, and the host attaches it. Everything else here is
// the guard around that — because "the plugin can reach the host you configured"
// must not quietly mean "the plugin can reach anything that host redirects to".

func newGate(t *testing.T, rules []ResolvedEgress, creds map[string]Credential) *HTTPGate {
	t.Helper()
	return NewHTTPGate(rules, creds, nil)
}

func TestAllowedRequestCarriesTheCredential(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)

	gate := newGate(t,
		[]ResolvedEgress{{Host: u.Host, Methods: []string{"GET"}, PathPrefixes: []string{"/rest/"}}},
		map[string]Credential{"api": {Kind: "bearer", Token: "SEKRET"}})

	res, err := gate.Do(context.Background(), HTTPRequest{
		Credential: "api", Method: "GET", URL: up.URL + "/rest/api/2/myself",
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if res.Status != 200 || !strings.Contains(res.Body, "ok") {
		t.Fatalf("res = %+v", res)
	}
	if gotAuth != "Bearer SEKRET" {
		t.Fatalf("upstream saw %q, want the host-attached credential", gotAuth)
	}
	// The plugin must never see the token come back.
	if strings.Contains(res.Body, "SEKRET") {
		t.Fatal("credential leaked into the plugin-visible response")
	}
}

// A rendered picture is bytes, not text. Pushed through a JSON string its invalid
// UTF-8 is replaced byte by byte and the model gets a corrupt image; asked for
// as binary it arrives intact, base64-encoded.
func TestBinaryResponseArrivesIntactAsBase64(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0xfe, 0x00, 0x80}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	gate := newGate(t, []ResolvedEgress{{Host: u.Host, Methods: []string{"GET"}, PathPrefixes: []string{"/"}}}, nil)

	res, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: up.URL + "/renders/1.png", Binary: true})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if res.Body != "" {
		t.Fatal("a binary response must not also be offered as text")
	}
	got, err := base64.StdEncoding.DecodeString(res.BodyBase64)
	if err != nil || string(got) != string(png) {
		t.Fatalf("bytes did not survive: %v %x", err, got)
	}
	// And the same bytes as text would NOT have survived a JSON round trip.
	asText, _ := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: up.URL + "/renders/1.png"})
	raw, _ := json.Marshal(asText)
	var back HTTPResponse
	_ = json.Unmarshal(raw, &back)
	if back.Body == string(png) {
		t.Fatal("the test premise is wrong: text survived JSON — then binary would be unnecessary")
	}
}

func TestHostOutsideTheAllowlistIsRefused(t *testing.T) {
	gate := newGate(t, []ResolvedEgress{{Host: "jira.example.com"}}, nil)
	_, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://evil.example.net/"})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("err = %v", err)
	}
}

func TestMethodAndPathAreEnforced(t *testing.T) {
	gate := newGate(t, []ResolvedEgress{{
		Host: "jira.example.com", Methods: []string{"GET"}, PathPrefixes: []string{"/rest/api/"},
	}}, nil)

	if _, err := gate.Do(context.Background(), HTTPRequest{
		Method: "DELETE", URL: "https://jira.example.com/rest/api/2/issue/1",
	}); err == nil || !strings.Contains(err.Error(), "DELETE") {
		t.Fatalf("method not enforced: %v", err)
	}
	if _, err := gate.Do(context.Background(), HTTPRequest{
		Method: "GET", URL: "https://jira.example.com/secure/admin/",
	}); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("path not enforced: %v", err)
	}
}

// The one that matters: an allowed host answering 302 must not become a way to
// reach the cloud metadata service — or anything else.
func TestRedirectsAreNotFollowed(t *testing.T) {
	// Stands in for whatever the redirect points at — the cloud metadata service
	// in the real attack. It must never be reached.
	var reached bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte("SECRET-CREDENTIALS"))
	}))
	defer target.Close()

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/latest/meta-data/", http.StatusFound)
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)

	gate := newGate(t, []ResolvedEgress{{Host: u.Host}}, nil)
	res, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: up.URL + "/x"})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if reached {
		t.Fatal("the redirect was followed: the plugin reached a host outside its allowlist")
	}
	if res.Status != 302 {
		t.Fatalf("status = %d, want the redirect handed back unfollowed", res.Status)
	}
	if res.Headers["Location"] == "" {
		t.Error("the plugin should still see where it was being sent")
	}
	if strings.Contains(res.Body, "SECRET-CREDENTIALS") {
		t.Fatal("content from the redirect target leaked into the response")
	}
}

// Link-local is where every cloud keeps its credential service, and nobody
// configures an integration against it on purpose.
func TestLinkLocalIsRefusedEvenWhenAllowlisted(t *testing.T) {
	gate := newGate(t, []ResolvedEgress{{Host: "169.254.169.254"}}, nil)
	_, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: "http://169.254.169.254/latest/"})
	if err == nil || !strings.Contains(err.Error(), "never a valid") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

// The opposite case, and the one that matters for the product: a self-hosted
// tracker on the customer's own private network must work. The connector lives
// inside that perimeter to reach exactly such systems.
func TestPrivateAddressesAreAllowedBecauseThatIsThePoint(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"self":"hosted"}`))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)

	gate := newGate(t, []ResolvedEgress{{Host: u.Host}}, nil)
	res, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: up.URL + "/"})
	if err != nil {
		t.Fatalf("a private-network tracker must be reachable: %v", err)
	}
	if res.Status != 200 || !strings.Contains(res.Body, "hosted") {
		t.Fatalf("res = %+v", res)
	}
}

func TestOversizeResponseIsRefusedNotTruncated(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)

	gate := NewHTTPGate([]ResolvedEgress{{Host: u.Host}}, nil, nil)
	gate.MaxBody = 1000
	_, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: up.URL + "/"})
	if err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("err = %v — an oversize body must fail loudly, never come back trimmed", err)
	}
}

// Every request is journalled with the call that caused it. Without this, actions
// in the customer's tracker are anonymous "service account did something" and an
// incident cannot be reconstructed.
func TestRequestsAreJournalled(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)

	var entries []HTTPLogEntry
	gate := NewHTTPGate([]ResolvedEgress{{Host: u.Host}},
		map[string]Credential{"api": {Kind: "bearer", Token: "SEKRET"}},
		func(e HTTPLogEntry) { entries = append(entries, e) })

	ctx := WithCallID(context.Background(), "call-42")
	if _, err := gate.Do(ctx, HTTPRequest{Credential: "api", Method: "GET", URL: up.URL + "/a"}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	e := entries[0]
	if e.CallID != "call-42" || e.Method != "GET" || e.Status != 200 || e.Credential != "api" {
		t.Fatalf("entry = %+v", e)
	}
	if strings.Contains(e.URL, "SEKRET") {
		t.Fatal("journal must never carry the credential")
	}
}

// The plugin's request arrives as JSON over the channel, so the handler is the
// real entry point and needs its own contract.
func TestHandlerRejectsUnknownMethodsAndAnswersHTTP(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"v":1}`))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)

	gate := newGate(t, []ResolvedEgress{{Host: u.Host}}, nil)
	h := gate.Handler(nil)

	if _, err := h(context.Background(), "buff/nonsense", nil); err == nil {
		t.Fatal("unknown buff method must be refused")
	}
	params, _ := json.Marshal(HTTPRequest{Method: "GET", URL: up.URL + "/"})
	out, err := h(context.Background(), "buff/http", params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out.(HTTPResponse).Status != 200 {
		t.Fatalf("out = %+v", out)
	}
}

// ---- the web rule ---------------------------------------------------------
//
// The destination is the model's choice, so the guard cannot be a host list.
// What replaces it: the operator's policy, public addresses only, and a dial
// pinned to the address that passed the check. The resolver is injected: a
// test decides that "allowed.test" is a public site living, for the purpose of
// the test, on the loopback server — which also proves the pin, because the
// name itself resolves nowhere.

func webGate(t *testing.T, up *httptest.Server, allow, deny, types []string) *HTTPGate {
	t.Helper()
	pol, err := NewWebPolicy(allow, deny, types)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	gate := NewHTTPGate([]ResolvedEgress{{Web: pol, Methods: []string{"GET"}}},
		map[string]Credential{"api": {Kind: "bearer", Token: "SEKRET"}}, nil)
	u, _ := url.Parse(up.URL)
	gate.resolve = func(host string) (net.IP, error) {
		switch host {
		case "allowed.test", "other.test":
			return net.ParseIP(u.Hostname()), nil
		}
		return nil, fmt.Errorf("%s resolves to 10.0.0.5, which is not a public address", host)
	}
	return gate
}

func webURL(up *httptest.Server, host, path string) string {
	u, _ := url.Parse(up.URL)
	return "http://" + host + ":" + u.Port() + path
}

func TestWebRuleReachesAPublicSiteThroughThePinnedAddress(t *testing.T) {
	var gotHost, gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotAuth = r.Host, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<h1>hello</h1>"))
	}))
	defer up.Close()
	gate := webGate(t, up, nil, nil, nil)

	res, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: webURL(up, "allowed.test", "/page")})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if res.Status != 200 || !strings.Contains(res.Body, "hello") {
		t.Fatalf("res = %+v", res)
	}
	if !strings.HasPrefix(gotHost, "allowed.test") {
		t.Fatalf("the site must see the name it was asked for, got Host %q", gotHost)
	}
	if gotAuth != "" {
		t.Fatal("no credential may ever travel on a web request")
	}
}

func TestWebRuleRefusesANameThatResolvesInside(t *testing.T) {
	var reached bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true }))
	defer up.Close()
	gate := webGate(t, up, nil, nil, nil)

	_, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: webURL(up, "intranet.test", "/")})
	if err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("err = %v", err)
	}
	if reached {
		t.Fatal("the server was reached despite the refusal")
	}
}

func TestWebRuleNeverCarriesACredential(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer up.Close()
	gate := webGate(t, up, nil, nil, nil)
	_, err := gate.Do(context.Background(), HTTPRequest{
		Credential: "api", Method: "GET", URL: webURL(up, "allowed.test", "/"),
	})
	if err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("a web request naming a credential must be refused outright: %v", err)
	}
}

func TestWebRuleAppliesTheOperatorsListsBeforeResolving(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer up.Close()
	gate := webGate(t, up, nil, []string{"other.test"}, nil)
	resolved := false
	inner := gate.resolve
	gate.resolve = func(h string) (net.IP, error) { resolved = true; return inner(h) }

	_, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: webURL(up, "other.test", "/")})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("err = %v", err)
	}
	if resolved {
		t.Fatal("a blocked site must not even be looked up")
	}
	if _, err := gate.Do(context.Background(), HTTPRequest{Method: "POST", URL: webURL(up, "allowed.test", "/")}); err == nil ||
		!strings.Contains(err.Error(), "POST") {
		t.Fatalf("the rule's methods hold on the web too: %v", err)
	}
	if _, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: "ftp://allowed.test/"}); err == nil {
		t.Fatal("only http(s) is the web")
	}
}

func TestWebRuleWithholdsContentOfATypeTheOperatorExcluded(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/doc.pdf" {
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.4 secret"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer up.Close()
	gate := webGate(t, up, nil, nil, []string{"text/html", "application/json"})

	res, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: webURL(up, "allowed.test", "/doc.pdf")})
	if err == nil || !strings.Contains(err.Error(), "application/pdf") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(res.Body, "PDF") {
		t.Fatal("the body of a refused type leaked")
	}
	if res, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: webURL(up, "allowed.test", "/data")}); err != nil ||
		!strings.Contains(res.Body, "ok") {
		t.Fatalf("an allowed type passes: %v %+v", err, res)
	}
}

// A configured host keeps its own, narrower rule even when a web rule sits
// beside it: the search engine's API is still limited to its paths, and a
// request there still carries the engine's key.
func TestConfiguredHostRuleTakesPrecedenceOverTheWebRule(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Key")
		_, _ = w.Write([]byte("ok"))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	pol, _ := NewWebPolicy(nil, nil, nil)
	gate := NewHTTPGate([]ResolvedEgress{
		{Host: u.Host, Methods: []string{"GET"}, PathPrefixes: []string{"/api/"}},
		{Web: pol, Methods: []string{"GET"}},
	}, map[string]Credential{"engine": {Kind: "header", Header: "X-Key", Token: "K"}}, nil)

	if _, err := gate.Do(context.Background(), HTTPRequest{
		Credential: "engine", Method: "GET", URL: up.URL + "/api/search",
	}); err != nil || gotAuth != "K" {
		t.Fatalf("the configured rule must serve its host with its credential: %v %q", err, gotAuth)
	}
	// Off the configured paths the same host is not quietly promoted to "the
	// web": loopback is not public, and the fixed rule's answer is the one that
	// names the real problem.
	if _, err := gate.Do(context.Background(), HTTPRequest{Method: "GET", URL: up.URL + "/admin"}); err == nil {
		t.Fatal("a path outside the configured rule must not pass")
	}
}

func TestURLAllowedAnswersFromTheListsWithoutResolving(t *testing.T) {
	pol, _ := NewWebPolicy(nil, []string{"pastebin.com"}, nil)
	gate := NewHTTPGate([]ResolvedEgress{{Web: pol, Methods: []string{"GET"}}}, nil, nil)
	gate.resolve = func(string) (net.IP, error) { t.Fatal("url_allowed must not resolve"); return nil, nil }
	h := gate.Handler(nil)

	ask := func(u string) (bool, string) {
		out, err := h(context.Background(), "buff/url_allowed", json.RawMessage(`{"url":`+strconv.Quote(u)+`}`))
		if err != nil {
			t.Fatalf("url_allowed: %v", err)
		}
		m := out.(map[string]any)
		reason, _ := m["reason"].(string)
		return m["allowed"].(bool), reason
	}
	if ok, _ := ask("https://wikipedia.org/wiki/Go"); !ok {
		t.Fatal("an open policy allows a public site")
	}
	if ok, why := ask("https://pastebin.com/x"); ok || !strings.Contains(why, "blocked") {
		t.Fatalf("blocked site: %v %q", ok, why)
	}
	if ok, _ := ask("ftp://x/"); ok {
		t.Fatal("not the web")
	}
	// Without a web rule nothing is allowed — and the plugin is told so.
	plain := NewHTTPGate([]ResolvedEgress{{Host: "jira.example.com"}}, nil, nil).Handler(nil)
	out, _ := plain(context.Background(), "buff/url_allowed", json.RawMessage(`{"url":"https://a.b/"}`))
	if out.(map[string]any)["allowed"].(bool) {
		t.Fatal("a plugin with no web rule has no web")
	}
}

// A document to upload is bytes too. Pushed through the JSON body string it is
// re-encoded as UTF-8 — every byte past 0x7f becomes two — and the customer's
// system stores a corrupt file. Sent as body_base64 it arrives byte for byte.
func TestBinaryRequestBodyArrivesIntact(t *testing.T) {
	pdf := []byte{'%', 'P', 'D', 'F', '-', 0x00, 0xff, 0xfe, 0x80, 0x0a}
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		got = buf[:n]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"uploaded"}`))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	gate := newGate(t, []ResolvedEgress{{Host: u.Host, Methods: []string{"POST"}, PathPrefixes: []string{"/"}}}, nil)

	res, err := gate.Do(context.Background(), HTTPRequest{
		Method: "POST", URL: up.URL + "/v1/file_uploads/1/send",
		Headers:    map[string]string{"Content-Type": "application/octet-stream"},
		BodyBase64: base64.StdEncoding.EncodeToString(pdf),
	})
	if err != nil || res.Status != 200 {
		t.Fatalf("do: %v %+v", err, res)
	}
	if string(got) != string(pdf) {
		t.Fatalf("upstream received %x, want %x", got, pdf)
	}
	// A body given both ways is a plugin bug, not a guess to make.
	if _, err := gate.Do(context.Background(), HTTPRequest{
		Method: "POST", URL: up.URL + "/v1/x", Body: "text", BodyBase64: "AA==",
	}); err == nil {
		t.Fatal("a request with both body and body_base64 was accepted")
	}
}

// buff/credential hands a secret to the plugin — ONLY one of the `password`
// kind, which the manifest declared as handed. Every other kind stays in the
// host: a plugin asking for its bearer token gets a refusal, not the token.
func TestOnlyAPasswordCredentialIsHandedToThePlugin(t *testing.T) {
	var journal []HTTPLogEntry
	gate := NewHTTPGate(nil, map[string]Credential{
		"share": {Kind: KindPassword, Username: `CORP\svc`, Password: "hunter2"},
		"api":   {Kind: "bearer", Token: "SEKRET"},
	}, func(e HTTPLogEntry) { journal = append(journal, e) })
	h := gate.Handler(nil)

	out, err := h(WithCallID(context.Background(), "call-1"), "buff/credential", json.RawMessage(`{"name":"share"}`))
	if err != nil {
		t.Fatalf("handed: %v", err)
	}
	got := out.(HandedCredential)
	if got.Username != `CORP\svc` || got.Password != "hunter2" {
		t.Fatalf("got %+v", got)
	}
	if len(journal) != 1 || journal[0].Credential != "share" || journal[0].Method != "credential" || journal[0].CallID != "call-1" {
		t.Fatalf("a handed secret is journalled: %+v", journal)
	}
	if _, err := h(context.Background(), "buff/credential", json.RawMessage(`{"name":"api"}`)); err == nil {
		t.Fatal("a bearer token must never be handed to the plugin")
	}
	if _, err := h(context.Background(), "buff/credential", json.RawMessage(`{"name":"nope"}`)); err == nil {
		t.Fatal("an unknown credential is an error, not an empty answer")
	}
}
