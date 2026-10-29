package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"hash"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/i18n"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// A webhook does not parse Jira's callback payload. It only says "something
// changed over there, read it now" — the reading is the poller's job, exactly as
// it is without a webhook.
//
// That keeps ONE path producing events, with one watermark and one dedup. A
// second parser would be a second set of edge cases to get wrong, and the
// webhook and the poller would drift. What the webhook buys is latency: seconds
// instead of the poll interval.

// waker collects which integrations have been asked to read now. Waking is a
// signal, not a queue: a burst of callbacks is one read, not one read each.
type waker struct {
	mu      sync.Mutex
	pending map[string]bool
	ch      chan struct{}
}

func newWaker() *waker {
	return &waker{pending: map[string]bool{}, ch: make(chan struct{}, 1)}
}

func (w *waker) wake(name string) {
	w.mu.Lock()
	w.pending[name] = true
	w.mu.Unlock()
	select {
	case w.ch <- struct{}{}:
	default: // already signalled; the pending set carries the detail
	}
}

// drain takes the set of integrations to read and clears it.
func (w *waker) drain() map[string]bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.pending
	w.pending = map[string]bool{}
	return out
}

// signal fires when at least one integration is waiting to be read.
func (w *waker) signal() <-chan struct{} { return w.ch }

// webhookRoute is one callback address: where it listens, the secret that
// guards it, and the connection it belongs to.
type webhookRoute struct {
	path   string
	secret string
	listen string
	// instanceID is the connection's GUID — the waker's key and the address a
	// delivered body is carried to.
	instanceID string
	// deliver hands the validated body to the plugin instead of waking a poll.
	deliver bool
	// signed is set for a source that signs with a secret IT introduces (see
	// signing.go); stateDir is where that secret is kept.
	signed   *plugin.SigSpec
	stateDir string
}

// maxCallbackBody bounds what a callback may carry. A plugin runs under its own
// uid with its own memory, and a callback is an event, not a file upload.
const maxCallbackBody = 1 << 20

// webhookCall is one validated callback, on its way to a plugin.
type webhookCall struct {
	instanceID string
	headers    map[string]string
	body       []byte
}

// carriedHeaders are the ones a plugin may need to read: several trackers put
// the event type in a header and nothing else distinguishes one callback from
// another. Everything else is dropped — a plugin has no business seeing a
// customer's cookies or authorisation headers.
var carriedHeaders = []string{
	"X-Event-Kind", "X-Event-Key", "X-Github-Event", "X-Gitlab-Event",
	"X-Gitea-Event",
	"X-Atlassian-Webhook-Identifier", "Content-Type", "User-Agent",
}

// webhookRoutes collects the callback addresses of the connections that want
// one. The connector owns the listener; a connection contributes a path and the
// secret that guards it.
func webhookRoutes(cfg *config.Config) []webhookRoute {
	var out []webhookRoute
	// One listener for every connection: the connector IS the listener, so the
	// address is a property of the machine, not of an integration.
	if cfg.Webhooks.Enabled() {
		for _, p := range cfg.Plugins {
			if !p.WebhookEnabled() {
				continue
			}
			out = append(out, webhookRoute{
				path: p.WebhookPath(), secret: p.WebhookSecret,
				listen: cfg.Webhooks.Listen, instanceID: p.ID,
			})
		}
	}
	return out
}

// webhookHandler routes callbacks to the connection they belong to.
//
// The URL carries the secret because a legacy tracker cannot sign its callbacks
// at all — so the whole URL is a credential. Where it CAN sign (X-Hub-Signature,
// HMAC-SHA256 over the body), the signature is checked too, and a bad one is
// refused even when the URL is right.
func webhookHandler(routes []webhookRoute, wake func(key string), deliver func(webhookCall)) http.Handler {
	byPath := map[string]webhookRoute{}
	for _, r := range routes {
		byPath[r.path] = r
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The path is <webhook path>/<secret>; both halves must match.
		at := strings.LastIndex(r.URL.Path, "/")
		if at <= 0 {
			http.NotFound(w, r)
			return
		}
		prefix, given := r.URL.Path[:at], r.URL.Path[at+1:]
		rt, ok := byPath[prefix]
		if !ok || given == "" {
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(given), []byte(rt.secret)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxCallbackBody+1))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(body) > maxCallbackBody {
			http.Error(w, "callback too large", http.StatusRequestEntityTooLarge)
			return
		}
		// GitLab does not sign a callback at all: it sends the shared secret
		// back in a header. Where that header is present it must match, which is
		// worth having — a URL leaks into logs and proxies far more easily than
		// a header does.
		if token := r.Header.Get("X-Gitlab-Token"); token != "" &&
			subtle.ConstantTimeCompare([]byte(token), []byte(rt.secret)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !signaturesHold(r.Header, rt.secret, body) {
			http.Error(w, "bad signature", http.StatusForbidden)
			return
		}
		// A source that signs with its OWN secret: the unsigned handshake that
		// introduces it is remembered and goes no further; everything else must
		// carry a signature that verifies with the secret in force.
		if rt.signed != nil && rt.signed.SecretFrom != "" {
			given := r.Header.Get(rt.signed.Header)
			if given == "" {
				if token := handshakeSecret(body, rt.signed.SecretFrom); token != "" {
					acceptHandshake(rt.stateDir, rt.instanceID, token)
					w.WriteHeader(http.StatusOK)
					return
				}
				http.Error(w, "unsigned", http.StatusForbidden)
				return
			}
			secret := plugin.LoadSigningSecret(rt.stateDir, rt.instanceID)
			if secret == "" {
				http.Error(w, "the source's signing secret is not known yet", http.StatusForbidden)
				return
			}
			if !sourceSignatureHolds(given, secret, body) {
				http.Error(w, "bad signature", http.StatusForbidden)
				return
			}
		}
		// Validated. What happens next is the only difference between the two
		// modes — the check above is the same one, in one place, for both.
		if rt.deliver && deliver != nil {
			deliver(webhookCall{instanceID: rt.instanceID, headers: carried(r.Header), body: body})
		} else {
			wake(rt.instanceID)
		}
		w.WriteHeader(http.StatusOK)
	})
}

