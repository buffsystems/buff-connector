package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/mcpsource"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// fakeHost is the agent as the admin surface sees it, for the custom-server
// handlers: it remembers what it was asked and answers with a canned draft.
type fakeHost struct {
	store    *plugin.Store
	archive  string
	github   string
	synced   *config.Config
	draft    mcpsource.Draft
	draftErr error
}

func (f *fakeHost) Privilege() plugin.Privilege         { return plugin.Privilege{CanSwitchUID: true} }
func (f *fakeHost) Reports() []plugin.InstanceReport    { return nil }
func (f *fakeHost) Store() *plugin.Store                { return f.store }
func (f *fakeHost) Log(string) []plugin.LogLine         { return nil }
func (f *fakeHost) RuntimeInstalled() bool              { return true }
func (f *fakeHost) Runtime() string                     { return "/usr/bin/python3" }
func (f *fakeHost) Toolchain() mcpsource.Toolchain      { return mcpsource.Toolchain{Node: "/usr/bin/node"} }
func (f *fakeHost) CodeFields(string) []mcpsource.Field { return f.draft.Launch.Fields }
func (f *fakeHost) Check(context.Context, string) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}
func (f *fakeHost) Sync(_ context.Context, cfg *config.Config) { f.synced = cfg }
func (f *fakeHost) PrepareCustomArchive(_ context.Context, name string, data []byte) (mcpsource.Draft, error) {
	f.archive = name + ":" + string(data)
	return f.draft, f.draftErr
}
func (f *fakeHost) PrepareCustomGitHub(_ context.Context, url, token string) (mcpsource.Draft, error) {
	f.github = url + " " + token
	return f.draft, f.draftErr
}

func mcpServer(t *testing.T) (*httptest.Server, *fakeHost, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "connector.yaml")
	if err := os.WriteFile(cfgPath, []byte("middleware_url: wss://relay.example.com/nats\nstate_dir: "+dir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := &fakeHost{store: plugin.NewStore(dir, nil, nil), draft: mcpsource.Draft{
		ID: "7QK3M9XBWZ0FVT2H", Source: "archive", Origin: "weather.mcpb",
		Launch: mcpsource.Launch{Name: "weather", Version: "0.3.0", Runtime: "node", Command: "node",
			Args: []string{"${__dirname}/server/index.js"}, Env: map[string]string{"API_KEY": "${user_config.api_key}"},
			Fields: []mcpsource.Field{{Key: "api_key", Title: "API key", Sensitive: true, Required: true}}},
	}}
	s := New(cfgPath, "test", nil).WithPlugins(host, nil)
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return srv, host, cfgPath
}

func fingerprintOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.Header.Set("X-Buff-Token", os.Getenv(tokenEnv))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var st map[string]any
	_ = json.NewDecoder(res.Body).Decode(&st)
	fp, _ := st["fingerprint"].(string)
	return fp
}

// The dropped file reaches the agent by its name and bytes, and the page gets
// the draft back: what will run, what to ask, what the install printed.
func TestUploadHandsTheArchiveToTheAgentAndReturnsTheDraft(t *testing.T) {
	srv, host, _ := mcpServer(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("archive", "weather.mcpb")
	part.Write([]byte("PK-bytes"))
	mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/mcp/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Buff-Local", "1")
	req.Header.Set("X-Buff-Token", os.Getenv(tokenEnv))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		OK    bool
		Draft mcpsource.Draft
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if !out.OK || out.Draft.Launch.Name != "weather" || len(out.Draft.Launch.Fields) != 1 {
		t.Fatalf("answer = %+v", out)
	}
	if host.archive != "weather.mcpb:PK-bytes" {
		t.Fatalf("agent got %q", host.archive)
	}
}

func TestGitHubLinkReachesTheAgentWithItsToken(t *testing.T) {
	srv, host, _ := mcpServer(t)
	r := post(t, srv, "/api/mcp/github", map[string]any{"url": "https://github.com/acme/weather", "token": " ghp_x "})
	if r["ok"] != true || host.github != "https://github.com/acme/weather ghp_x" {
		t.Fatalf("answer = %v, agent got %q", r, host.github)
	}
}

// Saving writes the server into the config with the consent, the secret into
// a file the YAML only points at, and reconciles the agent at once.
func TestSaveWritesTheServerItsConsentAndItsSecretsApart(t *testing.T) {
	srv, host, cfgPath := mcpServer(t)
	r := post(t, srv, "/api/mcp/save", map[string]any{
		"id": "7QK3M9XBWZ0FVT2H", "source": "archive", "origin": "weather.mcpb", "name": "weather", "version": "0.3.0",
		"command": "node", "args": []string{"${__dirname}/server/index.js"},
		"env":   map[string]string{"API_KEY": "${user_config.api_key}"},
		"alias": "Weather prod", "dev_project": "backend", "config": map[string]any{"units": "metric"},
		"credentials": map[string]any{"api_key": map[string]any{"kind": "secret", "value": "k-SEKRET"}},
		"accepted":    true,
		"fingerprint": fingerprintOf(t, srv),
	})
	if r["ok"] != true {
		t.Fatalf("save: %v", r)
	}
	raw, _ := os.ReadFile(cfgPath)
	yaml := string(raw)
	if strings.Contains(yaml, "k-SEKRET") {
		t.Fatal("the secret reached the config file")
	}
	for _, want := range []string{"mcp:", "source: archive", "command: node", "accepted: true", "slug: weather_prod", "kind: secret"} {
		if !strings.Contains(yaml, want) {
			t.Errorf("config lacks %q:\n%s", want, yaml)
		}
	}
	if host.synced == nil || len(host.synced.Plugins) != 1 || host.synced.Plugins[0].MCP == nil ||
		host.synced.Plugins[0].Credentials[0].Token != "k-SEKRET" {
		t.Fatalf("the agent was not reconciled with the resolved secret: %+v", host.synced)
	}

	// The page draws the form back from the state — with the fields the
	// packaging asks, never the secret.
	req, _ := http.NewRequest("GET", srv.URL+"/api/plugins", nil)
	req.Header.Set("X-Buff-Token", os.Getenv(tokenEnv))
	res, _ := http.DefaultClient.Do(req)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), `"fields":[{"key":"api_key"`) || strings.Contains(string(b), "k-SEKRET") {
		t.Fatalf("state = %s", b)
	}
	// The page reads the toolchain by lowercase keys.
	if !strings.Contains(string(b), `"toolchain":{"node":"/usr/bin/node"`) {
		t.Fatalf("toolchain keys are not what the page reads: %s", b)
	}
}

