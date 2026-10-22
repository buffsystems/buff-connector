package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

func customServer() PluginInstance {
	return PluginInstance{
		ID: "7QK3M9XBWZ0FVT2H", Slug: "weather", Alias: "Погода",
		MCP: &CustomMCP{
			Source: "archive", Origin: "weather-mcp.mcpb", Name: "weather", Version: "0.3.0",
			Command: "node", Args: []string{"server/index.js"},
			Env:      map[string]string{"WEATHER_UNITS": "metric"},
			Accepted: true,
		},
	}
}

// A server the operator brought is not a catalogue plugin: it names no plugin
// and no version to pin, it names a command (or an address) instead — and it
// runs only once the operator has accepted, in the config itself, that nobody
// verified the code. The consent is a field so that a config written by hand
// cannot skip it.
func TestACustomServerNeedsNoPluginButNeedsConsent(t *testing.T) {
	ok := customServer()
	if err := ok.validate(); err != nil {
		t.Fatalf("a complete custom server must validate: %v", err)
	}

	cases := []struct {
		name string
		edit func(p *PluginInstance)
		want string
	}{
		{"consent missing", func(p *PluginInstance) { p.MCP.Accepted = false }, "accepted"},
		{"nothing to run", func(p *PluginInstance) { p.MCP.Command, p.MCP.URL = "", "" }, "command"},
		{"both stdio and remote", func(p *PluginInstance) { p.MCP.URL = "https://mcp.example.com/mcp" }, "either"},
		{"remote must be http", func(p *PluginInstance) { p.MCP.Command, p.MCP.URL = "", "ftp://x" }, "http"},
		{"env name is not a variable", func(p *PluginInstance) { p.MCP.Env = map[string]string{"bad name": "x"} }, "bad name"},
		{"unknown source", func(p *PluginInstance) { p.MCP.Source = "floppy" }, "source"},
		{"a plugin and a server at once", func(p *PluginInstance) { p.Plugin, p.Version = "jira", "1.0.0" }, "either"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := customServer()
			c.edit(&p)
			err := p.validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}

	remote := customServer()
	remote.MCP.Source, remote.MCP.Command, remote.MCP.Args = "url", "", nil
	remote.MCP.URL = "https://mcp.example.com/mcp"
	if err := remote.validate(); err != nil {
		t.Fatalf("a remote server must validate: %v", err)
	}
}

// The process restarts when what it RUNS changes, and only then. The read-only
// marks are the operator's decision about the tools, read live — ticking a box
// must not bounce the server.
func TestCustomServerFingerprintCoversTheCommandNotTheMarks(t *testing.T) {
	base := customServer()
	f := base.Fingerprint()

	for name, edit := range map[string]func(p *PluginInstance){
		"command": func(p *PluginInstance) { p.MCP.Command = "python3" },
		"args":    func(p *PluginInstance) { p.MCP.Args = []string{"other.js"} },
		"env":     func(p *PluginInstance) { p.MCP.Env = map[string]string{"WEATHER_UNITS": "imperial"} },
		"url":     func(p *PluginInstance) { p.MCP.URL = "https://b" },
	} {
		p := customServer()
		edit(&p)
		if p.Fingerprint() == f {
			t.Errorf("changing the %s must restart the server", name)
		}
	}

	marked := customServer()
	marked.MCP.ReadOnly = []string{"forecast"}
	if marked.Fingerprint() != f {
		t.Error("marking a tool read-only restarted the server")
	}
	reordered := customServer()
	reordered.MCP.Env = map[string]string{"WEATHER_UNITS": "metric"}
	if reordered.Fingerprint() != f {
		t.Error("fingerprint depends on env map order")
	}
}

// A custom server has no plugin name to fall back on for its slug; it has its
// own name, and that is what the model sees when the operator gave no alias.
func TestACustomServerIsNamedAfterItselfWhenTheOperatorGaveNoAlias(t *testing.T) {
	cfg := Config{Plugins: []PluginInstance{{MCP: &CustomMCP{Source: "command", Command: "node", Name: "weather-mcp", Accepted: true}}}}
	cfg.CompleteInstances()
	if got := cfg.Plugins[0].Slug; got != "weather_mcp" {
		t.Fatalf("slug = %q", got)
	}
	if cfg.Plugins[0].ID == "" {
		t.Fatal("id not minted")
	}
}

// The config on disk is the record: a custom server round-trips through YAML
// with everything the connector needs to run it again, and nothing secret.
func TestCustomServerRoundTripsThroughYAML(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "k")
	if err := os.WriteFile(keyFile, []byte("SEKRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{MiddlewareURL: "wss://relay.example.com/nats", StateDir: dir,
		Plugins: []PluginInstance{customServer()}}
	cfg.Plugins[0].MCP.ReadOnly = []string{"forecast"}
	cfg.Plugins[0].Credentials = []InstanceCredential{{Name: "WEATHER_API_KEY", Kind: "env", TokenFile: keyFile, Token: "SEKRET"}}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "SEKRET") {
		t.Fatal("a secret reached the config file")
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("load back: %v\n%s", err, out)
	}
	if back.Plugins[0].Credentials[0].Token != "SEKRET" {
		t.Fatal("the env secret was not resolved from its file")
	}
	m := back.Plugins[0].MCP
	if m == nil || m.Command != "node" || m.Args[0] != "server/index.js" || m.ReadOnly[0] != "forecast" ||
		!m.Accepted || m.Env["WEATHER_UNITS"] != "metric" || m.Origin != "weather-mcp.mcpb" {
		t.Fatalf("round trip lost something: %+v\n%s", m, out)
	}
}

// A custom server takes its secrets at start, so a rotated value has to
// restart it — without the value itself reaching the fingerprint.
func TestARotatedSecretRestartsACustomServerWithoutLeaking(t *testing.T) {
	p := customServer()
	p.Credentials = []InstanceCredential{{Name: "API_KEY", Kind: CredentialEnv, TokenFile: "/s/k", Token: "OLD-SEKRET"}}
	f := p.Fingerprint()
	if strings.Contains(f, "OLD-SEKRET") {
		t.Fatal("the secret reached the fingerprint")
	}
	p.Credentials[0].Token = "NEW-SEKRET"
	if p.Fingerprint() == f {
		t.Fatal("a rotated secret must restart the server that took the old one at start")
	}
}

// A custom server's credential names a variable and a file; anything else is
// refused, and so is a kind meant for the host gate.
func TestCustomServerCredentialsAreIdentifiersOfKnownKinds(t *testing.T) {
	for _, bad := range []InstanceCredential{
		{Name: "../../etc/passwd", Kind: CredentialEnv},
		{Name: "api key", Kind: CredentialSecret},
		{Name: "API_KEY", Kind: "bearer"},
	} {
		p := customServer()
		p.Credentials = []InstanceCredential{bad}
		if err := p.validate(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