// carried picks the headers a plugin may need to read: several trackers put the
// event type in a header and nothing else distinguishes one callback from
// another. Everything else is dropped — a plugin has no business seeing a
// customer's cookies or authorisation headers.
func carried(h http.Header) map[string]string {
	out := map[string]string{}
	for _, name := range carriedHeaders {
		if v := h.Get(name); v != "" {
			out[name] = v
		}
	}
	return out
}

// signedHeaders are the ways a tracker says it signed a callback, and how to
// read each. One sender uses several at once: Gitea sends all three, GitHub the
// first two. Reading only "X-Hub-Signature" AS SHA-256 refused every callback
// either of them sends, because that header carries a SHA-1 for compatibility
// and the SHA-256 lives beside it under another name.
var signedHeaders = []struct {
	name string
	alg  func() hash.Hash
	// prefixed says the value is "<algo>=<hex>" rather than a bare hex digest.
	prefixed bool
}{
	{"X-Hub-Signature-256", sha256.New, true},
	{"X-Gitea-Signature", sha256.New, false},
	{"X-Hub-Signature", sha1.New, true},
}

// signaturesHold verifies EVERY signature the sender supplied. A sender that can
// sign gets held to it — a forged digest is refused even with the right secret
// in the URL — while one that cannot sign at all is still admitted by the URL,
// which is itself a credential.
func signaturesHold(h http.Header, secret string, body []byte) bool {
	for _, s := range signedHeaders {
		value := h.Get(s.name)
		if value == "" {
			continue
		}
		alg := s.alg
		if s.prefixed {
			name, rest, found := strings.Cut(value, "=")
			if !found {
				return false
			}
			// The header names its own algorithm; believe it over the default,
			// so a sender that upgrades does not silently fail the check.
			if strings.EqualFold(name, "sha256") {
				alg = sha256.New
			} else if strings.EqualFold(name, "sha1") {
				alg = sha1.New
			} else {
				return false
			}
			value = rest
		}
		want, err := hex.DecodeString(strings.TrimSpace(value))
		if err != nil {
			return false
		}
		mac := hmac.New(alg, []byte(secret))
		mac.Write(body)
		if !hmac.Equal(want, mac.Sum(nil)) {
			return false
		}
	}
	return true
}

// webhookAddrs lists the distinct addresses to listen on. Several sources
// commonly share one address, which is one server, not a port clash.
func webhookAddrs(routes []webhookRoute) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range routes {
		if r.listen == "" || seen[r.listen] {
			continue
		}
		seen[r.listen] = true
		out = append(out, r.listen)
	}
	return out
}

// serveWebhooks starts a listener per distinct address and stops them when ctx
// is done. A listener that cannot bind is logged and skipped: an integration
// that also polls keeps working, and one that does not is visibly broken rather
// than silently so.
func serveWebhooks(ctx context.Context, routes []webhookRoute, wake func(string), deliver func(webhookCall)) []*http.Server {
	handler := webhookHandler(routes, wake, deliver)
	var servers []*http.Server
	for _, addr := range webhookAddrs(routes) {
		srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
		go func(s *http.Server) {
			i18n.Logf("вебхуки — слушаю %s", s.Addr)
			if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				i18n.Logf("вебхуки на %s — %v", s.Addr, err)
			}
		}(srv)
		servers = append(servers, srv)
	}
	go func() {
		<-ctx.Done()
		for _, s := range servers {
			_ = s.Close()
		}
	}()
	return servers
}
