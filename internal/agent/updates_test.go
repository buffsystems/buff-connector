package agent

import (
	"testing"

	"github.com/buffsystems/buff-connector/internal/catalog"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// A connection reports what it runs AND what it could run: an operator who never
// opens the catalogue has no other way to learn that the version they installed
// was replaced — which is exactly how a customer kept polling an issue tracker
// Atlassian had already removed (#302).
func TestAReportSaysWhenANewerVersionIsPublished(t *testing.T) {
	m := NewPluginManager(nil, t.TempDir(), nil)
	m.catalogue = func() *catalog.Index {
		return &catalog.Index{Plugins: []catalog.Entry{{
			Name:     "bitbucket-cloud",
			Versions: []catalog.Version{{Version: "1.0.4"}, {Version: "1.0.3"}},
		}}}
	}
	m.failed["i1"] = plugin.InstanceReport{
		ID: "i1", Slug: "bb", Plugin: "bitbucket-cloud", Version: "1.0.3", Status: "failed",
	}

	got := m.Reports()
	if len(got) != 1 {
		t.Fatalf("reports = %d, want 1", len(got))
	}
	if got[0].Latest != "1.0.4" {
		t.Fatalf("Latest = %q, want 1.0.4", got[0].Latest)
	}
}

func TestAConnectionOnTheNewestVersionIsNotNagged(t *testing.T) {
	m := NewPluginManager(nil, t.TempDir(), nil)
	m.catalogue = func() *catalog.Index {
		return &catalog.Index{Plugins: []catalog.Entry{{
			Name: "gitea", Versions: []catalog.Version{{Version: "1.0.5"}},
		}}}
	}
	m.failed["i1"] = plugin.InstanceReport{ID: "i1", Slug: "g", Plugin: "gitea", Version: "1.0.5"}
	if got := m.Reports()[0].Latest; got != "" {
		t.Fatalf("Latest = %q, want empty", got)
	}
}

// No catalogue on disk — a connector that has never reached one, or an offline
// kit — must report versions and claim nothing about updates.
func TestWithoutACatalogueNothingIsClaimed(t *testing.T) {
	m := NewPluginManager(nil, t.TempDir(), nil)
	m.failed["i1"] = plugin.InstanceReport{ID: "i1", Slug: "g", Plugin: "gitea", Version: "1.0.5"}
	if got := m.Reports()[0].Latest; got != "" {
		t.Fatalf("Latest = %q, want empty", got)
	}
}

// The connector itself is the other half: a core with a known defect is worth
// saying out loud, and the version to move to travels in the same signed
// catalogue rather than a second thing to trust.
func TestTheReportSaysWhenANewerConnectorIsPublished(t *testing.T) {
	dir := t.TempDir()
	m := NewPluginManager(nil, dir, plugin.NewStore(dir, plugin.TrustedKeys, nil))
	m.catalogue = func() *catalog.Index { return &catalog.Index{Connector: "v0.7.2"} }

	payload := buildFullReportPayload("ACCESS", nil, "v0.7.1", m, nil)
	if payload["core_latest"] != "v0.7.2" {
		t.Fatalf("core_latest = %v, want v0.7.2", payload["core_latest"])
	}

	current := buildFullReportPayload("ACCESS", nil, "v0.7.2", m, nil)
	if _, said := current["core_latest"]; said {
		t.Fatalf("a connector on the newest version must claim nothing: %v", current["core_latest"])
	}
}
