package devgate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// whoLink answers developer_whoami like the backend does (#368).
type whoLink struct {
	calls int
	login string
	orgs  []any
	err   error
}

func (w *whoLink) Request(_ context.Context, subject string, payload map[string]any) (map[string]any, error) {
	if subject != "data.cmd.developer_whoami" {
		return nil, errors.New("unexpected " + subject)
	}
	w.calls++
	if w.err != nil {
		return map[string]any{"ok": false, "error": w.err.Error()}, w.err
	}
	return map[string]any{"ok": true, "login": w.login, "orgs": w.orgs}, nil
}

type fakeTools struct {
	called []string
	caller string
	args   string
	fail   error
}

func (f *fakeTools) Catalog() []Tool {
	return []Tool{
		{Name: "files__read", Description: "Read a file.", InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnly: true},
		{Name: "jira__create_issue", Description: "Create an issue.", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
}

func (f *fakeTools) Instructions() string { return "files — файлы отдела" }

func (f *fakeTools) Call(_ context.Context, name string, args json.RawMessage, caller string) (json.RawMessage, error) {
	f.called = append(f.called, name)
	f.caller, f.args = caller, string(args)
	if f.fail != nil {
		return nil, f.fail
	}
	return json.RawMessage(`{"content":[{"type":"text","text":"hello"}]}`), nil
}

func mcpGate(link Requester, tools Tools) http.Handler {
	return HandlerWithTools(link, tools, "acme")
}

func rpc(t *testing.T, h http.Handler, session, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer DEV")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		r.Header.Set("Mcp-Session-Id", session)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w, m
}

func initialize(t *testing.T, h http.Handler) string {
	t.Helper()
	w, m := rpc(t, h, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"buff","version":"1"}}}`)
	if w.Code != 200 {
		t.Fatalf("initialize: %d %s", w.Code, w.Body)
	}
	res, _ := m["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocol: %v", m)
	}
	if caps, _ := res["capabilities"].(map[string]any); caps["tools"] == nil {
		t.Fatalf("no tools capability: %v", res)
	}
	if res["instructions"] != "files — файлы отдела" {
		t.Fatalf("instructions: %v", res)
	}
	session := w.Header().Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("initialize must open a session")
	}
	return session
}

func TestMCPNeedsTheDevelopersToken(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	w := httptest.NewRecorder()
	mcpGate(&whoLink{login: "ivan", orgs: []any{"acme"}}, &fakeTools{}).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestMCPRefusesARevokedDeviceAndAnotherOrganisation(t *testing.T) {
	w, _ := rpc(t, mcpGate(&whoLink{err: errors.New("Устройство отозвано — войдите заново.")}, &fakeTools{}), "", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "отозвано") {
		t.Fatalf("revoked: %d %s", w.Code, w.Body)
	}
	w, _ = rpc(t, mcpGate(&whoLink{login: "ivan", orgs: []any{"globex"}}, &fakeTools{}), "", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("another org: %d %s", w.Code, w.Body)
	}
}

func TestMCPRefusesWorkBeforeTheHandshake(t *testing.T) {
	_, m := rpc(t, mcpGate(&whoLink{login: "ivan", orgs: []any{"acme"}}, &fakeTools{}), "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if e, _ := m["error"].(map[string]any); e == nil {
		t.Fatalf("tools/list without a session must be refused: %v", m)
	}
}

func TestMCPUnknownSessionIs404SoTheClientStartsANewOne(t *testing.T) {
	// The connector restarted: the session Buff Code holds is gone. MCP says 404 — the client
	// then initializes again by itself; a 400 left Buff Code without tools until its restart.
	s := initialize(t, mcpGate(&whoLink{login: "ivan", orgs: []any{"acme"}}, &fakeTools{}))
	restarted := mcpGate(&whoLink{login: "ivan", orgs: []any{"acme"}}, &fakeTools{})
	if w, _ := rpc(t, restarted, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown session: want 404, got %d %s", w.Code, w.Body)
	}
	if w, _ := rpc(t, restarted, "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no session header: want 400, got %d", w.Code)
	}
}

func TestMCPListsTheConnectorsToolsWithReadOnlyHints(t *testing.T) {
	h := mcpGate(&whoLink{login: "ivan", orgs: []any{"acme"}}, &fakeTools{})
	s := initialize(t, h)
	if w, _ := rpc(t, h, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); w.Code != http.StatusAccepted {
		t.Fatalf("initialized: %d", w.Code)
	}
	_, m := rpc(t, h, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	res, _ := m["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools: %v", m)
	}
	first := tools[0].(map[string]any)
	if first["name"] != "files__read" || first["inputSchema"] == nil {
		t.Fatalf("tool: %v", first)
	}
	if ann, _ := first["annotations"].(map[string]any); ann["readOnlyHint"] != true {
		t.Fatalf("read-only hint: %v", first)
	}
	if ann, _ := tools[1].(map[string]any)["annotations"].(map[string]any); ann["readOnlyHint"] != false {
		t.Fatalf("write tool hint: %v", tools[1])
	}
}

func TestMCPCallsAToolAsTheDeveloper(t *testing.T) {
	tools := &fakeTools{}
	h := mcpGate(&whoLink{login: "ivan", orgs: []any{"acme"}}, tools)
	s := initialize(t, h)
	_, m := rpc(t, h, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"files__read","arguments":{"path":"a.md"}}}`)
	res, _ := m["result"].(map[string]any)
	if res == nil || tools.caller != "ivan" || tools.called[0] != "files__read" || tools.args != `{"path":"a.md"}` {
		t.Fatalf("call: %v %+v", m, tools)
	}
}

func TestMCPToolFailureIsAResultTheModelCanRead(t *testing.T) {
	tools := &fakeTools{fail: errors.New("files__read is not a read-only tool")}
	h := mcpGate(&whoLink{login: "ivan", orgs: []any{"acme"}}, tools)
	s := initialize(t, h)
	_, m := rpc(t, h, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"files__read","arguments":{}}}`)
	res, _ := m["result"].(map[string]any)
	if res["isError"] != true || !strings.Contains(w2s(res["content"]), "read-only") {
		t.Fatalf("tool error: %v", m)
	}
}

func TestMCPUnknownMethod(t *testing.T) {
	h := mcpGate(&whoLink{login: "ivan", orgs: []any{"acme"}}, &fakeTools{})
	s := initialize(t, h)
	_, m := rpc(t, h, s, `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`)
	if e, _ := m["error"].(map[string]any); e == nil || e["code"] != float64(-32601) {
		t.Fatalf("unknown method: %v", m)
	}
}

func TestMCPAsksTheBackendAtMostOnceAMinutePerToken(t *testing.T) {
	link := &whoLink{login: "ivan", orgs: []any{"acme"}}
	h := mcpGate(link, &fakeTools{})
	s := initialize(t, h)
	rpc(t, h, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if link.calls != 1 {
		t.Fatalf("whoami calls within a minute: %d", link.calls)
	}
	defer func(old func() time.Time) { now = old }(now)
	now = func() time.Time { return time.Now().Add(61 * time.Second) }
	rpc(t, h, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if link.calls != 2 {
		t.Fatalf("after a minute the backend is asked again: %d", link.calls)
	}
}

func TestMCPOffersNoServerStream(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/mcp", nil)
	r.Header.Set("Authorization", "Bearer DEV")
	w := httptest.NewRecorder()
	mcpGate(&whoLink{login: "ivan", orgs: []any{"acme"}}, &fakeTools{}).ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", w.Code)
	}
}

func w2s(v any) string { b, _ := json.Marshal(v); return string(b) }
