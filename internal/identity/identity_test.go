package identity

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateAndPubB64IsSpki(t *testing.T) {
	id := Generate()
	pub := id.PubB64()
	// Must be base64url(SPKI-DER) of an ed25519 public key — exactly what the
	// backend's isValidPubkey/verifyPoP expect.
	der, err := base64.RawURLEncoding.DecodeString(pub)
	if err != nil {
		t.Fatalf("pub not base64url: %v", err)
	}
	pk, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatalf("pub not SPKI: %v", err)
	}
	if _, ok := pk.(ed25519.PublicKey); !ok {
		t.Fatalf("pub is not ed25519")
	}
}

func TestSignVerifiesWithOwnKey(t *testing.T) {
	id := Generate()
	msg := PopMessage("cid-1", "jti-9")
	sigB64 := id.Sign(msg)
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("sig not base64url: %v", err)
	}
	der, _ := base64.RawURLEncoding.DecodeString(id.PubB64())
	pk, _ := x509.ParsePKIXPublicKey(der)
	if !ed25519.Verify(pk.(ed25519.PublicKey), []byte(msg), sig) {
		t.Fatal("signature did not verify against own pubkey")
	}
}

func TestPopMessageMatchesBackend(t *testing.T) {
	// Must byte-match the backend's popMessage(cid, jti).
	if got := PopMessage("cid-1", "jti-9"); got != "connector-refresh:cid-1:jti-9" {
		t.Fatalf("popMessage = %q", got)
	}
}

func TestFromSeedRoundTrip(t *testing.T) {
	id := Generate()
	seed := id.Seed()
	id2 := FromSeed(seed)
	if id2.PubB64() != id.PubB64() {
		t.Fatal("FromSeed did not reproduce the same public key")
	}
	msg := PopMessage("c", "j")
	if id2.Sign(msg) != id.Sign(msg) {
		t.Fatal("FromSeed did not reproduce the same signing key")
	}
}

func TestStateSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	id := Generate()
	st := State{
		Seed:        base64.RawURLEncoding.EncodeToString(id.Seed()),
		ConnectorID: "cid-1",
		Org:         "acme",
		Access:      "acc",
		Refresh:     "ref",
	}
	if err := Save(dir, st); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != st {
		t.Fatalf("round-trip mismatch: %+v != %+v", got, st)
	}
}

func TestSaveUsesTightPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := Save(dir, State{Seed: "x", ConnectorID: "c", Org: "o"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state.json perm = %o, want 600", perm)
	}
}

func TestLoadMissingIsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error loading missing state")
	}
}
