package plugin

import (
	"os"
	"path/filepath"
	"strings"
)

// A webhook signing secret the SOURCE introduces, kept beside the instance's
// other state.
//
// Most trackers sign a callback with the secret in the URL the connector issued.
// Notion mints its own: the first, unsigned call carries a verification token in
// the body, the operator pastes it back into Notion to confirm the subscription,
// and every call after is an HMAC over the body with that token. The token is
// therefore learnt from that first call — never typed, never copied between
// machines — and lives here, under the instance GUID like everything else that
// outlives a rename.
//
// A SECOND unsigned handshake is a different matter. Once callbacks are being
// verified with a token, anyone holding the URL could otherwise install their
// own by replaying the handshake. The new token is kept aside as PENDING, shown
// to the operator, and takes effect only when they accept it.

func signingSecretPath(stateDir, id string) string {
	return filepath.Join(stateDir, "instances", id, "signing_secret")
}

func pendingSigningSecretPath(stateDir, id string) string {
	return signingSecretPath(stateDir, id) + ".pending"
}

// LoadSigningSecret is the token callbacks are verified with; empty until the
// source has introduced one or the operator has entered it.
func LoadSigningSecret(stateDir, id string) string {
	return readPrivate(signingSecretPath(stateDir, id))
}

// PendingSigningSecret is a token the source introduced while another was
// already in force — waiting for the operator to accept it.
func PendingSigningSecret(stateDir, id string) string {
	return readPrivate(pendingSigningSecretPath(stateDir, id))
}

// SaveSigningSecret makes `secret` the one callbacks are verified with, and
// clears anything pending: this IS the acceptance.
func SaveSigningSecret(stateDir, id, secret string) error {
	if err := writePrivate(signingSecretPath(stateDir, id), secret); err != nil {
		return err
	}
	_ = os.Remove(pendingSigningSecretPath(stateDir, id))
	return nil
}

// SavePendingSigningSecret keeps a newly introduced token for the operator.
func SavePendingSigningSecret(stateDir, id, secret string) error {
	return writePrivate(pendingSigningSecretPath(stateDir, id), secret)
}

func readPrivate(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writePrivate(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
