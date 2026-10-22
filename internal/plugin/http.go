package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// buff/http is the only way out for a plugin, and the reason the plugin can be
// given none of the customer's credentials: it names one, the host attaches it.
//
// The guard around that is the interesting part. "The plugin may reach the host
// you configured" has to survive the ways that sentence is usually broken:
//
//   - a redirect. An allowed host answering 302 to 169.254.169.254 would walk
//     the plugin straight into the cloud metadata service, so redirects are
//     never followed — the response is handed back as-is.
//   - a name that resolves at the cloud metadata service. Link-local is refused
//     outright. Private ranges are NOT: the connector runs inside the customer's
//     perimeter precisely to reach systems that live on 10.x — refusing those
//     would refuse the product.
//   - too much rope on an allowed host. The manifest can pin methods and path
//     prefixes, so "read tickets" does not also mean "call the admin API".
//
// Nothing here truncates: an oversize body is an error, not a trimmed string.
//
// A WEB rule (web.go) is the exception to "the host you configured": there the
// destination is the model's choice. What holds it instead: the operator's
// lists, public addresses only, a dial pinned to the address that was checked,
// never a credential, and the content types the operator allowed.

// Credential is one secret the operator supplied. It lives in the host and is
// attached to outgoing requests; it is never sent to the plugin.
type Credential struct {
	Kind     string // bearer | basic | header | service_account | oauth2
	Header   string // header name, for kind "header"
	Token    string
	Username string
	Password string

	// File is where the secret lives, for the minted kinds: it is re-read at
	// mint time because an OAuth sign-in adds the refresh token to it while
	// the instance runs.
	File string
	// OAuth is how a minted kind's bearer is issued, from the manifest.
	OAuth *OAuthSpec
}

