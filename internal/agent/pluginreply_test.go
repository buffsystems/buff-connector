package agent

import (
	"encoding/base64"
	"testing"

	"github.com/buffsystems/buff-connector/internal/config"
)

// A reply job names the connection it belongs to, by GUID. An answer for a
// connection this connector does not serve must not be delivered through
// whichever other connection happens to be at hand — the alias is the
// operator's label, the GUID is the identity.
func TestReplyJobIsRoutedToTheConnectionThatRaisedIt(t *testing.T) {
	cfg := &config.Config{Plugins: []config.PluginInstance{
		{ID: "7QK3M9XBWZ0FVT2H", Slug: "jira", Alias: "corp"},
	}}

	if p, ok := instanceByID(cfg, "7QK3M9XBWZ0FVT2H"); !ok || p.Slug != "jira" {
		t.Error("a job for a plugin connection did not find it")
	}
	if _, ok := instanceByID(cfg, "corp"); ok {
		t.Error("a job resolved by alias instead of by GUID")
	}
	if _, ok := instanceByID(cfg, "J4RD8N1VC6PQY5ZE"); ok {
		t.Error("a job for a connection we do not serve resolved anyway")
	}
}

// The plugin's reply contract is its own vocabulary, not the platform's job
// shape. This is where one becomes the other.
func TestReplyParamsCarryEveryPartOfTheAnswer(t *testing.T) {
	r := replyJob{
		IssueKey: "PROJ-8", Comment: "Разобрался: дело в кодировке.",
		LinkURL: "https://buff.systems/app/x", LinkTitle: "Исследование",
		FileName: "report.pdf", HasFile: true,
		Document: "# Кодировка\n\nЭкспорт пишет в cp1251.", DocumentTitle: "Кодировка",
	}
	params := pluginReplyParams(r, []byte("PDF-BYTES"))

	if params["issue"] != "PROJ-8" || params["text"] != "Разобрался: дело в кодировке." {
		t.Fatalf("params = %+v", params)
	}
	link, _ := params["link"].(map[string]any)
	if link["url"] != "https://buff.systems/app/x" || link["title"] != "Исследование" {
		t.Errorf("link = %+v", link)
	}
	att, _ := params["attachment"].(map[string]any)
	if att["filename"] != "report.pdf" {
		t.Errorf("attachment = %+v", att)
	}
	// The document travels base64: JSON-RPC carries text, and the plugin decodes
	// it on the other side.
	if got, _ := att["base64"].(string); got != base64.StdEncoding.EncodeToString([]byte("PDF-BYTES")) {
		t.Errorf("attachment content = %q", got)
	}
	// The finished text goes along as a document with its title, so a source
	// that can hold a page (Notion) publishes the report itself, not a PDF of it.
	doc, _ := params["document"].(map[string]any)
	if doc["title"] != "Кодировка" || doc["markdown"] != "# Кодировка\n\nЭкспорт пишет в cp1251." {
		t.Errorf("document = %+v", doc)
	}
	// …and a reply without one carries no empty document for a plugin to trip on.
	if _, has := pluginReplyParams(replyJob{IssueKey: "X", Comment: "ok"}, nil)["document"]; has {
		t.Error("an absent document was sent as an empty one")
	}
}

// An answer with nothing in it is a job that would post an empty comment into a
// customer's ticket.
func TestAnEmptyReplyIsRefused(t *testing.T) {
	if err := (replyJob{IssueKey: "PROJ-8"}).validate(); err == nil {
		t.Error("a reply with no comment, link or document was accepted")
	}
	if err := (replyJob{Comment: "текст"}).validate(); err == nil {
		t.Error("a reply with no ticket was accepted")
	}
	if err := (replyJob{IssueKey: "PROJ-8", Comment: "текст"}).validate(); err != nil {
		t.Errorf("a complete reply was refused: %v", err)
	}
}

// A reply keeps the visibility of the request it answers: an internal note is
// answered with an internal note, never with a comment everyone sees
// (cloud-front#476). An ordinary comment sends nothing, as before.
func TestReplyKeepsTheVisibilityOfTheRequest(t *testing.T) {
	r := parseReplyJob(map[string]any{"spec": map[string]any{"issue_key": "PROJ-7", "comment": "ответ", "visibility": "internal"}})
	if got := pluginReplyParams(r, nil)["visibility"]; got != "internal" {
		t.Fatalf("visibility = %v, want internal", got)
	}
	if _, has := pluginReplyParams(replyJob{IssueKey: "X", Comment: "ok"}, nil)["visibility"]; has {
		t.Fatal("an ordinary comment must not send a visibility")
	}
}
