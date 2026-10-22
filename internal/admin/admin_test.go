package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// The whole package talks to an authenticated surface, so pin one token for the
// suite through the same environment knob operators use. Tests that care about
// token GENERATION clear it explicitly.
func TestMain(m *testing.M) {
	_ = os.Setenv(tokenEnv, "test-admin-token")
	os.Exit(m.Run())
}

// saveCfg posts to /api/save the way the page does: reading the current
// fingerprint first, so the write is checked against what it was based on.
func saveCfg(t *testing.T, srv *httptest.Server, body map[string]any) map[string]any {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.Header.Set("X-Buff-Token", os.Getenv(tokenEnv))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	var st map[string]any
	_ = json.NewDecoder(res.Body).Decode(&st)
	res.Body.Close()
	body["fingerprint"] = st["fingerprint"]
	return post(t, srv, "/api/save", body)
}

func post(t *testing.T, srv *httptest.Server, path string, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Buff-Local", "1")
	req.Header.Set("X-Buff-Token", os.Getenv(tokenEnv))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer res.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	return m
}

func TestGuardBlocksCSRFAndRebinding(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "c.yaml"), "test", nil)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	// POST without the custom header (a CSRF form/simple request) → 403.
	res, _ := http.Post(srv.URL+"/api/save", "application/json", strings.NewReader("{}"))
	if res.StatusCode != 403 {
		t.Fatalf("CSRF POST status = %d, want 403", res.StatusCode)
	}
	res.Body.Close()

	// Non-loopback Host (DNS rebinding) → 403.
	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.Host = "evil.example.com"
	res2, _ := http.DefaultClient.Do(req)
	if res2.StatusCode != 403 {
		t.Fatalf("rebind Host status = %d, want 403", res2.StatusCode)
	}
	res2.Body.Close()
}

func TestSaveWritesTokenFileNotYamlAndStateReflects(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "connector.yaml")
	s := New(cfgPath, "test", nil)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	r := post(t, srv, "/api/save", map[string]any{
		"middleware_url": "nats://relay/x",
		"state_dir":      dir,
		"providers": []map[string]any{
			{"kind": "gitea", "base_url": "https://g", "token": "SECRET", "repos": []string{"a/b"}},
		},
	})
	if r["ok"] != true {
		t.Fatalf("save not ok: %v", r)
	}
	// The resolved token must be in a 0600 secret file, NOT in the YAML.
	yaml, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(yaml), "SECRET") {
		t.Fatalf("token leaked into YAML:\n%s", yaml)
	}
	tf := filepath.Join(dir, "secrets", "provider-0.token")
	if b, _ := os.ReadFile(tf); string(b) != "SECRET" {
		t.Fatalf("token file = %q", b)
	}
	fi, _ := os.Stat(tf)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file perm = %o", fi.Mode().Perm())
	}
	// And it loads back as a valid config (token re-resolved from the file).
	if _, err := config.Load(cfgPath); err != nil {
		t.Fatalf("saved config not valid: %v", err)
	}

	// /api/state reflects it (without the token).
	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.Header.Set("X-Buff-Token", os.Getenv(tokenEnv))
	res, _ := http.DefaultClient.Do(req)
	var stm map[string]any
	_ = json.NewDecoder(res.Body).Decode(&stm)
	res.Body.Close()
	if stm["middleware_url"] != "nats://relay/x" {
		t.Fatalf("state = %v", stm)
	}
	provs := stm["providers"].([]any)
	if len(provs) != 1 {
		t.Fatalf("providers = %v", provs)
	}
}

