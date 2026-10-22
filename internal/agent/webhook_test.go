package agent

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// hookConfig is a connector with one connection that wants a callback. The
// listener belongs to the connector; the connection contributes only the switch
// and the secret.
func hookConfig() *config.Config {
	on := true
	return &config.Config{
		Webhooks: config.Webhooks{Listen: "127.0.0.1:0"},
		Plugins: []config.PluginInstance{{
			ID: "7QK3M9XBWZ0FVT2H", Plugin: "jira", Slug: "jira", Alias: "corp",
			DevProject: "backend", Webhook: &on, WebhookSecret: "s3cret",
		}},
	}
}

// hookPath is that connection's callback path, secret and all.
const hookPath = "/plugin/7QK3M9XBWZ0FVT2H/s3cret"

func post(t *testing.T, h http.Handler, path, body string, headers map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// The URL carries the secret because a legacy tracker cannot sign its callbacks
// at all. A wrong or missing secret must not wake anything.
func TestWebhookAcceptsOnlyTheRightSecret(t *testing.T) {
	woken := make(chan string, 4)
	h := webhookHandler(webhookRoutes(hookConfig()), func(name string) { woken <- name }, nil)

	if code := post(t, h, hookPath, `{"webhookEvent":"comment_created"}`, nil); code != http.StatusOK {
		t.Errorf("correct secret: status = %d, want 200", code)
	}
	select {
	case name := <-woken:
		// The GUID, not the alias: the waker's key is what the poller narrows
		// on, and a rename must not lose a connection's callbacks.
		if name != "7QK3M9XBWZ0FVT2H" {
			t.Errorf("woke %q", name)
		}
	case <-time.After(time.Second):
		t.Fatal("a valid callback must wake the connection")
	}

	for _, path := range []string{
		"/plugin/7QK3M9XBWZ0FVT2H/wrong",
		"/plugin/7QK3M9XBWZ0FVT2H/",
		"/plugin/7QK3M9XBWZ0FVT2H",
		"/plugin/J4RD8N1VC6PQY5ZE/s3cret",
		"/",
	} {
		if code := post(t, h, path, `{}`, nil); code == http.StatusOK {
			t.Errorf("%s: status = %d, want a refusal", path, code)
		}
	}
	select {
	case name := <-woken:
		t.Fatalf("a refused callback woke %q", name)
	default:
	}
}

// Where a tracker can sign its callbacks it does, and we check the signature.
// The secret in the URL is the fallback for the ones that cannot.
func TestWebhookVerifiesSignatureWhenPresent(t *testing.T) {
	woken := make(chan string, 4)
	h := webhookHandler(webhookRoutes(hookConfig()), func(name string) { woken <- name }, nil)
	body := `{"webhookEvent":"jira:issue_updated"}`

	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(body))
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if code := post(t, h, hookPath, body, map[string]string{"X-Hub-Signature": good}); code != http.StatusOK {
		t.Errorf("valid signature: status = %d, want 200", code)
	}
	<-woken

	bad := "sha256=" + strings.Repeat("00", 32)
	if code := post(t, h, hookPath, body, map[string]string{"X-Hub-Signature": bad}); code == http.StatusOK {
		t.Error("a bad signature must be refused even with the right secret in the URL")
	}
	select {
	case name := <-woken:
		t.Fatalf("a bad signature woke %q", name)
	default:
	}
}

// A body that changed in flight must fail the signature it was sent with.
func TestWebhookSignatureCoversTheBody(t *testing.T) {
	woken := make(chan string, 4)
	h := webhookHandler(webhookRoutes(hookConfig()), func(name string) { woken <- name }, nil)

	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(`{"webhookEvent":"comment_created"}`))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	code := post(t, h, hookPath, `{"webhookEvent":"tampered"}`,
		map[string]string{"X-Hub-Signature": sig})
	if code == http.StatusOK {
		t.Error("a signature from a different body must not pass")
	}
}

