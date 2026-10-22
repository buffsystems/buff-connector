package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/buffsystems/buff-connector/internal/config"
)

func pluginEvent(id, at, action string) map[string]any {
	return map[string]any{
		"id": id, "at": at, "kind": "comment", "title": "Экспорт в CSV ломается",
		"body": "разберись почему", "author": "Пётр", "action": action,
		"url":   "https://jira.corp.local/browse/PROJ-8",
		"extra": map[string]any{"issue": "PROJ-8", "trigger": "mention"},
	}
}

func instanceFor(id string) config.PluginInstance {
	return config.PluginInstance{
		ID: id, Plugin: "jira", Version: "1.0.0", Slug: "jira",
		Alias: "Jira прод", DevProject: "buff/backend",
	}
}

// The platform is source-agnostic: it takes an integration, an event id, a
// ticket and an instruction. This is where a plugin's event becomes that.
func TestPluginEventBecomesAPlatformEvent(t *testing.T) {
	raw, err := json.Marshal(pluginEvent("comment:9@2026-09-04T10:00:00Z", "2026-09-04T10:00:00Z", "ask"))
	if err != nil {
		t.Fatal(err)
	}
	var ev PluginEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}

	p := buildPluginEventPayload("ACCESS", instanceFor("7QK3M9XBWZ0FVT2H"), ev, "v0.2.1")

	// Attribution is keyed on the GUID, never on the alias: renaming a
	// connection must not split a customer's spend history in two.
	if p["integration"] != "7QK3M9XBWZ0FVT2H" {
		t.Errorf("integration = %v, want the instance GUID", p["integration"])
	}
	if p["source"] != "jira" {
		t.Errorf("source = %v", p["source"])
	}
	if p["issue_key"] != "PROJ-8" {
		t.Errorf("issue_key = %v — the ticket is in extra.issue", p["issue_key"])
	}
	if p["instruction"] != "разберись почему" {
		t.Errorf("instruction = %v", p["instruction"])
	}
	if p["summary"] != "Экспорт в CSV ломается" {
		t.Errorf("summary = %v", p["summary"])
	}
	if p["action"] != "ask" || p["trigger"] != "mention" {
		t.Errorf("action/trigger = %v / %v", p["action"], p["trigger"])
	}
	if p["dev_project"] != "buff/backend" {
		t.Errorf("dev_project = %v", p["dev_project"])
	}
	// The ticket's address travels with the event so a draft can point back at
	// it. Without it the person picking the task up gets an instruction with no
	// way to see what it came from.
	if p["url"] != "https://jira.corp.local/browse/PROJ-8" {
		t.Errorf("url = %v", p["url"])
	}
}

// A question asked in a pull request has to be answered on the code of THAT
// pull request, so the repository and branch travel with the event. An older
// plugin sends neither, and the platform then answers on the default branch.
func TestPullRequestEventCarriesItsBranch(t *testing.T) {
	ev := PluginEvent{
		ID: "comment:3@2026-09-04T10:00:00Z", At: "2026-09-04T10:00:00Z", Kind: "comment",
		Title: "Экспорт", Body: "почему падает", Action: "ask",
		Extra: map[string]any{
			"issue": "acme/api#7", "trigger": "mention", "context": "pull",
			"repo": "acme/api", "ref": "feature/csv",
		},
	}
	if err := ev.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	p := buildPluginEventPayload("ACCESS", instanceFor("7QK3M9XBWZ0FVT2H"), ev, "v0.2.1")

	if p["context"] != "pull" {
		t.Errorf("context = %v", p["context"])
	}
	if p["repo"] != "acme/api" {
		t.Errorf("repo = %v", p["repo"])
	}
	if p["ref"] != "feature/csv" {
		t.Errorf("ref = %v", p["ref"])
	}
}

// A ticket carries no branch, and the platform then answers on the project's
// default branch.
func TestIssueEventCarriesNoBranch(t *testing.T) {
	ev := PluginEvent{
		ID: "comment:3@2026-09-04T10:00:00Z", At: "2026-09-04T10:00:00Z", Kind: "comment",
		Title: "Экспорт", Body: "почему падает", Action: "ask",
		Extra: map[string]any{"issue": "PROJ-8", "trigger": "mention"},
	}
	p := buildPluginEventPayload("ACCESS", instanceFor("7QK3M9XBWZ0FVT2H"), ev, "v0.2.1")
	if p["repo"] != "" || p["ref"] != "" {
		t.Errorf("repo/ref = %v / %v, want empty", p["repo"], p["ref"])
	}
	if p["context"] != "issue" {
		t.Errorf("context = %v, want the default", p["context"])
	}
}

