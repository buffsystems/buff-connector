// Package config loads and validates the connector agent's configuration.
//
// The config is a YAML file for the durable, non-secret settings (middleware
// URL, state directory, and per-provider repo allowlists). Secrets — the git
// token for each provider — are NEVER stored in the YAML; each provider names
// either an environment variable (token_env) or a file (token_file) that the
// agent reads at load time. This keeps tokens out of the image and out of git.
package config

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/buffsystems/buff-connector/internal/problem"
	yaml "gopkg.in/yaml.v3"
)

// DefaultRelay is the platform's public middleware endpoint — baked into the
// build so the operator never has to copy it. It is the SAME address the browser
// SPA uses (wss://<host>/nats). Overridable per-build via
//
//	-ldflags "-X github.com/buffsystems/buff-connector/internal/config.DefaultRelay=wss://…/nats"
//
// and per-install via the config's middleware_url (for self-hosted deployments).
var DefaultRelay = "wss://buff.systems/nats"

// retiredRelay is the default of the builds that ran on the ide.buff.systems zone.
// The setup page and the wizard saved it into the file, so it is read as "the
// default" and follows DefaultRelay (autonomous-dev/cloud-front#411).
const retiredRelay = "wss://ide.buff.systems/nats"

// knownKinds is the set of git providers the agent can drive. The provider is
// declared explicitly (never guessed from the host) because self-hosted
// instances live on arbitrary hostnames.
var knownKinds = map[string]bool{
	"github":           true, // github.com (SaaS)
	"ghe":              true, // GitHub Enterprise Server
	"gitlab":           true, // gitlab.com or self-hosted GitLab
	"gitea":            true, // Gitea / Forgejo
	"bitbucket":        true, // bitbucket.org (SaaS)
	"bitbucket-server": true, // Bitbucket Server / Data Center
}

// Provider is one git server the agent serves repos from/to.
type Provider struct {
	Kind      string   `yaml:"kind"`
	BaseURL   string   `yaml:"base_url"`
	Repos     []string `yaml:"repos"`
	TokenEnv  string   `yaml:"token_env"`
	TokenFile string   `yaml:"token_file"`
	// Token is resolved at load time from TokenEnv/TokenFile. It is never read
	// from YAML and must never be logged.
	Token string `yaml:"-"`
}

// DeveloperGateway: the address developers' Buff Code reaches, and its TLS. Tokens
// travel through it, so without a certificate the connector says so on every start.
// OwnModel is where the company's own model server listens and the key it wants.
type OwnModel struct {
	BaseURL string `yaml:"base_url,omitempty"`
	APIKey  string `yaml:"api_key,omitempty"`
}

type DeveloperGateway struct {
	// Listen is the address to bind, e.g. "0.0.0.0:8767". Empty: no gateway.
	Listen string `yaml:"listen,omitempty"`
	// Host is how developers REACH it — "https://connector.corp.local:8767".
	Host string `yaml:"host,omitempty"`
	// TLSCert and TLSKey are PEM files; both set means the gateway speaks HTTPS.
	TLSCert string `yaml:"tls_cert,omitempty"`
	TLSKey  string `yaml:"tls_key,omitempty"`
}

// Config is the agent's full configuration.
type Config struct {
	MiddlewareURL string     `yaml:"middleware_url"`
	StateDir      string     `yaml:"state_dir"`
	Providers     []Provider `yaml:"providers"`
	// Plugins lists the plugin INSTANCES this connector runs — one per connected
	// customer system. See plugins.go for why an instance is keyed by a minted id
	// rather than by its name.
	Plugins []PluginInstance `yaml:"plugins,omitempty"`

	// Webhooks is where THIS CONNECTOR accepts callbacks. One listener serves
	// every connection: the connector is the listener, so the address is a
	// property of the machine, not of an integration. Asking for it per
	// connection made an operator repeat one answer and invited a second
	// listener for no reason.
	Webhooks Webhooks `yaml:"webhooks,omitempty"`

	// DeveloperGateway is where developers' Buff Code signs in through this
	// connector (#366). The connector lives in the company's network, not on the
	// developer's machine, so this is the door the developer's machine knocks on.
	DeveloperGateway DeveloperGateway `yaml:"developer_gateway,omitempty"`

	// OwnModel is the company's own OpenAI-compatible model server (#517, Bring Your Own
	// AI): the platform reaches it only through this connector. Empty: none.
	OwnModel OwnModel `yaml:"own_model,omitempty"`

	// Sections this build does not know land here instead of failing the load,
	// and are written back untouched on save.
	//
	// An agent already in the field must survive a config written by a later
	// one. That is the lesson of the jira section itself: the day it was added,
	// every agent already installed would have refused to start on a config
	// that mentioned it. Those binaries cannot be fixed after the fact — this
	// stops the same thing happening when the next integration lands.
	//
	// Strictness is kept exactly where mistakes actually happen: an unknown key
	// INSIDE a section this build understands is still a hard error, because
	// that is a typo, not the future.
	Unknown map[string]any `yaml:",inline"`
}