// Gitea and GitHub sign the same callback three ways at once: X-Hub-Signature
// (SHA-1, kept for compatibility), X-Hub-Signature-256 and — Gitea's own —
// X-Gitea-Signature, a bare SHA-256 hex with no algorithm prefix. Reading only
// the first one AS SHA-256 refused every Gitea callback there is.
func TestWebhookUnderstandsEverySignatureItIsSent(t *testing.T) {
	body := `{"action":"created"}`
	sum := func(h func() hash.Hash) string {
		mac := hmac.New(h, []byte("s3cret"))
		mac.Write([]byte(body))
		return hex.EncodeToString(mac.Sum(nil))
	}

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"gitea", map[string]string{"X-Gitea-Signature": sum(sha256.New)}},
		{"github 256", map[string]string{"X-Hub-Signature-256": "sha256=" + sum(sha256.New)}},
		{"legacy sha1", map[string]string{"X-Hub-Signature": "sha1=" + sum(sha1.New)}},
		{"both, as gitea sends them", map[string]string{
			"X-Hub-Signature":     "sha1=" + sum(sha1.New),
			"X-Hub-Signature-256": "sha256=" + sum(sha256.New),
			"X-Gitea-Signature":   sum(sha256.New),
		}},
	}
	for _, c := range cases {
		woken := make(chan string, 1)
		h := webhookHandler(webhookRoutes(hookConfig()), func(name string) { woken <- name }, nil)
		if code := post(t, h, hookPath, body, c.headers); code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", c.name, code)
			continue
		}
		select {
		case <-woken:
		default:
			t.Errorf("%s: nothing was woken", c.name)
		}
	}
}

// A forged signature is refused whichever header carries it — the URL secret
// alone is not enough once a sender proved it can sign.
func TestEveryForgedSignatureIsRefused(t *testing.T) {
	for _, header := range []string{"X-Gitea-Signature", "X-Hub-Signature-256", "X-Hub-Signature"} {
		h := webhookHandler(webhookRoutes(hookConfig()),
			func(string) { t.Errorf("%s: a forged signature woke a connection", header) }, nil)
		value := strings.Repeat("00", 32)
		if header != "X-Gitea-Signature" {
			value = "sha256=" + value
		}
		if code := post(t, h, hookPath, `{}`, map[string]string{header: value}); code == http.StatusOK {
			t.Errorf("%s: a forged signature was accepted", header)
		}
	}
}

// GitLab signs nothing; it hands the shared secret back in a header. Where the
// header is there it is held to, and where it is absent the URL still decides.
func TestTheSharedSecretHeaderIsHeldTo(t *testing.T) {
	woken := make(chan string, 1)
	h := webhookHandler(webhookRoutes(hookConfig()), func(name string) { woken <- name }, nil)

	if code := post(t, h, hookPath, `{"object_kind":"note"}`,
		map[string]string{"X-Gitlab-Token": "s3cret"}); code != http.StatusOK {
		t.Errorf("the right token was refused: %d", code)
	}
	<-woken

	if code := post(t, h, hookPath, `{"object_kind":"note"}`,
		map[string]string{"X-Gitlab-Token": "guessed"}); code == http.StatusOK {
		t.Error("a wrong token was accepted")
	}
	select {
	case name := <-woken:
		t.Fatalf("a wrong token woke %q", name)
	default:
	}
}

// A connection with the webhook switched off must not answer on its path at
// all, even with the right secret.
func TestWebhookIgnoresDisabledConnections(t *testing.T) {
	off := false
	cfg := hookConfig()
	cfg.Plugins[0].Webhook = &off
	h := webhookHandler(webhookRoutes(cfg), func(string) { t.Error("a disabled connection was woken") }, nil)

	if code := post(t, h, hookPath, `{}`, nil); code == http.StatusOK {
		t.Errorf("status = %d, want a refusal", code)
	}
}

// The listener is the connector's, so switching it off closes every path at
// once — that is the difference between "this connection does not want
// callbacks" and "this machine does not take them".
func TestNoConnectorListenerOpensNoRouteAtAll(t *testing.T) {
	cfg := hookConfig()
	cfg.Webhooks = config.Webhooks{}
	if routes := webhookRoutes(cfg); len(routes) != 0 {
		t.Fatalf("routes = %+v, want none", routes)
	}
}

