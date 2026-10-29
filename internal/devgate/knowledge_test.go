package devgate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

// knowLink answers the knowledge commands like the backend does (#371) and keeps what it was asked.
type knowLink struct {
	mu  sync.Mutex
	got map[string][]map[string]any
}

func (k *knowLink) Request(_ context.Context, subject string, p map[string]any) (map[string]any, error) {
	k.mu.Lock()
	if k.got == nil {
		k.got = map[string][]map[string]any{}
	}
	k.got[subject] = append(k.got[subject], p)
	k.mu.Unlock()
	switch subject {
	case "data.cmd.developer_whoami":
		return map[string]any{"ok": true, "login": "ivan", "orgs": []any{"acme"}}, nil
	case "data.cmd.developer_knowledge":
		return map[string]any{"ok": true, "instructions": "ЗНАНИЯ " + str(p["project"]), "entries": []any{map[string]any{"name": "sborka"}}}, nil
	case "data.cmd.developer_knowledge_entry":
		if p["name"] == "missing" { // as the backend answers it, and as natslink hands it over
			reply := map[string]any{"ok": true, "found": false, "error": "записи «missing» нет в базе знаний этого рабочего контекста"}
			return reply, errors.New("data.cmd.developer_knowledge_entry: " + reply["error"].(string))
		}
		return map[string]any{"ok": true, "found": true, "name": p["name"], "body": "ТЕЛО"}, nil
	case "data.cmd.developer_knowledge_create":
		if p["name"] == "zanyato" {
			err := errors.New("имя «zanyato» уже занято")
			return map[string]any{"ok": false, "error": err.Error()}, err
		}
		return map[string]any{"ok": true, "entry": map[string]any{"name": p["name"], "project": "shop", "author": "ivan", "author_kind": "human"}}, nil
	}
	return nil, errors.New("unexpected " + subject)
}

func (k *knowLink) asked(subject string) []map[string]any {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.got[subject]
}

func str(v any) string { s, _ := v.(string); return s }

func knowGate(link Requester) http.Handler { return HandlerWithTools(link, nil, "acme") }

func withContext(r *http.Request) {
	r.Header.Set("Authorization", "Bearer DEV")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("X-Buff-Org", "acme")
	r.Header.Set("X-Buff-Project", "shop")
	r.Header.Set("X-Buff-Spaces", "handbook,wiki")
}

func krpc(t *testing.T, h http.Handler, session, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/knowledge/mcp", strings.NewReader(body))
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

const initNoForms = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"buff","version":"1"}}}`
const initWithForms = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"elicitation":{}},"clientInfo":{"name":"buff","version":"1"}}}`

func TestKnowledgeStartsWithTheBlockForTheWorkContext(t *testing.T) {
	link := &knowLink{}
	w, m := krpc(t, knowGate(link), "", initNoForms)
	res, _ := m["result"].(map[string]any)
	if w.Code != 200 || res["instructions"] != "ЗНАНИЯ shop" {
		t.Fatalf("initialize: %d %v", w.Code, m)
	}
	p := link.asked("data.cmd.developer_knowledge")[0]
	if p["token"] != "DEV" || p["org"] != "acme" || p["project"] != "shop" || w2s(p["spaces"]) != `["handbook","wiki"]` {
		t.Fatalf("context relayed: %v", p)
	}
}

func TestKnowledgeWithoutAChosenContextSendsNone(t *testing.T) {
	link := &knowLink{}
	r := httptest.NewRequest(http.MethodPost, "/v1/knowledge/mcp", strings.NewReader(initNoForms))
	r.Header.Set("Authorization", "Bearer DEV")
	knowGate(link).ServeHTTP(httptest.NewRecorder(), r)
	p := link.asked("data.cmd.developer_knowledge")[0]
	if p["org"] != "" || p["project"] != "" || w2s(p["spaces"]) != `[]` {
		t.Fatalf("no context: %v", p)
	}
}

