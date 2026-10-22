package agent

import (
	"strings"
	"testing"
)

// The two causes of an ignored section need opposite advice. Getting this wrong
// is not cosmetic: an operator whose config still carries the retired jira
// section, told to "upgrade", upgrades into the version that removed it and
// then opens a ticket.
func TestIgnoredSectionsAreExplainedByCause(t *testing.T) {
	lines := ignoredSectionNotices([]string{"jira", "slack"})
	if len(lines) != 2 {
		t.Fatalf("notices = %v, want one per section", lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "jira") || !strings.Contains(joined, "каталог") {
		t.Errorf("the retired section must point at its replacement: %q", joined)
	}
	if strings.Contains(lines[0], "upgrade") {
		t.Errorf("a retired section must not advise an upgrade: %q", lines[0])
	}
	if !strings.Contains(lines[1], "slack") || !strings.Contains(lines[1], "upgrade") {
		t.Errorf("a section from the future must advise an upgrade: %q", lines[1])
	}
}

func TestNothingIgnoredSaysNothing(t *testing.T) {
	if lines := ignoredSectionNotices(nil); len(lines) != 0 {
		t.Errorf("notices = %v, want none", lines)
	}
}