func TestValidateListsRepos(t *testing.T) {
	// A fake gitea that lists repos for the token.
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/search" {
			_, _ = w.Write([]byte(`{"data":[{"full_name":"acme/app","default_branch":"main","private":true}]}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer gitea.Close()

	s := New(filepath.Join(t.TempDir(), "c.yaml"), "test", nil)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	r := post(t, srv, "/api/validate", map[string]any{"kind": "gitea", "base_url": gitea.URL, "token": "TOK"})
	if r["ok"] != true {
		t.Fatalf("validate not ok: %v", r)
	}
	repos := r["repos"].([]any)
	if len(repos) != 1 || repos[0].(map[string]any)["repo"] != "acme/app" {
		t.Fatalf("repos = %v", repos)
	}
}

func TestValidateReportsNoAccess(t *testing.T) {
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer gitea.Close()
	s := New(filepath.Join(t.TempDir(), "c.yaml"), "test", nil)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	r := post(t, srv, "/api/validate", map[string]any{"kind": "gitea", "base_url": gitea.URL, "token": "BAD"})
	if r["ok"] != false || r["error"] == nil {
		t.Fatalf("expected no-access error, got %v", r)
	}
}

// The setup UI rewrites the whole file. Anything it does not itself render —
// plugin instances, and sections a NEWER connector understands but this build
// does not — has to survive that round trip. Losing them means an operator's
// integrations vanish because somebody opened the settings page.
func TestSaveKeepsPluginsAndSectionsThisBuildDoesNotRender(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "c.yaml")
	seed := `middleware_url: nats://relay/x
state_dir: ` + dir + `
plugins:
  - id: 7QK3M9XBWZ0FVT2H
    plugin: jira
    version: 3.2.0
    slug: jira_prod
    config:
      base_url: https://jira.example.com
slack:
  - name: corp
`
	if err := os.WriteFile(cfgPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(cfgPath, "test", nil)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	r := saveCfg(t, srv, map[string]any{
		"middleware_url": "nats://relay/x",
		"state_dir":      dir,
		"providers": []map[string]any{
			{"kind": "gitea", "base_url": "https://git.example.com", "repos": []string{"o/r"}, "token": "T"},
		},
	})
	if r["ok"] != true {
		t.Fatalf("save not ok: %v", r)
	}

	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "7QK3M9XBWZ0FVT2H") {
		t.Errorf("plugin instance was dropped by the settings page:\n%s", raw)
	}
	if !strings.Contains(string(raw), "slack") {
		t.Errorf("a section this build does not know was dropped:\n%s", raw)
	}
}

// A config still carrying the retired jira section must say so ON THE PAGE.
// The startup log is where it is written; the settings page is where the
// operator looks, and "my Jira stopped working" is exactly the moment they
// open it.
func TestStateNamesARetiredSectionSoThePageCanShowIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connector.yaml")
	os.WriteFile(path, []byte(`
state_dir: `+dir+`
jira:
  - name: corp
    base_url: https://jira.corp.local
`), 0o600)

	s := &Server{cfgPath: path}
	rec := httptest.NewRecorder()
	s.state(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))

	var got struct {
		Ignored []struct{ Name, Note string } `json:"ignored"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Ignored) != 1 || got.Ignored[0].Name != "jira" {
		t.Fatalf("ignored = %+v, want the jira section named", got.Ignored)
	}
	if got.Ignored[0].Note == "" {
		t.Error("a retired section must carry what to do instead")
	}
}

// The settings form is drawn from instanceConfigs: a field it does not carry
// is a field the operator cannot see back after saving it.
func TestInstanceConfigsCarryTheNotesForTheModel(t *testing.T) {
	cfg := &config.Config{Plugins: []config.PluginInstance{{
		ID: "7QK3M9XBWZ0FVT2H", Plugin: "jira", Version: "1.0.0", Slug: "jira_prod",
		AINotes: "PROJ — биллинг.", AINotesUnbounded: true,
	}}}
	out := instanceConfigs(cfg)
	if len(out) != 1 || out[0]["ai_notes"] != "PROJ — биллинг." || out[0]["ai_notes_unbounded"] != true {
		t.Fatalf("notes missing from the form's data: %+v", out)
	}
}

// The page draws the "sign in" button from what the report says about each
// credential: which kind it was saved as, and — for a sign-in — whether the
// refresh token is on file and whose it is.
func TestInstanceConfigsReportTheKindAndTheSignInState(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "google.token")
	if err := plugin.SaveOAuthSecret(file, plugin.OAuthSecret{ClientID: "cid", ClientSecret: "s", RefreshToken: "r", Account: "me@gmail.com"}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{StateDir: dir, Plugins: []config.PluginInstance{{
		ID: "0123456789ABCDEF", Plugin: "google-drive", Version: "1.0.0", Slug: "gd",
		Credentials: []config.InstanceCredential{{Name: "google", Kind: "oauth2", TokenFile: file}},
	}, {
		ID: "0123456789ABCDEG", Plugin: "jira", Version: "1.0.0", Slug: "jira",
		Credentials: []config.InstanceCredential{{Name: "api", Kind: "bearer", TokenFile: "/nowhere"}},
	}}}
	out := instanceConfigs(cfg)
	kinds := out[0]["credential_kinds"].(map[string]string)
	oauth := out[0]["oauth"].(map[string]map[string]any)
	if kinds["google"] != "oauth2" || oauth["google"]["signed_in"] != true || oauth["google"]["account"] != "me@gmail.com" {
		t.Fatalf("google: kinds=%v oauth=%v", kinds, oauth)
	}
	if len(out[1]["oauth"].(map[string]map[string]any)) != 0 {
		t.Fatalf("a bearer credential has no sign-in state: %v", out[1]["oauth"])
	}
}

// A plugin's check answers {ok, detail}. Its verdict is the answer's verdict:
// a failed check must not come back green because the CALL succeeded.
func TestCheckAnswerCarriesThePluginsVerdict(t *testing.T) {
	good := checkAnswer(map[string]any{"ok": true, "detail": "доступно 3 из 3 корней"}, nil)
	if good["ok"] != true {
		t.Fatalf("a passing check is green: %v", good)
	}
	bad := checkAnswer(map[string]any{"ok": false, "detail": "доступно 1 из 3 корней; nas: нет пути"}, nil)
	if bad["ok"] != false || bad["error"] != "доступно 1 из 3 корней; nas: нет пути" {
		t.Fatalf("a failing check must be red with its detail: %v", bad)
	}
	if bad["result"] == nil {
		t.Fatal("the plugin's answer still travels with the verdict")
	}
	silent := checkAnswer(map[string]any{"detail": "no verdict"}, nil)
	if silent["ok"] != true {
		t.Fatalf("a check that gives no verdict passed the call: %v", silent)
	}
	failed := checkAnswer(nil, errors.New("instance X is not running"))
	if failed["ok"] != false || failed["error"] != "instance X is not running" {
		t.Fatalf("a call that failed: %v", failed)
	}
}
