// Package identity holds the connector agent's ed25519 keypair and its persisted
// state (keypair seed + connector id/org + current tokens).
//
// The private key never leaves the agent. Its public key is exchanged as
// base64url(SPKI-DER) — byte-compatible with the backend's isValidPubkey/verifyPoP.
// Proof-of-possession signatures are over PopMessage(cid, jti), matching the
// backend's popMessage exactly.
package identity

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Identity wraps an ed25519 private key.
type Identity struct {
	priv ed25519.PrivateKey
}

// Generate makes a fresh identity.
func Generate() Identity {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return Identity{priv: priv}
}

// FromSeed reconstructs an identity from its 32-byte seed.
func FromSeed(seed []byte) Identity {
	return Identity{priv: ed25519.NewKeyFromSeed(seed)}
}

// Seed returns the 32-byte private seed (persist this, never log it).
func (id Identity) Seed() []byte {
	return id.priv.Seed()
}

// PubB64 returns the public key as base64url(SPKI-DER).
func (id Identity) PubB64() string {
	der, err := x509.MarshalPKIXPublicKey(id.priv.Public())
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(der)
}

// Sign returns a base64url ed25519 signature over message.
func (id Identity) Sign(message string) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(id.priv, []byte(message)))
}

// PopMessage is the canonical proof-of-possession message. MUST byte-match the
// backend's popMessage(cid, jti).
func PopMessage(cid, jti string) string {
	return "connector-refresh:" + cid + ":" + jti
}

// State is the agent's persisted state.
type State struct {
	Seed        string `json:"seed"` // base64url(32-byte ed25519 seed)
	ConnectorID string `json:"connector_id"`
	Org         string `json:"org"`
	Access      string `json:"access"`
	Refresh     string `json:"refresh"`
}

func statePath(dir string) string { return filepath.Join(dir, "state.json") }

// Save writes state to <dir>/state.json with tight permissions (dir 0700, file 0600).
func Save(dir string, st State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	// Write via a temp file + rename so a crash can't leave a half-written state.
	tmp := statePath(dir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return os.Rename(tmp, statePath(dir))
}

// Load reads state from <dir>/state.json.
func Load(dir string) (State, error) {
	b, err := os.ReadFile(statePath(dir))
	if err != nil {
		return State{}, fmt.Errorf("read state: %w", err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("parse state: %w", err)
	}
	return st, nil
}

// IdentityFromState rebuilds the ed25519 identity from persisted state.
func (st State) Identity() (Identity, error) {
	seed, err := base64.RawURLEncoding.DecodeString(st.Seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return Identity{}, fmt.Errorf("bad seed in state")
	}
	return FromSeed(seed), nil
}
