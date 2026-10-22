package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// A config the connector cannot load must never reach the disk.
//
// This is not theory. Saving a connection whose dev_project carried the
// organisation prefix succeeded — the page listed the connection, the file was
// written — and then the RUNNING connector refused to reload it and kept the
// previous config. From the operator's side: a connection that is there, and
// never starts, with nothing on the page saying why. The write is the last
// moment anyone can be told, so the refusal belongs here.
func TestSavingAConfigTheAgentCannotLoadIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connector.yaml")
	cfg := &Config{
		MiddlewareURL: "nats://relay.example/x",
		StateDir:      t.TempDir(),
		Plugins: []PluginInstance{{
			ID: "7QK3M9XBWZ0FVT2H", Plugin: "jira", Version: "1.0.0", Slug: "jira",
			DevProject: "acme/dev_project",
		}},
	}

	err := Save(path, cfg)
	if err == nil {
		t.Fatal("a config that cannot be loaded back was written")
	}
	if !strings.Contains(err.Error(), "dev_project") {
		t.Errorf("the refusal must name what is wrong, got: %v", err)
	}
	if fp, _ := Fingerprint(path); fp != "" {
		t.Error("the file was written despite the refusal")
	}

	cfg.Plugins[0].DevProject = "dev_project"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("a valid config was refused: %v", err)
	}
}

// SaveIf guards the same door: the conflict check is not the only reason a
// write can be wrong.
func TestSaveIfAlsoRefusesAConfigTheAgentCannotLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connector.yaml")
	cfg := &Config{MiddlewareURL: "nats://relay.example/x", StateDir: t.TempDir()}
	if err := SaveIf(path, cfg, ""); err != nil {
		t.Fatal(err)
	}
	fp, _ := Fingerprint(path)

	cfg.Providers = []Provider{{Kind: "gitea", BaseURL: "https://git.example", TokenEnv: "T"}}
	if err := SaveIf(path, cfg, fp); err == nil {
		t.Fatal("a provider with no repo allowlist was written")
	}
	if now, _ := Fingerprint(path); now != fp {
		t.Error("the file changed despite the refusal")
	}
}

// The check must mirror the LOAD path, not merely resemble it: Load fills the
// baked-in relay before validating, so omitting middleware_url is legal and a
// save must not refuse it.
func TestAConfigWithoutTheRelayIsStillSaveable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connector.yaml")
	if err := Save(path, &Config{StateDir: t.TempDir()}); err != nil {
		t.Fatalf("a config relying on the baked-in relay was refused: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("what was written does not load: %v", err)
	}
}
