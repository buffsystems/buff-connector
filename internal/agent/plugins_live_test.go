package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// The real thing: a Python plugin, in a real bundle, signed, installed, started
// under its own uid by the real runtime, answering a real tool call and reaching
// a real HTTP server through the host.
//
// Everything above this test is unit-tested with fakes; those prove the pieces
// behave. This proves they fit — which is the part fixtures have historically
// been wrong about.
//
// Needs the interpreter, so it names where to find it rather than guessing:
//
//	BUFF_TEST_RUNTIME=/path/to/python-root go test ./internal/agent/ -run Live
func TestLivePluginRoundTrip(t *testing.T) {
	runtime := os.Getenv("BUFF_TEST_RUNTIME")
	if runtime == "" {
		t.Skip("set BUFF_TEST_RUNTIME to the unpacked python-build-standalone root")
	}
	if os.Geteuid() != 0 {
		t.Skip("plugins run under their own uid, which needs privilege")
	}

	root, err := os.MkdirTemp("", "bufflive")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := plugin.PrepareStateDir(root); err != nil {
		t.Fatal(err)
	}
	// The interpreter lives in the state directory, readable by plugin users.
	if err := copyTree(filepath.Join(runtime), filepath.Join(root, "runtime", "python")); err != nil {
		t.Fatalf("stage runtime: %v", err)
	}

	// A system the plugin is allowed to reach — stands in for the customer's
	// tracker. It asserts the host attached the credential.
	var sawAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"self":"ok"}`))
	}))
	defer upstream.Close()

	bundle, sig, pub := buildDemoBundle(t)
	store := plugin.NewStore(root, []string{pub}, nil)
	in, err := store.Install(bundle, sig)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := store.Record(in); err != nil {
		t.Fatal(err)
	}

	tokenFile := filepath.Join(root, "secrets", "demo-api.token")
	if err := os.WriteFile(tokenFile, []byte("SEKRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewPluginManager(context.Background(), root, store)
	cfg := &config.Config{Plugins: []config.PluginInstance{{
		ID: "7QK3M9XBWZ0FVT2H", Plugin: "demo", Version: "1.0.0",
		Alias: "Demo", Slug: "demo",
		Config: map[string]any{"base_url": upstream.URL},
		Credentials: []config.InstanceCredential{
			{Name: "api", Kind: "bearer", TokenFile: tokenFile},
		},
	}}}
	if err := cfg.Plugins[0].ResolveSecrets(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m.Sync(ctx, cfg)
	defer m.StopAll()

	inst := m.Instance("demo")
	if inst == nil {
		t.Fatalf("instance did not start: %+v", m.Reports())
	}

	// 1. The handshake found the plugin's tools and its read-only claims.
	tools := inst.Tools()
	if len(tools) != 2 {
		t.Fatalf("tools = %+v; reports = %+v", tools, m.Reports())
	}
	for _, tl := range tools {
		if !tl.ReadOnly {
			t.Errorf("tool %s should be read-only", tl.Name)
		}
	}

	// 2. A tool call reaches the Python code and comes back.
	var echoed struct {
		Content []struct{ Text string }
	}
	if err := inst.CallTool(ctx, "echo", map[string]string{"value": "привет"}, &echoed); err != nil {
		t.Fatalf("echo: %v", err)
	}
	if len(echoed.Content) == 0 || !strings.Contains(echoed.Content[0].Text, "привет") {
		t.Fatalf("echo result = %+v", echoed)
	}

	// 3. The plugin reaches the customer's system THROUGH the host, and the host
	//    is the one that attached the credential.
	var fetched struct {
		Content []struct{ Text string }
	}
	if err := inst.CallTool(ctx, "fetch", map[string]string{"path": "/rest/api/2/myself"}, &fetched); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(fetched.Content) == 0 || !strings.Contains(fetched.Content[0].Text, "ok") {
		t.Fatalf("fetch result = %+v", fetched)
	}
	if sawAuth != "Bearer SEKRET" {
		t.Fatalf("upstream saw %q — the host must attach the credential", sawAuth)
	}

	// 4. The event surface answers too.
	var polled struct {
		Events []struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		} `json:"events"`
	}
	if err := inst.Call(ctx, "buff/poll", map[string]any{"since": nil}, &polled); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(polled.Events) != 1 || polled.Events[0].Action != "ask" {
		t.Fatalf("events = %+v", polled.Events)
	}

	// 5. And the report says what an operator needs to see.
	r := m.Reports()[0]
	if r.Status != "running" || len(r.Tools) != 2 || len(r.ReadOnly) != 2 {
		t.Fatalf("report = %+v", r)
	}
	if len(r.Egress) != 1 {
		t.Fatalf("egress not resolved into the report: %+v", r)
	}
}

func buildDemoBundle(t *testing.T) (bundle, sig []byte, pub string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	add := func(name string, body []byte) {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"manifest.yaml", "main.py"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "demo-plugin", f))
		if err != nil {
			t.Fatal(err)
		}
		add(f, b)
	}
	// The SDK ships inside the bundle: a plugin is one self-contained artifact.
	// Read the whole package rather than a hand-kept list — a new module in the
	// SDK that the list forgot would leave the bundle unable to import itself.
	sdkDir := filepath.Join("..", "..", "sdk", "buff_plugin")
	entries, err := os.ReadDir(sdkDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".py" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(sdkDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		add("buff_plugin/"+e.Name(), b)
	}
	tw.Close()
	gz.Close()

	pk, sk, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), ed25519.Sign(sk, buf.Bytes()), base64.StdEncoding.EncodeToString(pk)
}

// copyTree stages the interpreter into the state directory.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}
