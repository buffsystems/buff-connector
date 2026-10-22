package config

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// A plugin instance is one connection to one customer system. The distinction
// that shapes this file: a PLUGIN is code, an INSTANCE is a connection. Two Jira
// servers are two instances of one plugin — same code, different credentials,
// different allowed hosts, different webhook URL, different uid.
//
// Which is why the identifier is a GUID and not a name. The alias is for the
// operator and changes freely; the GUID is what the webhook URL, the state
// directory and the spend attribution are keyed on, so a rename never breaks a
// URL pasted into Jira and never splits a customer's billing history in two.
//
// The split between OUR fields and the plugin's is just as deliberate.
// dev_project, alias, slug and enabled are platform concerns — where work lands,
// what the operator calls it — and the core always renders them. The manifest's
// schema describes the CONNECTION only. Without that line the first plugin
// invents its own name for "the project tasks go to", and every plugin after it
// invents a different one.

// PluginInstance is one configured connection.
type PluginInstance struct {
	ID      string `yaml:"id"`
	Plugin  string `yaml:"plugin,omitempty"`
	Version string `yaml:"version,omitempty"`
	Alias   string `yaml:"alias,omitempty"`
	Slug    string `yaml:"slug,omitempty"`

	// DevProject is where work from this instance lands: the project's name
	// INSIDE the organisation, not "org/project". The platform prefixes the
	// organisation itself, so a slash here produces "org/org%2Fproject" — a
	// repository that does not exist, discovered as an error comment in the
	// customer's own ticket, which is the worst place to find out.
	DevProject string `yaml:"dev_project,omitempty"`
	Enabled    *bool  `yaml:"enabled,omitempty"`

	// Config is validated against the plugin manifest's schema, not here: this
	// package has no business knowing what a Jira base URL looks like.
	Config map[string]any `yaml:"config,omitempty"`

	// Credentials name the environment variable or file holding each secret the
	// manifest asks for. Values are never written to YAML.
	Credentials []InstanceCredential `yaml:"credentials,omitempty"`

	// Webhook says this connection accepts callbacks. WHERE the connector
	// listens is not asked here — that is one address for the whole machine and
	// lives in the config's webhooks section. What is per-connection is the
	// secret, because the URL is what a tracker is given and each connection
	// must get its own.
	Webhook       *bool  `yaml:"webhook,omitempty"`
	WebhookSecret string `yaml:"webhook_secret,omitempty"`

	// Askers is who may ask the bot from a comment in the customer's system,
	// one rule per entry (see askers.go); AskersMembers adds every member of
	// the organisation. Both empty: anyone, as before (cloud-front#476).
	Askers        []string `yaml:"askers,omitempty"`
	AskersMembers bool     `yaml:"askers_members,omitempty"`

	// PollSeconds is how often THIS connection is read. Zero means "what the
	// plugin suggests, else the connector's default": a source with no callback
	// of its own is worth reading every few seconds, an archive is not, and the
	// operator is the one who knows which is which. Read live by the loop, so it
	// is deliberately outside Fingerprint — changing it must not restart the
	// plugin, which does not care how often it is asked.
	PollSeconds int `yaml:"poll_seconds,omitempty"`

	// AINotes is what the operator tells the MODEL about this connection —
	// which project is which, what the team calls things, what to look at
	// first. It reaches the model beside the connection's tools, not the plugin
	// process, so it lives outside Fingerprint: editing it restarts nothing.
	//
	// Bounded by default (AINotesLimit, counted in characters): a long text in
	// the prompt costs the model accuracy, and the operator is warned before
	// lifting the bound. Lifted means lifted — nothing is trimmed after that.
	AINotes          string `yaml:"ai_notes,omitempty"`
	AINotesUnbounded bool   `yaml:"ai_notes_unbounded,omitempty"`

	// MCP, when set, makes this connection a server the operator brought rather
	// than an instance of a catalogue plugin: Plugin and Version are then empty,
	// and what runs is described in the block (#351).
	MCP *CustomMCP `yaml:"mcp,omitempty"`
}

// IsCustom reports whether this connection is a server the operator brought.
func (p *PluginInstance) IsCustom() bool { return p.MCP != nil }

// AINotesLimit is how long the notes for the model may be until the operator
// lifts the bound, in characters — a Russian note gets the same room as an
// English one.
const AINotesLimit = 2048

