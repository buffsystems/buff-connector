package catalog

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The offline kit is a directory of the same signed files, served from disk.
//
// It exists because a machine inside a closed perimeter may have no route
// anywhere — not to our registry, not to a mirror. Somebody carries the files
// in. The signature still decides what may be installed: a directory on a USB
// stick is exactly the transport a signature is for.
func TestACatalogueOnDiskIsReadAndVerifiedLikeAnyOther(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := base64Std(pub)

	idx, _ := json.Marshal(Index{Plugins: []Entry{{
		Name: "jira",
		Versions: []Version{{Version: "1.0.0", HostAPI: 1,
			Bundle: "jira-1.0.0.tar.gz", Signature: "jira-1.0.0.tar.gz.sig"}},
	}}})
	write := func(name string, body []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".sig"), ed25519.Sign(priv, body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("catalog.json", idx)
	write("jira-1.0.0.tar.gz", []byte("a bundle"))
	write("revocations.json", []byte(`{"serial":1}`))

	c := New("file://"+dir, []string{key}, t.TempDir())
	got, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got.Plugins) != 1 || got.Plugins[0].Name != "jira" {
		t.Fatalf("index = %+v", got)
	}

	_, v, _ := got.Find("jira", "1.0.0")
	body, sig, err := c.Download(context.Background(), "jira", v)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(body) != "a bundle" || len(sig) == 0 {
		t.Errorf("bundle = %q", body)
	}

	// The withdrawal list travels with the kit. Without it, installing from a
	// file would simply route around a version we have withdrawn.
	if _, err := c.Revocations(context.Background()); err != nil {
		t.Errorf("revocations: %v", err)
	}
}

// A kit somebody edited on the way in is not our catalogue.
func TestATamperedKitIsRefused(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(nil)
	idx, _ := json.Marshal(Index{Plugins: []Entry{{Name: "jira"}}})
	_ = os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(`{"plugins":[{"name":"evil"}]}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "catalog.json.sig"), ed25519.Sign(priv, idx), 0o644)

	c := New("file://"+dir, []string{base64Std(pub)}, t.TempDir())
	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("an edited catalogue was accepted")
	}
}

// A path that is not there says so, rather than looking like an empty catalogue.
func TestAMissingKitIsAnError(t *testing.T) {
	c := New("file:///nonexistent-kit", []string{"k"}, t.TempDir())
	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("a missing directory read as an empty catalogue")
	}
}
