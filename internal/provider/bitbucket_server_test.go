package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBitbucketServerPushRemoteURL(t *testing.T) {
	// No userinfo: the token travels in a header — see the test at the bottom of
	// this file for why a live Data Center leaves no alternative.
	p, _ := New(KindBitbucketServer, "https://bb.example.com", "PROJ/my-repo", nil)
	got := p.PushRemoteURL("TOK")
	want := "https://bb.example.com/scm/PROJ/my-repo.git"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBitbucketServerCheckAccessOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/1.0/projects/PROJ/repos/my-repo" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer TOK" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"slug":"my-repo"}`))
	}))
	defer srv.Close()
	p, _ := New(KindBitbucketServer, srv.URL, "PROJ/my-repo", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); err != nil {
		t.Fatal(err)
	}
}

func TestBitbucketServerCheckAccess401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p, _ := New(KindBitbucketServer, srv.URL, "PROJ/my-repo", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}

func TestBitbucketServerCheckAccess404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	p, _ := New(KindBitbucketServer, srv.URL, "PROJ/my-repo", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestBitbucketServerCreatePRSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/1.0/projects/PROJ/repos/my-repo/pull-requests" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var got struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			FromRef     struct {
				ID string `json:"id"`
			} `json:"fromRef"`
			ToRef struct {
				ID string `json:"id"`
			} `json:"toRef"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		if got.Title != "T" || got.Description != "B" || got.FromRef.ID != "refs/heads/feat" || got.ToRef.ID != "refs/heads/main" {
			t.Errorf("body = %+v", got)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":42,"links":{"self":[{"href":"https://bb.example.com/projects/PROJ/repos/my-repo/pull-requests/42"}]}}`))
	}))
	defer srv.Close()
	p, _ := New(KindBitbucketServer, srv.URL, "PROJ/my-repo", srv.Client())
	res, err := p.CreatePR(context.Background(), "TOK", PRSpec{Head: "feat", Base: "main", Title: "T", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Number != 42 || res.URL != "https://bb.example.com/projects/PROJ/repos/my-repo/pull-requests/42" {
		t.Fatalf("res = %+v", res)
	}
}

func TestBitbucketServerCreatePRAlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"errors":[{"message":"Only one pull request may be open for a given source and target branch"}]}`))
	}))
	defer srv.Close()
	p, _ := New(KindBitbucketServer, srv.URL, "PROJ/my-repo", srv.Client())
	res, err := p.CreatePR(context.Background(), "TOK", PRSpec{Head: "feat", Base: "main"})
	if err != nil || !res.AlreadyExists {
		t.Fatalf("want AlreadyExists success, got res=%+v err=%v", res, err)
	}
}

func TestBitbucketServerListReposOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/api/1.0/repos" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("limit") != "100" {
			t.Errorf("limit = %q", r.URL.Query().Get("limit"))
		}
		if r.Header.Get("Authorization") != "Bearer TOK" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"values":[{"project":{"key":"PROJ"},"slug":"my-repo","public":false},{"project":{"key":"OPS"},"slug":"tools","public":true}]}`))
	}))
	defer srv.Close()
	p, _ := New(KindBitbucketServer, srv.URL, "PROJ/my-repo", srv.Client())
	repos, err := p.ListRepos(context.Background(), "TOK")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("len = %d", len(repos))
	}
	if repos[0] != (RepoRef{Repo: "PROJ/my-repo", DefaultBranch: "", Private: true}) {
		t.Errorf("repos[0] = %+v", repos[0])
	}
	if repos[1] != (RepoRef{Repo: "OPS/tools", DefaultBranch: "", Private: false}) {
		t.Errorf("repos[1] = %+v", repos[1])
	}
}

func TestBitbucketServerListRepos401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p, _ := New(KindBitbucketServer, srv.URL, "PROJ/my-repo", srv.Client())
	if _, err := p.ListRepos(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}

// Data Center refuses an HTTP access token in the URL whatever username it is
// paired with — only the account's own name works, and the connector does not
// know it. The token goes in a bearer header instead, which is what Atlassian
// documents for these tokens and what a live server accepts.
func TestBitbucketServerAuthenticatesGitWithAHeaderNotAURL(t *testing.T) {
	p, err := New(KindBitbucketServer, "https://bitbucket.corp.local", "ACME/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	url := p.PushRemoteURL("BBDC-token")
	if strings.Contains(url, "BBDC-token") || strings.Contains(url, "@") {
		t.Errorf("the token is in the URL: %s", url)
	}
	if url != "https://bitbucket.corp.local/scm/ACME/api.git" {
		t.Errorf("url = %s", url)
	}
	cfg := p.GitConfig("BBDC-token")
	if len(cfg) != 1 || cfg[0] != "http.extraHeader=Authorization: Bearer BBDC-token" {
		t.Errorf("git config = %v", cfg)
	}
}
