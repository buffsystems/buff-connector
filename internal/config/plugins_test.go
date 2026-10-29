package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/i18n"
	"github.com/buffsystems/buff-connector/internal/problem"
)

func TestNewInstanceIDIsUnambiguousAndUnique(t *testing.T) {
	a, b := NewInstanceID(), NewInstanceID()
	if a == b {
		t.Fatal("two ids collided")
	}
	if len(a) != 16 {
		t.Fatalf("len = %d", len(a))
	}
	// Crockford drops the symbols that get misread aloud or when copied.
	if strings.ContainsAny(a, "ILOU") {
		t.Fatalf("id %q contains an ambiguous symbol", a)
	}
}

func TestSlugForProducesAToolSafeName(t *testing.T) {
	for _, tc := range []struct{ alias, want string }{
		{"Jira прод", "jira_prod"},
		{"Jira Legacy", "jira_legacy"},
		// A Cyrillic alias is transliterated, not dropped: «Сетевая папка»
		// used to become a bare «i» (cloud-front#475).
		{"Сетевая папка", "setevaya_papka"},
		{"Интернет", "internet"},
		{"Щёлково-Юг", "shchelkovo_yug"},
		{"  spaced  out  ", "spaced_out"},
		{"2nd tracker", "i2nd_tracker"},
		{"!!!", "i"},
	} {
		if got := SlugFor(tc.alias); got != tc.want {
			t.Errorf("SlugFor(%q) = %q, want %q", tc.alias, got, tc.want)
		}
	}
}

