package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

func postLang(t *testing.T, srv *httptest.Server, path string, body any, set func(*http.Request)) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Buff-Local", "1")
	req.Header.Set("X-Buff-Token", os.Getenv(tokenEnv))
	set(req)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	return m
}

// The texts the server puts on the page follow the page's language: its switch
// (a cookie) first, then the browser's Accept-Language; Russian otherwise.
func TestServerTextsFollowTheRequestLanguage(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "c.yaml"), "test", nil)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	en := postLang(t, srv, "/api/pair", map[string]any{"code": "x"}, func(r *http.Request) {
		r.Header.Set("Accept-Language", "en-GB,en;q=0.9")
	})
	if msg, _ := en["error"].(string); !strings.HasPrefix(msg, "set up a provider and save it first: ") {
		t.Errorf("English request got %q", msg)
	}
	ru := postLang(t, srv, "/api/pair", map[string]any{"code": "x"}, func(r *http.Request) {
		r.Header.Set("Accept-Language", "en-GB")
		r.AddCookie(&http.Cookie{Name: i18n.CookieName, Value: "ru"})
	})
	if msg, _ := ru["error"].(string); !strings.HasPrefix(msg, "сначала настройте провайдера и сохраните: ") {
		t.Errorf("the page's Russian switch got %q", msg)
	}
	bare := postLang(t, srv, "/api/pair", map[string]any{"code": "x"}, func(*http.Request) {})
	if msg, _ := bare["error"].(string); !strings.HasPrefix(msg, "сначала настройте") {
		t.Errorf("a request that says nothing got %q", msg)
	}
}

// Only the fields that carry a message to a person are translated, at any
// depth; names, ids and the operator's own text are left alone.
func TestLocalizeJSONTouchesOnlyMessages(t *testing.T) {
	in := map[string]any{
		"error": "нет такого подключения",
		"instances": []map[string]any{{
			"alias":      "нет такого подключения",
			"last_error": "нет такого подключения",
			"restarts":   9007199254740993,
		}},
	}
	got := localizeJSON(i18n.EN, in)
	b, _ := json.Marshal(got)
	want := `{"error":"no such connection","instances":[{"alias":"нет такого подключения","last_error":"no such connection","restarts":9007199254740993}]}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

// An installed plugin's card and settings form read in the page's language,
// from the English its bundle carries.
func TestInstalledPluginInThePageLanguage(t *testing.T) {
	srv, host, _ := mcpServer(t)
	dir := filepath.Join(host.store.Root(), "0123456789abcdef")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("name: demo\nversion: 1.0.0\nhost_api: 1\npublisher: buff\n"+
		"summary: Демо\nentry: main.py\nwebhook: none\nconfig_schema:\n  type: object\n  properties:\n    base_url:\n      type: string\n      title: Адрес\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "manifest.en.yaml"), []byte("summary: Demo\nconfig_schema:\n  base_url:\n    title: Address\n"), 0o644)

	read := func(lang string) (summary, title string) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/plugins", nil)
		req.Header.Set("X-Buff-Token", os.Getenv(tokenEnv))
		req.Header.Set("Accept-Language", lang)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var st struct {
			Installed []struct {
				Summary string `json:"summary"`
				Schema  struct {
					Properties map[string]struct {
						Title string `json:"title"`
					} `json:"properties"`
				} `json:"schema"`
			} `json:"installed"`
		}
		_ = json.NewDecoder(res.Body).Decode(&st)
		if len(st.Installed) != 1 {
			t.Fatalf("installed = %+v", st.Installed)
		}
		return st.Installed[0].Summary, st.Installed[0].Schema.Properties["base_url"].Title
	}
	if s, ti := read("en"); s != "Demo" || ti != "Address" {
		t.Errorf("English: %q, %q", s, ti)
	}
	if s, ti := read("ru"); s != "Демо" || ti != "Адрес" {
		t.Errorf("Russian: %q, %q", s, ti)
	}
}
