package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTmp writes content to a temp file and returns its path.
func writeTmp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	return p
}

const validYAML = `
middleware_url: wss://relay.example.com/nats
state_dir: /var/lib/connector
providers:
  - kind: gitlab
    base_url: https://gitlab.corp.local
    token_env: GITLAB_TOKEN
    repos:
      - group/app
      - group/lib
  - kind: bitbucket-server
    base_url: https://bitbucket.corp.local
    token_file: TOKENFILE
    repos:
      - PROJ/service
`

func TestLoadValid(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-secret")
	tokenFile := writeTmp(t, "bb.token", "bbtoken-secret\n")

	yaml := validYAML
	// substitute the token_file placeholder with the real temp path
	yaml = strings.ReplaceAll(yaml, "TOKENFILE", tokenFile)
	p := writeTmp(t, "config.yaml", yaml)

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MiddlewareURL != "wss://relay.example.com/nats" {
		t.Errorf("MiddlewareURL = %q", cfg.MiddlewareURL)
	}
	if cfg.StateDir != "/var/lib/connector" {
		t.Errorf("StateDir = %q", cfg.StateDir)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("providers = %d, want 2", len(cfg.Providers))
	}
	gl := cfg.Providers[0]
	if gl.Kind != "gitlab" || gl.BaseURL != "https://gitlab.corp.local" {
		t.Errorf("provider[0] = %+v", gl)
	}
	if gl.Token != "glpat-secret" {
		t.Errorf("gitlab token not resolved from env: %q", gl.Token)
	}
	if len(gl.Repos) != 2 || gl.Repos[0] != "group/app" {
		t.Errorf("gitlab repos = %v", gl.Repos)
	}
	bb := cfg.Providers[1]
	if bb.Kind != "bitbucket-server" {
		t.Errorf("provider[1].Kind = %q", bb.Kind)
	}
	if bb.Token != "bbtoken-secret" {
		t.Errorf("bitbucket token not resolved/trimmed from file: %q", bb.Token)
	}
}

func TestLoadMissingMiddlewareUsesBakedDefault(t *testing.T) {
	// middleware_url is optional: absent → the built-in DefaultRelay (baked into
	// the build), so the operator never copies it.
	p := writeTmp(t, "c.yaml", "state_dir: /x\nproviders:\n  - kind: gitea\n    base_url: https://g\n    token_env: T\n    repos: [a/b]\n")
	t.Setenv("T", "tok")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MiddlewareURL != DefaultRelay {
		t.Fatalf("MiddlewareURL = %q, want baked default %q", cfg.MiddlewareURL, DefaultRelay)
	}
}

func TestLoadRetiredRelayMovesToBakedDefault(t *testing.T) {
	// The setup page and the wizard write the pre-filled default into the file, so an
	// install from the ide.buff.systems days carries that address. The zone is gone
	// (autonomous-dev/cloud-front#411): the retired default follows the baked one.
	p := writeTmp(t, "c.yaml", "middleware_url: wss://ide.buff.systems/nats\nstate_dir: /x\nproviders:\n  - kind: gitea\n    base_url: https://g\n    token_env: T\n    repos: [a/b]\n")
	t.Setenv("T", "tok")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MiddlewareURL != DefaultRelay {
		t.Fatalf("MiddlewareURL = %q, want baked default %q", cfg.MiddlewareURL, DefaultRelay)
	}
}

func TestLoadSelfHostedRelayKept(t *testing.T) {
	p := writeTmp(t, "c.yaml", "middleware_url: wss://relay.corp.example/nats\nstate_dir: /x\nproviders:\n  - kind: gitea\n    base_url: https://g\n    token_env: T\n    repos: [a/b]\n")
	t.Setenv("T", "tok")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MiddlewareURL != "wss://relay.corp.example/nats" {
		t.Fatalf("MiddlewareURL = %q, want the self-hosted address kept", cfg.MiddlewareURL)
	}
}

func TestBakedDefaultIsTheMainServer(t *testing.T) {
	if DefaultRelay != "wss://buff.systems/nats" {
		t.Fatalf("DefaultRelay = %q, want wss://buff.systems/nats", DefaultRelay)
	}
}

func TestLoadUnknownProviderKind(t *testing.T) {
	p := writeTmp(t, "c.yaml", "middleware_url: wss://x/nats\nstate_dir: /x\nproviders:\n  - kind: svn\n    base_url: https://g\n    token_env: T\n    repos: [a/b]\n")
	t.Setenv("T", "tok")
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for unknown provider kind")
	}
}

