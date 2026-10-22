package agent

import "testing"

// A staged interpreter is what the connector prefers: it is pinned, it is the
// one the plugins were tested against, and it carries nothing the machine
// happens to have installed.
func TestStagedRuntimeWins(t *testing.T) {
	got := pickInterpreter("/state/runtime/python/bin/python3",
		func(p string) bool { return p == "/state/runtime/python/bin/python3" },
		[]string{"/usr/bin/python3"},
		func(string) (int, int, bool) { return 3, 13, true })
	if got != "/state/runtime/python/bin/python3" {
		t.Fatalf("interpreter = %q", got)
	}
}

// Without one, a plugin should not simply refuse to run on a machine that has a
// perfectly good Python — that turned "install an integration" into a dead end
// with no way out documented anywhere.
func TestASystemInterpreterIsUsedWhenNothingIsStaged(t *testing.T) {
	got := pickInterpreter("/state/runtime/python/bin/python3",
		func(string) bool { return false },
		[]string{"/usr/bin/python3.9", "/usr/bin/python3.12"},
		func(p string) (int, int, bool) {
			if p == "/usr/bin/python3.9" {
				return 3, 9, true
			}
			return 3, 12, true
		})
	if got != "/usr/bin/python3.12" {
		t.Fatalf("interpreter = %q, want the one new enough", got)
	}
}

// Too old is not "nearly right": the plugins are written in syntax 3.9 cannot
// parse, and starting them would fail with a traceback that names a line rather
// than the reason.
func TestATooOldInterpreterIsRefused(t *testing.T) {
	got := pickInterpreter("/staged",
		func(string) bool { return false },
		[]string{"/usr/bin/python3", "/usr/bin/python"},
		func(string) (int, int, bool) { return 3, 9, true })
	if got != "" {
		t.Fatalf("interpreter = %q, want none", got)
	}
}

func TestAnUnusableCandidateIsSkipped(t *testing.T) {
	got := pickInterpreter("/staged",
		func(string) bool { return false },
		[]string{"/usr/bin/broken", "/usr/bin/python3"},
		func(p string) (int, int, bool) {
			if p == "/usr/bin/broken" {
				return 0, 0, false
			}
			return 3, 11, true
		})
	if got != "/usr/bin/python3" {
		t.Fatalf("interpreter = %q", got)
	}
}

// A plugin runs as its OWN user, so an interpreter root can execute is not
// necessarily one the plugin can. The machine that found this had Python 3.12
// under /root, mode 700 on the home directory: the connector picked it, the
// plugin died with "permission denied", and the message named a path rather than
// the reason.
func TestAnInterpreterTheInstanceCannotReachIsSkipped(t *testing.T) {
	reachable := map[string]bool{"/usr/local/bin/python3.12": true}
	got := pickInterpreterReachable("/staged",
		func(string) bool { return false },
		[]string{"/root/miniconda3/bin/python3.12", "/usr/local/bin/python3.12"},
		func(string) (int, int, bool) { return 3, 12, true },
		func(p string) bool { return reachable[p] })
	if got != "/usr/local/bin/python3.12" {
		t.Fatalf("interpreter = %q, want the one every user can run", got)
	}
}

// Walking up the path is what decides it: a file anyone may execute inside a
// directory only its owner may enter is still unreachable.
func TestReachabilityFollowsEveryDirectoryOnTheWay(t *testing.T) {
	modes := map[string]uint32{
		"/":                 0o755,
		"/root":             0o700,
		"/root/bin":         0o755,
		"/root/bin/python3": 0o755,
		"/opt":              0o755,
		"/opt/py":           0o755,
		"/opt/py/python3":   0o755,
	}
	mode := func(p string) (uint32, bool) { m, ok := modes[p]; return m, ok }
	if reachableByOthers("/root/bin/python3", mode) {
		t.Error("a binary behind a 0700 directory is not reachable")
	}
	if !reachableByOthers("/opt/py/python3", mode) {
		t.Error("a binary every user may reach was called unreachable")
	}
}