// A branch named without a repository cannot be checked out anywhere, and a
// context nobody knows is a plugin bug. Both are refused beside the plugin.
func TestABranchWithoutARepositoryIsRefused(t *testing.T) {
	ev := PluginEvent{
		ID: "comment:3@2026-09-04T10:00:00Z", At: "2026-09-04T10:00:00Z", Kind: "comment",
		Body: "почему падает", Action: "ask",
		Extra: map[string]any{"issue": "acme/api#7", "ref": "feature/csv"},
	}
	if err := ev.validate(); err == nil {
		t.Error("a ref with no repo was accepted")
	}
	ev.Extra["repo"] = "acme/api"
	ev.Extra["context"] = "whatever"
	if err := ev.validate(); err == nil {
		t.Error("an unknown context was accepted")
	}
}

// An event with no ticket cannot be acted on, and the platform refuses it at the
// door. Better to say so here, next to the plugin that produced it.
func TestEventWithoutATicketIsRefusedBeforeItIsSent(t *testing.T) {
	ev := PluginEvent{ID: "x", At: "2026-09-04T10:00:00Z", Action: "ask", Body: "что-то"}
	if err := ev.validate(); err == nil {
		t.Fatal("an event with no ticket was accepted")
	}
	ev.Extra = map[string]any{"issue": "PROJ-1"}
	if err := ev.validate(); err != nil {
		t.Fatalf("a complete event was refused: %v", err)
	}
}

func TestUnknownActionIsRefused(t *testing.T) {
	ev := PluginEvent{
		ID: "x", At: "2026-09-04T10:00:00Z", Action: "delete-everything",
		Body: "…", Extra: map[string]any{"issue": "PROJ-1"},
	}
	if err := ev.validate(); err == nil {
		t.Fatal("an unknown action was accepted")
	}
}

// The watermark is keyed on the GUID for the same reason attribution is: an
// operator renaming a connection must not make the connector replay a tracker's
// back catalogue at everyone in it.
func TestWatermarkSurvivesARename(t *testing.T) {
	dir := t.TempDir()
	at := "2026-09-04T10:00:00Z"
	saveInstanceWatermark(dir, "7QK3M9XBWZ0FVT2H", at)

	if got := loadInstanceWatermark(dir, "7QK3M9XBWZ0FVT2H"); got != at {
		t.Fatalf("watermark = %q, want %q", got, at)
	}
	// Nothing about the alias appears in the path.
	if _, err := os.Stat(filepath.Join(dir, "instances", "7QK3M9XBWZ0FVT2H", "watermark")); err != nil {
		t.Errorf("watermark is not keyed on the GUID: %v", err)
	}
}

// A connection switched on today must not answer everything said in the tracker
// before it existed.
func TestAFreshInstanceStartsFromNow(t *testing.T) {
	got := loadInstanceWatermark(t.TempDir(), "J4RD8N1VC6PQY5ZE")
	ts, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("watermark %q is not a timestamp: %v", got, err)
	}
	if time.Since(ts) > time.Minute {
		t.Errorf("a fresh instance starts at %s, not now", got)
	}
}

// The watermark may only advance past events that were actually delivered.
// Advancing past a failed one loses the request silently, which is the failure
// mode that is impossible to notice from the outside.
func TestWatermarkAdvancesOnlyPastDeliveredEvents(t *testing.T) {
	events := []PluginEvent{
		{ID: "a", At: "2026-09-04T10:00:00Z"},
		{ID: "b", At: "2026-09-04T11:00:00Z"},
		{ID: "c", At: "2026-09-04T12:00:00Z"},
	}
	// b failed to reach the relay.
	got := highWatermark("2026-09-04T09:00:00Z", events, map[string]bool{"a": true, "c": true})
	if got != "2026-09-04T10:00:00Z" {
		t.Fatalf("watermark = %q, want the moment before the first failure", got)
	}

	all := highWatermark("2026-09-04T09:00:00Z", events, map[string]bool{"a": true, "b": true, "c": true})
	if all != "2026-09-04T12:00:00Z" {
		t.Fatalf("watermark = %q, want the last delivered event", all)
	}
}