func TestLoadProviderNeedsBaseURL(t *testing.T) {
	p := writeTmp(t, "c.yaml", "middleware_url: wss://x/nats\nstate_dir: /x\nproviders:\n  - kind: gitlab\n    token_env: T\n    repos: [a/b]\n")
	t.Setenv("T", "tok")
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for missing base_url")
	}
}

func TestLoadProviderNeedsExactlyOneTokenSource(t *testing.T) {
	// both token_env and token_file set → error
	both := "middleware_url: wss://x/nats\nstate_dir: /x\nproviders:\n  - kind: gitea\n    base_url: https://g\n    token_env: T\n    token_file: /f\n    repos: [a/b]\n"
	if _, err := Load(writeTmp(t, "b.yaml", both)); err == nil {
		t.Fatal("expected error when both token sources set")
	}
	// neither set → error
	neither := "middleware_url: wss://x/nats\nstate_dir: /x\nproviders:\n  - kind: gitea\n    base_url: https://g\n    repos: [a/b]\n"
	if _, err := Load(writeTmp(t, "n.yaml", neither)); err == nil {
		t.Fatal("expected error when no token source set")
	}
}

func TestLoadEnvTokenMissing(t *testing.T) {
	os.Unsetenv("NOPE_TOKEN")
	p := writeTmp(t, "c.yaml", "middleware_url: wss://x/nats\nstate_dir: /x\nproviders:\n  - kind: gitea\n    base_url: https://g\n    token_env: NOPE_TOKEN\n    repos: [a/b]\n")
	if _, err := Load(p); err == nil {
		t.Fatal("expected error when env token var is empty/unset")
	}
}

func TestSaveRoundTripsWithoutInlineToken(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "gl.token")
	if err := os.WriteFile(tokenFile, []byte("glsecret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		MiddlewareURL: "wss://relay/nats",
		StateDir:      dir,
		Providers: []Provider{{
			Kind: "gitlab", BaseURL: "https://gl", Repos: []string{"g/a"},
			TokenFile: tokenFile, Token: "glsecret",
		}},
	}
	p := filepath.Join(dir, "connector.yaml")
	if err := Save(p, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	// The resolved token must NOT be written to the YAML.
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "glsecret") {
		t.Fatalf("resolved token leaked into YAML:\n%s", raw)
	}
	// And it reloads to an equivalent config (token re-resolved from the file).
	got, err := Load(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Providers[0].Token != "glsecret" || got.Providers[0].Repos[0] != "g/a" {
		t.Fatalf("reloaded = %+v", got.Providers[0])
	}
}

func TestLoadProviderNeedsRepos(t *testing.T) {
	p := writeTmp(t, "c.yaml", "middleware_url: wss://x/nats\nstate_dir: /x\nproviders:\n  - kind: gitea\n    base_url: https://g\n    token_env: T\n    repos: []\n")
	t.Setenv("T", "tok")
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for empty repo allowlist")
	}
}

// An operator pairs the connector BEFORE configuring anything — that is the
// order the setup flow puts them in. A config with nothing in it yet has to
// load, or pairing is impossible until something has been configured for a
// connector that is not yet connected to anything.
func TestAnEmptyConfigIsValidBecauseThatIsHowEveryConnectorStarts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := "middleware_url: nats://relay/x\nstate_dir: " + dir + "\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("a freshly installed connector must load: %v", err)
	}
	if len(cfg.Providers) != 0 || len(cfg.Plugins) != 0 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadReadsTheDeveloperGateway(t *testing.T) {
	// #366: developers' Buff Code signs in through the company's connector.
	p := writeTmp(t, "c.yaml", `
middleware_url: wss://relay.example/nats
state_dir: /var/lib/connector
providers: []
developer_gateway:
  listen: 0.0.0.0:8767
  host: https://connector.corp.local:8767
  tls_cert: /etc/connector/gw.crt
  tls_key: /etc/connector/gw.key
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	g := cfg.DeveloperGateway
	if g.Listen != "0.0.0.0:8767" || g.Host != "https://connector.corp.local:8767" || g.TLSCert != "/etc/connector/gw.crt" || g.TLSKey != "/etc/connector/gw.key" {
		t.Fatalf("gateway %+v", g)
	}
	if len(cfg.UnknownSections()) != 0 {
		t.Fatalf("unknown sections %v", cfg.UnknownSections())
	}
}
