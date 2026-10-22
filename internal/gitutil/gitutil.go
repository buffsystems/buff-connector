// Package gitutil wraps the system `git` for the connector's transport: mirror
// a provider repo, pack it into a bundle to ship to the backend, and (for
// publish) apply a bundle + push a branch. The agent shells out to git (the
// bundle semantics are exact); callers pass fully-formed authenticated URLs.
package gitutil

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// run executes git with the given args in `dir` (dir "" = cwd) and returns stdout.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out.String(), nil
}

// MirrorClone makes a bare mirror clone of `url` at `dir` (all refs, no worktree).
//
// `cfg` carries `-c key=value` settings the provider needs. Not every server
// authenticates through the URL: Bitbucket Data Center takes an HTTP access
// token only as a bearer HEADER, and a token in the userinfo is refused.
func MirrorClone(ctx context.Context, url, dir string, cfg ...string) error {
	_, err := run(ctx, "", append(configArgs(cfg), "clone", "--mirror", "--quiet", url, dir)...)
	return err
}

// configArgs turns ["k=v"] into ["-c", "k=v"].
func configArgs(cfg []string) []string {
	out := make([]string, 0, len(cfg)*2)
	for _, c := range cfg {
		out = append(out, "-c", c)
	}
	return out
}

// BundleAll writes a bundle of ALL refs of the repo at `repoDir` to `outPath`.
func BundleAll(ctx context.Context, repoDir, outPath string) error {
	_, err := run(ctx, repoDir, "bundle", "create", outPath, "--all")
	return err
}

// Clone makes a normal (working-tree-less) clone of `url` at `dir` — used for
// publish, where we need the provider repo's history to push a branch onto.
func Clone(ctx context.Context, url, dir string, cfg ...string) error {
	_, err := run(ctx, "", append(configArgs(cfg), "clone", "--no-checkout", "--quiet", url, dir)...)
	return err
}

// FetchRefspec fetches `refspec` from `source` (a bundle path or a URL) into the
// repo at `dir`.
func FetchRefspec(ctx context.Context, dir, source, refspec string) error {
	_, err := run(ctx, dir, "fetch", source, refspec)
	return err
}

// Push pushes `refspec` to `remote` from the repo at `dir`.
func Push(ctx context.Context, dir, remote, refspec string, cfg ...string) error {
	_, err := run(ctx, dir, append(configArgs(cfg), "push", remote, refspec)...)
	return err
}

// VerifyBundle checks a bundle is well-formed and self-contained. Git before 2.40 verifies a
// bundle only inside a repository, so the check runs in a throwaway empty one — the same on
// every git a host may have.
func VerifyBundle(ctx context.Context, bundlePath string) error {
	scratch, err := os.MkdirTemp("", "bundle-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	if _, err := run(ctx, scratch, "init", "-q"); err != nil {
		return err
	}
	_, err = run(ctx, scratch, "bundle", "verify", bundlePath)
	return err
}

// BundleRefs lists the refs a bundle carries → { ref: sha }.
func BundleRefs(ctx context.Context, bundlePath string) (map[string]string, error) {
	out, err := run(ctx, "", "bundle", "list-heads", bundlePath)
	if err != nil {
		return nil, err
	}
	return parseRefLines(out), nil
}

// ListRemoteRefs runs ls-remote against `url` → { ref: sha } (clone-free drift check).
func ListRemoteRefs(ctx context.Context, url string) (map[string]string, error) {
	out, err := run(ctx, "", "ls-remote", url)
	if err != nil {
		return nil, err
	}
	return parseRefLines(out), nil
}

// parseRefLines parses "<sha>\t<ref>" or "<sha> <ref>" lines into a map.
func parseRefLines(s string) map[string]string {
	refs := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var sha, ref string
		if i := strings.IndexAny(line, " \t"); i > 0 {
			sha = strings.TrimSpace(line[:i])
			ref = strings.TrimSpace(line[i+1:])
		}
		if sha != "" && strings.HasPrefix(ref, "refs/") {
			refs[ref] = sha
		}
	}
	return refs
}
