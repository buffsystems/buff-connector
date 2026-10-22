package agent

import (
	"testing"
)

func TestParseReplyJobReadsEveryPart(t *testing.T) {
	job := map[string]any{"id": "j1", "kind": "jira_reply", "spec": map[string]any{
		"integration":    "corp",
		"issue_key":      "PROJ-7",
		"comment":        "готово, отчёт во вложении",
		"link_url":       "https://buff.systems/app/dev/42",
		"link_title":     "Исследование #42",
		"link_icon":      "https://buff.systems/favicon.ico",
		"file_name":      "Исследование.pdf",
		"has_file":       true,
		"document":       "# Почему падает экспорт\n\nДело в кодировке.",
		"document_title": "Почему падает экспорт",
	}}
	got := parseReplyJob(job)
	want := replyJob{
		Integration: "corp", IssueKey: "PROJ-7",
		Comment:   "готово, отчёт во вложении",
		LinkURL:   "https://buff.systems/app/dev/42",
		LinkTitle: "Исследование #42",
		LinkIcon:  "https://buff.systems/favicon.ico",
		FileName:  "Исследование.pdf", HasFile: true,
		// The text itself travels too, for a source that can hold a page.
		Document:      "# Почему падает экспорт\n\nДело в кодировке.",
		DocumentTitle: "Почему падает экспорт",
	}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

// An acknowledgement is a reply with nothing but a comment; every other part
// must stay empty rather than defaulting to something that would be posted.
func TestParseJiraReplyToleratesAnAcknowledgement(t *testing.T) {
	got := parseReplyJob(map[string]any{"id": "j1", "spec": map[string]any{
		"integration": "corp", "issue_key": "PROJ-7", "comment": "принял, работаю",
	}})
	if got.Comment != "принял, работаю" {
		t.Errorf("comment = %q", got.Comment)
	}
	if got.HasFile || got.LinkURL != "" || got.FileName != "" {
		t.Errorf("got %+v, want only a comment", got)
	}
}

func TestParseReplyJobToleratesAMissingSpec(t *testing.T) {
	if got := parseReplyJob(map[string]any{"id": "j1"}); got != (replyJob{}) {
		t.Errorf("got %+v, want the zero value", got)
	}
}