func TestKnowledgeToolsAreTheRunsOnesWithStrictSchemas(t *testing.T) {
	h := knowGate(&knowLink{})
	s := krpcSession(t, h, initNoForms)
	_, m := krpc(t, h, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools, _ := m["result"].(map[string]any)["tools"].([]any)
	names := []string{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		names = append(names, str(tool["name"]))
		schema := tool["inputSchema"].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Fatalf("%s: schema not strict: %v", tool["name"], schema)
		}
		props, _ := schema["properties"].(map[string]any)
		req, _ := schema["required"].([]any)
		if len(props) != len(req) {
			t.Fatalf("%s: every property required: %v", tool["name"], schema)
		}
	}
	if strings.Join(names, ",") != "list_entries,read_entry,write_entry" {
		t.Fatalf("tools: %v", names)
	}
}

func TestKnowledgeReadsAnEntryByNameInTheContext(t *testing.T) {
	link := &knowLink{}
	h := knowGate(link)
	s := krpcSession(t, h, initNoForms)
	_, m := krpc(t, h, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_entry","arguments":{"name":"sborka"}}}`)
	if text := w2s(m["result"]); !strings.Contains(text, "ТЕЛО") || strings.Contains(text, `\"ok\"`) {
		t.Fatalf("read: %v", m)
	}
	p := link.asked("data.cmd.developer_knowledge_entry")[0]
	if p["name"] != "sborka" || p["project"] != "shop" {
		t.Fatalf("read relayed: %v", p)
	}
}

func TestKnowledgeMissingEntryIsAnAnswerNotAFailure(t *testing.T) {
	h := knowGate(&knowLink{})
	s := krpcSession(t, h, initNoForms)
	_, m := krpc(t, h, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_entry","arguments":{"name":"missing"}}}`)
	res, _ := m["result"].(map[string]any)
	if res["isError"] == true || !strings.Contains(w2s(res), `\"found\": false`) || !strings.Contains(w2s(res), "нет в базе знаний") {
		t.Fatalf("missing entry: %v", m)
	}
}

const proposalCall = `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"write_entry","arguments":{"name":"%s","title":"Сборка","description":"нужен тулчейн","type":"fact","scope":"shop","body":"ставь тулчейн 1.22"}}}`

func TestKnowledgeWriteNeedsAClientThatCanAskTheUser(t *testing.T) {
	link := &knowLink{}
	h := knowGate(link)
	s := krpcSession(t, h, initNoForms)
	_, m := krpc(t, h, s, strings.Replace(proposalCall, "%s", "sborka", 1))
	res, _ := m["result"].(map[string]any)
	if res["isError"] != true || !strings.Contains(w2s(res), "кабинет") {
		t.Fatalf("no forms: %v", m)
	}
	if len(link.asked("data.cmd.developer_knowledge_create")) != 0 {
		t.Fatal("nothing may be written without asking the person")
	}
}

func TestKnowledgeWritesOnlyAfterThePersonSaysYes(t *testing.T) {
	link := &knowLink{}
	srv := httptest.NewServer(knowGate(link))
	defer srv.Close()
	session := liveInit(t, srv.URL)

	events, done := liveCall(t, srv.URL, session, strings.Replace(proposalCall, "%s", "sborka", 1))
	ask := <-events
	if ask["method"] != "elicitation/create" || !strings.Contains(w2s(ask["params"]), "ставь тулчейн 1.22") {
		t.Fatalf("the person must see the whole entry: %v", ask)
	}
	if len(link.asked("data.cmd.developer_knowledge_create")) != 0 {
		t.Fatal("written before the answer")
	}
	answer(t, srv.URL, session, ask["id"], `{"action":"accept","content":{"add":true}}`)
	final := <-events
	<-done
	if !strings.Contains(w2s(final["result"]), `\"written\": true`) || !strings.Contains(w2s(final["result"]), "ivan (человек)") {
		t.Fatalf("result: %v", final)
	}
	p := link.asked("data.cmd.developer_knowledge_create")[0]
	if p["name"] != "sborka" || p["scope"] != "shop" || p["body"] != "ставь тулчейн 1.22" || p["token"] != "DEV" || p["project"] != "shop" {
		t.Fatalf("create relayed: %v", p)
	}
}

func TestKnowledgeWritesNothingWhenThePersonDeclines(t *testing.T) {
	for _, reply := range []string{`{"action":"decline"}`, `{"action":"cancel"}`, `{"action":"accept","content":{"add":false}}`} {
		link := &knowLink{}
		srv := httptest.NewServer(knowGate(link))
		session := liveInit(t, srv.URL)
		events, done := liveCall(t, srv.URL, session, strings.Replace(proposalCall, "%s", "sborka", 1))
		ask := <-events
		answer(t, srv.URL, session, ask["id"], reply)
		final := <-events
		<-done
		srv.Close()
		if !strings.Contains(w2s(final["result"]), `\"written\": false`) || len(link.asked("data.cmd.developer_knowledge_create")) != 0 {
			t.Fatalf("%s: %v", reply, final)
		}
	}
}

func TestKnowledgeWriteRefusalReachesTheModel(t *testing.T) {
	link := &knowLink{}
	srv := httptest.NewServer(knowGate(link))
	defer srv.Close()
	session := liveInit(t, srv.URL)
	events, done := liveCall(t, srv.URL, session, strings.Replace(proposalCall, "%s", "zanyato", 1))
	ask := <-events
	answer(t, srv.URL, session, ask["id"], `{"action":"accept","content":{"add":true}}`)
	final := <-events
	<-done
	res, _ := final["result"].(map[string]any)
	if res["isError"] != true || !strings.Contains(w2s(res), "уже занято") {
		t.Fatalf("refusal: %v", final)
	}
}

func TestKnowledgeAnswerFromAnotherSessionIsRefused(t *testing.T) {
	h := knowGate(&knowLink{})
	s := krpcSession(t, h, initWithForms)
	other := krpcSession(t, h, initWithForms)
	_ = s
	w, _ := krpc(t, h, other, `{"jsonrpc":"2.0","id":"elicit-x","result":{"action":"accept","content":{"add":true}}}`)
	if w.Code == http.StatusAccepted {
		t.Fatal("an answer nobody asked for must not be accepted")
	}
}

func krpcSession(t *testing.T, h http.Handler, init string) string {
	t.Helper()
	w, _ := krpc(t, h, "", init)
	s := w.Header().Get("Mcp-Session-Id")
	if s == "" {
		t.Fatalf("no session: %s", w.Body)
	}
	return s
}

func livePost(t *testing.T, url, session, body string) *http.Response {
	t.Helper()
	r, _ := http.NewRequest(http.MethodPost, url+"/v1/knowledge/mcp", strings.NewReader(body))
	withContext(r)
	if session != "" {
		r.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func liveInit(t *testing.T, url string) string {
	t.Helper()
	resp := livePost(t, url, "", initWithForms)
	defer resp.Body.Close()
	return resp.Header.Get("Mcp-Session-Id")
}

// liveCall posts a tools/call and yields each server message of its event stream.
func liveCall(t *testing.T, url, session, body string) (<-chan map[string]any, <-chan struct{}) {
	t.Helper()
	resp := livePost(t, url, session, body)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("want an event stream, got %q", ct)
	}
	events := make(chan map[string]any, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var m map[string]any
				_ = json.Unmarshal([]byte(data), &m)
				events <- m
			}
		}
	}()
	return events, done
}

func answer(t *testing.T, url, session string, id any, result string) {
	t.Helper()
	resp := livePost(t, url, session, `{"jsonrpc":"2.0","id":`+w2s(id)+`,"result":`+result+`}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("answer: %d", resp.StatusCode)
	}
}

func TestKnowledgeToolsSayWhatAndWhenWithinCodeModesFirstLine(t *testing.T) {
	// Code Mode lists a tool by the first line of its description, cut at 120 characters: the
	// trigger has to be there, or the model never sees when to call (#371, measured 0/5 before).
	// In either language the model works in.
	for l, trigger := range map[i18n.Lang]string{i18n.RU: "вызывай", i18n.EN: "call"} {
		for _, tool := range knowledgeTools.in(l) {
			first, _, _ := strings.Cut(tool.Description, "\n")
			if n := len([]rune(first)); n > 120 || !strings.Contains(strings.ToLower(first), trigger) {
				t.Fatalf("%s %s: first line must hold the trigger within 120 chars (%d): %q", l, tool.Name, n, first)
			}
		}
	}
}
