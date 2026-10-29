package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	yaml "gopkg.in/yaml.v3"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

// EnglishFile sits beside manifest.yaml and carries the English of the
// manifest's text a PERSON reads — the summary, each tool's summary, each
// setting's title and description — keyed the way the manifest names them:
//
//	summary: GitLab — questions and answers right in issues and merge requests
//	tools:
//	  search_issues: Find issues and merge requests by text — …
//	config_schema:
//	  base_url:
//	    title: GitLab address
//	    description: The server's root — …
//
// It is a file of its own rather than new keys in manifest.yaml because a
// connector reads manifests strictly: a key it does not know fails the
// install, so an older connector given a bundle with English inside the
// manifest could no longer install it. Beside it, the older connector simply
// does not look. The publisher copies it into the catalogue (summary_en,
// tools[].summary_en), where the site's marketplace and the local page find it.
// What the model is told — the tools' descriptions and their parameters' — is
// not here either: it is in the plugin's tool texts (ToolTextsFile), per language.
const EnglishFile = "manifest.en.yaml"

// English is one plugin's EnglishFile.
type English struct {
	Summary string               `yaml:"summary"`
	Tools   map[string]string    `yaml:"tools"`
	Config  map[string]FieldText `yaml:"config_schema"`
}

// FieldText is the English of one setting.
type FieldText struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
}

// ParseEnglish reads an EnglishFile as strictly as the manifest is read.
func ParseEnglish(b []byte) (*English, error) {
	var e English
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("%s: %w", EnglishFile, err)
	}
	return &e, nil
}

// Check holds the English to its manifest: every text the manifest shows a
// person has its English, nothing names a tool or setting the manifest lacks,
// and the English is English.
func (e *English) Check(m *Manifest) error {
	var problems []string
	need := func(what, ru, en string) {
		switch {
		case ru != "" && strings.TrimSpace(en) == "":
			problems = append(problems, what+": no English")
		case ru == "" && en != "":
			problems = append(problems, what+": the manifest has no such text")
		case hasCyrillic(en):
			problems = append(problems, what+": not English")
		}
	}
	need("summary", m.Summary, e.Summary)
	tools := map[string]bool{}
	for _, t := range m.Tools {
		tools[t.Name] = true
		need("tool "+t.Name, t.Summary, e.Tools[t.Name])
	}
	for name := range e.Tools {
		if !tools[name] {
			problems = append(problems, "tool "+name+": not in the manifest")
		}
	}
	props := map[string]*Schema{}
	if m.Schema != nil {
		props = m.Schema.Properties
	}
	for key, p := range props {
		need("config_schema."+key+".title", p.Title, e.Config[key].Title)
		need("config_schema."+key+".description", p.Desc, e.Config[key].Description)
	}
	for key := range e.Config {
		if props[key] == nil {
			problems = append(problems, "config_schema."+key+": not in the manifest")
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%s: %s", EnglishFile, strings.Join(problems, "; "))
	}
	return nil
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// InEnglish is the manifest with its person-facing text in English; what the
// overlay leaves out stays as the manifest says. The manifest itself is not
// changed — it is still what runs.
func (m *Manifest) InEnglish(e *English) *Manifest {
	if e == nil {
		return m
	}
	out := *m
	if e.Summary != "" {
		out.Summary = e.Summary
	}
	out.Tools = make([]ToolSpec, len(m.Tools))
	for i, t := range m.Tools {
		if en := e.Tools[t.Name]; en != "" {
			t.Summary = en
		}
		out.Tools[i] = t
	}
	if m.Schema != nil {
		schema := *m.Schema
		schema.Properties = make(map[string]*Schema, len(m.Schema.Properties))
		for key, p := range m.Schema.Properties {
			field := *p
			if en := e.Config[key]; en.Title != "" || en.Description != "" {
				if en.Title != "" {
					field.Title = en.Title
				}
				if en.Description != "" {
					field.Desc = en.Description
				}
			}
			schema.Properties[key] = &field
		}
		out.Schema = &schema
	}
	return &out
}

// ManifestIn is the installed manifest as a person reading l sees it.
func (in *Installed) ManifestIn(l i18n.Lang) *Manifest {
	if l != i18n.EN {
		return in.Manifest
	}
	return in.Manifest.InEnglish(in.English)
}

// readEnglish reads an unpacked bundle's English, if it has one that fits its
// manifest. A bundle without it — every bundle published before — is shown in
// Russian, and so is one whose English does not parse: the text is a courtesy,
// never a reason to refuse code that runs.
func readEnglish(dir string) *English {
	b, err := os.ReadFile(filepath.Join(dir, EnglishFile))
	if err != nil {
		return nil
	}
	e, err := ParseEnglish(b)
	if err != nil {
		return nil
	}
	return e
}
