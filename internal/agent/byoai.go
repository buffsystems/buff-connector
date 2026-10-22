package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OwnModel is the customer's own OpenAI-compatible server, in their perimeter (#517, Bring
// Your Own AI). The platform reaches it only through this connector: the model gateway asks
// over the relay, the connector calls the server with the customer's key and answers whole —
// the relay carries one reply per request, so the call is made without streaming.
type OwnModel struct {
	base   string
	key    string
	client *http.Client
}

func NewOwnModel(base, key string, client *http.Client) *OwnModel {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	return &OwnModel{base: strings.TrimRight(base, "/"), key: key, client: client}
}

// Chat forwards one chat completion and returns the server's status and body as is.
func (m *OwnModel) Chat(ctx context.Context, params json.RawMessage) (any, error) {
	if m == nil || m.base == "" {
		return nil, errors.New("свой сервер моделей на коннекторе не настроен")
	}
	var in struct {
		Request map[string]any `json:"request"`
	}
	if err := json.Unmarshal(params, &in); err != nil || in.Request == nil {
		return nil, errors.New("вызов модели без запроса")
	}
	in.Request["stream"] = false
	delete(in.Request, "stream_options")
	body, _ := json.Marshal(in.Request)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.key != "" {
		req.Header.Set("Authorization", "Bearer "+m.key)
	}
	res, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("свой сервер моделей не ответил: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		// Not JSON (a proxy's error page): carried as text, the gateway reports the status.
		raw, _ = json.Marshal(string(raw))
	}
	return map[string]any{"status": res.StatusCode, "body": json.RawMessage(raw)}, nil
}

// relayHandler answers the platform's requests: model calls to the customer's own server,
// everything else to the tool surface.
func relayHandler(tools func(context.Context, string, json.RawMessage) (any, error), model func(context.Context, json.RawMessage) (any, error)) func(context.Context, string, json.RawMessage) (any, error) {
	return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method == "model/chat" {
			return model(ctx, params)
		}
		return tools(ctx, method, params)
	}
}
