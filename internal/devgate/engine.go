package devgate

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// Buff Code's own session store, row by row (#439). The developer's engine duplicates
// every write to its database into an outbox and posts its head here; the gateway relays
// the rows to the backend unchanged, with the developer's token and the work context from
// the X-Buff-* headers, and answers only what the backend answered — a refusal is not a
// success, so the sender keeps the rows and tries again.
const engineRowsPath = "/v1/engine/rows"

// A batch is a few hundred rows of session text; the relay waits longer than a command.
const engineRowsTimeout = 60 * time.Second

func serveEngineRows(w http.ResponseWriter, r *http.Request, link Requester) bool {
	if r.URL.Path != engineRowsPath {
		return false
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "только POST"})
		return true
	}
	token := bearer(r)
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "нужен токен разработчика"})
		return true
	}
	var body struct {
		Source string `json:"source"`
		Rows   []any  `json:"rows"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "тело запроса — не JSON"})
		return true
	}
	payload := map[string]any{
		"op": "developer_engine_rows", "token": token,
		"org": r.Header.Get("X-Buff-Org"), "project": r.Header.Get("X-Buff-Project"),
		"source": body.Source, "rows": body.Rows,
	}
	ctx, cancel := context.WithTimeout(r.Context(), engineRowsTimeout)
	defer cancel()
	reply, err := link.Request(ctx, "data.cmd.developer_engine_rows", payload)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, reply)
	case reply != nil:
		writeJSON(w, http.StatusForbidden, reply)
	default:
		log.Printf("developer gateway: engine rows: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "коннектор не достучался до Buff — попробуйте ещё раз"})
	}
	return true
}
