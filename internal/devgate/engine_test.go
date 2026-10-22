package devgate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #439: Buff Code's engine duplicates every write to its own database and sends the rows
// here; the gateway relays them with the developer's token and work context, unchanged.
func TestEngineRowsRelayedWithTokenAndContext(t *testing.T) {
	link := &fakeLink{reply: map[string]any{"ok": true, "applied": 2}}
	h := Handler(link)
	req := httptest.NewRequest(http.MethodPost, "/v1/engine/rows",
		strings.NewReader(`{"source":"/home/ivan/.local/share/buff/opencode.db","rows":[{"seq":1,"table":"kv","op":"insert","key":["k"],"row":{"key":"k"}},{"seq":2,"table":"kv","op":"delete","key":["k"],"row":{"key":"k"}}]}`))
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("X-Buff-Org", "acme")
	req.Header.Set("X-Buff-Project", "shop")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(link.calls) != 1 || link.calls[0].subject != "data.cmd.developer_engine_rows" {
		t.Fatalf("calls: %+v", link.calls)
	}
	p := link.calls[0].payload
	if p["token"] != "dev-token" || p["org"] != "acme" || p["project"] != "shop" || p["source"] != "/home/ivan/.local/share/buff/opencode.db" {
		t.Fatalf("payload: %+v", p)
	}
	if rows, ok := p["rows"].([]any); !ok || len(rows) != 2 {
		t.Fatalf("rows: %+v", p["rows"])
	}
}

func TestEngineRowsRefusedWithoutToken(t *testing.T) {
	link := &fakeLink{reply: map[string]any{"ok": true}}
	rec, _ := post(t, Handler(link), "/v1/engine/rows", `{"source":"/x","rows":[]}`)
	if rec.Code != http.StatusUnauthorized || len(link.calls) != 0 {
		t.Fatalf("status %d, calls %d", rec.Code, len(link.calls))
	}
}

// A refusal of the backend (not a member, no context) comes back as 403 with its words, so
// the sender keeps the rows and tries again later rather than dropping them.
func TestEngineRowsBackendRefusalIsNotSuccess(t *testing.T) {
	link := &fakeLink{reply: map[string]any{"ok": false, "error": "организация не из вашего списка"}, err: context.DeadlineExceeded}
	req := httptest.NewRequest(http.MethodPost, "/v1/engine/rows", strings.NewReader(`{"source":"/x","rows":[]}`))
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("X-Buff-Org", "other")
	rec := httptest.NewRecorder()
	Handler(link).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}
