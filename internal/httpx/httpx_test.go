package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMessageAcrossRemoteShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "github/gitea message", body: `{"message":"Bad credentials"}`, want: "Bad credentials"},
		{name: "gitlab error", body: `{"error":"insufficient_scope"}`, want: "insufficient_scope"},
		{name: "jira errorMessages", body: `{"errorMessages":["Issue does not exist"],"errors":{}}`, want: "Issue does not exist"},
		{name: "bitbucket nested errors", body: `{"errors":[{"message":"Repository not found"}]}`, want: "Repository not found"},
		{name: "not json falls back to raw", body: "  plain failure  ", want: "plain failure"},
		{name: "empty", body: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Message([]byte(tt.body)); got != tt.want {
				t.Errorf("Message(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestAccessErrForStatus(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{status: 200, want: nil},
		{status: 204, want: nil},
		{status: 401, want: ErrNoAccess},
		{status: 403, want: ErrNoAccess},
		{status: 404, want: ErrNotFound},
	}
	for _, tt := range tests {
		err := AccessErrForStatus(Resp{Status: tt.status})
		if !errors.Is(err, tt.want) {
			t.Errorf("status %d: got %v, want %v", tt.status, err, tt.want)
		}
	}
	err := AccessErrForStatus(Resp{Status: 500, Body: []byte(`{"errorMessages":["boom"]}`)})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("500 must surface the remote message, got %v", err)
	}
}

func TestDoJSONSendsBodyAndHeaders(t *testing.T) {
	var gotBody, gotCT, gotUA, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotCT = string(b), r.Header.Get("Content-Type")
		gotUA, gotAuth = r.Header.Get("User-Agent"), r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"7"}`))
	}))
	defer srv.Close()

	resp, err := DoJSON(context.Background(), srv.Client(), http.MethodPost, srv.URL,
		map[string]string{"body": "hi"}, map[string]string{"Authorization": "Bearer tok"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != http.StatusCreated || !resp.OK() {
		t.Errorf("status = %d, OK = %v", resp.Status, resp.OK())
	}
	if gotBody != `{"body":"hi"}` {
		t.Errorf("body = %q", gotBody)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotUA != UserAgent {
		t.Errorf("user-agent = %q, want %q", gotUA, UserAgent)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("authorization = %q", gotAuth)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := Decode(resp.Body, &out); err != nil || out.ID != "7" {
		t.Errorf("Decode: %v, id = %q", err, out.ID)
	}
}

// A nil body must not set Content-Type — some remotes reject a GET that claims
// to carry JSON.
func TestDoJSONNilBodySetsNoContentType(t *testing.T) {
	var gotCT string
	var hadBody bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		hadBody = len(b) > 0
	}))
	defer srv.Close()

	if _, err := DoJSON(context.Background(), srv.Client(), http.MethodGet, srv.URL, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCT != "" {
		t.Errorf("content-type = %q, want empty", gotCT)
	}
	if hadBody {
		t.Error("GET must not carry a body")
	}
}

// DoRaw exists for remotes that demand a non-JSON payload — Jira's attachment
// upload is multipart and needs its own Content-Type plus an extra header.
func TestDoRawPassesBodyAndHeadersVerbatim(t *testing.T) {
	var gotBody, gotCT, gotXSRF string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotCT = string(b), r.Header.Get("Content-Type")
		gotXSRF = r.Header.Get("X-Atlassian-Token")
	}))
	defer srv.Close()

	_, err := DoRaw(context.Background(), srv.Client(), http.MethodPost, srv.URL,
		strings.NewReader("--boundary--"),
		map[string]string{"Content-Type": "multipart/form-data; boundary=boundary", "X-Atlassian-Token": "no-check"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody != "--boundary--" {
		t.Errorf("body = %q", gotBody)
	}
	if gotCT != "multipart/form-data; boundary=boundary" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotXSRF != "no-check" {
		t.Errorf("x-atlassian-token = %q", gotXSRF)
	}
}

func TestDoJSONReportsTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := srv.Client()
	srv.Close() // nothing is listening any more

	if _, err := DoJSON(context.Background(), client, http.MethodGet, srv.URL, nil, nil); err == nil {
		t.Fatal("want a transport error")
	}
}