// HTTPRequest is what a plugin asks for.
type HTTPRequest struct {
	Credential string            `json:"credential,omitempty"`
	Method     string            `json:"method"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	// BodyBase64 is the request body as bytes, for an upload: a document pushed
	// through the text field is re-encoded as UTF-8 on the way and arrives
	// corrupt. One of Body and BodyBase64, never both.
	BodyBase64 string `json:"body_base64,omitempty"`
	// Binary asks for the response body as base64 (BodyBase64) instead of text.
	// A rendered PNG is not text: forced through a JSON string its invalid UTF-8
	// is replaced byte by byte and the picture arrives corrupt.
	Binary bool `json:"binary,omitempty"`
}

// HTTPResponse is what it gets back.
type HTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	// BodyBase64 carries the body when the request asked for Binary; Body is
	// then empty.
	BodyBase64 string `json:"body_base64,omitempty"`
}

// HandedCredential is what buff/credential answers: the account and password
// of a `password` credential, for the plugin's own client.
type HandedCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// HTTPLogEntry is the journal line for one request. It carries the call that
// caused it so an action in the customer's system can be traced back to a run
// instead of reading as an anonymous service account.
type HTTPLogEntry struct {
	CallID     string        `json:"call_id,omitempty"`
	Credential string        `json:"credential,omitempty"`
	Method     string        `json:"method"`
	URL        string        `json:"url"`
	Status     int           `json:"status"`
	Duration   time.Duration `json:"duration_ms"`
	Error      string        `json:"error,omitempty"`
}

type callIDKey struct{}

// WithCallID tags a context so every request made under it is attributable.
func WithCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callIDKey{}, id)
}

// CallID reads the tag back.
func CallID(ctx context.Context) string {
	v, _ := ctx.Value(callIDKey{}).(string)
	return v
}

// HTTPGate enforces one instance's egress policy.
type HTTPGate struct {
	rules   []ResolvedEgress
	creds   map[string]Credential
	journal func(HTTPLogEntry)
	tokens  *tokenMinter

	// MaxBody bounds a response. Past it we fail: a truncated ticket is worse
	// than a clear error, because nobody notices the missing half.
	MaxBody int64
	Timeout time.Duration
	client  *http.Client

	// resolve turns a web destination into the ONE public address the dial is
	// pinned to. Injected so a test can decide what a name means.
	resolve func(host string) (net.IP, error)
}

// pin is the address a web request must be dialled to, carried on the context
// so the shared transport can honour it per request.
type pinKey struct{}

type pin struct {
	host string
	ip   net.IP
}

// pinnedTransport dials the checked address when the connection is to the
// checked name. A proxy from the environment, if any, is dialled as itself:
// the name then travels to the proxy, which resolves it on its own side.
func pinnedTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if p, ok := ctx.Value(pinKey{}).(pin); ok {
			if host, port, err := net.SplitHostPort(addr); err == nil && strings.EqualFold(host, p.host) {
				addr = net.JoinHostPort(p.ip.String(), port)
			}
		}
		return dialer.DialContext(ctx, network, addr)
	}
	return tr
}

func NewHTTPGate(rules []ResolvedEgress, creds map[string]Credential, journal func(HTTPLogEntry)) *HTTPGate {
	return &HTTPGate{
		rules:   rules,
		creds:   creds,
		journal: journal,
		tokens:  newTokenMinter(),
		MaxBody: 32 << 20,
		Timeout: 60 * time.Second,
		client: &http.Client{
			Transport: pinnedTransport(),
			// Never follow: a redirect is the classic way past a host allowlist.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		resolve: func(host string) (net.IP, error) { return publicAddressFor(host, net.LookupIP) },
	}
}

// Handler adapts the gate to the plugin channel. Extra methods can be layered on
// by the caller through next.
func (g *HTTPGate) Handler(next Handler) Handler {
	return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "buff/http":
			var req HTTPRequest
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, fmt.Errorf("buff/http: bad request: %w", err)
			}
			return g.Do(ctx, req)
		case "buff/log":
			return map[string]any{}, nil // drained by the instance logger
		case "buff/url_allowed":
			var req struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, fmt.Errorf("buff/url_allowed: bad request: %w", err)
			}
			return g.urlAllowed(req.URL), nil
		case "buff/credential":
			var req struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, fmt.Errorf("buff/credential: bad request: %w", err)
			}
			return g.hand(ctx, req.Name)
		}
		if next != nil {
			return next(ctx, method, params)
		}
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

// hand gives the plugin a credential of the handed kind — and refuses every
// other kind by name, because the plugin naming its bearer token here would be
// exactly the leak the host exists to prevent. Journalled like a request: a
// secret leaving the host is an event the operator can trace.
func (g *HTTPGate) hand(ctx context.Context, name string) (HandedCredential, error) {
	c, ok := g.creds[name]
	var err error
	switch {
	case !ok:
		err = fmt.Errorf("buff/credential: credential %q is not configured", name)
	case !Handed(c.Kind):
		err = fmt.Errorf("buff/credential: credential %q is of kind %s, which is never handed to a plugin", name, c.Kind)
	}
	if g.journal != nil {
		e := HTTPLogEntry{CallID: CallID(ctx), Credential: name, Method: "credential"}
		if err != nil {
			e.Error = err.Error()
		}
		g.journal(e)
	}
	if err != nil {
		return HandedCredential{}, err
	}
	return HandedCredential{Username: c.Username, Password: c.Password}, nil
}

// Do performs one request under the policy.
func (g *HTTPGate) Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	started := time.Now()
	res, err := g.do(ctx, req)
	if g.journal != nil {
		e := HTTPLogEntry{
			CallID: CallID(ctx), Credential: req.Credential, Method: req.Method,
			URL: req.URL, Status: res.Status, Duration: time.Since(started),
		}
		if err != nil {
			e.Error = err.Error()
		}
		g.journal(e)
	}
	return res, err
}

func (g *HTTPGate) do(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	u, err := url.Parse(req.URL)
	if err != nil || u.Host == "" {
		return HTTPResponse{}, fmt.Errorf("buff/http: %q is not a usable URL", req.URL)
	}
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	rule, err := g.match(u, req.Method)
	if err != nil {
		return HTTPResponse{}, err
	}
	if rule.Web != nil {
		// The model chose this address. A credential on it would hand the
		// operator's key to whoever the model was talked into naming.
		if req.Credential != "" {
			return HTTPResponse{}, fmt.Errorf("buff/http: a web request never carries a credential")
		}
		ip, err := g.resolve(u.Hostname())
		if err != nil {
			return HTTPResponse{}, fmt.Errorf("buff/http: %w", err)
		}
		ctx = context.WithValue(ctx, pinKey{}, pin{host: u.Hostname(), ip: ip})
	} else if err := refuseDangerous(u.Hostname()); err != nil {
		return HTTPResponse{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel()

	var body io.Reader
	switch {
	case req.Body != "" && req.BodyBase64 != "":
		return HTTPResponse{}, fmt.Errorf("buff/http: a request carries either body or body_base64, not both")
	case req.BodyBase64 != "":
		raw, err := base64.StdEncoding.DecodeString(req.BodyBase64)
		if err != nil {
			return HTTPResponse{}, fmt.Errorf("buff/http: body_base64: %w", err)
		}
		body = bytes.NewReader(raw)
	case req.Body != "":
		body = strings.NewReader(req.Body)
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("buff/http: %w", err)
	}
	for k, v := range req.Headers {
		// The plugin may not set its own authorisation: that is the host's job,
		// and letting it through would defeat the whole arrangement.
		if strings.EqualFold(k, "authorization") {
			continue
		}
		hr.Header.Set(k, v)
	}
	if req.Credential != "" {
		cred, ok := g.creds[req.Credential]
		if !ok {
			return HTTPResponse{}, fmt.Errorf("buff/http: no credential named %q is configured", req.Credential)
		}
		if err := g.attach(ctx, hr, req.Credential, cred); err != nil {
			return HTTPResponse{}, fmt.Errorf("buff/http: %w", err)
		}
	}

	resp, err := g.client.Do(hr)
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("buff/http: %w", err)
	}
	defer resp.Body.Close()

	// The operator's type list is checked on the headers, before a byte of body
	// is handed over. Only a successful answer is a document; an error page is
	// returned as-is so the plugin can say what went wrong.
	if rule.Web != nil && resp.StatusCode/100 == 2 {
		if err := rule.Web.AllowsType(resp.Header.Get("Content-Type")); err != nil {
			return HTTPResponse{Status: resp.StatusCode}, fmt.Errorf("buff/http: %w", err)
		}
	}

	// Read one byte past the limit so exceeding it is detectable rather than
	// silently satisfied.
	buf, err := io.ReadAll(io.LimitReader(resp.Body, g.MaxBody+1))
	if err != nil {
		return HTTPResponse{Status: resp.StatusCode}, fmt.Errorf("buff/http: reading response: %w", err)
	}
	if int64(len(buf)) > g.MaxBody {
		return HTTPResponse{Status: resp.StatusCode},
			fmt.Errorf("buff/http: response is larger than the %d byte limit", g.MaxBody)
	}
	headers := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}
	if req.Binary {
		return HTTPResponse{Status: resp.StatusCode, Headers: headers,
			BodyBase64: base64.StdEncoding.EncodeToString(buf)}, nil
	}
	return HTTPResponse{Status: resp.StatusCode, Headers: headers, Body: string(buf)}, nil
}

// match finds the rule covering this request, or explains which part failed.
//
// A configured host is served by its own rule and by nothing else: the search
// engine's API keeps its paths and its key, and is not quietly promoted to
// "the web" off those paths. Only a host no rule configured is the web's.
func (g *HTTPGate) match(u *url.URL, method string) (ResolvedEgress, error) {
	var hostMatched bool
	for _, r := range g.rules {
		if r.Web != nil || !strings.EqualFold(r.Host, u.Host) {
			continue
		}
		hostMatched = true
		if len(r.Methods) > 0 && !containsFold(r.Methods, method) {
			continue
		}
		if len(r.PathPrefixes) > 0 && !hasAnyPrefix(u.Path, r.PathPrefixes) {
			continue
		}
		return r, nil
	}
	if !hostMatched {
		if web, ok := g.webRule(); ok {
			if err := web.Web.Allows(u); err != nil {
				return ResolvedEgress{}, fmt.Errorf("buff/http: %w", err)
			}
			if len(web.Methods) > 0 && !containsFold(web.Methods, method) {
				return ResolvedEgress{}, fmt.Errorf("buff/http: %s is not permitted on the web", method)
			}
			return web, nil
		}
		return ResolvedEgress{}, fmt.Errorf("buff/http: %s is not allowed for this instance", u.Host)
	}
	for _, r := range g.rules {
		if strings.EqualFold(r.Host, u.Host) && len(r.Methods) > 0 && !containsFold(r.Methods, method) {
			return ResolvedEgress{}, fmt.Errorf("buff/http: %s is not permitted on %s", method, u.Host)
		}
	}
	return ResolvedEgress{}, fmt.Errorf("buff/http: path %s is outside what this instance may call", u.Path)
}

func (g *HTTPGate) webRule() (ResolvedEgress, bool) {
	for _, r := range g.rules {
		if r.Web != nil {
			return r, true
		}
	}
	return ResolvedEgress{}, false
}

// urlAllowed is the plugin's way to apply the operator's lists BEFORE fetching
// — to a search result, say — with the one implementation of those lists. No
// lookup happens here; a name is checked when it is actually dialled.
func (g *HTTPGate) urlAllowed(raw string) map[string]any {
	web, ok := g.webRule()
	if !ok {
		return map[string]any{"allowed": false, "reason": "this plugin has no web access"}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return map[string]any{"allowed": false, "reason": "not a URL"}
	}
	if err := web.Web.Allows(u); err != nil {
		return map[string]any{"allowed": false, "reason": err.Error()}
	}
	return map[string]any{"allowed": true, "reason": ""}
}

// refuseDangerous blocks the addresses that are never a legitimate destination
// for an integration: link-local (which is where every cloud keeps its metadata
// and credential service) and the unspecified address.
//
// Private ranges are deliberately allowed. The connector's whole purpose is to
// reach systems inside the customer's network, and most self-hosted trackers
// live on one — a blanket private-range refusal would refuse the product itself.
// The host allowlist is what keeps the plugin to the system it was configured
// for; this check only removes the destinations nobody configures on purpose.
func refuseDangerous(host string) error {
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("buff/http: cannot resolve %s: %w", host, err)
	}
	for _, ip := range ips {
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("buff/http: %s resolves to %s, which is never a valid integration target", host, ip)
		}
	}
	return nil
}

func (g *HTTPGate) attach(ctx context.Context, r *http.Request, name string, c Credential) error {
	switch c.Kind {
	case KindServiceAccount, KindOAuth2:
		tok, err := g.tokens.bearer(ctx, name, c)
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", "Bearer "+tok)
	case "basic":
		r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString(
			[]byte(c.Username+":"+c.Password)))
	case "header":
		// A key the tracker accepts only in a header of its own. The name comes
		// from the manifest, so the operator supplies a key and nothing else.
		name := c.Header
		if name == "" {
			name = "Authorization"
		}
		r.Header.Set(name, c.Token)
	default:
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return nil
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
