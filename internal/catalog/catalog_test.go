package catalog

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/oci/ocitest"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

func signer(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return priv, base64.StdEncoding.EncodeToString(pub)
}

func indexJSON(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(Index{Plugins: []Entry{{
		Name: "jira", Summary: "tracker",
		Versions: []Version{{Version: "1.0.0", HostAPI: 1,
			Bundle: "jira-1.0.0.tar.gz", Signature: "jira-1.0.0.tar.gz.sig"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// The whole point of the registry move: the connector reads the SAME signed
// catalogue, just over a different transport. If the format had to change, the
// offline kit and the static host would diverge from it immediately.
func TestCatalogueOverARegistryIsTheSameSignedCatalogue(t *testing.T) {
	priv, pub := signer(t)
	reg := ocitest.New(t)

	idx := indexJSON(t)
	bundle := []byte("a bundle")
	push := func(repo, ref string, body []byte) {
		t.Helper()
		if err := reg.Client().Push(context.Background(), repo, ref, body, ed25519.Sign(priv, body)); err != nil {
			t.Fatal(err)
		}
	}
	push("buff/plugins/catalog", "v1", idx)
	push("buff/plugins/jira", "1.0.0", bundle)

	c := New("oci+http://"+reg.Host()+"/buff/plugins", []string{pub}, t.TempDir())
	got, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got.Plugins) != 1 || got.Plugins[0].Name != "jira" {
		t.Fatalf("index = %+v", got)
	}

	_, v, ok := got.Find("jira", "1.0.0")
	if !ok {
		t.Fatal("version not found in the index we just published")
	}
	body, sig, err := c.Download(context.Background(), "jira", v)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(body) != string(bundle) {
		t.Errorf("bundle = %q", body)
	}
	if err := plugin.Verify(body, sig, []string{pub}); err != nil {
		t.Errorf("the signature that came with it does not check out: %v", err)
	}
}

// A registry is transport. Someone who can write to it — or a mirror the
// customer runs — must not be able to change what the connector will install.
func TestACatalogueSignedByAnotherKeyIsRefused(t *testing.T) {
	priv, _ := signer(t)
	_, otherPub := signer(t)
	reg := ocitest.New(t)

	idx := indexJSON(t)
	if err := reg.Client().Push(context.Background(), "buff/plugins/catalog", "v1", idx, ed25519.Sign(priv, idx)); err != nil {
		t.Fatal(err)
	}

	c := New("oci+http://"+reg.Host()+"/buff/plugins", []string{otherPub}, t.TempDir())
	_, err := c.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("err = %v, want a refusal naming the signature", err)
	}
}

// The static transport keeps working: the offline kit is a directory of these
// same files, and a self-hosted mirror may serve them over plain HTTPS.
func TestFilesOverHTTPStillWork(t *testing.T) {
	priv, pub := signer(t)
	idx := indexJSON(t)
	files := map[string][]byte{
		"/catalog.json":     idx,
		"/catalog.json.sig": ed25519.Sign(priv, idx),
	}
	srv := httptest.NewServer(fileServer(files))
	defer srv.Close()

	c := New(srv.URL, []string{pub}, t.TempDir())
	got, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(got.Plugins) != 1 {
		t.Fatalf("index = %+v", got)
	}
}
