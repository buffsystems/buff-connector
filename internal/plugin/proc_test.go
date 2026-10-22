package plugin

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A plugin runs as its own OS user. That is the whole isolation story: without
// it a child process reads the connector's identity seed and every provider
// token with a plain open(), because it shares the parent's uid.
//
// Switching uid needs privilege the connector may not have (a bare binary under
// an operator account, `docker run --user`, macOS launchd user agents). Where it
// is missing we refuse to run plugins rather than pretend they are contained.

// stateRoot builds a state directory whose whole path is traversable. t.TempDir
// nests under a 0700 parent, which would deny the child at the first path
// component — and make a "permission denied" assertion pass for the wrong
// reason.
func stateRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "buffstate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := PrepareStateDir(root); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return root
}

func TestCheckPrivilegeReportsWhatThisProcessCanDo(t *testing.T) {
	p := CheckPrivilege()
	if runtime.GOOS == "windows" {
		if p.CanSwitchUID {
			t.Fatal("windows has no supported way to drop to another account yet")
		}
		return
	}
	wantRoot := os.Geteuid() == 0
	if p.CanSwitchUID != wantRoot {
		t.Fatalf("CanSwitchUID = %v, euid = %d", p.CanSwitchUID, os.Geteuid())
	}
	if !p.CanSwitchUID && p.Reason == "" {
		t.Error("a refusal must carry a reason the operator can act on")
	}
}

func TestStartRefusesWithoutPrivilegeAndSaysWhy(t *testing.T) {
	s := &Starter{Priv: Privilege{CanSwitchUID: false, Reason: "connector is not running as root"}}

	_, err := s.Start(context.Background(), Spec{
		Path: "/bin/sh", Args: []string{"-c", "true"}, UID: 61234, GID: 61234,
	})
	if !errors.Is(err, ErrNoPrivilege) {
		t.Fatalf("err = %v, want ErrNoPrivilege", err)
	}
	if !strings.Contains(err.Error(), "not running as root") {
		t.Errorf("error should carry the diagnosis, got %q", err)
	}
}

func TestStartRunsUnderTheRequestedUID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no uid on windows")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs privilege to drop privilege")
	}
	s := &Starter{Priv: CheckPrivilege()}

	p, err := s.Start(context.Background(), Spec{
		Path: "/bin/sh", Args: []string{"-c", "id -u"}, UID: 61234, GID: 61234,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	out, _ := io.ReadAll(p.Stdout)
	p.Stop()
	if got := strings.TrimSpace(string(out)); got != "61234" {
		t.Fatalf("child uid = %q, want 61234", got)
	}
}

// The child must not be able to read what the connector keeps for itself. This
// is the assertion the whole design rests on, so it runs against the real
// filesystem layout rather than a mock.
func TestChildCannotReadCoreSecrets(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() != 0 {
		t.Skip("needs a real uid switch")
	}
	root := stateRoot(t)
	if err := os.WriteFile(filepath.Join(root, "secrets", "provider-0.token"), []byte("TOP"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &Starter{Priv: CheckPrivilege()}
	p, err := s.Start(context.Background(), Spec{
		Path: "/bin/sh",
		Args: []string{"-c", "cat " + filepath.Join(root, "secrets", "provider-0.token") + " 2>&1"},
		UID:  61234, GID: 61234,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	raw, _ := io.ReadAll(p.Stdout)
	p.Stop()
	out := string(raw)
	if strings.Contains(out, "TOP") {
		t.Fatalf("child read a core secret: %q", out)
	}
	if !strings.Contains(out, "denied") {
		t.Fatalf("expected a permission denial, got %q", out)
	}
}

// An instance owns its own state directory and nothing else: not the core's, and
// not another instance's.
func TestInstanceDirIsPrivateToItsInstance(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() != 0 {
		t.Skip("needs a real uid switch")
	}
	root := stateRoot(t)
	mine, err := PrepareInstanceDir(root, "GUIDMINE", 61234, 61234)
	if err != nil {
		t.Fatalf("prepare mine: %v", err)
	}
	theirs, err := PrepareInstanceDir(root, "GUIDTHEIRS", 61235, 61235)
	if err != nil {
		t.Fatalf("prepare theirs: %v", err)
	}

	s := &Starter{Priv: CheckPrivilege()}
	// Unchained on purpose: a failure in the first half must not hide the second.
	script := "(echo hi > " + mine + "/w && cat " + mine + "/w) 2>&1; ls " + theirs + " 2>&1"
	p, err := s.Start(context.Background(), Spec{
		Path: "/bin/sh", Args: []string{"-c", script}, UID: 61234, GID: 61234,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	raw, _ := io.ReadAll(p.Stdout)
	p.Stop()
	out := string(raw)
	if !strings.Contains(out, "hi") {
		t.Fatalf("instance could not write its own state: %q", out)
	}
	if !strings.Contains(out, "denied") {
		t.Fatalf("instance could see another instance's state: %q", out)
	}
}

// Stopping must actually stop: a plugin that ignores the polite signal is killed
// rather than left behind as a zombie holding a customer credential's session.
func TestStopKillsAnUncooperativeChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal semantics differ")
	}
	s := &Starter{Priv: Privilege{CanSwitchUID: false}, GraceDuration: 50 * time.Millisecond}
	p, err := s.Start(context.Background(), Spec{
		Path: "/bin/sh", Args: []string{"-c", "trap '' TERM; sleep 30"},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan struct{})
	go func() { p.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return: the child was left running")
	}
	if !p.Exited() {
		t.Fatal("Stop returned but the child is still running")
	}
}
