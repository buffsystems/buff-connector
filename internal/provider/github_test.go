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

func TestGitHubPushRemoteURL(t *testing.T) {
	p, err := New(KindGitHub, "", "acme/widgets", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := p.PushRemoteURL("TOK")
	want := "https://x-access-token:TOK@github.com/acme/widgets.git"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestGHEPushRemoteURLUsesBaseHost(t *testing.T) {
	p, err := New(KindGHE, "https://ghe.example.com", "acme/widgets", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := p.PushRemoteURL("TOK")
	want := "https://x-access-token:TOK@ghe.example.com/acme/widgets.git"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestGitHubCheckAccessOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/widgets" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer TOK" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("accept = %q", r.Header.Get("Accept"))
		}
		if r.Header.Get("User-Agent") != "buff-connector" {
			t.Errorf("ua = %q", r.Header.Get("User-Agent"))
		}
		w.Write([]byte(`{"permissions":{"push":true}}`))
	}))
	defer srv.Close()

	p, _ := New(KindGitHub, srv.URL, "acme/widgets", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); err != nil {
		t.Fatalf("expected access, got %v", err)
	}
}

func TestGitHubCheckAccessNoPush(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"permissions":{"push":false}}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitHub, srv.URL, "acme/widgets", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}

func TestGitHubCheckAccess403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	p, _ := New(KindGitHub, srv.URL, "acme/widgets", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}

func TestGitHubCheckAccess404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	p, _ := New(KindGitHub, srv.URL, "acme/widgets", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGitHubCreatePRSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/acme/widgets/pulls" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var got map[string]string
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		if got["title"] != "T" || got["head"] != "feat" || got["base"] != "main" || got["body"] != "B" {
			t.Errorf("body = %v", got)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"html_url":"https://github.com/acme/widgets/pull/7","number":7}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitHub, srv.URL, "acme/widgets", srv.Client())
	res, err := p.CreatePR(context.Background(), "TOK", PRSpec{Head: "feat", Base: "main", Title: "T", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Number != 7 || res.URL != "https://github.com/acme/widgets/pull/7" || res.AlreadyExists {
		t.Fatalf("res = %+v", res)
	}
}

func TestGitHubCreatePRAlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"A pull request already exists for acme:feat."}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitHub, srv.URL, "acme/widgets", srv.Client())
	res, err := p.CreatePR(context.Background(), "TOK", PRSpec{Head: "feat", Base: "main", Title: "T"})
	if err != nil {
		t.Fatalf("already-exists must be success, got %v", err)
	}
	if !res.AlreadyExists {
		t.Fatalf("want AlreadyExists, got %+v", res)
	}
}

func TestGitHubCreatePRServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message":"boom"}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitHub, srv.URL, "acme/widgets", srv.Client())
	_, err := p.CreatePR(context.Background(), "TOK", PRSpec{Head: "feat", Base: "main"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want error containing boom, got %v", err)
	}
}

func TestGHECheckAccessHitsAPIv3(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/acme/widgets" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`{"permissions":{"push":true}}`))
	}))
	defer srv.Close()
	p, _ := New(KindGHE, srv.URL, "acme/widgets", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubListReposOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/repos" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
		}
		if r.URL.Query().Get("affiliation") != "owner,collaborator,organization_member" {
			t.Errorf("affiliation = %q", r.URL.Query().Get("affiliation"))
		}
		if r.Header.Get("Authorization") != "Bearer TOK" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("accept = %q", r.Header.Get("Accept"))
		}
		w.Write([]byte(`[{"full_name":"acme/widgets","default_branch":"main","private":true},{"full_name":"acme/public","default_branch":"trunk","private":false}]`))
	}))
	defer srv.Close()
	p, _ := New(KindGitHub, srv.URL, "acme/widgets", srv.Client())
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
	if repos[1] != (RepoRef{Repo: "acme/public", DefaultBranch: "trunk", Private: false}) {
		t.Errorf("repos[1] = %+v", repos[1])
	}
}

func TestGitHubListRepos403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	p, _ := New(KindGitHub, srv.URL, "acme/widgets", srv.Client())
	if _, err := p.ListRepos(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}
