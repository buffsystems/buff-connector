package natslink

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// The command transport mirrors the browser SPA exactly: publish a command with
// a `reply` subject IN THE BODY (the backend replies to cmd.reply, not the NATS
// reply-to), and read the answer on a gid-scoped inbox the guest is allowed to sub.

// newGid returns a fresh 16-byte random guest id as hex (32 chars).
func newGid() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// replySubject is the per-request inbox `_INBOX.<gid>.<seq>` (a child of the
// `_INBOX.<gid>.>` the guest is permitted to subscribe).
func replySubject(gid string, seq uint64) string {
	return "_INBOX." + gid + "." + strconv.FormatUint(seq, 10)
}

// envelope serialises the command with the reply subject merged in, without
// mutating the caller's payload map.
func envelope(payload map[string]any, reply string) ([]byte, error) {
	m := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		m[k] = v
	}
	m["reply"] = reply
	return json.Marshal(m)
}
