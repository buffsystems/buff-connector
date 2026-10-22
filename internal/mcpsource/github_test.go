package mcpsource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The operator pastes whatever their browser or their git client shows; every
// spelling names the same repository.
func TestParseGitHubReadsEverySpelling(t *testing.T) {
	cases := map[string]Repo{
		"https://github.com/acme/weather-mcp":                     {Owner: "acme", Name: "weather-mcp"},
		"https://github.com/acme/weather-mcp.git":                 {Owner: "acme", Name: "weather-mcp"},
		"https://github.com/acme/weather-mcp/":                    {Owner: "acme", Name: "weather-mcp"},
		"github.com/acme/weather-mcp":                             {Owner: "acme", Name: "weather-mcp"},
		"git@github.com:acme/weather-mcp.git":                     {Owner: "acme", Name: "weather-mcp"},
		"https://github.com/acme/weather-mcp/tree/release-2":      {Owner: "acme", Name: "weather-mcp", Ref: "release-2"},
		"https://github.com/acme/weather-mcp/tree/v1.2.0/src":     {Owner: "acme", Name: "weather-mcp", Ref: "v1.2.0"},
		"https://github.com/acme/weather-mcp/commit/0123abcd":     {Owner: "acme", Name: "weather-mcp", Ref: "0123abcd"},
		"https://github.com/acme/weather-mcp/releases/tag/v1.2.0": {Owner: "acme", Name: "weather-mcp", Ref: "v1.2.0"},
	}
	for raw, want := range cases {
		got, err := ParseGitHub(raw)
		if err != nil || got != want {
			t.Errorf("%s → %+v, %v (want %+v)", raw, got, err, want)
		}
	}
	for _, bad := range []string{"https://gitlab.com/acme/x", "https://github.com/acme", "not a url", ""} {
		if _, err := ParseGitHub(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The download is the API's zipball — one path for public and private
// repositories; a token, when given, goes in the header and nowhere else.
func TestFetchGitHubDownloadsTheZipballWithTheToken(t *testing.T) {
	var seenPath, seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath, seenAuth = r.URL.Path, r.Header.Get("Authorization")
		if r.URL.Path == "/repos/acme/private/zipball/main" {
			if seenAuth == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// GitHub answers with a redirect to codeload; the client follows it.
			http.Redirect(w, r, "/codeload/private.zip", http.StatusFound)
			return
		}
		if r.URL.Path == "/codeload/private.zip" {
			w.Write([]byte("PK\x03\x04zip-bytes"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	data, err := FetchGitHub(context.Background(), srv.Client(), srv.URL, Repo{Owner: "acme", Name: "private", Ref: "main"}, "ghp_secret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "PK") {
		t.Fatalf("data = %q", data)
	}
	if seenPath != "/codeload/private.zip" {
		t.Errorf("did not follow the redirect: %s", seenPath)
	}

	_, err = FetchGitHub(context.Background(), srv.Client(), srv.URL, Repo{Owner: "acme", Name: "private", Ref: "main"}, "")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("a refusal must carry the status: %v", err)
	}
}
