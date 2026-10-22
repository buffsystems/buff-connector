package natslink

import (
	"encoding/json"
	"testing"
)

func TestReplySubjectShape(t *testing.T) {
	if got := replySubject("gid123", 7); got != "_INBOX.gid123.7" {
		t.Fatalf("replySubject = %q", got)
	}
}

func TestEnvelopeMergesReply(t *testing.T) {
	b, err := envelope(map[string]any{"op": "connector_claim", "code": "bufc_x"}, "_INBOX.g.1")
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["op"] != "connector_claim" || m["code"] != "bufc_x" {
		t.Fatalf("payload not preserved: %v", m)
	}
	if m["reply"] != "_INBOX.g.1" {
		t.Fatalf("reply not set: %v", m["reply"])
	}
}

func TestEnvelopeDoesNotMutateInput(t *testing.T) {
	in := map[string]any{"op": "x"}
	if _, err := envelope(in, "_INBOX.g.1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := in["reply"]; ok {
		t.Fatal("envelope mutated the caller's payload")
	}
}

func TestNewGidShape(t *testing.T) {
	g := newGid()
	// 16 random bytes hex = 32 chars, within the middleware's [8,64] guest-id bound.
	if len(g) != 32 {
		t.Fatalf("gid length = %d", len(g))
	}
	for _, c := range g {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("gid has non-hex char %q", c)
		}
	}
	if newGid() == g {
		t.Fatal("gids should be unique")
	}
}
