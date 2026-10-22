package catalog

import "testing"

// Version semantics live in ONE place: the catalogue is what says which versions
// exist, so it is also what says which of two is newer. The connector's page, the
// upgrade check and the report all ask here (#302).
func TestNewerComparesNumbersNotText(t *testing.T) {
	cases := []struct {
		candidate, have string
		newer           bool
	}{
		// The reason this is not a string comparison at all.
		{"1.10.0", "1.9.9", true},
		{"1.9.9", "1.10.0", false},
		{"1.0.4", "1.0.3", true},
		{"1.0.3", "1.0.4", false},
		{"1.0.0", "1.0.0", false},
		// A version that says less means zeros, not "unknown".
		{"1.1", "1.0.9", true},
		{"1.0", "1.0.0", false},
		{"2", "1.9.9", true},
		// A leading "v" is how the connector's own version is written.
		{"v0.7.2", "v0.7.1", true},
		{"v0.7.1", "v0.7.2", false},
		{"v0.7.2", "0.7.2", false},
		// Nothing to compare is not "newer": an empty answer must not light up
		// a badge telling an operator to update to nowhere.
		{"", "1.0.0", false},
		{"1.0.0", "", false},
		{"", "", false},
		// Neither is a version with no numbers in it at all. A build made
		// outside a release says "dev", and telling its author to update is
		// noise dressed as advice — we cannot know what they are running.
		{"1.0.0", "dev", false},
		{"dev", "1.0.0", false},
		{"1.0.0", "какая-то-ветка", false},
	}
	for _, c := range cases {
		if got := Newer(c.candidate, c.have); got != c.newer {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.candidate, c.have, got, c.newer)
		}
	}
}

func TestLatestIsTheNewestVersionAnOperatorMayInstall(t *testing.T) {
	idx := Index{Plugins: []Entry{{
		Name: "jira",
		Versions: []Version{
			{Version: "1.4.4"},
			{Version: "1.10.0"},
			{Version: "1.9.9"},
		},
	}, {
		Name:     "gitea",
		Versions: []Version{{Version: "1.0.5"}},
	}}}
	if got := idx.Latest("jira"); got != "1.10.0" {
		t.Errorf("Latest(jira) = %q, want 1.10.0", got)
	}
	if got := idx.Latest("gitea"); got != "1.0.5" {
		t.Errorf("Latest(gitea) = %q, want 1.0.5", got)
	}
	// A plugin the catalogue does not carry says nothing rather than guessing.
	if got := idx.Latest("unknown"); got != "" {
		t.Errorf("Latest(unknown) = %q, want empty", got)
	}
}

// A withdrawn version is not an upgrade: pointing an operator at one would be
// telling them to install what we took back.
func TestLatestSkipsAWithdrawnVersion(t *testing.T) {
	idx := Index{Plugins: []Entry{{
		Name: "jira",
		Versions: []Version{
			{Version: "1.5.0", Status: "withdrawn", Reason: "утечка токена"},
			{Version: "1.4.4"},
		},
	}}}
	if got := idx.Latest("jira"); got != "1.4.4" {
		t.Errorf("Latest = %q, want 1.4.4 (the withdrawn 1.5.0 is not an upgrade)", got)
	}
}

// Deprecated is not withdrawn: it still installs, and an operator moving off an
// even older version should see it.
func TestLatestKeepsADeprecatedVersion(t *testing.T) {
	idx := Index{Plugins: []Entry{{
		Name:     "jira",
		Versions: []Version{{Version: "1.4.4", Status: "deprecated"}},
	}}}
	if got := idx.Latest("jira"); got != "1.4.4" {
		t.Errorf("Latest = %q, want 1.4.4", got)
	}
}

// What a badge needs: the version to move to, or nothing at all. The rule lives
// here so the local page, the report to the platform and the app cannot disagree
// about when to nag an operator.
func TestUpdateAnswersOnlyWhenThereIsSomethingNewer(t *testing.T) {
	idx := Index{
		Connector: "v0.7.2",
		Plugins: []Entry{{
			Name:     "bitbucket-cloud",
			Versions: []Version{{Version: "1.0.4"}, {Version: "1.0.3"}},
		}},
	}
	if got := idx.Update("bitbucket-cloud", "1.0.3"); got != "1.0.4" {
		t.Errorf("Update = %q, want 1.0.4", got)
	}
	if got := idx.Update("bitbucket-cloud", "1.0.4"); got != "" {
		t.Errorf("Update on the newest = %q, want empty", got)
	}
	// Running something the catalogue does not carry at all — a hand-built or
	// withdrawn version — is not a reason to point anywhere.
	if got := idx.Update("unknown", "1.0.0"); got != "" {
		t.Errorf("Update(unknown) = %q, want empty", got)
	}
	// An operator ahead of the catalogue (a version we pulled) is not nagged
	// downwards.
	if got := idx.Update("bitbucket-cloud", "1.1.0"); got != "" {
		t.Errorf("Update below the installed = %q, want empty", got)
	}

	if got := idx.CoreUpdate("v0.7.1"); got != "v0.7.2" {
		t.Errorf("CoreUpdate = %q, want v0.7.2", got)
	}
	if got := idx.CoreUpdate("v0.7.2"); got != "" {
		t.Errorf("CoreUpdate on the newest = %q, want empty", got)
	}
	// A catalogue that says nothing about the connector — an older publisher, an
	// offline kit — must not produce a badge out of thin air.
	if got := (Index{}).CoreUpdate("v0.7.1"); got != "" {
		t.Errorf("CoreUpdate without a catalogue answer = %q, want empty", got)
	}
}
