package plugin

import (
	"strings"
	"testing"
)

// Some sources sign their callbacks with a secret THEY mint — Notion hands a
// verification token to the receiver in one unsigned call and signs everything
// after with it. The manifest names the body field that carries it, and the
// connector does the rest; the operator never invents or copies a secret.
func TestASourceMayIntroduceItsOwnSigningSecret(t *testing.T) {
	m, err := ParseManifest([]byte(`
name: notion
version: 1.0.0
host_api: 3
entry: main.py
webhook: deliver
signature:
  header: X-Notion-Signature
  algo: hmac-sha256
  secret_from: verification_token
`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Signature == nil || m.Signature.SecretFrom != "verification_token" {
		t.Fatalf("signature = %+v, want secret_from carried through", m.Signature)
	}

	// A secret introduced by the source is only meaningful with a header to
	// check it against, and only with the one algorithm the check implements.
	for _, bad := range []string{
		"signature:\n  algo: hmac-sha256\n  secret_from: verification_token\n",
		"signature:\n  header: X-Notion-Signature\n  algo: shared-secret\n  secret_from: verification_token\n",
	} {
		_, err := ParseManifest([]byte("name: x\nversion: 1.0.0\nhost_api: 3\nentry: main.py\nwebhook: wake\n" + bad))
		if err == nil || !strings.Contains(err.Error(), "secret_from") {
			t.Errorf("%q: err = %v, want a refusal naming secret_from", bad, err)
		}
	}
}
