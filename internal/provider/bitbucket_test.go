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

func TestBitbucketPushRemoteURL(t *testing.T) {
	p, _ := New(KindBitbucket, "", "team/repo", nil)
	got := p.PushRemoteURL("TOK")
	want := "https://x-token-auth:TOK@bitbucket.org/team/repo.git"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBitbucketCheckAccessOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2.0/user/permissions/repositories" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if q := r.URL.Query().Get("q"); q != `repository.full_name="team/repo"` {
			t.Errorf("q = %q", q)
		}
		if r.Header.Get("Authorization") != "Bearer TOK" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"values":[{"permission":"write"}]}`))
	}))
	defer srv.Close()
	p, _ := New(KindBitbucket, srv.URL, "team/repo", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); err != nil {
		t.Fatal(err)
	}
}

func TestBitbucketCheckAccessReadOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"values":[{"permission":"read"}]}`))
	}))
	defer srv.Close()
	p, _ := New(KindBitbucket, srv.URL, "team/repo", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}

func TestBitbucketCheckAccessEmptyValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"values":[]}`))
	}))
	defer srv.Close()
	p, _ := New(KindBitbucket, srv.URL, "team/repo", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}

func TestBitbucketCheckAccess403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	p, _ := New(KindBitbucket, srv.URL, "team/repo", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}

func TestBitbucketCreatePRSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/2.0/repositories/team/repo/pullrequests" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var got struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Source      struct {
				Branch struct{ Name string } `json:"branch"`
			} `json:"source"`
			Destination struct {
				Branch struct{ Name string } `json:"branch"`
			} `json:"destination"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		if got.Title != "T" || got.Description != "B" || got.Source.Branch.Name != "feat" || got.Destination.Branch.Name != "main" {
			t.Errorf("body = %+v", got)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":11,"links":{"html":{"href":"https://bitbucket.org/team/repo/pull-requests/11"}}}`))
	}))
	defer srv.Close()
	p, _ := New(KindBitbucket, srv.URL, "team/repo", srv.Client())
	res, err := p.CreatePR(context.Background(), "TOK", PRSpec{Head: "feat", Base: "main", Title: "T", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Number != 11 || res.URL != "https://bitbucket.org/team/repo/pull-requests/11" {
		t.Fatalf("res = %+v", res)
	}
}

func TestBitbucketCreatePRAlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"A pull request already exists."}}`))
	}))
	defer srv.Close()
	p, _ := New(KindBitbucket, srv.URL, "team/repo", srv.Client())
	res, err := p.CreatePR(context.Background(), "TOK", PRSpec{Head: "feat", Base: "main"})
	if err != nil || !res.AlreadyExists {
		t.Fatalf("want AlreadyExists success, got res=%+v err=%v", res, err)
	}
}

func TestBitbucketListReposOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/2.0/repositories" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("role") != "member" {
			t.Errorf("role = %q", r.URL.Query().Get("role"))
		}
		if r.URL.Query().Get("pagelen") != "100" {
			t.Errorf("pagelen = %q", r.URL.Query().Get("pagelen"))
		}
		if r.Header.Get("Authorization") != "Bearer TOK" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"values":[{"full_name":"team/repo","mainbranch":{"name":"main"},"is_private":true},{"full_name":"team/open","mainbranch":{"name":"develop"},"is_private":false}]}`))
	}))
	defer srv.Close()
	p, _ := New(KindBitbucket, srv.URL, "team/repo", srv.Client())
	repos, err := p.ListRepos(context.Background(), "TOK")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("len = %d", len(repos))
	}
	if repos[0] != (RepoRef{Repo: "team/repo", DefaultBranch: "main", Private: true}) {
		t.Errorf("repos[0] = %+v", repos[0])
	}
	if repos[1] != (RepoRef{Repo: "team/open", DefaultBranch: "develop", Private: false}) {
		t.Errorf("repos[1] = %+v", repos[1])
	}
}

func TestBitbucketListRepos403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	p, _ := New(KindBitbucket, srv.URL, "team/repo", srv.Client())
	if _, err := p.ListRepos(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}
