package provider

import (
	"errors"
	"testing"
)

func TestParseRepoPathOwnerRepo(t *testing.T) {
	owner, repo, err := parseRepoPath("acme/widgets")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "acme" || repo != "widgets" {
		t.Fatalf("got %q/%q", owner, repo)
	}
}

func TestParseRepoPathTrimsGitAndSlashes(t *testing.T) {
	owner, repo, err := parseRepoPath("/acme/widgets.git/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "acme" || repo != "widgets" {
		t.Fatalf("got %q/%q", owner, repo)
	}
}

func TestParseRepoPathBitbucketServerProjectSlug(t *testing.T) {
	// Bitbucket Server uses PROJECTKEY/slug — same two-segment shape.
	proj, slug, err := parseRepoPath("PROJ/my-repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proj != "PROJ" || slug != "my-repo" {
		t.Fatalf("got %q/%q", proj, slug)
	}
}

func TestParseRepoPathRejectsBadInput(t *testing.T) {
	for _, bad := range []string{"", "onlyone", "a/b/c", "/", "acme/"} {
		if _, _, err := parseRepoPath(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestNewRejectsUnknownKind(t *testing.T) {
	if _, err := New("svn", "", "a/b", nil); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestNewRejectsBadRepoPath(t *testing.T) {
	if _, err := New(KindGitHub, "", "nope", nil); err == nil {
		t.Fatal("expected error for bad repo path")
	}
}

func TestSentinelErrorsAreDistinct(t *testing.T) {
	if errors.Is(ErrNoAccess, ErrNotFound) {
		t.Fatal("sentinels must be distinct")
	}
}