// Waking is a signal, not a queue: a burst of callbacks must not pile up work.
func TestWakeIsCollapsed(t *testing.T) {
	w := newWaker()
	w.wake("corp")
	w.wake("corp")
	w.wake("corp")

	if got := w.drain(); len(got) != 1 || !got["corp"] {
		t.Fatalf("drain = %v, want one corp", got)
	}
	if got := w.drain(); len(got) != 0 {
		t.Errorf("drain again = %v, want nothing left", got)
	}
}

func TestWakeKeepsConnectionsApart(t *testing.T) {
	w := newWaker()
	w.wake("corp")
	w.wake("other")

	got := w.drain()
	if len(got) != 2 || !got["corp"] || !got["other"] {
		t.Fatalf("drain = %v, want both", got)
	}
}

// Every connection that wants a callback shares the connector's one address, so
// three connections are one server, not three port clashes.
func TestWebhookListenAddressesAreDeduped(t *testing.T) {
	on := true
	cfg := &config.Config{
		Webhooks: config.Webhooks{Listen: "127.0.0.1:8766"},
		Plugins: []config.PluginInstance{
			{ID: "7QK3M9XBWZ0FVT2H", Webhook: &on, WebhookSecret: "x"},
			{ID: "J4RD8N1VC6PQY5ZE", Webhook: &on, WebhookSecret: "y"},
			{ID: "M2XW9BKT6HFV3QDS", WebhookSecret: "z"},
		},
	}
	if got := webhookAddrs(webhookRoutes(cfg)); len(got) != 1 || got[0] != "127.0.0.1:8766" {
		t.Fatalf("addresses = %v, want the connector's one", got)
	}
}

// One listener, many connections. Each is reached at its own path and wakes on
// its own GUID; a secret is good for exactly the connection it was minted for.
func TestOneListenerServesEveryConnection(t *testing.T) {
	on := true
	var woke []string
	h := webhookHandler(webhookRoutes(&config.Config{
		Webhooks: config.Webhooks{Listen: "127.0.0.1:0"},
		Plugins: []config.PluginInstance{
			{ID: "7QK3M9XBWZ0FVT2H", Slug: "jira", Webhook: &on, WebhookSecret: "ASECRET"},
			{ID: "J4RD8N1VC6PQY5ZE", Slug: "jira-test", Webhook: &on, WebhookSecret: "BSECRET"},
		}}),
		func(key string) { woke = append(woke, key) }, nil)

	for _, tc := range []struct {
		name, path string
		want       int
	}{
		{"first connection", "/plugin/7QK3M9XBWZ0FVT2H/ASECRET", 200},
		{"second connection", "/plugin/J4RD8N1VC6PQY5ZE/BSECRET", 200},
		{"wrong secret", "/plugin/7QK3M9XBWZ0FVT2H/nope", 403},
		{"the other connection's secret", "/plugin/7QK3M9XBWZ0FVT2H/BSECRET", 403},
		{"unknown connection", "/plugin/M2XW9BKT6HFV3QDS/ASECRET", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader("{}")))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}

	if len(woke) != 2 || woke[0] != "7QK3M9XBWZ0FVT2H" || woke[1] != "J4RD8N1VC6PQY5ZE" {
		t.Fatalf("woke = %v", woke)
	}
}

// A connection with no secret, or a connector with no listener, opens nothing.
func TestNoSecretAndNoListenerOpenNoRoute(t *testing.T) {
	on := true
	inst := config.PluginInstance{ID: "7QK3M9XBWZ0FVT2H", Webhook: &on}
	routes := webhookRoutes(&config.Config{
		Webhooks: config.Webhooks{Listen: "127.0.0.1:0"},
		Plugins:  []config.PluginInstance{inst}})
	if len(routes) != 0 {
		t.Fatalf("a connection with no secret opened a route: %+v", routes)
	}
	inst.WebhookSecret = "PSECRET"
	routes = webhookRoutes(&config.Config{Plugins: []config.PluginInstance{inst}})
	if len(routes) != 0 {
		t.Fatalf("routes = %+v", routes)
	}
	if addrs := webhookAddrs(routes); len(addrs) != 0 {
		t.Fatalf("addrs = %v", addrs)
	}
}

