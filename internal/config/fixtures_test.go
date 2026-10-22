package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Shared fixtures for the config tests. They lived in jira_test.go until the
// built-in integration was removed; the forward-compatibility tests still need
// them, so they moved here rather than being copied into each file.

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "connector.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// A git provider, because most fixtures need a config that serves something.
const providerBlock = `
state_dir: /var/lib/connector
providers:
  - kind: gitea
    base_url: https://gitea.corp.local
    token_env: GITEA_TOKEN
    repos: [team/app]
`
