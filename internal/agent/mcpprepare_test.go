package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/buffsystems/buff-connector/internal/config"
)

// A stand-in toolchain: node and npm as shell scripts the connection's user
// can run. npm leaves a marker where it ran and says what real npm says.
func fakeToolchain(t *testing.T, root string) {
	t.Helper()
	bin := filepath.Join(root, "plugins", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"node": "#!/bin/sh\nexit 0\n",
		"npm":  "#!/bin/sh\necho \"npm $* in $(pwd) as $(id -u)\" > installed.marker\necho 'added 3 packages'\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

func nodeArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"weather-mcp-main/package.json": `{"name": "weather-mcp", "version": "1.0.0", "main": "index.js", "dependencies": {"x": "1"}}`,
		"weather-mcp-main/index.js":     "// server",
	} {
		f, _ := w.Create(name)
		f.Write([]byte(body))
	}
	w.Close()
	return buf.Bytes()
}

// Dropping an archive ends with a draft the operator can read: the launch the
// packaging describes, the log of the install, and the code on disk owned by
// the connection's own user — which is who ran npm.
func TestPreparingAnArchiveInstallsAsTheConnectionsUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs privilege to hand the code to another user")
	}
	root, store := stateWithStore(t)
	fakeToolchain(t, root)
	m := NewPluginManager(context.Background(), root, store)

	d, err := m.PrepareCustomArchive(context.Background(), "weather-mcp-main.zip", nodeArchive(t))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if d.Needs != "" {
		t.Fatalf("needs = %q", d.Needs)
	}
	if d.Source != config.MCPFromArchive || d.Launch.Runtime != "node" || d.Launch.Name != "weather-mcp" {
		t.Fatalf("draft = %+v", d)
	}
	if !strings.Contains(d.Log, "added 3 packages") {
		t.Errorf("the install log must carry npm's own words: %q", d.Log)
	}
	marker, err := os.ReadFile(filepath.Join(CodeDir(root, d.ID), "installed.marker"))
	if err != nil {
		t.Fatalf("npm did not run in the code directory: %v", err)
	}
	uid, _ := m.uids.For(d.ID)
	if !strings.Contains(string(marker), "as "+strconv.Itoa(int(uid))) {
		t.Errorf("npm ran as the wrong user: %s", marker)
	}
	fi, _ := os.Stat(filepath.Join(CodeDir(root, d.ID), "index.js"))
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != uid {
		t.Errorf("the code is owned by uid %d, want the connection's %d", st.Uid, uid)
	}
}

// A machine without the runtime says so in the draft — before anything is
// saved, and without pretending an install happened.
func TestPreparingSaysWhatTheMachineLacks(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs privilege")
	}
	root, store := stateWithStore(t)
	t.Setenv("PATH", filepath.Join(root, "nowhere"))
	m := NewPluginManager(context.Background(), root, store)
	d, err := m.PrepareCustomArchive(context.Background(), "x.zip", nodeArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Needs, "Node.js") || d.Log != "" {
		t.Fatalf("draft = %+v", d)
	}
}

// A repository is the same thing with a download in front.
func TestPreparingAGitHubRepositoryDownloadsTheZipball(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs privilege")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/weather-mcp/zipball/main" {
			w.WriteHeader(404)
			return
		}
		w.Write(nodeArchive(t))
	}))
	defer srv.Close()
	root, store := stateWithStore(t)
	fakeToolchain(t, root)
	m := NewPluginManager(context.Background(), root, store)
	m.github = srv.URL
	d, err := m.PrepareCustomGitHub(context.Background(), "https://github.com/acme/weather-mcp/tree/main", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != config.MCPFromGitHub || d.Launch.Name != "weather-mcp" || d.Needs != "" {
		t.Fatalf("draft = %+v", d)
	}
	if _, err := os.Stat(filepath.Join(CodeDir(root, d.ID), "package.json")); err != nil {
		t.Fatal("the code did not land")
	}
}

func itoa(u uint32) string {
	return strings.TrimSpace(strings.Join(strings.Fields(string(rune('0'+u%10))), ""))
}
