package devgate

// Our models for the developer's Buff Code (#370): an OpenAI-compatible door on the
// connector's gateway. The connector does not call any provider — it relays the request
// over the pipe to the backend, which resolves the organisation's model (the admin chose
// it), calls the provider with OUR key and streams the answer back (#365). The developer's
// own token, the organisation and the project (the work context, #391) ride along.
//
// The backend's stream starts with one line of meta — {"status","content_type"} — and then
// the provider's body as is (SSE for a streaming call, JSON otherwise), so the client gets
// exactly what an OpenAI-compatible server would send, refusals with their own status.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Streamer reads a streamed answer (natslink.Link does).
type Streamer interface {
	Stream(ctx context.Context, subject string, payload map[string]any, onPiece func([]byte) error) error
}

// keepAliveEvery: an SSE comment after this much silence, so the client does not take a
// model that is thinking for a dead line.
var keepAliveEvery = 15 * time.Second

var modelCalls = map[string]string{
	"/v1/chat/completions": "chat",
	"/v1/responses":        "responses",
}

func openAIError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": kind}})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

// serveModels answers the model routes; false when the path is not one of them.
func serveModels(w http.ResponseWriter, r *http.Request, link Requester) bool {
	api, isCall := modelCalls[r.URL.Path]
	if r.URL.Path != "/v1/models" && !isCall {
		return false
	}
	token := bearer(r)
	if token == "" {
		openAIError(w, http.StatusUnauthorized, "invalid_api_key", "нужен вход в Buff: токен разработчика в Authorization")
		return true
	}
	org, project := r.Header.Get("X-Buff-Org"), r.Header.Get("X-Buff-Project")
	if !isCall {
		listModels(w, r, link, token, org)
		return true
	}
	streamer, ok := link.(Streamer)
	if !ok {
		openAIError(w, http.StatusNotImplemented, "server_error", "этот коннектор не умеет потоковые ответы")
		return true
	}
	if r.Method != http.MethodPost {
		openAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "только POST")
		return true
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		openAIError(w, http.StatusBadRequest, "invalid_request_error", "не удалось прочитать запрос")
		return true
	}
	// Buff Code's session the call belongs to and the assistant message it answers: the
	// backend keeps the turn under them (#369, #439); and what the call is for — the work or
	// a service to it — the cabinet hides the latter (#373).
	relayCall(w, r, streamer, map[string]any{
		"op": "developer_model_call", "token": token, "org": org, "project": project, "api": api, "request": string(body),
		"session": r.Header.Get("X-Buff-Session"), "message": r.Header.Get("X-Buff-Message"), "kind": r.Header.Get("X-Buff-Call"),
	})
	return true
}

func listModels(w http.ResponseWriter, r *http.Request, link Requester, token, org string) {
	ctx, cancel := context.WithTimeout(r.Context(), commandTimeout)
	defer cancel()
	reply, err := link.Request(ctx, "data.cmd.developer_models", map[string]any{"op": "developer_models", "token": token, "org": org})
	if err != nil {
		status := http.StatusBadGateway
		if reply != nil {
			status = http.StatusForbidden
		}
		msg := err.Error()
		if e, ok := reply["error"].(string); ok {
			msg = e
		}
		openAIError(w, status, "invalid_request_error", msg)
		return
	}
	// The name Buff Code shows for a model is the backend's (#435): the tariff for our model,
	// the model itself only where the organisation chose it.
	names, _ := reply["names"].(map[string]any)
	data := []map[string]any{}
	if list, ok := reply["models"].([]any); ok {
		for _, m := range list {
			if id, ok := m.(string); ok {
				model := map[string]any{"id": id, "object": "model", "owned_by": "buff"}
				if name, ok := names[id].(string); ok && name != "" {
					model["name"] = name
				}
				data = append(data, model)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// relayCall streams the backend's answer to the client: the meta line sets the status and
// type, the rest is written as it comes, with keep-alives while an SSE answer is silent.
func relayCall(w http.ResponseWriter, r *http.Request, streamer Streamer, payload map[string]any) {
	flusher, _ := w.(http.Flusher)
	every := keepAliveEvery
	var mu sync.Mutex
	started, sse := false, false
	last := time.Now()
	write := func(b []byte) error {
		mu.Lock()
		defer mu.Unlock()
		last = time.Now()
		if _, err := w.Write(b); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				mu.Lock()
				due := started && sse && time.Since(last) >= every
				mu.Unlock()
				if due {
					_ = write([]byte(": keep-alive\n\n"))
				}
			}
		}
	}()

	var pending []byte // bytes before the meta line is complete
	err := streamer.Stream(r.Context(), "data.cmd.developer_model_call", payload, func(piece []byte) error {
		if started {
			return write(piece)
		}
		pending = append(pending, piece...)
		nl := strings.IndexByte(string(pending), '\n')
		if nl < 0 {
			return nil
		}
		var meta struct {
			Status      int    `json:"status"`
			ContentType string `json:"content_type"`
		}
		if json.Unmarshal(pending[:nl], &meta) != nil || meta.Status == 0 {
			meta.Status, meta.ContentType = http.StatusOK, "application/json"
		}
		mu.Lock()
		w.Header().Set("Content-Type", meta.ContentType)
		if strings.HasPrefix(meta.ContentType, "text/event-stream") {
			sse = true
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.WriteHeader(meta.Status)
		started = true
		mu.Unlock()
		if rest := pending[nl+1:]; len(rest) > 0 {
			return write(rest)
		}
		return write(nil)
	})
	if err == nil || r.Context().Err() != nil {
		return
	}
	if !started {
		openAIError(w, http.StatusBadGateway, "server_error", err.Error())
		return
	}
	if sse {
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": err.Error(), "type": "server_error"}})
		_ = write([]byte("data: " + string(b) + "\n\n"))
	}
}
