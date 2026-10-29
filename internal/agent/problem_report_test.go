package agent

import (
	"context"
	"os"
	"testing"
	"unicode"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/problem"
)

// Every way a connection fails reaches the platform as a status its clients
// can write in their reader's language (#718): each link a known code, and
// nothing the connector wrote itself passed on as raw text.
func TestEveryFailedConnectionIsReportedAsACodedProblem(t *testing.T) {
	root, store := stateWithStore(t)
	writeDemoPlugin(t, store)
	m := NewPluginManager(context.Background(), root, store)
	t.Cleanup(m.StopAll)

	missing := instance("A4RD8N1VC6PQY5ZE", "missing", nil)
	missing.Version = "9.9.9"
	plugins := []config.PluginInstance{
		missing,
		instance("B4RD8N1VC6PQY5ZE", "bad_settings", map[string]any{"base_url": 42}),
		instance("C4RD8N1VC6PQY5ZE", "twice", map[string]any{"base_url": "https://a.example.com"}),
		instance("D4RD8N1VC6PQY5ZE", "twice", map[string]any{"base_url": "https://b.example.com"}),
	}
	if os.Geteuid() == 0 {
		unfilled := customInstance(fakeMCPBinary(t, root))
		unfilled.ID, unfilled.Slug, unfilled.Config = "E4RD8N1VC6PQY5ZE", "unfilled", nil
		absent := customInstance("definitely-not-a-command-on-this-machine")
		absent.ID, absent.Slug = "F4RD8N1VC6PQY5ZE", "absent"
		plugins = append(plugins, unfilled, absent)
	}
	m.Sync(context.Background(), &config.Config{Plugins: plugins})

	failed := 0
	for _, r := range m.Reports() {
		if r.LastError == "" {
			continue
		}
		failed++
		if r.Problem == nil {
			t.Errorf("%s: last error %q has no problem", r.Slug, r.LastError)
			continue
		}
		checkCoded(t, r.Slug, r.Problem)
	}
	// The first of the two "twice" may run, or fail for want of a runtime.
	if want := len(plugins) - 1; failed < want {
		t.Errorf("%d connections failed, want at least %d", failed, want)
	}
}

func checkCoded(t *testing.T, slug string, p *problem.Problem) {
	t.Helper()
	known := problem.Codes()
	for link := p; link != nil; link = link.Cause {
		if link.Raw != "" {
			for _, r := range link.Raw {
				if unicode.Is(unicode.Cyrillic, r) {
					t.Errorf("%s: the connector's own text passed on raw: %q", slug, link.Raw)
					break
				}
			}
			continue
		}
		if _, ok := known[link.Code]; !ok {
			t.Errorf("%s: unknown code %q", slug, link.Code)
		}
	}
}
