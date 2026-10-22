package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSaveIfRefusesWhenTheFileMovedUnderneath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("state_dir: /a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	// Somebody else edits it — the CLI, or the operator in an editor.
	if err := os.WriteFile(path, []byte("state_dir: /b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = SaveIf(path, &Config{StateDir: "/c"}, stale)
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "/b") {
		t.Fatal("the other writer's change was overwritten")
	}
}

func TestSaveIfAcceptsAnUnchangedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("state_dir: /a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fp, _ := Fingerprint(path)
	if err := SaveIf(path, &Config{StateDir: "/c"}, fp); err != nil {
		t.Fatalf("save: %v", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "/c") {
		t.Fatalf("not written: %s", b)
	}
}

func TestFingerprintOfAMissingFileIsEmpty(t *testing.T) {
	fp, err := Fingerprint(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil || fp != "" {
		t.Fatalf("fp = %q, err = %v", fp, err)
	}
}

// The lock is what stops two writers interleaving. Whatever the order, the file
// must end up as exactly one writer's whole config — never a mixture.
func TestConcurrentSavesDoNotInterleave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = Save(path, &Config{StateDir: strings.Repeat("x", 200+n)})
		}(i)
	}
	wg.Wait()

	cfg, err := LoadLenient(path)
	if err != nil {
		t.Fatalf("the file is not parseable after concurrent writes: %v", err)
	}
	if len(cfg.StateDir) < 200 {
		t.Fatalf("state_dir looks torn: %q", cfg.StateDir)
	}
}

// A read-only config directory must produce a plain refusal, not a save that
// quietly did nothing.
func TestSaveOnReadOnlyDirectoryFailsLoudly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	if err := Save(path, &Config{StateDir: "/a"}); err == nil {
		t.Fatal("saving into a read-only directory must fail")
	}
}
