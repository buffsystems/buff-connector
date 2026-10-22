package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// The command line is not a lesser surface: a connector on a headless server is
// the normal case. It used to write every credential as a bearer token whatever
// the plugin's manifest said, so a Redmine connection configured by script sent
// `Authorization: Bearer …` to a tracker that accepts its key only in
// `X-Redmine-API-Key`, and a Jira Cloud one authenticated as nobody.
func TestCredentialSpecComesFromTheManifest(t *testing.T) {
	man := &plugin.Manifest{Creds: []plugin.CredSpec{
		{Name: "api", Kinds: []string{"header"}, Header: "X-Redmine-API-Key"},
	}}
	kinds, header := credentialSpec(man, "api")
	got := config.CredentialFor("api", kinds, header, "", "/secrets/api.token")
	if got.Kind != "header" || got.Header != "X-Redmine-API-Key" {
		t.Fatalf("credential = %+v", got)
	}
}

// A plugin the store cannot resolve must not silently produce a bearer token
// either: the caller is told, and bearer stays what an unnamed credential means.
func TestAnUnknownCredentialFallsBackToBearer(t *testing.T) {
	man := &plugin.Manifest{Creds: []plugin.CredSpec{{Name: "api", Kinds: []string{"basic"}}}}
	kinds, header := credentialSpec(man, "other")
	if kinds != nil || header != "" {
		t.Fatalf("kinds = %v, header = %q", kinds, header)
	}
	if got := config.CredentialFor("other", kinds, header, "", "/f"); got.Kind != "bearer" {
		t.Errorf("kind = %q", got.Kind)
	}
	if kinds, _ := credentialSpec(nil, "api"); kinds != nil {
		t.Errorf("a missing manifest must name no kinds, got %v", kinds)
	}
}

// The notes for the model come in three ways — inline, from a file, or from
// stdin through "-" — and the bound is lifted only when the operator says so
// in words. The flag's absence must leave a saved note alone: `instance set
// --alias` on a connection with notes must not wipe them.
func TestAINotesFlagsEditOnlyWhatWasGiven(t *testing.T) {
	inst := config.PluginInstance{AINotes: "старое", AINotesUnbounded: true}
	if err := applyAINotes(&inst, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if inst.AINotes != "старое" || !inst.AINotesUnbounded {
		t.Fatalf("flags not given changed the notes: %+v", inst)
	}
	if err := applyAINotes(&inst, "новое", "", "false"); err != nil {
		t.Fatal(err)
	}
	if inst.AINotes != "новое" || inst.AINotesUnbounded {
		t.Fatalf("inline note not applied: %+v", inst)
	}
	if err := applyAINotes(&inst, "", "", "yes"); err == nil {
		t.Fatal("an unbounded value that is not true/false was accepted")
	}

	file := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(file, []byte("из файла\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyAINotes(&inst, "", file, "true"); err != nil {
		t.Fatal(err)
	}
	if inst.AINotes != "из файла\n" || !inst.AINotesUnbounded {
		t.Fatalf("note from a file not applied: %+v", inst)
	}
	if err := applyAINotes(&inst, "оба", file, ""); err == nil {
		t.Fatal("inline and file at once must be refused: which one did they mean?")
	}
}
