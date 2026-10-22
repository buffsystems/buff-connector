package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Which Python a plugin runs under.
//
// The connector prefers an interpreter STAGED beside its own state: it is
// pinned, it is what the plugins were tested against, and it carries nothing the
// machine happens to have installed. But refusing to run at all without one made
// "install an integration" a dead end on an ordinary server that already has
// Python — so a system interpreter that is new enough is used instead, and the
// local page says which one is in use.

// minPython is the oldest interpreter the plugins can be parsed by: they are
// written with `X | None` annotations, which 3.9 reads as a syntax error at
// import time — a traceback that names a line rather than the reason.
var minPython = [2]int{3, 11}

// interpreterCandidates are the usual names, newest first. `python` last, and
// only if it turns out to be a Python 3: on some machines it is still Python 2.
var interpreterCandidates = []string{
	"python3.13", "python3.12", "python3.11", "python3", "python",
}

// pickInterpreter returns the interpreter to run plugins under, or "" when the
// machine has none suitable. It takes its lookups as arguments so the decision
// is testable without a filesystem or a Python.
func pickInterpreter(staged string, exists func(string) bool, candidates []string,
	version func(string) (int, int, bool)) string {
	return pickInterpreterReachable(staged, exists, candidates, version, func(string) bool { return true })
}

// pickInterpreterReachable is the same choice with the second condition a plugin
// actually depends on: it runs as its OWN user, so an interpreter this process
// can execute is not necessarily one the plugin can.
func pickInterpreterReachable(staged string, exists func(string) bool, candidates []string,
	version func(string) (int, int, bool), reachable func(string) bool) string {
	if exists(staged) {
		return staged
	}
	for _, path := range candidates {
		major, minor, ok := version(path)
		if !ok {
			continue
		}
		if major < minPython[0] || (major == minPython[0] && minor < minPython[1]) {
			continue
		}
		if !reachable(path) {
			continue
		}
		return path
	}
	return ""
}

// reachableByOthers reports whether a user other than the owner can run this
// file: every directory on the way needs the search bit, and the file itself
// needs read and execute. `mode` answers with a file's permission bits.
func reachableByOthers(path string, mode func(string) (uint32, bool)) bool {
	m, ok := mode(path)
	if !ok || m&0o005 != 0o005 {
		return false
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		m, ok := mode(dir)
		if !ok || m&0o001 == 0 {
			return false
		}
		if dir == filepath.Dir(dir) {
			return true
		}
	}
}

// modeOf is reachableByOthers's live lookup, following symlinks — /usr/bin/python3
// is one on most machines.
func modeOf(path string) (uint32, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return uint32(fi.Mode().Perm()), true
}

// systemInterpreters resolves the candidate names against PATH, keeping only
// what is actually there.
func systemInterpreters() []string {
	var out []string
	for _, name := range interpreterCandidates {
		if path, err := exec.LookPath(name); err == nil {
			out = append(out, path)
		}
	}
	return out
}

// pythonVersion asks an interpreter what it is. Anything that does not answer in
// a second, or answers something unexpected, is not a Python we will run a
// plugin under.
func pythonVersion(path string) (int, int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-c",
		"import sys;print('%d.%d' % sys.version_info[:2])").Output()
	if err != nil {
		return 0, 0, false
	}
	major, minor, ok := strings.Cut(strings.TrimSpace(string(out)), ".")
	if !ok {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(major)
	b, err2 := strconv.Atoi(minor)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return a, b, true
}

// interpreter is the live resolution: the staged runtime if it is there, else
// whatever the machine offers. Resolved on demand rather than once at start, so
// staging one later needs no restart.
func (m *PluginManager) interpreter() string {
	return pickInterpreterReachable(m.runtime, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}, systemInterpreters(), pythonVersion, func(p string) bool {
		return reachableByOthers(p, modeOf)
	})
}

// RuntimeDir is where a pinned interpreter is staged.
func (m *PluginManager) RuntimeDir() string { return filepath.Dir(filepath.Dir(m.runtime)) }