// A callback for a plugin that PARSES payloads carries the body to it. The
// connector still owns the listener, the secret and the signature — what changes
// is only what it does with a callback it has already found genuine.
func TestDeliverModeCarriesTheBodyToThePlugin(t *testing.T) {
	on := true
	inst := config.PluginInstance{
		ID: "7QK3M9XBWZ0FVT2H", Slug: "svc", Webhook: &on, WebhookSecret: "PSECRET",
	}
	routes := webhookRoutes(&config.Config{
		Webhooks: config.Webhooks{Listen: "127.0.0.1:0"},
		Plugins:  []config.PluginInstance{inst}})
	routes[0].deliver = true

	var got webhookCall
	h := webhookHandler(routes,
		func(string) { t.Error("a deliver route must not wake the poller") },
		func(c webhookCall) { got = c })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/plugin/7QK3M9XBWZ0FVT2H/PSECRET",
		strings.NewReader(`{"event":"comment_created"}`))
	req.Header.Set("X-Event-Kind", "comment")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got.instanceID != "7QK3M9XBWZ0FVT2H" {
		t.Errorf("instance = %q", got.instanceID)
	}
	if string(got.body) != `{"event":"comment_created"}` {
		t.Errorf("body = %q", got.body)
	}
	// Headers travel too: some trackers put the event type only there.
	if got.headers["X-Event-Kind"] != "comment" {
		t.Errorf("headers = %v", got.headers)
	}
}

// A body big enough to matter is refused rather than carried: the plugin runs
// under its own uid with its own memory, and a callback is not a file upload.
func TestAnOversizedCallbackIsRefused(t *testing.T) {
	on := true
	inst := config.PluginInstance{
		ID: "7QK3M9XBWZ0FVT2H", Slug: "svc", Webhook: &on, WebhookSecret: "PSECRET",
	}
	routes := webhookRoutes(&config.Config{
		Webhooks: config.Webhooks{Listen: "127.0.0.1:0"},
		Plugins:  []config.PluginInstance{inst}})
	routes[0].deliver = true

	called := false
	h := webhookHandler(routes, func(string) {}, func(webhookCall) { called = true })

	rec := httptest.NewRecorder()
	body := strings.Repeat("x", maxCallbackBody+1)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/plugin/7QK3M9XBWZ0FVT2H/PSECRET", strings.NewReader(body)))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if called {
		t.Error("an oversized callback was carried to the plugin")
	}
}

// notionRoute is a connection whose source signs callbacks with a secret IT
// introduces: the first, unsigned call carries the token, every later call is
// signed with it. The connector learns the token from that call; the operator
// only has to paste it back into the source to confirm the subscription.
func notionRoute(t *testing.T) (webhookRoute, string) {
	t.Helper()
	dir := t.TempDir()
	return webhookRoute{
		path: "/plugin/7QK3M9XBWZ0FVT2H", secret: "s3cret", listen: "127.0.0.1:0",
		instanceID: "7QK3M9XBWZ0FVT2H",
		signed: &plugin.SigSpec{Header: "X-Notion-Signature", Algo: "hmac-sha256",
			SecretFrom: "verification_token"},
		stateDir: dir,
	}, dir
}

