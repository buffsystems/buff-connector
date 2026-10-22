package plugin

import (
	"strings"
	"testing"
)

// A plugin knows how quickly its source is worth reading — a comment feed with
// no callback of its own wants seconds, a nightly digest does not — so the
// manifest may suggest an interval. It is a DEFAULT: the connection's own
// setting wins, and a manifest that says nothing leaves the connector's.
func TestManifestMaySuggestAPollInterval(t *testing.T) {
	m, err := ParseManifest([]byte(`
name: notion
version: 1.0.0
host_api: 3
entry: main.py
poll_seconds: 5
`))
	if err != nil {
		t.Fatal(err)
	}
	if m.PollSeconds != 5 {
		t.Fatalf("poll_seconds = %d, want 5", m.PollSeconds)
	}

	quiet, err := ParseManifest([]byte("name: jira\nversion: 1.0.0\nhost_api: 1\nentry: main.py\n"))
	if err != nil {
		t.Fatal(err)
	}
	if quiet.PollSeconds != 0 {
		t.Fatalf("a manifest that says nothing must suggest nothing, got %d", quiet.PollSeconds)
	}

	_, err = ParseManifest([]byte("name: x\nversion: 1.0.0\nhost_api: 1\nentry: main.py\npoll_seconds: -3\n"))
	if err == nil || !strings.Contains(err.Error(), "poll_seconds") {
		t.Fatalf("err = %v, want a refusal naming poll_seconds", err)
	}
}
