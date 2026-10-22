package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/buffsystems/buff-connector/internal/catalog"
	"github.com/buffsystems/buff-connector/internal/oci/ocitest"
)

func distDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(nil)
	idx, _ := json.Marshal(catalog.Index{Plugins: []catalog.Entry{{
		Name: "jira",
		Versions: []catalog.Version{
			{Version: "1.0.0", Bundle: "jira-1.0.0.tar.gz", Signature: "jira-1.0.0.tar.gz.sig"},
			{Version: "1.1.0", Bundle: "jira-1.1.0.tar.gz", Signature: "jira-1.1.0.tar.gz.sig"},
		},
	}, {
		Name:     "demo",
		Versions: []catalog.Version{{Version: "1.0.0", Bundle: "demo-1.0.0.tar.gz", Signature: "demo-1.0.0.tar.gz.sig"}},
	}}})
	write := func(name string, body []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".sig"), ed25519.Sign(priv, body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("catalog.json", idx)
	write("revocations.json", []byte(`{"serial":1}`))
	for _, f := range []string{"jira-1.0.0.tar.gz", "jira-1.1.0.tar.gz", "demo-1.0.0.tar.gz"} {
		write(f, []byte("bundle "+f))
	}
	return dir
}

// Every version in the index becomes repository:tag. A customer can then see
// what exists with `tags/list` — or with any registry UI — without running our
// code, and can mirror one plugin without mirroring the lot.
func TestPushPutsEveryIndexedVersionUnderItsOwnTag(t *testing.T) {
	reg := ocitest.New(t)
	pushed, err := pushDist(context.Background(), reg.Client(), "buff/plugins", distDir(t))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	want := map[string]bool{
		"buff/plugins/catalog:v1":     true,
		"buff/plugins/revocations:v1": true,
		"buff/plugins/jira:1.0.0":     true,
		"buff/plugins/jira:1.1.0":     true,
		"buff/plugins/demo:1.0.0":     true,
	}
	if len(pushed) != len(want) {
		t.Fatalf("pushed %d artifacts: %v", len(pushed), pushed)
	}
	for _, p := range pushed {
		if !want[p] {
			t.Errorf("unexpected artifact %q", p)
		}
	}
}

// The catalogue must be the LAST thing published. Push it first and there is a
// window where it advertises versions whose bundles are not there yet — every
// connector that refreshes in that window offers an install that will fail.
func TestCatalogueIsPublishedAfterTheBundlesItNames(t *testing.T) {
	reg := ocitest.New(t)
	if _, err := pushDist(context.Background(), reg.Client(), "buff/plugins", distDir(t)); err != nil {
		t.Fatal(err)
	}
	order := reg.Pushed()
	if order[len(order)-1] != "buff/plugins/catalog:v1" {
		t.Fatalf("catalogue was not published last: %v", order)
	}
}

func TestPushRefusesADirectoryWithNoSignature(t *testing.T) {
	dir := distDir(t)
	if err := os.Remove(filepath.Join(dir, "jira-1.0.0.tar.gz.sig")); err != nil {
		t.Fatal(err)
	}
	reg := ocitest.New(t)
	_, err := pushDist(context.Background(), reg.Client(), "buff/plugins", dir)
	if err == nil {
		t.Fatal("an unsigned bundle was published")
	}
}

// The offline kit is made by PULLING what is published, not by rebuilding it:
// a kit built from source could differ from the catalogue a connector with a
// network would install, and then two customers would be running different
// bytes under the same version number.
func TestKitIsPulledFromWhatIsPublished(t *testing.T) {
	reg := ocitest.New(t)
	dir := distDir(t)
	if _, err := pushDist(context.Background(), reg.Client(), "buff/plugins", dir); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	files, err := pullKit(context.Background(), reg.Client(), "buff/plugins", out)
	if err != nil {
		t.Fatalf("kit: %v", err)
	}

	for _, name := range []string{
		"catalog.json", "catalog.json.sig",
		"revocations.json", "revocations.json.sig",
		"jira-1.0.0.tar.gz", "jira-1.0.0.tar.gz.sig",
		"jira-1.1.0.tar.gz", "demo-1.0.0.tar.gz",
	} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s is missing from the kit: %v", name, err)
		}
	}
	if len(files) < 8 {
		t.Errorf("kit reported %d files", len(files))
	}

	// Byte for byte what was published: the kit is a copy, not a rebuild.
	published, _ := os.ReadFile(filepath.Join(dir, "jira-1.0.0.tar.gz"))
	carried, _ := os.ReadFile(filepath.Join(out, "jira-1.0.0.tar.gz"))
	if string(published) != string(carried) {
		t.Error("the kit's bundle is not the published one")
	}
}
