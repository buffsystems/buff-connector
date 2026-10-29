package devgate

// The connector's tools for the developer's Buff Code (#368): the same catalogue the
// platform's runs use — every connected integration and every operator's own MCP server,
// with their read-only marks, rate limit and call journal — served to Buff Code as a
// published MCP server (Streamable HTTP, JSON answers) at /v1/mcp on this gateway.
//
// The connector stands in the company's network, not on the developer's machine, so the
// door is the gateway and the key is the developer's token. Who holds it is asked of the
// backend (developer_whoami) and remembered for a minute: a device revoked in the cabinet
// is shut out within that minute. Only a member of this connector's organisation is let in.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/buffsystems/buff-connector/internal/i18n"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// Tool is one tool of the connector, named so two connections never collide.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	ReadOnly    bool
}

// Tools is the connector's catalogue (agent.MCPSurface serves it).
type Tools interface {
	// Catalog is the tools as a model working in l is told about them.
	Catalog(l i18n.Lang) []Tool
	// Instructions: what the connections are and the administrator's notes for the AI, in l.
	Instructions(l i18n.Lang) string
	// Call runs a tool; ctx carries the call's language (i18n.WithLang) for its answer.
	Call(ctx context.Context, name string, args json.RawMessage, caller string) (json.RawMessage, error)
}

// whoTTL: how long the backend's answer about a token is trusted.
const whoTTL = time.Minute

// sessionIdle: a session nobody used for this long is forgotten.
const sessionIdle = 24 * time.Hour

var now = time.Now

type who struct {
	login string
	orgs  []string
	err   string
	at    time.Time
}

// surface is what one MCP endpoint serves: its catalogue, what the model is told at the
// handshake, and its calls — each for the developer who asked. The connector's tools (#368)
// and the organisation's knowledge (#371) are two surfaces behind the same door.
type surface interface {
	catalog(l i18n.Lang) []Tool
	instructions(ctx context.Context, c caller) string
	call(ctx context.Context, name string, args json.RawMessage, c caller) (json.RawMessage, error)
}

// caller is the developer behind one request: their login and token, the headers their
// Buff Code sent (the work context rides there), a way to ask them — nil when their
// client cannot show a form — and the language they work in: the one their Buff Code
// names (X-Buff-Lang), else the connector's own. `named` says Buff Code named it, and
// only then is it passed on to the backend, which otherwise answers in the person's last.
type caller struct {
	login, token string
	header       http.Header
	ask          askFunc
	lang         i18n.Lang
	named        bool
}

// callerLang is the language of a request to the gateway, and whether the caller named it.
func callerLang(h http.Header) (i18n.Lang, bool) {
	if l, ok := i18n.Parse(h.Get(i18n.LangHeader)); ok {
		return l, true
	}
	return i18n.Default(), false
}

// toolsSurface serves the connector's catalogue (agent.MCPSurface) as it always was.
type toolsSurface struct{ tools Tools }

func (t toolsSurface) catalog(l i18n.Lang) []Tool { return t.tools.Catalog(l) }
func (t toolsSurface) instructions(_ context.Context, c caller) string {
	return t.tools.Instructions(c.lang)
}
func (t toolsSurface) call(ctx context.Context, name string, args json.RawMessage, c caller) (json.RawMessage, error) {
	return t.tools.Call(i18n.WithLang(ctx, c.lang), name, args, c.login)
}

type mcpServer struct {
	link    Requester
	surface surface
	org     string

	mu       sync.Mutex
	who      map[string]who                  // sha256(token) → the backend's answer
	sessions map[string]session              // Mcp-Session-Id → its developer
	pending  map[string]chan json.RawMessage // session + question id → where the answer goes
}

type session struct {
	login string
	seen  time.Time
	// forms: the client said at the handshake that it can show the person a form.
	forms bool
}

