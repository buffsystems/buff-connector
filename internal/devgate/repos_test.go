package devgate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// repoLink answers the repository commands like the backend does (#372) and keeps what it was asked.
type repoLink struct {
	mu  sync.Mutex
	got map[string][]map[string]any
}

func (k *repoLink) Request(_ context.Context, subject string, p map[string]any) (map[string]any, error) {
	k.mu.Lock()
	if k.got == nil {
		k.got = map[string][]map[string]any{}
	}
	k.got[subject] = append(k.got[subject], p)
	k.mu.Unlock()
	switch subject {
	case "data.cmd.developer_whoami":
		return map[string]any{"ok": true, "login": "ivan", "orgs": []any{"acme"}}, nil
	case "data.cmd.developer_repos":
		return map[string]any{"ok": true, "instructions": "РЕПОЗИТОРИИ " + str(p["project"]), "repos": []any{}}, nil
	case "data.cmd.developer_repo_glob":
		return map[string]any{"ok": true, "output": "sources/cms/src/app.ts", "next": nil}, nil
	case "data.cmd.developer_repo_grep":
		return map[string]any{"ok": true, "output": "Found 1 matches\nsources/cms/src/app.ts:\n  Line 1: export const port = 8080", "next": float64(7)}, nil
	case "data.cmd.developer_repo_read":
		if p["path"] == "nope.ts" {
			err := errors.New("File not found: nope.ts")
			return map[string]any{"ok": false, "error": err.Error()}, err
		}
		return map[string]any{"ok": true, "output": "1: export const port = 8080", "next": nil}, nil
	}
	return nil, errors.New("unexpected " + subject)
}

func (k *repoLink) asked(subject string) []map[string]any {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.got[subject]
}

func rrpc(t *testing.T, h http.Handler, session, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/repos/mcp", strings.NewReader(body))
	withContext(r)
	if session != "" {
		r.Header.Set("Mcp-Session-Id", session)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w, m
}

func reposSession(t *testing.T, h http.Handler) string {
	t.Helper()
	w, _ := rrpc(t, h, "", initNoForms)
	if s := w.Header().Get("Mcp-Session-Id"); s != "" {
		return s
	}
	t.Fatalf("no session: %s", w.Body)
	return ""
}

func TestReposStartWithTheProjectsRepositories(t *testing.T) {
	link := &repoLink{}
	w, m := rrpc(t, knowGate(link), "", initNoForms)
	res, _ := m["result"].(map[string]any)
	if w.Code != 200 || res["instructions"] != "РЕПОЗИТОРИИ shop" {
		t.Fatalf("initialize: %d %v", w.Code, m)
	}
	p := link.asked("data.cmd.developer_repos")[0]
	if p["token"] != "DEV" || p["org"] != "acme" || p["project"] != "shop" {
		t.Fatalf("context relayed: %v", p)
	}
}

func TestReposToolsAreReadOnlyStrictAndSayWhenFirst(t *testing.T) {
	h := knowGate(&repoLink{})
	s := reposSession(t, h)
	_, m := rrpc(t, h, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools, _ := m["result"].(map[string]any)["tools"].([]any)
	names := []string{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		names = append(names, str(tool["name"]))
		if ann, _ := tool["annotations"].(map[string]any); ann["readOnlyHint"] != true {
			t.Fatalf("%s must be read-only: %v", tool["name"], tool)
		}
		schema := tool["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		req, _ := schema["required"].([]any)
		if schema["additionalProperties"] != false || len(props) != len(req) {
			t.Fatalf("%s: strict schema, every property required: %v", tool["name"], schema)
		}
		first, _, _ := strings.Cut(str(tool["description"]), "\n")
		if n := len([]rune(first)); n > 120 || !strings.Contains(strings.ToLower(first), "вызывай") {
			t.Fatalf("%s: first line must hold the trigger within 120 chars (%d): %q", tool["name"], n, first)
		}
	}
	if strings.Join(names, ",") != "glob,grep,read" {
		t.Fatalf("tools: %v", names)
	}
}

func TestReposToolsTakeOpencodesOwnInputPlusBranchAndCursor(t *testing.T) {
	want := map[string]string{
		"glob": "branch,cursor,hidden,limit,path,pattern",
		"grep": "branch,caseSensitive,cursor,include,limit,literal,path,pattern",
		"read": "branch,cursor,limit,offset,path",
	}
	for _, tool := range reposTools {
		var schema struct{ Properties map[string]any }
		_ = json.Unmarshal(tool.InputSchema, &schema)
		keys := []string{}
		for k := range schema.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if got := strings.Join(keys, ","); got != want[tool.Name] {
			t.Fatalf("%s: %s, want %s", tool.Name, got, want[tool.Name])
		}
	}
}

func TestReposRelaysEachReadWithItsFieldsAndTheContext(t *testing.T) {
	link := &repoLink{}
	h := knowGate(link)
	s := reposSession(t, h)
	_, m := rrpc(t, h, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"grep","arguments":{"pattern":"port =","path":"","include":"*.ts","literal":false,"caseSensitive":false,"limit":0,"branch":"","cursor":0,"repo":"x"}}}`)
	text := w2s(m["result"])
	if !strings.Contains(text, "Line 1: export const port = 8080") || !strings.Contains(text, "cursor=7") {
		t.Fatalf("grep: the tool's own text, then where it continues: %v", m)
	}
	p := link.asked("data.cmd.developer_repo_grep")[0]
	if p["pattern"] != "port =" || p["include"] != "*.ts" || p["caseSensitive"] != false || p["project"] != "shop" || p["token"] != "DEV" {
		t.Fatalf("grep relayed: %v", p)
	}
	if _, ok := p["repo"]; ok {
		t.Fatalf("a field outside the schema must not travel: %v", p)
	}
	_, m = rrpc(t, h, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read","arguments":{"path":"sources/cms/src/app.ts","branch":"task/12","offset":10,"limit":5,"cursor":0}}}`)
	if text := w2s(m["result"]); !strings.Contains(text, "1: export const port = 8080") || strings.Contains(text, "cursor=") {
		t.Fatalf("read: whole answer, no continuation: %v", m)
	}
	r := link.asked("data.cmd.developer_repo_read")[0]
	if r["path"] != "sources/cms/src/app.ts" || r["branch"] != "task/12" || r["offset"] != float64(10) || r["limit"] != float64(5) {
		t.Fatalf("read relayed: %v", r)
	}
}

func TestReposRefusalReachesTheModel(t *testing.T) {
	h := knowGate(&repoLink{})
	s := reposSession(t, h)
	_, m := rrpc(t, h, s, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"read","arguments":{"path":"nope.ts","branch":"","offset":0,"limit":0,"cursor":0}}}`)
	res, _ := m["result"].(map[string]any)
	if res["isError"] != true || !strings.Contains(w2s(res), "nope.ts") {
		t.Fatalf("refusal: %v", m)
	}
}
