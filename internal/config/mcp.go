package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// A custom MCP server is code the OPERATOR brought (#351): a standard MCP server
// from anywhere — an archive dropped on the local page, a GitHub repository, an
// address of a server already running somewhere. It is not a catalogue plugin:
// nobody signed it, the connector cannot guard its network, and its word on
// which tools only read is a hint, not a fact.
//
// Everything that follows from that is recorded HERE, in the config, so that a
// connection written by hand cannot be more trusting than one made in the UI:
// the consent is a field, the read-only marks are a list, and the command that
// runs is exactly what the file says.

// Sources a custom server can come from.
const (
	MCPFromArchive = "archive" // an archive the operator dropped on the local page
	MCPFromGitHub  = "github"  // a repository fetched by URL
	MCPFromURL     = "url"     // a remote server, spoken to over HTTP
	MCPFromCommand = "command" // a command the operator typed
)

// The credential kinds a custom server takes — a foreign process knows nothing
// of buff/http, so a secret can only reach it through its environment:
//
//   - env    — exported as an environment variable named by the credential.
//   - secret — reachable only as ${user_config.<name>} in the command, its
//     arguments or its env block, the way an MCP Bundle wires its own fields.
//
// Both are also ${user_config.<name>} to the expansion; they differ in whether
// the process gets the value under its own name unasked.
const (
	CredentialEnv    = "env"
	CredentialSecret = "secret"
)

var mcpSources = map[string]bool{MCPFromArchive: true, MCPFromGitHub: true, MCPFromURL: true, MCPFromCommand: true}

// CustomMCP describes one server the operator brought. Exactly one of Command
// (a process the connector starts and speaks to over stdio) and URL (a remote
// server) is set.
type CustomMCP struct {
	Source string `yaml:"source"`
	// Origin is where the code came from, for the operator: the archive's file
	// name, the repository URL. Display only.
	Origin string `yaml:"origin,omitempty"`
	// Name and Version are what the bundle or repository called itself. The
	// running server's own serverInfo wins once it is up.
	Name    string `yaml:"name,omitempty"`
	Version string `yaml:"version,omitempty"`

	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	URL     string            `yaml:"url,omitempty"`

	// ReadOnly names the tools the OPERATOR marked as only reading. Only these
	// reach a reading stage. Read live — see Fingerprint.
	ReadOnly []string `yaml:"read_only,omitempty"`

	// Accepted records that the operator accepted, for this server, that the
	// code was not verified by Buff and that its network use is not guarded.
	// Required: a server without it does not run.
	Accepted bool `yaml:"accepted"`
}

// MCPCredentialKinds are the kinds a custom server's credentials may be.
var MCPCredentialKinds = map[string]bool{CredentialEnv: true, CredentialSecret: true, "header": true}

// secretsDigest folds the VALUES of a custom server's credentials into an
// identity that is not the values: they reach the process at start, so a
// rotated token needs a restart — which the ordinary fingerprint, built to
// keep secrets out of logs, would never trigger. A hash restarts without
// telling.
func secretsDigest(creds []InstanceCredential) string {
	h := sha256.New()
	for _, c := range creds {
		h.Write([]byte(c.Name))
		h.Write([]byte{0})
		h.Write([]byte(c.Token))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// IsRemote reports whether the server is spoken to over HTTP rather than run.
func (m *CustomMCP) IsRemote() bool { return m.URL != "" }

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsEnvName reports whether s can name an environment variable — and so a
// custom server's secret, which is also a file named after it.
func IsEnvName(s string) bool { return envNameRe.MatchString(s) }

func (m *CustomMCP) validate() error {
	if !mcpSources[m.Source] {
		return fmt.Errorf("mcp.source %q is unknown; it is one of archive, github, url, command", m.Source)
	}
	if m.Command == "" && m.URL == "" {
		return fmt.Errorf("mcp needs a command to run or a url to reach")
	}
	if m.Command != "" && m.URL != "" {
		return fmt.Errorf("mcp is either a command or a url, not both")
	}
	if m.URL != "" {
		u, err := url.Parse(m.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("mcp.url %q must be an http(s) address", m.URL)
		}
	}
	for name := range m.Env {
		if !envNameRe.MatchString(name) {
			return fmt.Errorf("mcp.env name %q is not an environment variable name", name)
		}
	}
	if !m.Accepted {
		return fmt.Errorf("mcp.accepted must be true: this server was not verified by Buff and its " +
			"network use is not guarded — the operator accepts that explicitly, per server")
	}
	return nil
}

// fingerprint is the part of the identity that would make the process need a
// restart: what runs and with what. The read-only marks are outside on purpose.
func (m *CustomMCP) fingerprint() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mcp|%s|%s|%s|", m.Command, strings.Join(m.Args, "\x00"), m.URL)
	keys := make([]string, 0, len(m.Env))
	for k := range m.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s;", k, m.Env[k])
	}
	return b.String()
}
