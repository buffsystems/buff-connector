package config

import "testing"

// A section this build cannot read has two very different causes, and the
// advice for them is opposite. "slack" means the config came from a NEWER
// connector: upgrade. "jira" means it came from an OLDER one: the section was
// retired, and telling that operator to upgrade sends them to the version that
// removed it.
func TestARetiredSectionIsToldApartFromOneFromTheFuture(t *testing.T) {
	note, ok := RetiredSection("jira")
	if !ok {
		t.Fatal("the built-in jira section must be recognised as retired")
	}
	if note == "" {
		t.Error("a retired section must say what replaced it")
	}
	if _, ok := RetiredSection("slack"); ok {
		t.Error("an unknown section must not be reported as retired")
	}
}
