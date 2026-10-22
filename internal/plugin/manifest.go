package plugin

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// The manifest is the plugin's contract with the connector and with the operator
// who is about to let it into their network: what it is, what settings it needs,
// where it may reach, and what it offers.
//
// Note what is NOT authoritative here: the tools list. At runtime the connector
// reads tools/list from the running plugin, because that is what will actually
// answer a call. The manifest's copy exists so the catalogue can show the
// operator what they are agreeing to BEFORE anything is installed.

type WebhookMode string

// How a plugin can be reached by a callback.
//
// In BOTH modes the connector owns the listener: the port, the URL, the secret
// and the signature check are ours, and the plugin never sees a socket. What
// differs is what happens after the callback is found genuine.
//
//   - wake    — "read now". The reading is the poller's job, exactly as it is
//     without a callback, so one path produces every event with one watermark
//     and one dedup. What it buys is latency.
//   - deliver — the validated body is handed to the plugin, which turns it into
//     events directly. For sources there is nothing to poll: an internal service
//     that can only push, a tracker with no usable search API, or a change that
//     no longer exists by the time a poll runs (a comment edited and reverted).
//     A wake-only connector cannot integrate any of those at all.
//
// A plugin that can do both should declare deliver and still implement poll:
// callbacks get lost, and the poll is what makes that recoverable.
const (
	WebhookNone    WebhookMode = "none"
	WebhookWake    WebhookMode = "wake"
	WebhookDeliver WebhookMode = "deliver"
)

// Manifest is one plugin version.
type Manifest struct {
	Name      string   `yaml:"name"`
	Version   string   `yaml:"version"`
	HostAPI   int      `yaml:"host_api"`
	Publisher string   `yaml:"publisher"`
	Summary   string   `yaml:"summary"`
	Platforms []string `yaml:"platforms"`
	Entry     string   `yaml:"entry"`
	Python    string   `yaml:"python"`

	Webhook   WebhookMode `yaml:"webhook"`
	Signature *SigSpec    `yaml:"signature"`
	// PollSeconds is how often the plugin SUGGESTS its source be read — a comment
	// feed with no callback of its own wants seconds, a digest does not. A
	// suggestion only: the connection's own setting wins, and zero leaves the
	// connector's default in place.
	PollSeconds int          `yaml:"poll_seconds"`
	Creds       []CredSpec   `yaml:"credentials"`
	Egress      []EgressRule `yaml:"egress"`
	// Network declares connections the PLUGIN opens itself, for a protocol the
	// host does not speak (SMB to a file share). The host cannot guard these
	// the way it guards buff/http; what it can do is show them before install,
	// so the rule names the list setting the addresses come from.
	Network []NetworkRule `yaml:"network"`
	Schema  *Schema       `yaml:"config_schema"`
	Tools   []ToolSpec    `yaml:"tools"`
}

// SigSpec is how the source signs its webhook callbacks — checked by the core,
// which is the only side holding the secret.
type SigSpec struct {
	Header string `yaml:"header" json:"header"`
	Algo   string `yaml:"algo" json:"algo"`

	// SecretFrom names the body field of the source's unsigned handshake that
	// carries the secret it will sign with — Notion's `verification_token`.
	// Set, it means the source mints the secret rather than using the one in
	// our URL: the connector learns it from that first call, verifies Header
	// with it from then on, and shows it to the operator to paste back into
	// the source. Only hmac-sha256 is implemented for this.
	SecretFrom string `yaml:"secret_from" json:"secret_from,omitempty"`
}

// CredSpec is a credential the operator must supply. The plugin never sees its
// value; it names the credential when asking the host to make a request.
type CredSpec struct {
	Name  string   `yaml:"name" json:"name"`
	Kinds []string `yaml:"kinds" json:"kinds"`

	// Header names where a `header` credential is sent. Some trackers accept a
	// key ONLY in a header of their own (Redmine's X-Redmine-API-Key), and the
	// alternative — telling the operator to type their key into a field labelled
	// "username" — is a workaround dressed as a contract.
	Header string `yaml:"header" json:"header,omitempty"`

	// OAuth is required by the minted kinds (service_account, oauth2): where a
	// sign-in starts, where tokens are issued, and which access is asked for.
	// It lives in the manifest — signed with the plugin — so the host that
	// holds the secret is never told where to send it by the secret itself.
	OAuth *OAuthSpec `yaml:"oauth" json:"oauth,omitempty"`
}

