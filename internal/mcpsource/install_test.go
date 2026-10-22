package mcpsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type call struct {
	dir  string
	name string
	args []string
}

func recorder(fail string) (*[]call, Runner) {
	var calls []call
	return &calls, func(_ context.Context, dir string, _ []string, name string, args ...string) (string, error) {
		calls = append(calls, call{dir, name, args})
		if fail != "" && strings.Contains(name+" "+strings.Join(args, " "), fail) {
			return "npm ERR! peer dep hell", errors.New("exit 1")
		}
		return "ok " + name, nil
	}
}

var tc = Toolchain{Node: "/usr/bin/node", Npm: "/usr/bin/npm", Npx: "/usr/bin/npx", Python: "/usr/bin/python3"}

// A Node project is installed with the lockfile when there is one, then built
// when the entry is generated — as the project's own user, in its own directory.
func TestInstallRunsNpmThenBuild(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o644)
	calls, run := recorder("")
	log, err := Install(context.Background(), dir, Launch{Deps: "npm", Build: true}, tc, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[0].name != "/usr/bin/npm" || (*calls)[0].args[0] != "ci" ||
		(*calls)[1].args[0] != "run" || (*calls)[1].args[1] != "build" {
		t.Fatalf("calls = %+v", *calls)
	}
	if (*calls)[0].dir != dir {
		t.Errorf("ran outside the code directory: %s", (*calls)[0].dir)
	}
	if !strings.Contains(log, "ok /usr/bin/npm") {
		t.Errorf("the log must carry what the tools printed: %q", log)
	}

	calls, run = recorder("")
	if _, err := Install(context.Background(), t.TempDir(), Launch{Deps: "npm"}, tc, run); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].args[0] != "install" {
		t.Fatalf("without a lockfile: %+v", *calls)
	}
}

// A failure carries the tool's own output — that is the whole diagnosis.
func TestInstallFailureCarriesTheToolsOutput(t *testing.T) {
	_, run := recorder("install")
	log, err := Install(context.Background(), t.TempDir(), Launch{Deps: "npm"}, tc, run)
	if err == nil || !strings.Contains(log, "peer dep hell") {
		t.Fatalf("err = %v, log = %q", err, log)
	}
}

// A Python project gets its own environment and is installed into it.
func TestInstallMakesAVenvForPython(t *testing.T) {
	dir := t.TempDir()
	calls, run := recorder("")
	if _, err := Install(context.Background(), dir, Launch{Deps: "pip"}, tc, run); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[0].name != "/usr/bin/python3" || strings.Join((*calls)[0].args, " ") != "-m venv .venv" ||
		(*calls)[1].name != filepath.Join(dir, ".venv", "bin", "python") ||
		strings.Join((*calls)[1].args, " ") != "-m pip install --disable-pip-version-check ." {
		t.Fatalf("calls = %+v", *calls)
	}
}

// Nothing to install is not a call.
func TestInstallDoesNothingForABundle(t *testing.T) {
	calls, run := recorder("")
	if _, err := Install(context.Background(), t.TempDir(), Launch{}, tc, run); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("calls = %+v", *calls)
	}
}

// What the launch needs is checked before anything is written down, and the
// refusal says what to install — in the operator's terms.
func TestNeedsNamesTheMissingRuntime(t *testing.T) {
	err := Launch{Runtime: "node", Deps: "npm"}.Needs(Toolchain{})
	if err == nil || !strings.Contains(err.Error(), "Node.js") {
		t.Fatalf("err = %v", err)
	}
	if err := (Launch{Runtime: "node"}).Needs(Toolchain{Node: "/n"}); err != nil {
		t.Fatalf("node present, no npm needed: %v", err)
	}
	if err := (Launch{Runtime: "node", Deps: "npm"}).Needs(Toolchain{Node: "/n"}); err == nil || !strings.Contains(err.Error(), "npm") {
		t.Fatalf("err = %v", err)
	}
	if err := (Launch{Runtime: "python", Deps: "pip"}).Needs(Toolchain{}); err == nil || !strings.Contains(err.Error(), "Python") {
		t.Fatalf("err = %v", err)
	}
	if err := (Launch{Runtime: ""}).Needs(Toolchain{}); err != nil {
		t.Fatalf("an unknown runtime asks nothing: %v", err)
	}
}

// The command written by the packaging ("node", "python") is resolved to the
// tool the machine actually has — the one the connection's user can run.
func TestResolveCommandMapsRuntimesToTheToolchain(t *testing.T) {
	for in, want := range map[string]string{"node": "/usr/bin/node", "npx": "/usr/bin/npx", "python": "/usr/bin/python3",
		"python3": "/usr/bin/python3", "/srv/.venv/bin/weather": "/srv/.venv/bin/weather"} {
		got, err := ResolveCommand(in, tc, func(s string) (string, error) { return s, nil })
		if err != nil || got != want {
			t.Errorf("%s → %s, %v", in, got, err)
		}
	}
	_, err := ResolveCommand("uvx", tc, func(s string) (string, error) { return "", errors.New("nope") })
	if err == nil || !strings.Contains(err.Error(), "uvx") {
		t.Fatalf("err = %v", err)
	}
}

// The toolchain is what is on PATH AND reachable by another user: a node under
// /root is a node the connection cannot run.
func TestFindToolchainKeepsOnlyWhatTheConnectionCanRun(t *testing.T) {
	found := map[string]string{"node": "/root/.nvm/node", "npm": "/usr/bin/npm", "python3": "/usr/bin/python3"}
	got := FindToolchain(func(n string) (string, error) {
		if p, ok := found[n]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}, func(p string) bool { return !strings.HasPrefix(p, "/root/") })
	if got.Node != "" || got.Npm != "/usr/bin/npm" || got.Python != "/usr/bin/python3" || got.Npx != "" {
		t.Fatalf("toolchain = %+v", got)
	}
}
