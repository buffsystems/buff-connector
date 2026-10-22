package devgate

// Asking the person from inside a tool call (MCP elicitation, #371). The call's own answer
// turns into an event stream: the server sends the question on it, Buff Code shows the person
// a form and POSTs their answer back under the same session, and the call's result follows
// on the stream. The model cannot answer in the person's place: the question never reaches it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// askFunc shows the person a form and says what they chose.
type askFunc func(ctx context.Context, message string, schema map[string]any) (elicitation, error)

// elicitation is the person's answer: accept (with the form's content), decline or cancel.
type elicitation struct {
	Action  string         `json:"action"`
	Content map[string]any `json:"content"`
}

// askTimeout: how long a question waits for the person before the call gives up on it.
const askTimeout = 30 * time.Minute

// eventStream is a call's answer as Server-Sent Events, started on the first message.
type eventStream struct {
	w       http.ResponseWriter
	started bool
}

func (e *eventStream) send(v any) error {
	if !e.started {
		e.w.Header().Set("Content-Type", "text/event-stream")
		e.w.Header().Set("Cache-Control", "no-cache")
		e.w.WriteHeader(http.StatusOK)
		e.started = true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(e.w, "event: message\ndata: %s\n\n", b); err != nil {
		return err
	}
	if f, ok := e.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// asker asks on stream and waits for the answer delivered to this session.
func (s *mcpServer) asker(sessionID string, stream *eventStream) askFunc {
	return func(ctx context.Context, message string, schema map[string]any) (elicitation, error) {
		id := "elicit-" + randomID()
		key := sessionID + "\x00" + id
		answer := make(chan json.RawMessage, 1)
		s.mu.Lock()
		s.pending[key] = answer
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.pending, key)
			s.mu.Unlock()
		}()
		question := map[string]any{"jsonrpc": "2.0", "id": id, "method": "elicitation/create",
			"params": map[string]any{"message": message, "requestedSchema": schema}}
		if err := stream.send(question); err != nil {
			return elicitation{}, err
		}
		ctx, cancel := context.WithTimeout(ctx, askTimeout)
		defer cancel()
		select {
		case raw := <-answer:
			var e elicitation
			if err := json.Unmarshal(raw, &e); err != nil {
				return elicitation{Action: "cancel"}, nil
			}
			return e, nil
		case <-ctx.Done():
			return elicitation{}, errors.New("пользователь не ответил на вопрос — ничего не сделано")
		}
	}
}

// deliver hands the person's answer to the call waiting on it. An answer nobody asked for
// in this session is refused: one session cannot answer another's question.
func (s *mcpServer) deliver(w http.ResponseWriter, sessionID string, req rpcRequest) {
	var id string
	_ = json.Unmarshal(req.ID, &id)
	s.mu.Lock()
	answer, ok := s.pending[sessionID+"\x00"+id]
	s.mu.Unlock()
	if !ok {
		rpcError(w, http.StatusBadRequest, req.ID, -32600, "на этот вопрос ответа не ждут")
		return
	}
	result := req.Result
	if len(req.Error) > 0 || len(result) == 0 { // the client failed to ask: as good as «cancel»
		result = json.RawMessage(`{"action":"cancel"}`)
	}
	select {
	case answer <- result:
	default: // answered already; the first answer stands
	}
	w.WriteHeader(http.StatusAccepted)
}