// EgressRule says where the plugin may reach — as a RULE, not an address. The
// host is whatever the operator puts in the named config field, so the concrete
// allowlist only exists once the form is filled in.
type EgressRule struct {
	FromConfig   string   `yaml:"from_config" json:"from_config,omitempty"`
	Methods      []string `yaml:"methods" json:"methods"`
	PathPrefixes []string `yaml:"path_prefixes" json:"path_prefixes"`

	// Optional marks a from_config rule the operator may leave unset: the rule
	// is then simply absent. A plugin offering several backends (three search
	// engines, one chosen) cannot demand an address for each of them.
	Optional bool `yaml:"optional" json:"optional,omitempty"`

	// Web is the rule whose destination the MODEL names. Instead of a host the
	// manifest names the settings that hold the operator's lists.
	Web *WebRule `yaml:"web" json:"web,omitempty"`
}

// NetworkRule is one protocol the plugin speaks on its own, to the hosts named
// in a list setting of the connection, on the given ports.
type NetworkRule struct {
	Protocol  string `yaml:"protocol" json:"protocol"`
	HostsFrom string `yaml:"hosts_from" json:"hosts_from"`
	Ports     []int  `yaml:"ports" json:"ports"`
}

// WebRule points at the settings an operator fills in to shape the model's
// reach into the internet: which sites (allow or deny) and which content types.
// Each is a list setting; an empty one means "no restriction of that kind".
// The rule the operator cannot relax — public addresses only — is not a
// setting at all.
type WebRule struct {
	AllowFrom string `yaml:"allow_from" json:"allow_from,omitempty"`
	DenyFrom  string `yaml:"deny_from" json:"deny_from,omitempty"`
	TypesFrom string `yaml:"types_from" json:"types_from,omitempty"`
}

