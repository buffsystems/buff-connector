package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/buffsystems/buff-connector/internal/i18n"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// Every plugin we publish carries its English (#565): the catalogue card, the
// site's marketplace and the local page's settings form read it.
func TestEveryPluginHasItsEnglish(t *testing.T) {
	dirs, err := filepath.Glob("../../plugins/*/manifest.yaml")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no plugins found: %v", err)
	}
	for _, path := range dirs {
		dir := filepath.Dir(path)
		raw, _ := os.ReadFile(path)
		man, err := plugin.ParseManifest(raw)
		if err != nil {
			t.Errorf("%s: %v", dir, err)
			continue
		}
		enRaw, err := os.ReadFile(filepath.Join(dir, plugin.EnglishFile))
		if err != nil {
			t.Errorf("%s: no %s", dir, plugin.EnglishFile)
			continue
		}
		en, err := plugin.ParseEnglish(enRaw)
		if err == nil {
			err = en.Check(man)
		}
		if err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
}

// Every plugin we publish tells the model about its tools in both languages
// (#565): each tool and each parameter, in tools.ru.md and tools.en.md.
func TestEveryPluginDescribesItsToolsInBothLanguages(t *testing.T) {
	dirs, _ := filepath.Glob("../../plugins/*/manifest.yaml")
	for _, path := range dirs {
		dir := filepath.Dir(path)
		raw, _ := os.ReadFile(path)
		man, err := plugin.ParseManifest(raw)
		if err != nil {
			t.Errorf("%s: %v", dir, err)
			continue
		}
		for _, l := range []i18n.Lang{i18n.RU, i18n.EN} {
			if _, err := os.Stat(filepath.Join(dir, plugin.ToolTextsFile(l))); err != nil {
				t.Errorf("%s: no %s", dir, plugin.ToolTextsFile(l))
			}
		}
		if err := plugin.CheckToolTexts(dir, man); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
}
