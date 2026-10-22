package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"strings"

	"github.com/buffsystems/buff-connector/internal/plugin"
)

// The webhook side of a signing secret the source introduces — see
// plugin/signing.go for what is kept and why.

// handshakeSecret reads the token out of an unsigned handshake body — the field
// the manifest named — or returns "" when the body is not a handshake.
func handshakeSecret(body []byte, field string) string {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return ""
	}
	v, _ := doc[field].(string)
	return strings.TrimSpace(v)
}

// acceptHandshake records a token the source introduced. The first one takes
// effect at once — there is nothing to protect yet; a later one waits for the
// operator, and callbacks keep being verified with the token in force.
func acceptHandshake(stateDir, id, token string) {
	if plugin.LoadSigningSecret(stateDir, id) == "" {
		if err := plugin.SaveSigningSecret(stateDir, id, token); err != nil {
			log.Printf("webhooks: %s: keep the source's signing secret: %v", id, err)
			return
		}
		log.Printf("webhooks: %s: the source introduced its signing secret — confirm the subscription "+
			"in the source with the token shown on the connection's page", id)
		return
	}
	if err := plugin.SavePendingSigningSecret(stateDir, id, token); err != nil {
		log.Printf("webhooks: %s: keep the source's new signing secret: %v", id, err)
		return
	}
	log.Printf("webhooks: %s: the source introduced a NEW signing secret; callbacks are still verified "+
		"with the current one until the operator accepts the new token on the connection's page", id)
}

// sourceSignatureHolds checks a source's own HMAC-SHA256 over the body, written
// either as "sha256=<hex>" — the spelling Notion documents — or as bare hex.
func sourceSignatureHolds(value, secret string, body []byte) bool {
	value = strings.TrimSpace(value)
	if name, rest, found := strings.Cut(value, "="); found {
		if !strings.EqualFold(name, "sha256") {
			return false
		}
		value = rest
	}
	want, err := hex.DecodeString(value)
	if err != nil || len(want) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(want, mac.Sum(nil))
}