// The mark must be set when the connection STARTS, not when it is first polled.
// A mark invented at first-poll time is later than the moment the operator
// switched the connection on, so the request they wrote to try it out is
// swallowed — and from the outside that is indistinguishable from the bot
// ignoring somebody.
func TestWatermarkIsSetWhenTheConnectionStarts(t *testing.T) {
	dir := t.TempDir()
	seedInstanceWatermark(dir, "7QK3M9XBWZ0FVT2H")
	seeded := loadInstanceWatermark(dir, "7QK3M9XBWZ0FVT2H")
	if seeded == "" {
		t.Fatal("no watermark was written at start")
	}

	// A restart must not move it: everything since the last read is still owed.
	time.Sleep(5 * time.Millisecond)
	seedInstanceWatermark(dir, "7QK3M9XBWZ0FVT2H")
	if again := loadInstanceWatermark(dir, "7QK3M9XBWZ0FVT2H"); again != seeded {
		t.Errorf("a restart moved the watermark from %q to %q", seeded, again)
	}
}

// The request carries who asked — login and address, not only the display name
// anyone can change — how it was written, and what the connection's list made of
// it, so the platform refuses before anything runs (cloud-front#476).
func TestPluginEventCarriesTheAskerAndTheVerdict(t *testing.T) {
	ev := PluginEvent{
		ID: "comment:9", At: "2026-10-05T10:00:00Z", Body: "что тут?", Action: "ask",
		Author: "Пётр", AuthorLogin: "petrov", AuthorEmail: "petrov@vendor.com", Visibility: "internal",
		Extra: map[string]any{"issue": "PROJ-7"},
	}
	open := buildPluginEventPayload("ACCESS", instanceFor("7QK3M9XBWZ0FVT2H"), ev, "v0.2.1")
	if open["author_login"] != "petrov" || open["author_email"] != "petrov@vendor.com" || open["visibility"] != "internal" {
		t.Fatalf("who asked and how did not travel: %v", open)
	}
	if open["askers"] != "open" {
		t.Fatalf("a connection with no list must stay open, got %v", open["askers"])
	}
	listed := instanceFor("7QK3M9XBWZ0FVT2H")
	listed.Askers = []string{"@company.ru"}
	if got := buildPluginEventPayload("ACCESS", listed, ev, "v0.2.1")["askers"]; got != "denied" {
		t.Fatalf("an author outside the list must be denied, got %v", got)
	}
	listed.AskersMembers = true
	if got := buildPluginEventPayload("ACCESS", listed, ev, "v0.2.1")["askers"]; got != "members" {
		t.Fatalf("with the members box the platform decides, got %v", got)
	}
}

// Jira Data Center writes a moment as `2026-10-04T23:19:56.846+0000` — an offset
// without the colon RFC 3339 wants. Saved as the watermark verbatim, it could not
// be read back, the next poll started from NOW, and a question written between two
// polls was lost (found on the stand, #476). A watermark is a moment, kept in one
// spelling, and moments are compared as moments.
func TestAWatermarkIsAMomentWhateverTheTrackerSpellsItAs(t *testing.T) {
	jira := []PluginEvent{{ID: "a", At: "2026-10-04T23:19:52.123+0000"}}
	got := highWatermark("2026-10-04T23:19:00Z", jira, map[string]bool{"a": true})
	if got != "2026-10-04T23:19:52.123Z" {
		t.Fatalf("watermark = %q, want the moment in UTC, RFC 3339", got)
	}
	dir := t.TempDir()
	saveInstanceWatermark(dir, "HV3M6XJBN9M1PVE2", got)
	if back := loadInstanceWatermark(dir, "HV3M6XJBN9M1PVE2"); back != got {
		t.Fatalf("the saved watermark read back as %q", back)
	}

	// 01:19:52 at +02:00 is 23:19:52 UTC — earlier than a string comparison says.
	local := []PluginEvent{{ID: "b", At: "2026-10-05T01:19:52+0200"}}
	if got := highWatermark("2026-10-04T23:19:55Z", local, map[string]bool{"b": true}); got != "2026-10-04T23:19:55Z" {
		t.Fatalf("watermark = %q, moved to an earlier moment written later", got)
	}
}

// A watermark already saved in Jira's spelling by an earlier connector is read as
// the moment it names, not as «now» — or the upgrade itself drops questions.
func TestAWatermarkSavedInJirasSpellingStillReads(t *testing.T) {
	dir := t.TempDir()
	saveInstanceWatermark(dir, "HV3M6XJBN9M1PVE2", "2026-10-04T23:19:52.123+0000")
	if got := loadInstanceWatermark(dir, "HV3M6XJBN9M1PVE2"); got != "2026-10-04T23:19:52.123Z" {
		t.Fatalf("watermark = %q, want the saved moment", got)
	}
}