// Without the consent nothing is written: the refusal names the field.
func TestSaveRefusesWithoutConsent(t *testing.T) {
	srv, _, cfgPath := mcpServer(t)
	r := post(t, srv, "/api/mcp/save", map[string]any{
		"source": "command", "name": "x", "command": "node", "accepted": false, "fingerprint": fingerprintOf(t, srv),
	})
	if r["ok"] == true || !strings.Contains(r["error"].(string), "accepted") {
		t.Fatalf("answer = %v", r)
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "mcp:") {
		t.Fatal("written despite the refusal")
	}
}

// The marks are their own write: the tools are only known once the server
// runs, and ticking one must not mean re-entering the form.
func TestMarksAreSavedOnTheirOwn(t *testing.T) {
	srv, host, cfgPath := mcpServer(t)
	post(t, srv, "/api/mcp/save", map[string]any{
		"id": "7QK3M9XBWZ0FVT2H", "source": "command", "name": "x", "command": "node", "accepted": true,
		"fingerprint": fingerprintOf(t, srv),
	})
	r := post(t, srv, "/api/mcp/marks", map[string]any{"id": "7QK3M9XBWZ0FVT2H", "read_only": []string{"forecast"}, "fingerprint": fingerprintOf(t, srv)})
	if r["ok"] != true {
		t.Fatalf("marks: %v", r)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "- forecast") {
		t.Fatalf("config = %s", raw)
	}
	if got := host.synced.Plugins[0].MCP.ReadOnly; len(got) != 1 || got[0] != "forecast" {
		t.Fatalf("agent got %v", got)
	}
}

// A Cyrillic alias is transliterated into the slug (cloud-front#475).
func TestACyrillicAliasIsTransliterated(t *testing.T) {
	srv, _, _ := mcpServer(t)
	r := post(t, srv, "/api/mcp/save", map[string]any{
		"source": "archive", "name": "weather-mcp", "command": "node", "alias": "Погода", "accepted": true,
		"fingerprint": fingerprintOf(t, srv),
	})
	if r["ok"] != true || r["slug"] != "pogoda" {
		t.Fatalf("answer = %v", r)
	}
}

// An alias with no letters reduces to no slug at all; the model then sees the
// server under the name its packaging gave it, not under "i".
func TestALetterlessAliasFallsBackToThePackagingsName(t *testing.T) {
	srv, _, cfgPath := mcpServer(t)
	r := post(t, srv, "/api/mcp/save", map[string]any{
		"source": "archive", "name": "weather-mcp", "command": "node", "alias": "★★★", "accepted": true,
		"fingerprint": fingerprintOf(t, srv),
	})
	if r["ok"] != true || r["slug"] != "weather_mcp" {
		t.Fatalf("answer = %v", r)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "slug: weather_mcp") {
		t.Fatalf("config = %s", raw)
	}
}

// A remote server has no packaging to be named after; an alias with no letters
// then leaves the host it lives on as the name the model sees.
func TestARemoteServerWithALetterlessAliasIsNamedAfterItsHost(t *testing.T) {
	srv, _, _ := mcpServer(t)
	r := post(t, srv, "/api/mcp/save", map[string]any{
		"source": "url", "url": "https://mcp.vendor.example/mcp", "alias": "★★★", "accepted": true,
		"fingerprint": fingerprintOf(t, srv),
	})
	if r["ok"] != true || r["slug"] != "mcp_mcp_vendor_example" {
		t.Fatalf("answer = %v", r)
	}
}

// A secret's name becomes a file name: one that climbs out is refused before
// anything is written.
func TestSaveRefusesACredentialNameThatIsNotAnIdentifier(t *testing.T) {
	srv, _, cfgPath := mcpServer(t)
	r := post(t, srv, "/api/mcp/save", map[string]any{
		"source": "command", "name": "x", "command": "node", "accepted": true,
		"credentials": map[string]any{"../../../evil": map[string]any{"kind": "env", "value": "v"}},
		"fingerprint": fingerprintOf(t, srv),
	})
	if r["ok"] == true {
		t.Fatalf("accepted: %v", r)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfgPath), "evil")); err == nil {
		t.Fatal("the file was written outside the secrets directory")
	}
}
