package devgate

// A picture for Buff Code (#27): POST /v1/draw {prompt} with the developer's token and the
// work context in the X-Buff-* headers. The backend's painter draws it on the organisation's
// key; the answer streams back like a model call — a picture is megabytes, one bus message
// is not — and Buff Code saves it into the session's folder.

import (
	"encoding/json"
	"net/http"
)

// serveDraw answers /v1/draw; false when the path is another.
func serveDraw(w http.ResponseWriter, r *http.Request, link Requester) bool {
	if r.URL.Path != "/v1/draw" {
		return false
	}
	token := bearer(r)
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "нужен вход в Buff: токен разработчика в Authorization"})
		return true
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "только POST"})
		return true
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "тело запроса — не JSON"})
		return true
	}
	streamer, ok := link.(Streamer)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"ok": false, "error": "этот коннектор не умеет потоковые ответы"})
		return true
	}
	relayCall(w, r, streamer, map[string]any{
		"op": "developer_draw", "token": token, "prompt": body.Prompt,
		"org": r.Header.Get("X-Buff-Org"), "project": r.Header.Get("X-Buff-Project"),
		"session": r.Header.Get("X-Buff-Session"), "message": r.Header.Get("X-Buff-Message"),
	})
	return true
}
