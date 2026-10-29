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

// streamLink answers Request like fakeLink and streams scripted pieces for Stream.
type streamLink struct {
	fakeLink
	streamed []sent
	pieces   []string
	err      error
	blocked  chan struct{} // when set, Stream waits here until ctx ends
}

func (s *streamLink) Stream(ctx context.Context, subject string, payload map[string]any, onPiece func([]byte) error) error {
	s.streamed = append(s.streamed, sent{subject, payload})
	for _, p := range s.pieces {
		if err := onPiece([]byte(p)); err != nil {
			return err
		}
	}
	if s.blocked != nil {
		close(s.blocked)
		<-ctx.Done()
		return ctx.Err()
	}
	return s.err
}

func call(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var dev = map[string]string{"Authorization": "Bearer DEV", "X-Buff-Org": "acme", "X-Buff-Project": "shop", "X-Buff-Session": "ses_1", "X-Buff-Message": "msg_a1", "X-Buff-Call": "primary"}

func TestModelsAreTheOnesTheOrganisationChose(t *testing.T) {
	l := &streamLink{fakeLink: fakeLink{reply: map[string]any{"ok": true, "models": []any{"deepseek/deepseek-v3.2"}}}}
	w := call(Handler(l), http.MethodGet, "/v1/models", "", dev)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got struct {
		Object string                        `json:"object"`
		Data   []struct{ ID, Object string } `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Object != "list" || len(got.Data) != 1 || got.Data[0].ID != "deepseek/deepseek-v3.2" || got.Data[0].Object != "model" {
		t.Fatalf("%+v", got)
	}
	s := l.calls[0]
	if s.subject != "data.cmd.developer_models" || s.payload["token"] != "DEV" || s.payload["org"] != "acme" {
		t.Fatalf("%+v", s)
	}
}

// The backend names each model for Buff Code (#435): the tariff for our model, the model
// itself only where the organisation chose it. A model the backend did not name has no name.
func TestModelsCarryTheNameTheBackendGave(t *testing.T) {
	l := &streamLink{fakeLink: fakeLink{reply: map[string]any{"ok": true, "models": []any{"buff", "other"},
		"names": map[string]any{"buff": "OpenRouter (Сбалансированные модели)"}}}}
	w := call(Handler(l), http.MethodGet, "/v1/models", "", dev)
	var got struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Data) != 2 || got.Data[0]["id"] != "buff" || got.Data[0]["name"] != "OpenRouter (Сбалансированные модели)" {
		t.Fatalf("%s", w.Body)
	}
	if _, named := got.Data[1]["name"]; named {
		t.Fatalf("an unnamed model got a name: %s", w.Body)
	}
}

// What a model reads and writes (#27) rides through as the backend said it, so Buff Code
// sends a picture only to a model that sees it. A model the backend said nothing about
// carries nothing — Buff Code then keeps text only.
func TestModelsCarryWhatTheyReadAndWrite(t *testing.T) {
	l := &streamLink{fakeLink: fakeLink{reply: map[string]any{"ok": true, "models": []any{"buff", "other"},
		"modalities": map[string]any{"buff": map[string]any{"input": []any{"text", "image"}, "output": []any{"text"}}}}}}
	w := call(Handler(l), http.MethodGet, "/v1/models", "", dev)
	var got struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	in, _ := got.Data[0]["input_modalities"].([]any)
	out, _ := got.Data[0]["output_modalities"].([]any)
	if len(in) != 2 || in[1] != "image" || len(out) != 1 || out[0] != "text" {
		t.Fatalf("%s", w.Body)
	}
	if _, said := got.Data[1]["input_modalities"]; said {
		t.Fatalf("an unknown model got modalities: %s", w.Body)
	}
}

func TestAChatCallStreamsTheProvidersAnswerAsIs(t *testing.T) {
	l := &streamLink{pieces: []string{
		`{"status":200,"content_type":"text/event-stream"}` + "\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"При\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"вет\"}}]}\n\ndata: [DONE]\n\n",
	}}
	body := `{"model":"deepseek/deepseek-v3.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	w := call(Handler(l), http.MethodPost, "/v1/chat/completions", body, dev)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), "При") || !strings.HasSuffix(w.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("body %q", w.Body.String())
	}
	s := l.streamed[0]
	if s.subject != "data.cmd.developer_model_call" || s.payload["api"] != "chat" || s.payload["project"] != "shop" ||
		s.payload["session"] != "ses_1" || s.payload["message"] != "msg_a1" || s.payload["kind"] != "primary" {
		t.Fatalf("%+v", s)
	}
	if req, _ := s.payload["request"].(string); !strings.Contains(req, `"stream":true`) {
		t.Fatalf("request not relayed whole: %v", s.payload["request"])
	}
}

func TestARefusalKeepsTheStatusTheBackendGave(t *testing.T) {
	l := &streamLink{pieces: []string{
		`{"status":402,"content_type":"application/json"}` + "\n",
		`{"error":{"message":"Баланс организации исчерпан","type":"insufficient_quota"}}`,
	}}
	w := call(Handler(l), http.MethodPost, "/v1/chat/completions", `{"model":"x","messages":[]}`, dev)
	if w.Code != 402 || !strings.Contains(w.Body.String(), "Баланс организации исчерпан") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestNoTokenNoModels(t *testing.T) {
	l := &streamLink{}
	w := call(Handler(l), http.MethodPost, "/v1/chat/completions", `{}`, map[string]string{"X-Buff-Org": "acme"})
	if w.Code != 401 || len(l.streamed) != 0 {
		t.Fatalf("%d %d", w.Code, len(l.streamed))
	}
}

func TestAStreamThatBreaksMidwaySaysSoInTheStream(t *testing.T) {
	l := &streamLink{
		pieces: []string{`{"status":200,"content_type":"text/event-stream"}` + "\n", "data: {}\n\n"},
		err:    errors.New("бэкенд замолчал"),
	}
	w := call(Handler(l), http.MethodPost, "/v1/chat/completions", `{"stream":true}`, dev)
	if !strings.Contains(w.Body.String(), "бэкенд замолчал") {
		t.Fatalf("%s", w.Body)
	}
}

func TestAClientThatLeavesCancelsTheCallUpstream(t *testing.T) {
	l := &streamLink{pieces: []string{`{"status":200,"content_type":"text/event-stream"}` + "\n"}, blocked: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true}`)).WithContext(ctx)
	for k, v := range dev {
		r.Header.Set(k, v)
	}
	done := make(chan struct{})
	go func() {
		Handler(l).ServeHTTP(httptest.NewRecorder(), r)
		close(done)
	}()
	select {
	case <-l.blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("the call never reached the stream")
	}
	cancel()
	select {
	case <-done: // the stream saw the cancellation
	case <-time.After(2 * time.Second):
		t.Fatal("a client that left did not end the call")
	}
}

func TestASilentModelKeepsTheClientConnected(t *testing.T) {
	keepAliveEvery = 10 * time.Millisecond
	defer func() { keepAliveEvery = 15 * time.Second }()
	l := &streamLink{pieces: []string{`{"status":200,"content_type":"text/event-stream"}` + "\n"}, blocked: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true}`)).WithContext(ctx)
	for k, v := range dev {
		r.Header.Set(k, v)
	}
	w := newSyncRecorder()
	done := make(chan struct{})
	go func() {
		Handler(l).ServeHTTP(w, r)
		close(done)
	}()
	<-l.blocked
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done
	if !strings.Contains(w.body(), ": keep-alive") {
		t.Fatalf("no keep-alive in %q", w.body())
	}
}