func TestInstanceValidationGuardsThePlatformFields(t *testing.T) {
	good := PluginInstance{ID: NewInstanceID(), Plugin: "jira", Version: "1.0.0", Slug: "jira_prod"}
	if err := good.validate(); err != nil {
		t.Fatalf("good instance rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		mut  func(*PluginInstance)
		want string
	}{
		{"bad id", func(p *PluginInstance) { p.ID = "short" }, "id"},
		{"no plugin", func(p *PluginInstance) { p.Plugin = "" }, "plugin"},
		{"no version", func(p *PluginInstance) { p.Version = "" }, "version"},
		{"slug with dash", func(p *PluginInstance) { p.Slug = "jira-prod" }, "slug"},
		{"slug starting with digit", func(p *PluginInstance) { p.Slug = "2jira" }, "slug"},
		{"duplicate credential", func(p *PluginInstance) {
			p.Credentials = []InstanceCredential{{Name: "api"}, {Name: "api"}}
		}, "duplicate"},
		{"notes over the limit", func(p *PluginInstance) {
			p.AINotes = strings.Repeat("я", AINotesLimit+1)
		}, "ai_notes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := good
			tc.mut(&p)
			err := p.validate()
			if err == nil || !strings.Contains(i18n.LocalizeError(i18n.EN, err), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
}

// The notes for the model are bounded by default — a long text in the prompt
// costs the model accuracy — and the bound is counted in characters, not bytes,
// or a Russian note would get half the room an English one gets. The operator
// lifts the bound on purpose, and then NOTHING is cut: a cap that silently
// trims what they wrote is worse than the length they accepted.
func TestAINotesAreBoundedInCharactersUntilTheOperatorLiftsTheBound(t *testing.T) {
	p := PluginInstance{ID: NewInstanceID(), Plugin: "jira", Version: "1.0.0", Slug: "jira_prod"}
	p.AINotes = strings.Repeat("я", AINotesLimit)
	if err := p.validate(); err != nil {
		t.Fatalf("a note exactly at the limit, in two-byte characters, was rejected: %v", err)
	}
	p.AINotes = strings.Repeat("я", AINotesLimit*8)
	if err := p.validate(); err == nil {
		t.Fatal("a note past the limit was accepted without the operator lifting the bound")
	}
	p.AINotesUnbounded = true
	if err := p.validate(); err != nil {
		t.Fatalf("the operator lifted the bound and the note was still rejected: %v", err)
	}
}

// The fingerprint decides what restarts. It must react to settings and to the
// SHAPE of credentials, and must not carry their values.
func TestFingerprintReactsToSettingsButNotToSecretValues(t *testing.T) {
	base := PluginInstance{
		ID: "7QK3M9XBWZ0FVT2H", Plugin: "jira", Version: "1.0.0", Slug: "jira",
		Config:      map[string]any{"base_url": "https://a", "n": 1},
		Credentials: []InstanceCredential{{Name: "api", TokenFile: "/s/api.token"}},
	}
	base.Credentials[0].Token = "SEKRET"
	f := base.Fingerprint()

	if strings.Contains(f, "SEKRET") {
		t.Fatal("a resolved secret must never reach the fingerprint — it gets logged")
	}

	changed := base
	changed.Config = map[string]any{"base_url": "https://b", "n": 1}
	if changed.Fingerprint() == f {
		t.Error("changing a setting must change the fingerprint")
	}

	// Same values in a different map order must fingerprint identically, or an
	// instance restarts on every reload for no reason.
	reordered := base
	reordered.Config = map[string]any{"n": 1, "base_url": "https://a"}
	if reordered.Fingerprint() != f {
		t.Error("fingerprint depends on map iteration order")
	}

	rotated := base
	rotated.Credentials = []InstanceCredential{{Name: "api", TokenFile: "/s/api.token", Token: "NEW"}}
	if rotated.Fingerprint() != f {
		t.Error("rotating a secret's VALUE must not restart the instance")
	}

	moved := base
	moved.Credentials = []InstanceCredential{{Name: "api", TokenFile: "/elsewhere.token"}}
	if moved.Fingerprint() == f {
		t.Error("pointing a credential at a different file must restart the instance")
	}

	// The notes are read by the MODEL, not by the plugin process: editing them
	// must not restart the connection.
	annotated := base
	annotated.AINotes, annotated.AINotesUnbounded = "Проект PROJ — это биллинг.", true
	if annotated.Fingerprint() != f {
		t.Error("editing the notes for the model restarted the instance")
	}
}

func TestConfigAcceptsPluginsWithoutProviders(t *testing.T) {
	// A customer who connected only a tracker has no git provider at all. That
	// configuration is legitimate and must start.
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := `
middleware_url: nats://relay/x
state_dir: ` + dir + `
plugins:
  - id: 7QK3M9XBWZ0FVT2H
    plugin: jira
    version: 3.2.0
    alias: "Jira прод"
    slug: jira_prod
    dev_project: backend
    config:
      base_url: https://jira.example.com
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Plugins) != 1 {
		t.Fatalf("plugins = %d", len(cfg.Plugins))
	}
	if cfg.Plugins[0].DevProject != "backend" {
		t.Errorf("dev_project lost: %+v", cfg.Plugins[0])
	}
}

// An instance written by hand needs no invented id: the connector mints one and
// writes it back, so YAML stays a first-class way to configure the connector.
func TestMissingInstanceIDIsMintedOnLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := `
middleware_url: nats://relay/x
state_dir: ` + dir + `
plugins:
  - plugin: jira
    version: 3.2.0
    alias: Jira
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !idRe.MatchString(cfg.Plugins[0].ID) {
		t.Fatalf("id not minted: %q", cfg.Plugins[0].ID)
	}
	if cfg.Plugins[0].Slug != "jira" {
		t.Fatalf("slug not derived: %q", cfg.Plugins[0].Slug)
	}
}

// Superseded: a duplicate no longer fails the LOAD — see
// TestDuplicateSlugStopsOneConnectionNotTheConnector for why. It is still
// refused, per connection.
func TestDuplicateInstanceIDsStopTheSecondConnection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := `
middleware_url: nats://relay/x
state_dir: ` + dir + `
plugins:
  - {id: 7QK3M9XBWZ0FVT2H, plugin: jira, version: 1.0.0, slug: a}
  - {id: 7QK3M9XBWZ0FVT2H, plugin: jira, version: 1.0.0, slug: b}
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the connector must still start: %v", err)
	}
	if len(cfg.InstanceProblems()) != 1 {
		t.Fatalf("problems = %+v", cfg.InstanceProblems())
	}
}

func TestDuplicateSlugsStopTheSecondConnection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := `
middleware_url: nats://relay/x
state_dir: ` + dir + `
plugins:
  - {id: 7QK3M9XBWZ0FVT2H, plugin: jira, version: 1.0.0, slug: jira}
  - {id: J4RD8N1VC6PQY5ZE, plugin: jira, version: 1.0.0, slug: jira}
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the connector must still start: %v", err)
	}
	problems := cfg.InstanceProblems()
	if len(problems) != 1 || !errors.Is(problems["J4RD8N1VC6PQY5ZE"], problem.New("instance_slug_taken")) {
		t.Fatalf("problems = %+v", problems)
	}
}

// Two connections named alike must not collide silently, and the operator must
// not have to learn what a slug is to avoid it.
func TestUniqueSlugForAvoidsCollisions(t *testing.T) {
	taken := map[string]bool{"jira_test": true, "jira_test_2": true}
	got := UniqueSlugFor("Jira тест", taken)
	if got != "jira_test_3" {
		t.Fatalf("UniqueSlugFor = %q, want jira_test_3", got)
	}
	if free := UniqueSlugFor("Redmine", taken); free != "redmine" {
		t.Fatalf("an unused slug must be left alone, got %q", free)
	}
}

// A collision that DOES reach the file stops that connection, not the connector.
// It used to fail the whole config load — taking the git bridge down for a fault
// in something git knows nothing about.
func TestDuplicateSlugStopsOneConnectionNotTheConnector(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := `
middleware_url: nats://relay/x
state_dir: ` + dir + `
plugins:
  - {id: 7QK3M9XBWZ0FVT2H, plugin: jira, version: 1.0.0, slug: jira, alias: "Jira прод"}
  - {id: J4RD8N1VC6PQY5ZE, plugin: jira, version: 1.0.0, slug: jira, alias: "Jira тест"}
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the connector must still start: %v", err)
	}
	problems := cfg.InstanceProblems()
	if len(problems) != 1 {
		t.Fatalf("problems = %+v", problems)
	}
	reason := problems["J4RD8N1VC6PQY5ZE"].Error()
	if !strings.Contains(reason, "Jira прод") {
		t.Errorf("the reason should name the connection it clashes with, got %q", reason)
	}
	if _, stopped := problems["7QK3M9XBWZ0FVT2H"]; stopped {
		t.Error("the first connection is fine and must keep running")
	}
}

// The connector is the listener, so WHERE it listens is asked once for the
// machine — not once per connection, which made an operator repeat one answer
// and invited a second listener for no reason.
func TestTheListenerBelongsToTheConnectorAndTheSecretToTheConnection(t *testing.T) {
	on := true
	cfg := &Config{
		Webhooks: Webhooks{Listen: "0.0.0.0:8766", Host: "connector.corp.local:8766"},
		Plugins: []PluginInstance{{
			ID: "7QK3M9XBWZ0FVT2H", Plugin: "jira", Version: "1.0.0", Slug: "jira",
			Alias: "Jira прод", Webhook: &on, WebhookSecret: "HOOK",
		}},
	}
	p := &cfg.Plugins[0]
	if !p.WebhookEnabled() {
		t.Fatal("a connection switched on with a secret should accept callbacks")
	}
	if got := p.WebhookPath(); got != "/plugin/7QK3M9XBWZ0FVT2H" {
		t.Errorf("WebhookPath = %q", got)
	}
	if got := cfg.WebhookURL(p); got != "http://connector.corp.local:8766/plugin/7QK3M9XBWZ0FVT2H/HOOK" {
		t.Errorf("WebhookURL = %q", got)
	}

	// Renaming must not change a URL already pasted into the customer's tracker.
	before := cfg.WebhookURL(p)
	p.Alias = "Jira песочница"
	if cfg.WebhookURL(p) != before {
		t.Error("renaming a connection changed the URL already in the tracker")
	}
}

// Host and Listen are not a duplicate: "0.0.0.0" cannot be dialled, and a
// connector behind NAT or in a container is reached at a different name entirely.
// With nothing in between they are the same, and saying so is the default.
func TestReachableAddressFallsBackToTheListenAddress(t *testing.T) {
	if got := (Webhooks{Listen: "127.0.0.1:8766"}).Address(); got != "127.0.0.1:8766" {
		t.Errorf("Address = %q", got)
	}
	if got := (Webhooks{Listen: "0.0.0.0:8766", Host: "connector.corp.local:8766"}).Address(); got != "connector.corp.local:8766" {
		t.Errorf("Address = %q", got)
	}
	if (Webhooks{}).Enabled() {
		t.Error("a connector with no listen address must open no listener")
	}
}

func TestAConnectionWithoutASecretReceivesNothing(t *testing.T) {
	on := true
	cfg := &Config{Webhooks: Webhooks{Listen: "0.0.0.0:8766"},
		Plugins: []PluginInstance{{ID: "7QK3M9XBWZ0FVT2H", Webhook: &on}}}
	if cfg.Plugins[0].WebhookEnabled() {
		t.Error("a route with no secret would accept anyone's callback")
	}
	if cfg.WebhookURL(&cfg.Plugins[0]) != "" {
		t.Error("a URL was offered for a webhook that cannot work")
	}
}

// A connector with no listener offers no URL, however a connection is switched.
func TestNoListenerMeansNoURL(t *testing.T) {
	on := true
	cfg := &Config{Plugins: []PluginInstance{{
		ID: "7QK3M9XBWZ0FVT2H", Webhook: &on, WebhookSecret: "HOOK",
	}}}
	if cfg.WebhookURL(&cfg.Plugins[0]) != "" {
		t.Error("a URL was offered while nothing is listening")
	}
}

// Saving a connection again without renaming it keeps the name its tools go
// by: the slug used to be recomputed on every save, so a change of how slugs
// are derived renamed every tool of an untouched connection (cloud-front#475).
func TestSlugOnSaveKeepsTheSlugOfAnUnrenamedConnection(t *testing.T) {
	prev := &PluginInstance{Alias: "Jira компании", Slug: "jira"}
	if got := SlugOnSave(prev, "Jira компании", map[string]bool{}); got != "jira" {
		t.Fatalf("an unrenamed connection got %q, want its slug jira", got)
	}
	if got := SlugOnSave(prev, "Jira поддержки", map[string]bool{}); got != "jira_podderzhki" {
		t.Fatalf("a renamed connection got %q, want jira_podderzhki", got)
	}
	if got := SlugOnSave(nil, "Сетевая папка", map[string]bool{}); got != "setevaya_papka" {
		t.Fatalf("a new connection got %q", got)
	}
	if got := SlugOnSave(prev, "Jira компании", map[string]bool{"jira": true}); got != "jira_kompanii" {
		t.Fatalf("a slug another connection took must not be kept, got %q", got)
	}
}
