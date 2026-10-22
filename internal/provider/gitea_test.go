package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGiteaPushRemoteURL(t *testing.T) {
	p, _ := New(KindGitea, "https://gitea.example.com", "acme/widgets", nil)
	got := p.PushRemoteURL("TOK")
	want := "https://oauth2:TOK@gitea.example.com/acme/widgets.git"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestGiteaPushRemoteURLPreservesHTTPScheme(t *testing.T) {
	// A self-hosted instance on plain HTTP (internal network) must yield an http
	// remote, not a broken https one.
	p, _ := New(KindGitea, "http://gitea.corp.local:3000", "acme/widgets", nil)
	got := p.PushRemoteURL("TOK")
	want := "http://oauth2:TOK@gitea.corp.local:3000/acme/widgets.git"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestGiteaCheckAccessOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/acme/widgets" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "token TOK" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"permissions":{"push":true}}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitea, srv.URL, "acme/widgets", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); err != nil {
		t.Fatal(err)
	}
}

func TestGiteaCheckAccessNoPush(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"permissions":{"push":false}}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitea, srv.URL, "acme/widgets", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}

func TestGiteaCheckAccess401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p, _ := New(KindGitea, srv.URL, "acme/widgets", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}

func TestGiteaCreatePRSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repos/acme/widgets/pulls" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var got map[string]string
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		if got["head"] != "feat" || got["base"] != "main" || got["title"] != "T" || got["body"] != "B" {
			t.Errorf("body = %v", got)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"html_url":"https://gitea.example.com/acme/widgets/pulls/9","number":9}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitea, srv.URL, "acme/widgets", srv.Client())
	res, err := p.CreatePR(context.Background(), "TOK", PRSpec{Head: "feat", Base: "main", Title: "T", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Number != 9 || res.URL != "https://gitea.example.com/acme/widgets/pulls/9" {
		t.Fatalf("res = %+v", res)
	}
}

func TestGiteaCreatePRAlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"pull request already exists for these targets"}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitea, srv.URL, "acme/widgets", srv.Client())
	res, err := p.CreatePR(context.Background(), "TOK", PRSpec{Head: "feat", Base: "main"})
	if err != nil || !res.AlreadyExists {
		t.Fatalf("want AlreadyExists success, got res=%+v err=%v", res, err)
	}
}

func TestGiteaListReposOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/repos/search" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("limit") != "100" {
			t.Errorf("limit = %q", r.URL.Query().Get("limit"))
		}
		if r.Header.Get("Authorization") != "token TOK" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"data":[{"full_name":"acme/widgets","default_branch":"main","private":true},{"full_name":"acme/docs","default_branch":"trunk","private":false}]}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitea, srv.URL, "acme/widgets", srv.Client())
	repos, err := p.ListRepos(context.Background(), "TOK")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("len = %d", len(repos))
	}
	if repos[0] != (RepoRef{Repo: "acme/widgets", DefaultBranch: "main", Private: true}) {
		t.Errorf("repos[0] = %+v", repos[0])
	}
	if repos[1] != (RepoRef{Repo: "acme/docs", DefaultBranch: "trunk", Private: false}) {
		t.Errorf("repos[1] = %+v", repos[1])
	}
}

func TestGiteaListRepos403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	p, _ := New(KindGitea, srv.URL, "acme/widgets", srv.Client())
	if _, err := p.ListRepos(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}
