package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/buffsystems/buff-connector/internal/plugin"
)

// The English travels inside the bundle, beside the manifest, and the index
// reads it from there.
func TestBundleCarriesTheEnglish(t *testing.T) {
	src := t.TempDir()
	_ = os.WriteFile(filepath.Join(src, "manifest.yaml"), []byte("name: demo\nversion: 1.0.0\nhost_api: 1\npublisher: buff\n"+
		"summary: Демо\nentry: main.py\nwebhook: none\ntools:\n  - name: search\n    read_only: true\n    summary: Найти\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, plugin.EnglishFile), []byte("summary: Demo\ntools:\n  search: Find\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "main.py"), []byte("pass\n"), 0o644)
	bundle, err := tarGz(src, "")
	if err != nil {
		t.Fatal(err)
	}
	man, en, err := manifestFromBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if man.Name != "demo" || en == nil || en.Summary != "Demo" || en.Tools["search"] != "Find" {
		t.Errorf("manifest %+v, English %+v", man, en)
	}

	// A bundle without English — every one published before — still indexes.
	_ = os.Remove(filepath.Join(src, plugin.EnglishFile))
	bundle, _ = tarGz(src, "")
	if _, en, err := manifestFromBundle(bundle); err != nil || en != nil {
		t.Errorf("no English: %v, %v", en, err)
	}
}
