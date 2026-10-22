package oci_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/oci/ocitest"
)

func TestPushThenPullReturnsBothLayers(t *testing.T) {
	reg := ocitest.New(t)
	payload, sig := []byte("catalogue body"), []byte("signature")

	if err := reg.Client().Push(context.Background(), "buff/plugins/catalog", "v1", payload, sig); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := reg.Pushed(); len(got) != 1 || got[0] != "buff/plugins/catalog:v1" {
		t.Fatalf("pushed = %v", got)
	}

	gotPayload, gotSig, err := reg.Client().Pull(context.Background(), "buff/plugins/catalog", "v1")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if string(gotPayload) != string(payload) || string(gotSig) != string(sig) {
		t.Fatalf("round trip lost data: %q / %q", gotPayload, gotSig)
	}
}

// The registry is transport, not trust. A blob that does not hash to the digest
// the manifest names is a substituted one, and must never reach the signature
// check wearing the name of what we asked for.
func TestBlobThatDoesNotMatchItsDigestIsRefused(t *testing.T) {
	reg := ocitest.New(t)
	bundle := []byte("bundle")
	if err := reg.Client().Push(context.Background(), "buff/plugins/jira", "1.0.0", bundle, []byte("sig")); err != nil {
		t.Fatal(err)
	}
	reg.Corrupt(bundle, []byte("something else entirely"))

	_, _, err := reg.Client().Pull(context.Background(), "buff/plugins/jira", "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("err = %v, want one naming the digest mismatch", err)
	}
}

func TestMissingArtifactSaysWhatWasAskedFor(t *testing.T) {
	reg := ocitest.New(t)
	_, _, err := reg.Client().Pull(context.Background(), "buff/plugins/ghost", "9.9.9")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v, want one naming the artifact", err)
	}
}

// The media types are wire format: a customer mirroring our artifacts into their
// own registry, and any tool that inspects them, sees these strings. Changing
// one is a breaking change, so it is pinned here rather than read from the code.
func TestManifestShapeIsWhatARegistryExpects(t *testing.T) {
	reg := ocitest.New(t)
	if err := reg.Client().Push(context.Background(), "buff/plugins/demo", "1.0.0", []byte("b"), []byte("s")); err != nil {
		t.Fatal(err)
	}
	var m struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Config        struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
			Size      int64  `json:"size"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(reg.Manifest("buff/plugins/demo", "1.0.0"), &m); err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != 2 || m.MediaType != "application/vnd.oci.image.manifest.v1+json" {
		t.Errorf("schemaVersion/mediaType = %d / %q", m.SchemaVersion, m.MediaType)
	}
	if len(m.Layers) != 2 {
		t.Fatalf("layers = %d, want payload + signature", len(m.Layers))
	}
	if m.Layers[0].MediaType != "application/vnd.buff.plugin.payload.v1" {
		t.Errorf("payload media type = %q", m.Layers[0].MediaType)
	}
	if m.Layers[1].MediaType != "application/vnd.buff.plugin.signature.v1" {
		t.Errorf("signature media type = %q", m.Layers[1].MediaType)
	}
	// A registry requires a config descriptor even when there is nothing to
	// configure; omitting it is refused by real ones with an obscure error.
	if m.Config.Digest == "" || m.Config.MediaType == "" {
		t.Error("no config descriptor")
	}
}
