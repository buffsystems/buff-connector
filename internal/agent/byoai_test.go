package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #517: a model call from the platform goes to the customer's own OpenAI-compatible server,
// in their perimeter, with their key; the answer comes back whole.
func TestOwnModelCallGoesToTheCustomersServerWithTheirKey(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	own := NewOwnModel(srv.URL+"/v1", "client-key", srv.Client())
	out, err := own.Chat(context.Background(), json.RawMessage(`{"request":{"model":"local","stream":true,"messages":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/chat/completions" || gotAuth != "Bearer client-key" {
		t.Fatalf("path %q auth %q", gotPath, gotAuth)
	}
	if gotBody["stream"] != false {
		t.Fatalf("the relay carries one answer: stream must be off, got %v", gotBody["stream"])
	}
	res := out.(map[string]any)
	if res["status"] != 200 || !strings.Contains(string(res["body"].(json.RawMessage)), `"content":"OK"`) {
		t.Fatalf("answer %v", res)
	}
}

func TestOwnModelNotConfiguredSaysSo(t *testing.T) {
	if _, err := NewOwnModel("", "", nil).Chat(context.Background(), json.RawMessage(`{"request":{}}`)); err == nil ||
		!strings.Contains(err.Error(), "не настроен") {
		t.Fatalf("want a clear refusal, got %v", err)
	}
}

func TestTheRelayRoutesModelCallsAndToolCalls(t *testing.T) {
	called := ""
	h := relayHandler(func(_ context.Context, m string, _ json.RawMessage) (any, error) {
		called = "mcp:" + m
		return nil, nil
	},
		func(_ context.Context, _ json.RawMessage) (any, error) { called = "model"; return nil, nil })
	_, _ = h(context.Background(), "model/chat", nil)
	if called != "model" {
		t.Fatal(called)
	}
	_, _ = h(context.Background(), "mcp/list", nil)
	if called != "mcp:mcp/list" {
		t.Fatal(called)
	}
}

func TestOwnModelNonJSONAnswerIsCarriedAsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}))
	defer srv.Close()
	out, err := NewOwnModel(srv.URL, "", srv.Client()).Chat(context.Background(), json.RawMessage(`{"request":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["status"] != 502 || !json.Valid(res["body"].(json.RawMessage)) {
		t.Fatalf("%v", res)
	}
}
