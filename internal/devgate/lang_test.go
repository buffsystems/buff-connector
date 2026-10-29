package devgate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

// mcpPost sends one JSON-RPC message to a gateway path as Buff Code does, naming its language
// when lang is not empty.
func mcpPost(t *testing.T, h http.Handler, path, session, lang, body string) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	withContext(r)
	if lang != "" {
		r.Header.Set(i18n.LangHeader, lang)
	}
	if session != "" {
		r.Header.Set("Mcp-Session-Id", session)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	if session == "" {
		m["session"] = w.Header().Get("Mcp-Session-Id")
	}
	return m
}

func listed(m map[string]any) map[string]map[string]any {
	res, _ := m["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	out := map[string]map[string]any{}
	for _, tl := range tools {
		tool := tl.(map[string]any)
		out[str(tool["name"])] = tool
	}
	return out
}

func paramText(tool map[string]any, name string) string {
	props, _ := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
	p, _ := props[name].(map[string]any)
	return str(p["description"])
}

// A developer working in English is told about every tool in English; one whose Buff Code
// names no language hears the connector's own (Russian here).
func TestToolsAreDescribedInTheCallersLanguage(t *testing.T) {
	gates := map[string]http.Handler{
		"/v1/knowledge/mcp": knowGate(&knowLink{}),
		"/v1/repos/mcp":     knowGate(&repoLink{}),
		"/v1/mcp":           mcpGate(&whoLink{login: "ivan", orgs: []any{"acme"}}, &fakeTools{}),
	}
	for path, h := range gates {
		for lang, want := range map[string]string{"en": "", "": "ru", "ru": "ru"} {
			s := str(mcpPost(t, h, path, "", lang, initNoForms)["session"])
			tools := listed(mcpPost(t, h, path, s, lang, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
			if len(tools) == 0 {
				t.Fatalf("%s %q: no tools", path, lang)
			}
			for name, tool := range tools {
				d := str(tool["description"])
				if hasCyrillic(d) != (want == "ru") {
					t.Errorf("%s %q: %s is described %q", path, lang, name, d)
				}
			}
		}
	}
	h := knowGate(&knowLink{})
	s := str(mcpPost(t, h, "/v1/knowledge/mcp", "", "en", initNoForms)["session"])
	tools := listed(mcpPost(t, h, "/v1/knowledge/mcp", s, "en", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if got := paramText(tools["write_entry"], "scope"); got != "org, or the name of a project/space from the work context" {
		t.Errorf("a parameter in English: %q", got)
	}
}

// Every tool of the gateway is described, with every parameter, in both languages.
func TestEveryGatewayToolIsDescribedInBothLanguages(t *testing.T) {
	for _, set := range []toolSet{knowledgeTools, reposTools} {
		for _, l := range []i18n.Lang{i18n.RU, i18n.EN} {
			for _, tool := range set.in(l) {
				if strings.TrimSpace(tool.Description) == "" {
					t.Errorf("%s %s: no description", l, tool.Name)
				}
				var schema struct {
					Properties map[string]struct {
						Description string `json:"description"`
					} `json:"properties"`
				}
				_ = json.Unmarshal(tool.InputSchema, &schema)
				for name, p := range schema.Properties {
					if strings.TrimSpace(p.Description) == "" {
						t.Errorf("%s %s.%s: no description", l, tool.Name, name)
					}
					if l == i18n.EN && hasCyrillic(p.Description) {
						t.Errorf("%s.%s: not English: %q", tool.Name, name, p.Description)
					}
				}
			}
		}
	}
}

// A tool's answer and its refusal reach the model in the caller's language.
func TestResultsAndErrorsAreInTheCallersLanguage(t *testing.T) {
	h := knowGate(&knowLink{})
	s := str(mcpPost(t, h, "/v1/knowledge/mcp", "", "en", initNoForms)["session"])
	m := mcpPost(t, h, "/v1/knowledge/mcp", s, "en", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_entry","arguments":{"name":" "}}}`)
	if got := resultText(m); got != "knowledge_read_entry: name is not set" {
		t.Errorf("English refusal: %q", got)
	}
	m = mcpPost(t, h, "/v1/knowledge/mcp", s, "en", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"write_entry","arguments":{"name":"a","title":"b","description":"c","type":"fact","scope":"org","body":"d"}}}`)
	if got := resultText(m); !strings.HasPrefix(got, "this client cannot ask the user") {
		t.Errorf("English refusal: %q", got)
	}
	m = mcpPost(t, h, "/v1/knowledge/mcp", s, "", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"read_entry","arguments":{"name":" "}}}`)
	if got := resultText(m); got != "knowledge_read_entry: name не задан" {
		t.Errorf("no language named — the connector's own: %q", got)
	}

	r := knowGate(&repoLink{})
	rs := str(mcpPost(t, r, "/v1/repos/mcp", "", "en", initNoForms)["session"])
	m = mcpPost(t, r, "/v1/repos/mcp", rs, "en", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"grep","arguments":{"pattern":"port"}}}`)
	if got := resultText(m); !strings.Contains(got, "[The output is not finished: call this tool again with the same arguments and cursor=7]") {
		t.Errorf("English page note: %q", got)
	}

	tools := &fakeTools{}
	g := mcpGate(&whoLink{login: "ivan", orgs: []any{"acme"}}, tools)
	gs := str(mcpPost(t, g, "/v1/mcp", "", "en", initNoForms)["session"])
	mcpPost(t, g, "/v1/mcp", gs, "en", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"files__read","arguments":{}}}`)
	if tools.lang != i18n.EN {
		t.Errorf("the connector's tools were called in %s", tools.lang)
	}
}

// The backend answers knowledge and repository commands in the language a command names and
// remembers it as the person's — so it is sent only when Buff Code named one.
func TestTheBackendIsToldTheLanguageOnlyWhenNamed(t *testing.T) {
	for lang, want := range map[string]any{"en": "en", "": nil} {
		link := &knowLink{}
		h := knowGate(link)
		s := str(mcpPost(t, h, "/v1/knowledge/mcp", "", lang, initNoForms)["session"])
		mcpPost(t, h, "/v1/knowledge/mcp", s, lang, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_entry","arguments":{"name":"sborka"}}}`)
		for _, p := range append(link.asked("data.cmd.developer_knowledge"), link.asked("data.cmd.developer_knowledge_entry")...) {
			if p["lang"] != want {
				t.Errorf("X-Buff-Lang %q: the command carried lang %v", lang, p["lang"])
			}
		}
	}
}

func resultText(m map[string]any) string {
	res, _ := m["result"].(map[string]any)
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	return str(content[0].(map[string]any)["text"])
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if r >= 'А' && r <= 'я' || r == 'ё' || r == 'Ё' {
			return true
		}
	}
	return false
}
