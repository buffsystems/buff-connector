package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The cabinet tells a new user to run `connector pair --config connector.yaml`
// on a clean machine, where no such file exists yet (autonomous-dev/cloud-front#628).
// Pairing creates the minimal config instead of refusing to read it.

func never(string) bool  { return false }
func always(string) bool { return true }

func TestCreateWritesALoadableMinimalConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connector.yaml")
	created, err := createIfMissing(path, never)
	if err != nil || created == nil {
		t.Fatalf("createIfMissing = %v, %v", created, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("a created config must load: %v", err)
	}
	if want := filepath.Join(dir, "connector-state"); cfg.StateDir != want || created.StateDir != want {
		t.Errorf("state_dir = %q (reported %q), want %q next to the config", cfg.StateDir, created.StateDir, want)
	}
	if cfg.MiddlewareURL != DefaultRelay {
		t.Errorf("middleware_url = %q, want the built-in relay", cfg.MiddlewareURL)
	}
}

func TestCreatePrefersTheDocumentedStateDirWhenUsable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connector.yaml")
	created, err := createIfMissing(path, always)
	if err != nil || created == nil || created.StateDir != DefaultStateDir {
		t.Fatalf("createIfMissing = %+v, %v; want state_dir %s", created, err, DefaultStateDir)
	}
}

func TestCreateLeavesAnExistingConfigUntouched(t *testing.T) {
	path := writeConfig(t, providerBlock)
	created, err := createIfMissing(path, always)
	if err != nil || created != nil {
		t.Fatalf("createIfMissing on an existing file = %+v, %v", created, err)
	}
	if b, _ := os.ReadFile(path); string(b) != providerBlock {
		t.Errorf("existing config changed:\n%s", b)
	}
}

// A relative --config (the cabinet's `connector.yaml`) must not leave a relative
// state_dir behind: `run` from another directory would look elsewhere.
func TestCreateRecordsAnAbsoluteStateDir(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	created, err := createIfMissing("connector.yaml", never)
	if err != nil || created == nil {
		t.Fatalf("createIfMissing = %v, %v", created, err)
	}
	if !filepath.IsAbs(created.StateDir) {
		t.Errorf("state_dir %q is relative", created.StateDir)
	}
}

func TestCreateMakesTheConfigDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "connector", "connector.yaml")
	if _, err := createIfMissing(path, never); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestUsableDir(t *testing.T) {
	if !usableDir(filepath.Join(t.TempDir(), "state")) {
		t.Error("a creatable directory is usable")
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if usableDir(filepath.Join(file, "state")) {
		t.Error("a directory under a file is not usable")
	}
}