// listFields names the settings this rule reads.
func (w *WebRule) listFields() []string {
	var out []string
	for _, f := range []string{w.AllowFrom, w.DenyFrom, w.TypesFrom} {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ToolSpec is the catalogue's view of a tool.
type ToolSpec struct {
	Name     string `yaml:"name" json:"name"`
	ReadOnly bool   `yaml:"read_only" json:"read_only"`
	// Answers with a picture — offered only to a model that can see.
	Vision  bool   `yaml:"vision" json:"vision,omitempty"`
	Summary string `yaml:"summary" json:"summary"`
}

// ResolvedEgress is an EgressRule with the operator's value filled in: this is
// what the consent screen shows and what the HTTP guard enforces.
type ResolvedEgress struct {
	Host         string
	Methods      []string
	PathPrefixes []string
	// Web is set instead of Host for the rule the model steers.
	Web *WebPolicy
}

// Label is what the consent screen, the report and the app show for this rule.
func (r ResolvedEgress) Label() string {
	if r.Web != nil {
		return r.Web.Label()
	}
	return r.Host
}

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+([-.][0-9A-Za-z.-]+)?$`)
)

// ParseManifest reads and fully validates a manifest. Everything it can catch
// here is something that would otherwise surface as a confusing runtime failure
// inside a customer's network.
func ParseManifest(b []byte) (*Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("manifest: name %q must be lowercase letters, digits and dashes", m.Name)
	}
	if !versionRe.MatchString(m.Version) {
		return fmt.Errorf("manifest: version %q must look like 1.2.3", m.Version)
	}
	if m.HostAPI <= 0 {
		return fmt.Errorf("manifest: host_api is required and must be positive")
	}
	if m.Entry == "" {
		return fmt.Errorf("manifest: entry is required")
	}
	if path.IsAbs(m.Entry) || strings.Contains(m.Entry, "..") || path.Clean(m.Entry) != m.Entry {
		return fmt.Errorf("manifest: entry %q must stay inside the bundle", m.Entry)
	}
	if m.PollSeconds < 0 {
		return fmt.Errorf("manifest: poll_seconds %d must be zero or a number of seconds", m.PollSeconds)
	}
	if m.Webhook == "" {
		m.Webhook = WebhookNone
	}
	switch m.Webhook {
	case WebhookNone, WebhookWake, WebhookDeliver:
	default:
		return fmt.Errorf("manifest: webhook %q must be none, wake or deliver", m.Webhook)
	}
	if m.Signature != nil && m.Signature.SecretFrom != "" {
		if m.Signature.Header == "" {
			return fmt.Errorf("manifest: signature.secret_from needs signature.header — the header the source signs in")
		}
		if m.Signature.Algo != "hmac-sha256" {
			return fmt.Errorf("manifest: signature.secret_from is implemented for hmac-sha256 only, not %q", m.Signature.Algo)
		}
	}
	for _, c := range m.Creds {
		if err := m.checkCred(c); err != nil {
			return err
		}
	}
	if err := m.Schema.check(""); err != nil {
		return err
	}
	for _, rule := range m.Egress {
		if err := m.checkEgress(rule); err != nil {
			return err
		}
	}
	for _, rule := range m.Network {
		if err := m.checkNetwork(rule); err != nil {
			return err
		}
	}
	return nil
}

// checkNetwork keeps a plugin's own connections declared in full: which
// protocol, which setting names the hosts, which ports.
func (m *Manifest) checkNetwork(rule NetworkRule) error {
	if rule.Protocol == "" {
		return fmt.Errorf("manifest: a network rule needs a protocol")
	}
	if len(rule.Ports) == 0 {
		return fmt.Errorf("manifest: network rule %s needs ports", rule.Protocol)
	}
	if rule.HostsFrom == "" || m.Schema == nil || m.Schema.Properties[rule.HostsFrom] == nil ||
		m.Schema.Properties[rule.HostsFrom].Type != "array" {
		return fmt.Errorf("manifest: network rule %s reads %q, which config_schema does not define as a list",
			rule.Protocol, rule.HostsFrom)
	}
	return nil
}

// OAuthFor is the minting spec of a named credential, resolved against the
// connection's settings (defaults filled); nil when it has none.
func (m *Manifest) OAuthFor(name string, cfg map[string]any) *OAuthSpec {
	for _, c := range m.Creds {
		if c.Name == name && c.OAuth != nil {
			spec := c.OAuth.Resolve(m.WithDefaults(cfg))
			return &spec
		}
	}
	return nil
}

// check keeps a credential's declaration whole: every kind is one the host
// knows how to attach, and a minted kind says where its tokens come from.
func (m *Manifest) checkCred(c CredSpec) error {
	if c.Name == "" {
		return fmt.Errorf("manifest: a credential needs a name")
	}
	for _, k := range c.Kinds {
		if !containsFold(KnownKinds, k) {
			return fmt.Errorf("manifest: credential %q: kind %q is not one of %s",
				c.Name, k, strings.Join(KnownKinds, ", "))
		}
		if !Minted(k) {
			continue
		}
		if c.OAuth == nil || len(c.OAuth.Scopes) == 0 {
			return fmt.Errorf("manifest: credential %q: kind %s needs an oauth block with scopes", c.Name, k)
		}
		if err := m.oauthAddress(c, "token_uri", c.OAuth.TokenURI, c.OAuth.TokenURIFrom); err != nil {
			return err
		}
		if k == KindOAuth2 {
			if err := m.oauthAddress(c, "auth_uri", c.OAuth.AuthURI, c.OAuth.AuthURIFrom); err != nil {
				return err
			}
		}
	}
	return nil
}

// oauthAddress accepts an address written down OR named from a string
// setting — exactly one.
func (m *Manifest) oauthAddress(c CredSpec, what, literal, from string) error {
	switch {
	case literal != "" && from != "":
		return fmt.Errorf("manifest: credential %q: oauth.%s is either written down or taken from a setting, not both", c.Name, what)
	case literal == "" && from == "":
		return fmt.Errorf("manifest: credential %q: oauth needs %s (or %s_from naming a setting)", c.Name, what, what)
	case from != "":
		if m.Schema == nil || m.Schema.Properties[from] == nil || m.Schema.Properties[from].Type != "string" {
			return fmt.Errorf("manifest: credential %q: oauth.%s_from points at %q, which config_schema does not define as a string", c.Name, what, from)
		}
	}
	return nil
}

func (m *Manifest) checkEgress(rule EgressRule) error {
	if rule.Web != nil {
		if rule.FromConfig != "" {
			return fmt.Errorf("manifest: an egress rule is either a configured host or the web, not both")
		}
		for _, f := range rule.Web.listFields() {
			if m.Schema == nil || m.Schema.Properties[f] == nil || m.Schema.Properties[f].Type != "array" {
				return fmt.Errorf("manifest: web rule reads %q, which config_schema does not define as a list", f)
			}
		}
		return nil
	}
	if rule.FromConfig == "" {
		return fmt.Errorf("manifest: an egress rule must name the config field its host comes from")
	}
	if m.Schema == nil || m.Schema.Properties[rule.FromConfig] == nil {
		return fmt.Errorf("manifest: egress rule points at %q, which config_schema does not define",
			rule.FromConfig)
	}
	return nil
}

// ValidateConfig checks one instance's settings against the plugin's schema.
// WithDefaults returns the config with every empty scalar setting that declares
// a default filled in. ONE place decides what an untouched field means — the
// validator, the egress rules and the plugin's BUFF_CONFIG all read this — so a
// plugin whose product has a fixed address (api.figma.com) can offer it as a
// default rather than make the operator type it, and the CLI path with no form
// arrives at the same value as the web form.
func (m *Manifest) WithDefaults(cfg map[string]any) map[string]any {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	if m.Schema == nil {
		return out
	}
	for name, sub := range m.Schema.Properties {
		if sub == nil || sub.Default == nil {
			continue
		}
		if v, present := out[name]; !present || v == nil || v == "" {
			out[name] = sub.Default
		}
	}
	return out
}

// ValidateConfig checks the operator's settings, with defaults applied first: a
// required setting the manifest supplies a default for is never "missing".
func (m *Manifest) ValidateConfig(cfg map[string]any) error {
	if m.Schema == nil {
		return nil
	}
	full := m.WithDefaults(cfg)
	if err := m.Schema.Validate("", full); err != nil {
		return err
	}
	// A web rule's lists are settings too: a bad entry is refused here, with the
	// field named, rather than at the instance's next start.
	for _, rule := range m.Egress {
		if rule.Web == nil {
			continue
		}
		if _, err := resolveWeb(rule.Web, full); err != nil {
			return err
		}
	}
	return nil
}

// underPath moves a manifest's path prefixes under the path of the address the
// operator actually configured.
//
// A manifest declares prefixes relative to the PRODUCT — "/rest/" — because it
// cannot know where the product lives. It lives under a path more often than
// not: Confluence Cloud answers at https://site.atlassian.net/wiki, and a
// self-hosted Jira or Confluence behind a context path answers at /jira. Without
// this the very first call is refused as "outside what this instance may call",
// and the operator has no way to tell that from a wrong address.
func underPath(base string, prefixes []string) []string {
	base = strings.TrimRight(base, "/")
	if base == "" || base == "/" {
		return prefixes
	}
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, base+"/"+strings.TrimLeft(p, "/"))
	}
	return out
}

// ResolveEgress turns the rules into the concrete hosts this instance may reach.
// The consent screen shows exactly this, which is the difference between "the
// plugin can make web requests" and "the plugin can reach jira.example.com:8443".
func (m *Manifest) ResolveEgress(cfg map[string]any) ([]ResolvedEgress, error) {
	out := make([]ResolvedEgress, 0, len(m.Egress))
	for _, rule := range m.Egress {
		if rule.Web != nil {
			pol, err := resolveWeb(rule.Web, cfg)
			if err != nil {
				return nil, err
			}
			out = append(out, ResolvedEgress{Web: pol, Methods: rule.Methods})
			continue
		}
		raw, ok := cfg[rule.FromConfig].(string)
		if !ok || raw == "" {
			if rule.Optional {
				continue
			}
			return nil, fmt.Errorf("egress needs %s, which is not set", rule.FromConfig)
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("%s is not a URL the plugin can be limited to: %q", rule.FromConfig, raw)
		}
		out = append(out, ResolvedEgress{
			Host: u.Host, Methods: rule.Methods,
			PathPrefixes: underPath(u.Path, rule.PathPrefixes),
		})
	}
	return out, nil
}

// ResolveNetwork lists the servers the plugin will dial itself, from the
// list settings its network rules name — one label per server and protocol,
// in the order first seen. A local path names no server and is skipped.
func (m *Manifest) ResolveNetwork(cfg map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	for _, rule := range m.Network {
		for _, entry := range stringList(cfg[rule.HostsFrom]) {
			host := ServerOf(entry)
			if host == "" {
				continue
			}
			for _, port := range rule.Ports {
				label := fmt.Sprintf("%s://%s:%d", rule.Protocol, host, port)
				if !seen[label] {
					seen[label] = true
					out = append(out, label)
				}
			}
		}
	}
	return out
}

// ServerOf is the server named by a network path — `\\server\share\dir`,
// `//server/share` or `smb://server/share` — and "" for a local path.
func ServerOf(entry string) string {
	e := strings.ReplaceAll(strings.TrimSpace(entry), "\\", "/")
	if i := strings.Index(e, "://"); i > 0 {
		e = e[i+3:]
	} else if !strings.HasPrefix(e, "//") {
		return ""
	}
	e = strings.TrimLeft(e, "/")
	if i := strings.IndexByte(e, '/'); i >= 0 {
		e = e[:i]
	}
	return e
}

// resolveWeb reads the operator's lists out of the settings. A bad entry is
// named together with its field, so the message can go straight onto the form.
func resolveWeb(rule *WebRule, cfg map[string]any) (*WebPolicy, error) {
	allow, deny, types := stringList(cfg[rule.AllowFrom]), stringList(cfg[rule.DenyFrom]), stringList(cfg[rule.TypesFrom])
	pol, err := NewWebPolicy(allow, deny, types)
	if err != nil {
		field := rule.AllowFrom
		switch {
		case strings.HasPrefix(err.Error(), "deny list"):
			field = rule.DenyFrom
		case strings.HasPrefix(err.Error(), "content types"):
			field = rule.TypesFrom
		}
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return pol, nil
}

func stringList(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
