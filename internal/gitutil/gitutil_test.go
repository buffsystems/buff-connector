package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeRepo builds a small bare-cloneable source repo with two commits on main
// and one extra branch, and returns its path (usable as a local clone URL).
func makeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("symbolic-ref", "HEAD", "refs/heads/main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "first")
	run("checkout", "-qb", "feature")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "second")
	run("checkout", "-q", "main")
	return dir
}

func TestMirrorCloneAndBundleAll(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	src := makeRepo(t)
	ctx := context.Background()

	work := t.TempDir()
	mirror := filepath.Join(work, "mirror.git")
	if err := MirrorClone(ctx, src, mirror); err != nil {
		t.Fatalf("mirror clone: %v", err)
	}

	bundle := filepath.Join(work, "all.bundle")
	if err := BundleAll(ctx, mirror, bundle); err != nil {
		t.Fatalf("bundle: %v", err)
	}
	// A valid bundle verifies, and carries BOTH branches.
	if err := VerifyBundle(ctx, bundle); err != nil {
		t.Fatalf("verify bundle: %v", err)
	}
	refs, err := BundleRefs(ctx, bundle)
	if err != nil {
		t.Fatalf("bundle refs: %v", err)
	}
	if _, ok := refs["refs/heads/main"]; !ok {
		t.Fatalf("bundle missing main; refs=%v", refs)
	}
	if _, ok := refs["refs/heads/feature"]; !ok {
		t.Fatalf("bundle missing feature; refs=%v", refs)
	}
}

func TestListRemoteRefs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	src := makeRepo(t)
	refs, err := ListRemoteRefs(context.Background(), src)
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if refs["refs/heads/main"] == "" || refs["refs/heads/feature"] == "" {
		t.Fatalf("expected main+feature SHAs, got %v", refs)
	}
}
