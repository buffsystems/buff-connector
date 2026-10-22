package plugin

import (
	"os"
	"testing"
)

// The shipped web plugin's manifest, resolved with an untouched form: the two
// engines with fixed addresses appear, the optional one does not, and the web
// rule is the open internet.
func TestShippedWebManifestResolvesWithDefaults(t *testing.T) {
	raw, err := os.ReadFile("../../plugins/web/manifest.yaml")
	if err != nil {
		t.Skip("plugin sources not beside the core")
	}
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.HostAPI != 2 {
		t.Fatalf("host_api = %d, the web rule needs 2", m.HostAPI)
	}
	cfg := m.WithDefaults(map[string]any{})
	if err := m.ValidateConfig(cfg); err != nil {
		t.Fatalf("an untouched form must be valid: %v", err)
	}
	rules, err := m.ResolveEgress(cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var labels []string
	for _, r := range rules {
		labels = append(labels, r.Label())
	}
	want := []string{"html.duckduckgo.com", "api.search.brave.com", "интернет"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("labels = %v, want %v", labels, want)
		}
	}
	// The lists are validated where the operator can see the message.
	if _, err := m.ResolveEgress(m.WithDefaults(map[string]any{"allow": []any{"https://x.com"}})); err == nil {
		t.Fatal("a bad allow entry must be refused")
	}
}