// Webhooks is where the connector accepts callbacks. One listener, every
// connection: the connector IS the listener.
type Webhooks struct {
	// Listen is the address to bind, e.g. "0.0.0.0:8766". Empty means the
	// connector accepts no callbacks at all and every connection polls.
	Listen string `yaml:"listen,omitempty"`
	// Host is how a tracker REACHES that listener — "connector.corp.local:8766".
	// It differs from Listen whenever the connector is behind NAT, in a
	// container, or bound to 0.0.0.0, which cannot be dialled. Empty falls back
	// to Listen, which is right on a machine that is directly reachable.
	Host string `yaml:"host,omitempty"`
}

// Enabled reports whether the connector should open a listener at all.
func (w Webhooks) Enabled() bool { return w.Listen != "" }

// Address is where a tracker should send a callback.
func (w Webhooks) Address() string {
	if w.Host != "" {
		return w.Host
	}
	return w.Listen
}

// WebhookEnabled reports whether this connection accepts callbacks. The secret
// is required: a route without one would take anyone's callback.
func (p *PluginInstance) WebhookEnabled() bool {
	return p.Webhook != nil && *p.Webhook && p.WebhookSecret != ""
}

// WebhookPath is keyed on the GUID, like everything else that outlives a rename.
// The URL is pasted into the customer's tracker; renaming the connection in our
// UI must not break it.
func (p *PluginInstance) WebhookPath() string { return "/plugin/" + p.ID }

// WebhookURL is the whole address to paste into the tracker — issued by the
// connector, never typed by anyone. The secret is part of the path because a
// legacy tracker cannot sign its callbacks at all, so the URL itself is the
// credential.
func (c *Config) WebhookURL(p *PluginInstance) string {
	if !c.Webhooks.Enabled() || !p.WebhookEnabled() {
		return ""
	}
	return "http://" + c.Webhooks.Address() + p.WebhookPath() + "/" + p.WebhookSecret
}

// InstanceCredential is one secret by reference. The plugin never sees the
// value; the host attaches it to outgoing requests.
type InstanceCredential struct {
	// Header is the header a `header` credential is sent in, copied from the
	// plugin's manifest when the connection is saved.
	Header string `yaml:"header,omitempty"`

	Name         string `yaml:"name"`
	Kind         string `yaml:"kind,omitempty"`
	Username     string `yaml:"username,omitempty"`
	TokenEnv     string `yaml:"token_env,omitempty"`
	TokenFile    string `yaml:"token_file,omitempty"`
	PasswordEnv  string `yaml:"password_env,omitempty"`
	PasswordFile string `yaml:"password_file,omitempty"`

	Token    string `yaml:"-"`
	Password string `yaml:"-"`
}

// IsEnabled defaults to true: an instance written down is an instance meant to
// run.
func (p *PluginInstance) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