func newMCPServer(link Requester, s surface, org string) *mcpServer {
	return &mcpServer{link: link, surface: s, org: org, who: map[string]who{}, sessions: map[string]session{},
		pending: map[string]chan json.RawMessage{}}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	// An answer to a question the server asked (elicitation): no method, the question's id.
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func rpcError(w http.ResponseWriter, l i18n.Lang, status int, id json.RawMessage, code int, message string) {
	message = i18n.Localize(l, message)
	writeJSON(w, status, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func rpcResult(w http.ResponseWriter, header http.Header, id json.RawMessage, result any) {
	for k, v := range header {
		w.Header()[k] = v
	}
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *mcpServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	lang, named := callerLang(r.Header)
	token := bearer(r)
	if token == "" {
		rpcError(w, lang, http.StatusUnauthorized, nil, -32001, "нужен вход в Buff: токен разработчика в Authorization")
		return
	}
	login, refusal := s.whoIs(r.Context(), token)
	if refusal != "" {
		rpcError(w, lang, http.StatusForbidden, nil, -32001, refusal)
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		s.mu.Lock()
		delete(s.sessions, r.Header.Get("Mcp-Session-Id"))
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		// No server-initiated stream: every answer comes back on its own POST.
		w.Header().Set("Allow", "POST, DELETE")
		rpcError(w, lang, http.StatusMethodNotAllowed, nil, -32000, "только POST")
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Method == "" && len(req.ID) == 0) {
		rpcError(w, lang, http.StatusBadRequest, nil, -32700, "тело запроса — не JSON-RPC")
		return
	}
	who := caller{login: login, token: token, header: r.Header, lang: lang, named: named}
	if req.Method == "initialize" {
		s.initialize(r.Context(), w, req, who)
		return
	}
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		rpcError(w, lang, http.StatusBadRequest, req.ID, -32600, "сначала initialize: сессия не открыта")
		return
	}
	// A session this gateway does not know (it restarted, or the session idled out) is 404, as
	// MCP requires: the client then opens a new one by itself instead of losing its tools.
	if !s.inSession(sessionID, login) {
		rpcError(w, lang, http.StatusNotFound, req.ID, -32001, "сессия не найдена — откройте новую (initialize)")
		return
	}
	if req.Method == "" { // the person's answer to a question a call is waiting on
		s.deliver(w, lang, sessionID, req)
		return
	}
	if len(req.ID) == 0 { // a notification (notifications/initialized and the like)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "ping":
		rpcResult(w, nil, req.ID, map[string]any{})
	case "tools/list":
		rpcResult(w, nil, req.ID, map[string]any{"tools": s.catalog(lang)})
	case "tools/call":
		s.call(r.Context(), w, r, req, who)
	default:
		rpcError(w, lang, http.StatusOK, req.ID, -32601, fmt.Sprintf("метод не поддерживается: %s", req.Method))
	}
}

func (s *mcpServer) initialize(ctx context.Context, w http.ResponseWriter, req rpcRequest, c caller) {
	var p struct {
		Capabilities struct {
			Elicitation json.RawMessage `json:"elicitation"`
		} `json:"capabilities"`
	}
	_ = json.Unmarshal(req.Params, &p)
	id := randomID()
	s.mu.Lock()
	t := now()
	for k, v := range s.sessions {
		if t.Sub(v.seen) > sessionIdle {
			delete(s.sessions, k)
		}
	}
	s.sessions[id] = session{login: c.login, seen: t, forms: len(p.Capabilities.Elicitation) > 0}
	s.mu.Unlock()
	header := http.Header{}
	header.Set("Mcp-Session-Id", id)
	rpcResult(w, header, req.ID, map[string]any{
		"protocolVersion": plugin.MCPProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": "buff-connector", "version": "1"},
		"instructions":    s.surface.instructions(ctx, c),
	})
}

func (s *mcpServer) inSession(id, login string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok || sess.login != login {
		return false
	}
	sess.seen = now()
	s.sessions[id] = sess
	return true
}

func (s *mcpServer) catalog(l i18n.Lang) []map[string]any {
	out := []map[string]any{}
	for _, t := range s.surface.catalog(l) {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, map[string]any{
			"name": t.Name, "description": t.Description, "inputSchema": schema,
			"annotations": map[string]any{"readOnlyHint": t.ReadOnly},
		})
	}
	return out
}

func (s *mcpServer) call(ctx context.Context, w http.ResponseWriter, r *http.Request, req rpcRequest, c caller) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		rpcError(w, c.lang, http.StatusOK, req.ID, -32602, "нужно имя инструмента")
		return
	}
	if len(p.Arguments) == 0 {
		p.Arguments = json.RawMessage(`{}`)
	}
	// A question to the person travels on this call's own answer, which then becomes an
	// event stream — possible only when the client can show a form and read a stream.
	stream := &eventStream{w: w}
	sessionID := r.Header.Get("Mcp-Session-Id")
	if s.forms(sessionID) && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		c.ask = s.asker(sessionID, stream)
	}
	var result any
	out, err := s.surface.call(ctx, p.Name, p.Arguments, c)
	if err != nil {
		// A tool's failure is the model's to read and act on, not a protocol error.
		// In the call's language: an error written in Russian deep inside reads in English
		// to a model working in English.
		result = map[string]any{"content": []any{map[string]any{"type": "text", "text": i18n.LocalizeError(c.lang, err)}}, "isError": true}
	} else {
		result = out
	}
	if stream.started {
		_ = stream.send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		return
	}
	rpcResult(w, nil, req.ID, result)
}

func (s *mcpServer) forms(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id].forms
}

// whoIs names the token's developer, or says why they are not let in.
func (s *mcpServer) whoIs(ctx context.Context, token string) (string, string) {
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	s.mu.Lock()
	cached, ok := s.who[key]
	s.mu.Unlock()
	if !ok || now().Sub(cached.at) >= whoTTL {
		var answered bool
		cached, answered = s.ask(ctx, token)
		// Only the backend's answer is remembered: a line that dropped is asked again.
		if answered {
			s.mu.Lock()
			for k, v := range s.who {
				if now().Sub(v.at) >= whoTTL {
					delete(s.who, k)
				}
			}
			s.who[key] = cached
			s.mu.Unlock()
		}
	}
	if cached.err != "" {
		return "", cached.err
	}
	for _, o := range cached.orgs {
		if o == s.org {
			return cached.login, ""
		}
	}
	return "", fmt.Sprintf("этот коннектор работает для организации %s, а вход выполнен в другую", s.org)
}

func (s *mcpServer) ask(ctx context.Context, token string) (who, bool) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	reply, err := s.link.Request(ctx, "data.cmd.developer_whoami", map[string]any{"op": "developer_whoami", "token": token})
	w := who{at: now()}
	if err != nil {
		w.err = "коннектор не достучался до Buff — попробуйте ещё раз"
		if e, ok := reply["error"].(string); ok && e != "" {
			w.err = e
		}
		return w, reply != nil
	}
	w.login, _ = reply["login"].(string)
	if list, ok := reply["orgs"].([]any); ok {
		for _, o := range list {
			if name, ok := o.(string); ok {
				w.orgs = append(w.orgs, name)
			}
		}
	}
	return w, true
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
