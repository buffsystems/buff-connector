package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buffsystems/buff-connector/internal/problem"
)

// A remote MCP server the way the published transport describes one: every
// message is a POST, the first answer names a session the client must quote
// from then on, an answer may arrive as a JSON body or as an event stream,
// and a notification is acknowledged with 202. Strict on purpose.
type fakeRemote struct {
	mu       sync.Mutex
	session  string
	expired  bool
	seenAuth []string
	calls    int
}

func (f *fakeRemote) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.seenAuth = append(f.seenAuth, r.Header.Get("Authorization"))
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") || !strings.Contains(r.Header.Get("Accept"), "application/json") {
			http.Error(w, "client must accept both json and event-stream", http.StatusNotAcceptable)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     *json.Number    `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if msg.Method == "initialize" {
			f.session = fmt.Sprintf("sess-%d", time.Now().UnixNano())
			f.expired = false
			w.Header().Set("Mcp-Session-Id", f.session)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "remote-weather", "version": "2.0.0"},
			}})
			return
		}
		if r.Header.Get("Mcp-Session-Id") != f.session || f.expired {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted) // a notification
			return
		}
		switch msg.Method {
		case "tools/list":
			// The stream form: a keep-alive comment, an unrelated notification,
			// then the answer — then the stream ends.
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": keep-alive\n\n")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{\"level\":\"info\",\"data\":\"hi\"}}\n\n")
			res, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"tools": []any{
				map[string]any{"name": "forecast", "description": "Forecast.", "inputSchema": map[string]any{"type": "object"},
					"annotations": map[string]any{"readOnlyHint": true}},
			}}})
			fmt.Fprintf(w, "id: 7\nevent: message\ndata: %s\n\n", res)
		case "tools/call":
			f.calls++
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "rain"}}}})
		default:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32601, "message": "unknown"}})
		}
	}
}

func remoteInstance(t *testing.T, url string) (*Instance, context.Context) {
	t.Helper()
	in := NewInstance(&Starter{}, InstanceConfig{
		GUID: "GUIDTEST", Slug: "remote", PlainMCP: true,
		Remote:       &RemoteSpec{URL: url, Headers: map[string]string{"Authorization": "Bearer t-1"}},
		StartBackoff: 10 * time.Millisecond, MaxBackoff: 40 * time.Millisecond, CallTimeout: 3 * time.Second,
	}, nil)
	t.Cleanup(in.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return in, ctx
}

// A remote server is the same instance to everyone above it: handshake, tools,
// a call — over the published HTTP transport, with the operator's header on
// every request and the session the server issued quoted back.
func TestRemoteServerIsSpokenToOverStreamableHTTP(t *testing.T) {
	f := &fakeRemote{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	in, ctx := remoteInstance(t, srv.URL+"/mcp")
	if err := in.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if in.Info().Name != "remote-weather" {
		t.Errorf("serverInfo = %+v", in.Info())
	}
	tools := in.Tools()
	if len(tools) != 1 || tools[0].Name != "forecast" || !tools[0].ReadOnlyHint {
		t.Fatalf("tools = %+v (the answer came as an event stream)", tools)
	}
	var res struct {
		Content []struct{ Text string } `json:"content"`
	}
	if err := in.CallTool(ctx, "forecast", map[string]any{}, &res); err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "rain" {
		t.Fatalf("result = %+v", res)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.seenAuth {
		if a != "Bearer t-1" {
			t.Errorf("request %d went without the operator's header: %q", i, a)
		}
	}
	if in.State().Status != StatusRunning {
		t.Fatalf("status = %v", in.State().Status)
	}
}

// A session the server forgot is not the end of the connection: the
// supervisor starts over with a new handshake, the same way it restarts a
// process that died.
func TestRemoteServerSessionLossIsRecoveredByAFreshHandshake(t *testing.T) {
	f := &fakeRemote{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	in, ctx := remoteInstance(t, srv.URL+"/mcp")
	if err := in.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	f.mu.Lock()
	f.expired = true
	f.mu.Unlock()

	var out json.RawMessage
	if err := in.CallTool(ctx, "forecast", map[string]any{}, &out); err == nil {
		t.Fatal("a lost session must surface as an error on the call that hit it")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := in.State(); st.Status == StatusRunning && st.Restarts > 0 {
			if err := in.CallTool(ctx, "forecast", map[string]any{}, &out); err == nil {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("did not come back: %+v", in.State())
}

// An address nobody answers is a readable failure that keeps being retried.
func TestRemoteServerUnreachableIsRetriedNotAbandoned(t *testing.T) {
	in, ctx := remoteInstance(t, "http://127.0.0.1:1/mcp")
	if err := in.Start(ctx); err == nil {
		t.Fatal("expected the handshake to fail")
	}
	time.Sleep(200 * time.Millisecond)
	st := in.State()
	if st.Status == StatusStopped || st.Restarts == 0 || st.LastError == "" {
		t.Fatalf("state = %+v", st)
	}
}

// A server that takes a request and answers nothing (202, the notification
// treatment) is an error on that call — not a nil the caller trips over.
func TestRemoteServerAcceptingARequestWithoutAnAnswerIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"initialize"`) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "s")
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"x","version":"1"}}}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	in, ctx := remoteInstance(t, srv.URL+"/mcp")
	err := in.Start(ctx)
	if err == nil || !errors.Is(err, problem.New("remote_no_answer")) {
		t.Fatalf("err = %v, want the unanswered tools/list named", err)
	}
}

// A remote server that stops answering after the handshake is a connection
// in backoff with the reason on it — not a "running" that answers nothing.
func TestRemoteServerGoingAwayIsBackoffNotRunning(t *testing.T) {
	f := &fakeRemote{}
	srv := httptest.NewServer(f.handler(t))
	in, ctx := remoteInstance(t, srv.URL+"/mcp")
	if err := in.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	srv.Close()

	var out json.RawMessage
	if err := in.CallTool(ctx, "forecast", map[string]any{}, &out); err == nil {
		t.Fatal("a call to a server that is gone must fail")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := in.State(); st.Status == StatusBackoff && st.LastError != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("state = %+v, want backoff with the reason", in.State())
}