var (
	idRe   = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{16}$`)
	slugRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)
)

// crockford is Crockford base32: no I, L, O or U, so an operator reading an id
// aloud or copying it out of a log cannot produce a different one.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewInstanceID mints an identifier. Sixteen symbols is 80 bits — far past
// collision range for a handful of instances, and still short enough to appear
// in a URL and a directory name.
func NewInstanceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("connector: cannot generate an instance id: " + err.Error())
	}
	out := make([]byte, 16)
	for i, v := range b {
		out[i] = crockford[int(v)%len(crockford)]
	}
	return string(out)
}

// UniqueSlugFor derives a slug that does not collide with the ones already in
// use. An operator naming two connections "Jira прод" and "Jira тест" has no
// reason to know that both reduce to the same identifier — this is ours to
// solve, not theirs to discover from an error.
func UniqueSlugFor(name string, taken map[string]bool) string {
	base := SlugFor(name)
	if !taken[base] {
		return base
	}
	for n := 2; n < 100; n++ {
		candidate := fmt.Sprintf("%s_%d", base, n)
		if !taken[candidate] {
			return candidate
		}
	}
	return base + "_" + NewInstanceID()[:4]
}

// SlugOnSave is the slug a connection gets when it is saved: an existing one
// that kept its name keeps its slug — the model knows its tools by it — and a
// new or renamed one gets a fresh, unique one.
func SlugOnSave(prev *PluginInstance, name string, taken map[string]bool) string {
	if prev != nil && prev.Slug != "" && prev.Alias == name && !taken[prev.Slug] {
		return prev.Slug
	}
	return UniqueSlugFor(name, taken)
}

// SlugFor derives the name the model will see for this instance's tools.
// opencode namespaces tools by server name, so the slug becomes part of every
// tool name — it has to be a plain identifier, and a bad one is refused rather
// than silently mangled.
func SlugFor(alias string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(alias) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '.':
			b.WriteByte('_')
		default:
			// Most aliases here are Russian: «Сетевая папка» must read as
			// setevaya_papka, not reduce to nothing (cloud-front#475).
			b.WriteString(cyrillicLatin[r])
		}
	}
	s := strings.Trim(b.String(), "_")
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		s = "i" + s
	}
	if len(s) > 24 {
		s = strings.Trim(s[:24], "_")
	}
	return s
}

// cyrillicLatin spells a Russian letter in Latin for a slug; a letter that is
// not here is dropped, as any other symbol is.
var cyrillicLatin = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts",
	'ч': "ch", 'ш': "sh", 'щ': "shch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu",
	'я': "ya",
}

// validate checks the platform's half of an instance. The plugin's half is
// checked against its manifest, which this package cannot see.
func (p *PluginInstance) validate() error {
	if !idRe.MatchString(p.ID) {
		return fmt.Errorf("id %q must be 16 Crockford base32 symbols (the connector mints one if you omit it)", p.ID)
	}
	switch {
	case p.MCP != nil && (p.Plugin != "" || p.Version != ""):
		return fmt.Errorf("a connection is either a catalogue plugin or a custom mcp server, not both")
	case p.MCP != nil:
		if err := p.MCP.validate(); err != nil {
			return err
		}
	case p.Plugin == "":
		return fmt.Errorf("plugin is required")
	case p.Version == "":
		return fmt.Errorf("version is required: an instance is pinned to one version on purpose")
	}
	if !slugRe.MatchString(p.Slug) {
		return fmt.Errorf("slug %q must start with a letter and use only lowercase letters, digits and "+
			"underscores — it becomes part of every tool name the model sees", p.Slug)
	}
	if p.PollSeconds < 0 {
		return fmt.Errorf("poll_seconds %d must be zero (the plugin's suggestion) or a number of seconds", p.PollSeconds)
	}
	if strings.Contains(p.DevProject, "/") {
		return fmt.Errorf("dev_project %q must be the project's name inside your organisation, "+
			"without the organisation and without a slash", p.DevProject)
	}
	if n := utf8.RuneCountInString(p.AINotes); n > AINotesLimit && !p.AINotesUnbounded {
		return fmt.Errorf("ai_notes is %d characters, the limit is %d: a long text in the prompt "+
			"can make the model worse — set ai_notes_unbounded: true to accept that and lift the limit",
			n, AINotesLimit)
	}
	seen := map[string]bool{}
	for _, c := range p.Credentials {
		if c.Name == "" {
			return fmt.Errorf("a credential needs a name")
		}
		if p.MCP != nil {
			if !MCPCredentialKinds[c.Kind] {
				return fmt.Errorf("credential %q: kind %q is not one a custom server takes (env, secret, header)", c.Name, c.Kind)
			}
			if !envNameRe.MatchString(c.Name) {
				return fmt.Errorf("credential %q: the name of a custom server's secret is an identifier, "+
					"used as a variable name and a file name", c.Name)
			}
		}
		if seen[c.Name] {
			return fmt.Errorf("duplicate credential %q", c.Name)
		}
		seen[c.Name] = true
	}
	return nil
}

// ResolveSecrets reads each credential from wherever the operator put it.
func (p *PluginInstance) ResolveSecrets() error {
	for i := range p.Credentials {
		c := &p.Credentials[i]
		tok, err := resolveSecret(c.Name+" token", c.TokenEnv, c.TokenFile)
		if err != nil {
			return err
		}
		pw, err := resolveSecret(c.Name+" password", c.PasswordEnv, c.PasswordFile)
		if err != nil {
			return err
		}
		c.Token, c.Password = tok, pw
	}
	return nil
}

// Fingerprint is the identity of everything that would make this instance need a
// restart. It covers the platform fields, the settings and the SHAPE of the
// credentials — deliberately not their values, which change without the process
// needing to notice, and must never end up in a log line.
//
// Per instance, not per config: editing one connection must restart one process,
// not every plugin the operator runs.
func (p *PluginInstance) Fingerprint() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%t|", p.ID, p.Plugin, p.Version, p.Slug, p.DevProject, p.IsEnabled())
	keys := make([]string, 0, len(p.Config))
	for k := range p.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%v;", k, p.Config[k])
	}
	b.WriteByte('|')
	for _, c := range p.Credentials {
		fmt.Fprintf(&b, "%s:%s:%s:%s:%s;", c.Name, c.Kind, c.Username, c.TokenEnv, c.TokenFile)
	}
	if p.MCP != nil {
		b.WriteByte('|')
		b.WriteString(p.MCP.fingerprint())
		b.WriteByte('|')
		b.WriteString(secretsDigest(p.Credentials))
	}
	return b.String()
}
