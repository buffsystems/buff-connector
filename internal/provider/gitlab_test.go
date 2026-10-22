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

func TestGitLabPushRemoteURL(t *testing.T) {
	p, _ := New(KindGitLab, "https://gitlab.example.com", "grp/app", nil)
	got := p.PushRemoteURL("TOK")
	want := "https://oauth2:TOK@gitlab.example.com/grp/app.git"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestGitLabPushRemoteURLCloudDefault(t *testing.T) {
	p, _ := New(KindGitLab, "", "grp/app", nil)
	if got := p.PushRemoteURL("TOK"); got != "https://oauth2:TOK@gitlab.com/grp/app.git" {
		t.Fatalf("got %q", got)
	}
}

func TestGitLabCheckAccessOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/v4/projects/grp%2Fapp" {
			t.Errorf("path = %q", r.URL.EscapedPath())
		}
		if r.Header.Get("PRIVATE-TOKEN") != "TOK" {
			t.Errorf("private-token = %q", r.Header.Get("PRIVATE-TOKEN"))
		}
		w.Write([]byte(`{"permissions":{"project_access":{"access_level":30},"group_access":null}}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitLab, srv.URL, "grp/app", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); err != nil {
		t.Fatal(err)
	}
}

func TestGitLabCheckAccessUsesGroupAccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"permissions":{"project_access":{"access_level":10},"group_access":{"access_level":40}}}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitLab, srv.URL, "grp/app", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); err != nil {
		t.Fatal(err)
	}
}

func TestGitLabCheckAccessTooLow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"permissions":{"project_access":{"access_level":20},"group_access":null}}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitLab, srv.URL, "grp/app", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}

func TestGitLabCheckAccess404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	p, _ := New(KindGitLab, srv.URL, "grp/app", srv.Client())
	if err := p.CheckAccess(context.Background(), "TOK"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGitLabCreatePRSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/v4/projects/grp%2Fapp/merge_requests" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.EscapedPath())
		}
		var got map[string]string
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		if got["source_branch"] != "feat" || got["target_branch"] != "main" || got["title"] != "T" || got["description"] != "B" {
			t.Errorf("body = %v", got)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"web_url":"https://gitlab.com/grp/app/-/merge_requests/3","iid":3}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitLab, srv.URL, "grp/app", srv.Client())
	res, err := p.CreatePR(context.Background(), "TOK", PRSpec{Head: "feat", Base: "main", Title: "T", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Number != 3 || res.URL != "https://gitlab.com/grp/app/-/merge_requests/3" {
		t.Fatalf("res = %+v", res)
	}
}

func TestGitLabCreatePRAlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"message":["Another open merge request already exists"]}`))
	}))
	defer srv.Close()
	p, _ := New(KindGitLab, srv.URL, "grp/app", srv.Client())
	res, err := p.CreatePR(context.Background(), "TOK", PRSpec{Head: "feat", Base: "main"})
	if err != nil || !res.AlreadyExists {
		t.Fatalf("want AlreadyExists success, got res=%+v err=%v", res, err)
	}
}

func TestGitLabListReposOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/projects" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("membership") != "true" {
			t.Errorf("membership = %q", r.URL.Query().Get("membership"))
		}
		if r.URL.Query().Get("simple") != "true" {
			t.Errorf("simple = %q", r.URL.Query().Get("simple"))
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
		}
		if r.Header.Get("PRIVATE-TOKEN") != "TOK" {
			t.Errorf("private-token = %q", r.Header.Get("PRIVATE-TOKEN"))
		}
		w.Write([]byte(`[{"path_with_namespace":"grp/app","default_branch":"main","visibility":"private"},{"path_with_namespace":"grp/site","default_branch":"master","visibility":"public"}]`))
	}))
	defer srv.Close()
	p, _ := New(KindGitLab, srv.URL, "grp/app", srv.Client())
	repos, err := p.ListRepos(context.Background(), "TOK")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("len = %d", len(repos))
	}
	if repos[0] != (RepoRef{Repo: "grp/app", DefaultBranch: "main", Private: true}) {
		t.Errorf("repos[0] = %+v", repos[0])
	}
	if repos[1] != (RepoRef{Repo: "grp/site", DefaultBranch: "master", Private: false}) {
		t.Errorf("repos[1] = %+v", repos[1])
	}
}

func TestGitLabListRepos403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	p, _ := New(KindGitLab, srv.URL, "grp/app", srv.Client())
	if _, err := p.ListRepos(context.Background(), "TOK"); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("want ErrNoAccess, got %v", err)
	}
}

// A GitLab project can live in a subgroup, and on gitlab.com it often has to:
// the API refuses to create a TOP-LEVEL group, so a customer whose namespace is
// group/subgroup is the ordinary case, not an exotic one. The connector used to
// answer `repo path "grp/sub/app" must be "owner/repo"` and the import died
// there — found by the live run against gitlab.com (#296).
func TestGitLabProjectInASubgroupKeepsItsWholePath(t *testing.T) {
	p, err := New(KindGitLab, "https://gitlab.example.com", "grp/sub/app", nil)
	if err != nil {
		t.Fatalf("nested path refused: %v", err)
	}
	want := "https://oauth2:TOK@gitlab.example.com/grp/sub/app.git"
	if got := p.PushRemoteURL("TOK"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestGitLabSubgroupPathIsEncodedWholeForTheAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/v4/projects/grp%2Fsub%2Fapp" {
			t.Errorf("path = %q", r.URL.EscapedPath())
		}
		w.Write([]byte(`{"permissions":{"project_access":{"access_level":30},"group_access":null}}`))
	}))
	defer srv.Close()
	p, err := New(KindGitLab, srv.URL, "grp/sub/app", srv.Client())
	if err != nil {
		t.Fatalf("nested path refused: %v", err)
	}
	if err := p.CheckAccess(context.Background(), "TOK"); err != nil {
		t.Fatal(err)
	}
}

// Nesting is GitLab's alone: everywhere else a path is an owner and a
// repository, and a third segment is a typo worth refusing early rather than a
// namespace worth guessing at.
func TestOwnerRepoProvidersStillRefuseAThirdSegment(t *testing.T) {
	for _, kind := range []Kind{KindGitHub, KindGHE, KindGitea, KindBitbucket, KindBitbucketServer} {
		if _, err := New(kind, "", "grp/sub/app", nil); err == nil {
			t.Errorf("%s accepted a three-segment path", kind)
		}
	}
}