func notionSign(token, body string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestTheSourceIntroducesItsSigningSecretOnTheFirstCall(t *testing.T) {
	rt, dir := notionRoute(t)
	woken := make(chan string, 4)
	delivered := make(chan webhookCall, 4)
	h := webhookHandler([]webhookRoute{rt}, func(name string) { woken <- name },
		func(c webhookCall) { delivered <- c })
	const token = "secret_fake_verification_token_for_tests"

	// Before the handshake nothing can be verified, so a signed call is refused
	// rather than trusted on the URL alone: a sender that says it signs is held
	// to it, and there is nothing to hold it to yet.
	event := `{"type":"comment.created","entity":{"id":"c1","type":"comment"}}`
	if code := post(t, h, hookPath, event, map[string]string{"X-Notion-Signature": notionSign(token, event)}); code != http.StatusForbidden {
		t.Fatalf("signed before the handshake: %d, want 403", code)
	}

	// The handshake: unsigned, and it carries the token. It is remembered and
	// it wakes nothing — there is no event in it.
	if code := post(t, h, hookPath, `{"verification_token":"`+token+`"}`, nil); code != http.StatusOK {
		t.Fatalf("handshake: %d, want 200", code)
	}
	if got := plugin.LoadSigningSecret(dir, rt.instanceID); got != token {
		t.Fatalf("stored secret = %q, want the token the source sent", got)
	}
	select {
	case <-woken:
		t.Fatal("the handshake woke the connection")
	case <-delivered:
		t.Fatal("the handshake was delivered to the plugin")
	default:
	}

	// From here on the source's signature is checked with that token.
	if code := post(t, h, hookPath, event, map[string]string{"X-Notion-Signature": notionSign(token, event)}); code != http.StatusOK {
		t.Fatalf("genuine signed callback: %d, want 200", code)
	}
	select {
	case <-woken:
	case <-time.After(time.Second):
		t.Fatal("a genuine signed callback woke nothing")
	}
	if code := post(t, h, hookPath, event, map[string]string{"X-Notion-Signature": notionSign("guessed", event)}); code != http.StatusForbidden {
		t.Fatalf("forged signature: %d, want 403", code)
	}
	// Unsigned and not a handshake: refused. The URL alone does not admit a
	// caller once the source is known to sign.
	if code := post(t, h, hookPath, event, nil); code != http.StatusForbidden {
		t.Fatalf("unsigned event: %d, want 403", code)
	}
	select {
	case name := <-woken:
		t.Fatalf("a refused callback woke %q", name)
	default:
	}
}

// A second handshake — the subscription was recreated in the source — must not
// silently replace a secret that callbacks are already verified with: anyone
// holding the URL could otherwise install their own. It is kept aside for the
// operator to accept, and the callbacks keep being checked against the old one.
func TestASecondHandshakeWaitsForTheOperator(t *testing.T) {
	rt, dir := notionRoute(t)
	h := webhookHandler([]webhookRoute{rt}, func(string) {}, nil)
	if err := plugin.SaveSigningSecret(dir, rt.instanceID, "first"); err != nil {
		t.Fatal(err)
	}

	if code := post(t, h, hookPath, `{"verification_token":"second"}`, nil); code != http.StatusOK {
		t.Fatalf("second handshake: %d, want 200 — the source must not see a failing endpoint", code)
	}
	if got := plugin.LoadSigningSecret(dir, rt.instanceID); got != "first" {
		t.Fatalf("the verified secret was replaced by an unsigned call: %q", got)
	}
	if got := plugin.PendingSigningSecret(dir, rt.instanceID); got != "second" {
		t.Fatalf("pending = %q, want the new token kept for the operator", got)
	}
	// Accepting it is the operator's act; it clears the pending one.
	if err := plugin.SaveSigningSecret(dir, rt.instanceID, "second"); err != nil {
		t.Fatal(err)
	}
	if plugin.PendingSigningSecret(dir, rt.instanceID) != "" {
		t.Fatal("accepting a secret must clear the pending one")
	}
	body := `{"type":"comment.created"}`
	if code := post(t, h, hookPath, body, map[string]string{"X-Notion-Signature": notionSign("second", body)}); code != http.StatusOK {
		t.Fatalf("after acceptance the new token must verify: %d", code)
	}
}

// The signature value is accepted with or without its algorithm prefix: Notion
// documents "sha256=<hex>", and a sender that drops the prefix is still signing.
func TestSourceSignatureReadsBothSpellings(t *testing.T) {
	mac := hmac.New(sha256.New, []byte("tok"))
	mac.Write([]byte("body"))
	sum := hex.EncodeToString(mac.Sum(nil))
	for _, v := range []string{"sha256=" + sum, sum} {
		if !sourceSignatureHolds(v, "tok", []byte("body")) {
			t.Errorf("%q must verify", v)
		}
	}
	for _, v := range []string{"sha1=" + sum, "sha256=" + strings.Repeat("0", 64), "", "nonsense"} {
		if sourceSignatureHolds(v, "tok", []byte("body")) {
			t.Errorf("%q must not verify", v)
		}
	}
}
