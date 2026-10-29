package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/buffsystems/buff-connector/internal/i18n"
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
			i18n.Logf("вебхуки %s — сохранение секрета подписи источника: %v", id, err)
			return
		}
		i18n.Logf("вебхуки %s — источник прислал секрет подписи; подтвердите подписку в источнике токеном со страницы подключения", id)
		return
	}
	if err := plugin.SavePendingSigningSecret(stateDir, id, token); err != nil {
		i18n.Logf("вебхуки %s — сохранение нового секрета подписи источника: %v", id, err)
		return
	}
	i18n.Logf("вебхуки %s — источник прислал НОВЫЙ секрет подписи; вебхуки проверяются прежним, пока администратор не примет новый токен на странице подключения", id)
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
