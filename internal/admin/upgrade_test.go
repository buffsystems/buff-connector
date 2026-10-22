package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// A connection is pinned to one version on purpose: a plugin must not change
// under a customer because we published something. Moving it is an act, and this
// is what that act has to check first.

func manifestNeeding(fields ...string) *plugin.Manifest {
	props := map[string]*plugin.Schema{}
	for _, f := range fields {
		props[f] = &plugin.Schema{Type: "string"}
	}
	return &plugin.Manifest{
		Name: "jira", Version: "2.0.0", HostAPI: 1,
		Schema: &plugin.Schema{Type: "object", Properties: props, Required: fields},
	}
}

func TestUpgradeRefusesWhenTheNewVersionNeedsSomethingTheConnectionLacks(t *testing.T) {
	inst := config.PluginInstance{
		ID: "7QK3M9XBWZ0FVT2H", Plugin: "jira", Version: "1.0.0", Slug: "jira",
		Config: map[string]any{"base_url": "https://jira.corp.local"},
	}
	// 2.0.0 wants a field the operator has never been asked for.
	err := checkUpgrade(inst, manifestNeeding("base_url", "project_keys"))
	if err == nil || !strings.Contains(err.Error(), "project_keys") {
		t.Fatalf("err = %v, want one naming the field that is missing", err)
	}
}

func TestUpgradeAllowedWhenTheSettingsStillFit(t *testing.T) {
	inst := config.PluginInstance{
		ID: "7QK3M9XBWZ0FVT2H", Plugin: "jira", Version: "1.0.0", Slug: "jira",
		Config: map[string]any{"base_url": "https://jira.corp.local"},
	}
	if err := checkUpgrade(inst, manifestNeeding("base_url")); err != nil {
		t.Fatalf("a compatible upgrade was refused: %v", err)
	}
}

// The listener belongs to the connector: one address for the machine. A page
// that does not render it must not erase it, and a page that does must be able
// to switch it off.
func TestSavePreservesTheListenerUnlessItIsSent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	base := &config.Config{
		MiddlewareURL: "nats://relay/x", StateDir: dir,
		Webhooks: config.Webhooks{Listen: "0.0.0.0:8766", Host: "connector.corp.local:8766"},
	}
	if err := config.Save(path, base); err != nil {
		t.Fatal(err)
	}

	s := New(path, "test", nil)
	s.token = "T"
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	// Every write carries the fingerprint the caller last read: the CLI and the
	// operator's editor write this same file, and a blind save would overwrite
	// whichever of them got there first.
	fingerprint := func() string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/state", nil)
		req.Header.Set("X-Buff-Token", "T")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct {
			Fingerprint string `json:"fingerprint"`
		}
		_ = json.NewDecoder(res.Body).Decode(&out)
		return out.Fingerprint
	}

	post := func(body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/save", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Buff-Local", "1")
		req.Header.Set("X-Buff-Token", "T")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			buf := make([]byte, 400)
			n, _ := res.Body.Read(buf)
			t.Logf("save %d: %s", res.StatusCode, buf[:n])
		}
		return res
	}

	// A save that says nothing about the listener leaves it alone.
	if res := post(`{"fingerprint":"` + fingerprint() + `","middleware_url":"nats://relay/x","state_dir":"` + dir + `","providers":[]}`); res.StatusCode != 200 {
		t.Fatalf("save returned %d", res.StatusCode)
	}
	after, err := config.LoadLenient(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Webhooks.Listen != "0.0.0.0:8766" {
		t.Fatalf("the listener was erased by a page that does not render it: %+v", after.Webhooks)
	}

	// A save that does mention it applies what it says, including switching off.
	if res := post(`{"fingerprint":"` + fingerprint() + `","middleware_url":"nats://relay/x","state_dir":"` + dir + `","providers":[],"webhooks":{"listen":"","host":""}}`); res.StatusCode != 200 {
		t.Fatalf("save returned %d", res.StatusCode)
	}
	after, _ = config.LoadLenient(path)
	if after.Webhooks.Enabled() {
		t.Errorf("the listener could not be switched off: %+v", after.Webhooks)
	}
}
