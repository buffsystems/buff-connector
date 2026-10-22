package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An agent released today must survive a config written by a later one.
//
// This is the lesson of the jira section itself: the moment it was added, every
// agent already in the field would refuse to start on a config that mentioned
// it. The old binaries cannot be fixed retroactively — but this can stop the
// same thing happening again when the next integration lands.
func TestLoadIgnoresSectionsThisVersionDoesNotKnow(t *testing.T) {
	t.Setenv("GITEA_TOKEN", "git-tok")
	path := writeConfig(t, providerBlock+`
slack:
  - name: corp
    workspace: acme
    channels: [dev]
confluence:
  - name: kb
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("a config naming a newer integration must still load: %v", err)
	}
	if len(cfg.Providers) != 1 {
		t.Errorf("the parts this version does understand must survive: %+v", cfg.Providers)
	}

	// The operator has to be able to learn why part of their config did nothing.
	skipped := cfg.UnknownSections()
	if len(skipped) != 2 {
		t.Fatalf("skipped = %v, want both unknown sections named", skipped)
	}
	if skipped[0] != "confluence" || skipped[1] != "slack" {
		t.Errorf("skipped = %v, want them sorted for a stable message", skipped)
	}
}

// Forward compatibility must not become a licence to swallow typos. A misspelled
// key inside a section this version DOES know is a mistake, not the future.
func TestLoadStillRejectsATypoInsideAKnownSection(t *testing.T) {
	t.Setenv("GITEA_TOKEN", "git-tok")
	t.Setenv("JIRA_A", "pat")
	cases := map[string]string{
		"provider": `
state_dir: /var/lib/connector
providers:
  - kind: gitea
    base_url: https://gitea.corp.local
    token_env: GITEA_TOKEN
    repos: [team/app]
    token_fil: /oops
`,
		"plugins": providerBlock + `
plugins:
  - id: 7QK3M9XBWZ0FVT2H
    plugin: jira
    version: 1.0.0
    slug: jira
    dev_projekt: backend
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, body))
			if err == nil {
				t.Fatal("a misspelled key must still be refused")
			}
			if !strings.Contains(err.Error(), "not found") {
				t.Errorf("error should name the unknown field, got: %v", err)
			}
		})
	}
}

// A config holding only a section this version cannot read LOADS — the agent
// starts, does what it can (nothing yet), and says which section it is ignoring.
//
// It used to be refused, on the reasoning that such a config serves nothing.
// That reasoning made pairing impossible before anything was configured, and it
// punished the very case forward-compatibility exists for: an operator running
// an older binary against a config a newer one wrote. Startup names the ignored
// section instead, which is the thing they actually need to know.
func TestLoadAcceptsAConfigOfOnlyUnknownSectionsAndNamesThem(t *testing.T) {
	path := writeConfig(t, "state_dir: /var/lib/connector\nslack:\n  - name: corp\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("an older agent must still start: %v", err)
	}
	if got := cfg.UnknownSections(); len(got) != 1 || got[0] != "slack" {
		t.Fatalf("unknown sections = %v, want the ignored one named", got)
	}
}

// Saving must not quietly delete configuration a newer agent wrote. An operator
// who opens the setup UI on an older binary should not lose their slack section
// by pressing Save.
func TestSavePreservesSectionsThisVersionDoesNotKnow(t *testing.T) {
	t.Setenv("GITEA_TOKEN", "git-tok")
	path := writeConfig(t, providerBlock+`
slack:
  - name: corp
    channels: [dev, ops]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "slack:") {
		t.Fatalf("the unknown section was dropped on save:\n%s", body)
	}
	if !strings.Contains(string(body), "ops") {
		t.Errorf("its contents were not preserved:\n%s", body)
	}
}

// LoadLenient is what the setup UI reads with; it must tolerate the same thing.
func TestLoadLenientToleratesUnknownSections(t *testing.T) {
	path := writeConfig(t, providerBlock+"\nslack:\n  - name: corp\n")
	cfg, err := LoadLenient(path)
	if err != nil {
		t.Fatalf("lenient load: %v", err)
	}
	if got := cfg.UnknownSections(); len(got) != 1 || got[0] != "slack" {
		t.Errorf("UnknownSections() = %v", got)
	}
}

func TestUnknownSectionsIsEmptyForAnOrdinaryConfig(t *testing.T) {
	t.Setenv("GITEA_TOKEN", "git-tok")
	cfg, err := Load(writeConfig(t, providerBlock))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.UnknownSections(); len(got) != 0 {
		t.Errorf("UnknownSections() = %v, want none", got)
	}
}

// The built-in Jira integration was removed: the connector serves git, and
// everything else is an integration from the catalogue. A customer whose config
// still carries the old section must not be met by a connector that refuses to
// start — that would take their git bridge down over something git knows
// nothing about. The section is kept, ignored, and named.
func TestAConfigWithTheOldJiraSectionStillStarts(t *testing.T) {
	t.Setenv("GITEA_TOKEN", "git-tok")
	path := writeConfig(t, providerBlock+`
jira:
  - name: corp
    base_url: https://jira.corp.local
    token_env: JIRA_A
    dev_project: backend
    project_keys: [PROJ]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("a connector with the old section must still start: %v", err)
	}
	if got := cfg.UnknownSections(); len(got) != 1 || got[0] != "jira" {
		t.Fatalf("unknown sections = %v, want jira named", got)
	}
	// And saving must not delete it: an operator pressing Save should not lose
	// the settings they will need when migrating to the catalogue integration.
	out := filepath.Join(t.TempDir(), "c.yaml")
	if err := Save(out, cfg); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(out)
	if !strings.Contains(string(body), "jira.corp.local") {
		t.Errorf("the old section was dropped on save:\n%s", body)
	}
}
