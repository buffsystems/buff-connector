package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

const englishManifest = `name: demo
version: 1.0.0
host_api: 1
publisher: buff
summary: Демо — вопросы и ответы
entry: main.py
webhook: none
config_schema:
  type: object
  properties:
    base_url:
      type: string
      title: Адрес
      description: Корень сервера
    projects:
      type: array
      title: Проекты
      items:
        type: string
tools:
  - name: search
    read_only: true
    summary: Найти задачи
`

const englishOverlay = `summary: Demo — questions and answers
tools:
  search: Find issues
config_schema:
  base_url:
    title: Address
    description: The server's root
  projects:
    title: Projects
`

func TestEnglishOverlayApplies(t *testing.T) {
	man, err := ParseManifest([]byte(englishManifest))
	if err != nil {
		t.Fatal(err)
	}
	en, err := ParseEnglish([]byte(englishOverlay))
	if err != nil {
		t.Fatal(err)
	}
	if err := en.Check(man); err != nil {
		t.Fatalf("a complete overlay was refused: %v", err)
	}
	got := man.InEnglish(en)
	if got.Summary != "Demo — questions and answers" || got.Tools[0].Summary != "Find issues" {
		t.Errorf("summary/tools = %q / %q", got.Summary, got.Tools[0].Summary)
	}
	p := got.Schema.Properties["base_url"]
	if p.Title != "Address" || p.Desc != "The server's root" || got.Schema.Properties["projects"].Title != "Projects" {
		t.Errorf("schema = %+v", got.Schema.Properties)
	}
	// The original is untouched: the Russian manifest is still what runs.
	if man.Summary != "Демо — вопросы и ответы" || man.Schema.Properties["base_url"].Title != "Адрес" || man.Tools[0].Summary != "Найти задачи" {
		t.Errorf("InEnglish changed the manifest it was given")
	}
	// Everything else is the same manifest.
	if got.Schema.Properties["base_url"].Type != "string" || got.Name != "demo" || got.Schema.Order[0] != "base_url" {
		t.Errorf("non-text fields lost: %+v", got)
	}
	if man.InEnglish(nil) != man {
		t.Errorf("no overlay must mean the manifest as it is")
	}
}

func TestEnglishOverlayMustMatchTheManifest(t *testing.T) {
	man, _ := ParseManifest([]byte(englishManifest))
	cases := map[string]string{
		"unknown tool":     strings.Replace(englishOverlay, "  search:", "  serch:", 1),
		"unknown field":    strings.Replace(englishOverlay, "  projects:\n", "  project:\n", 1),
		"missing summary":  strings.Replace(englishOverlay, "summary: Demo — questions and answers\n", "", 1),
		"missing tool":     strings.Replace(englishOverlay, "  search: Find issues\n", "", 1),
		"missing desc":     strings.Replace(englishOverlay, "    description: The server's root\n", "", 1),
		"still in Russian": strings.Replace(englishOverlay, "Find issues", "Найти задачи", 1),
		"text the manifest has not": strings.Replace(englishOverlay, "    title: Projects\n",
			"    title: Projects\n    description: Which projects\n", 1),
	}
	for name, overlay := range cases {
		en, err := ParseEnglish([]byte(overlay))
		if err == nil {
			err = en.Check(man)
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseEnglish([]byte("summary: x\nunknown: y\n")); err == nil {
		t.Error("an unknown key was accepted")
	}
}

// An installed bundle carries its English beside the manifest; the store reads
// it when present and does without it otherwise — an older bundle has none.
func TestInstalledInLanguage(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(englishManifest), 0o644)
	man, _ := ParseManifest([]byte(englishManifest))
	in := &Installed{Dir: dir, Manifest: man}
	if in.ManifestIn(i18n.EN).Summary != man.Summary {
		t.Error("no English file: the Russian manifest is shown")
	}
	_ = os.WriteFile(filepath.Join(dir, EnglishFile), []byte(englishOverlay), 0o644)
	in = &Installed{Dir: dir, Manifest: man, English: readEnglish(dir)}
	if got := in.ManifestIn(i18n.EN).Summary; got != "Demo — questions and answers" {
		t.Errorf("English: %q", got)
	}
	if got := in.ManifestIn(i18n.RU).Summary; got != man.Summary {
		t.Errorf("Russian: %q", got)
	}
}