// UnknownSections names the top-level sections this build could not read,
// sorted, so the agent can tell the operator which part of their config it
// ignored and why.
func (c *Config) UnknownSections() []string {
	if len(c.Unknown) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Unknown))
	for name := range c.Unknown {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Load reads, parses, validates, and resolves secrets for a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("чтение конфига: %w", err)
	}
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("разбор конфига: %w", err)
	}
	// The relay is baked into the build — the operator only sets it to override a
	// self-hosted deployment. Absent → use the built-in default.
	if cfg.MiddlewareURL == "" || cfg.MiddlewareURL == retiredRelay {
		cfg.MiddlewareURL = DefaultRelay
	}
	// Hand-written instances may omit the id and slug: minting them here keeps
	// editing the YAML directly a first-class path.
	cfg.CompleteInstances()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.resolveTokens(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// CompleteInstances fills in what the operator is not expected to invent by
// hand. It is idempotent, so saving a completed config changes nothing.
func (c *Config) CompleteInstances() {
	for i := range c.Plugins {
		p := &c.Plugins[i]
		if p.ID == "" {
			p.ID = NewInstanceID()
		}
		if p.Slug == "" {
			name := p.Alias
			if name == "" {
				name = p.Plugin
			}
			if name == "" && p.MCP != nil {
				name = p.MCP.Name
			}
			p.Slug = SlugFor(name)
		}
	}
}

// LoadLenient reads a config WITHOUT validation or token resolution — for the
// setup UI, which edits a possibly-incomplete config. A missing file yields an
// empty Config (not an error), so first-time setup starts from a blank slate.
func LoadLenient(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("чтение конфига: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("разбор конфига: %w", err)
	}
	return &cfg, nil
}

// Validate reports the first thing that would stop the agent from running with
// this config. The setup UI calls it before writing, so a config the agent would
// refuse to load is rejected at the point of saving, with the reason, instead of
// being stored and silently ignored.
func (c *Config) Validate() error {
	if c.MiddlewareURL == "" {
		return fmt.Errorf("нужен middleware_url")
	}
	u, err := url.Parse(c.MiddlewareURL)
	if err != nil || u.Scheme == "" {
		return fmt.Errorf("middleware_url — не адрес: %q", c.MiddlewareURL)
	}
	switch u.Scheme {
	case "ws", "wss", "nats":
	default:
		return fmt.Errorf("схема middleware_url — ws, wss или nats, а не %q", u.Scheme)
	}
	if c.StateDir == "" {
		return fmt.Errorf("нужен state_dir")
	}
	// A connector with nothing configured is legitimate, not an error. It is the
	// state every connector starts in: an operator pairs first and configures
	// afterwards, and one that only ever runs plugins never gets a git provider
	// at all. Refusing to start here made pairing impossible before there was
	// anything to pair FOR. The agent says so at startup instead.
	for i := range c.Providers {
		if err := c.Providers[i].validate(); err != nil {
			return fmt.Errorf("провайдер[%d]: %w", i, err)
		}
	}
	for i := range c.Plugins {
		if err := c.Plugins[i].validate(); err != nil {
			return fmt.Errorf("плагин[%d]: %w", i, err)
		}
	}
	return nil
}

func (p *Provider) validate() error {
	if !knownKinds[p.Kind] {
		return fmt.Errorf("неизвестный вид провайдера %q", p.Kind)
	}
	if p.BaseURL == "" {
		return fmt.Errorf("нужен base_url")
	}
	if u, err := url.Parse(p.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("base_url — не адрес: %q", p.BaseURL)
	}
	if len(p.Repos) == 0 {
		return fmt.Errorf("в списке нужен хотя бы один репозиторий")
	}
	return exactlyOneSecretRef("token", p.TokenEnv, p.TokenFile)
}

func (c *Config) resolveTokens() error {
	for i := range c.Providers {
		p := &c.Providers[i]
		token, err := resolveSecret("token", p.TokenEnv, p.TokenFile)
		if err != nil {
			return fmt.Errorf("провайдер[%d]: %w", i, err)
		}
		p.Token = token
	}
	for i := range c.Plugins {
		if err := c.Plugins[i].ResolveSecrets(); err != nil {
			return fmt.Errorf("плагин[%d]: %w", i, err)
		}
	}
	return nil
}

// InstanceProblems reports connections that cannot run, keyed by instance id.
//
// A collision here is real — two instances sharing a slug would give the model
// two different tools under one name — but it is not a reason to take the whole
// connector down. It used to be: a duplicate slug failed the config load, which
// stopped the git bridge as well, for a fault in something git knows nothing
// about. The offending instance is refused instead, and the rest carry on.
func (c *Config) InstanceProblems() map[string]error {
	problems := map[string]error{}
	seenID := map[string]int{}
	seenSlug := map[string]int{}
	for i := range c.Plugins {
		p := &c.Plugins[i]
		if first, dup := seenID[p.ID]; dup {
			problems[p.ID] = problem.New("instance_id_taken", p.ID, first+1)
			continue
		}
		if first, dup := seenSlug[p.Slug]; dup {
			problems[p.ID] = problem.New("instance_slug_taken", p.Slug, c.Plugins[first].Alias)
			continue
		}
		seenID[p.ID], seenSlug[p.Slug] = i, i
	}
	return problems
}
