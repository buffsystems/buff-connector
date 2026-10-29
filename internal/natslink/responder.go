package natslink

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// The connector's SECOND connection to the relay.
//
// The first one is a guest: it publishes commands and reads its own inbox. That
// shape cannot receive anything the platform wants to ASK, because a guest has
// nothing to subscribe to. So a connector that answers tool calls connects again
// with its own access token, which places it in its organisation's account and
// in a subspace named after itself, where it does one thing: respond.
//
// Its permissions were written when the connector was designed and have sat
// unused since: subscribe to events.connector.<cid>.>, publish only to inboxes,
// answer at most once per request. This is the code that finally uses them.
//
// The token expires and is refreshed while the process runs, so the token is
// read through a callback rather than captured at dial time — otherwise the
// first reconnect after a refresh would present a token that has already
// expired, and the connector would drop out of reach with nothing in the log
// pointing at why.

// Responder is the connector's answering connection.
type Responder struct {
	nc      *nats.Conn
	cid     string
	mu      sync.RWMutex
	handler func(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// TokenFunc hands back the connector's CURRENT access token.
type TokenFunc func() string

// Connect dials the relay with the connector's own identity.
func Connect(url, cid string, token TokenFunc) (*Responder, error) {
	r := &Responder{cid: cid}
	dial, proxyPath := wsDial(url)
	opts := []nats.Option{
		// Re-read on every connect, including reconnects after a refresh.
		nats.TokenHandler(func() string { return token() }),
		nats.Name("buff-connector-" + cid),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.RetryOnFailedConnect(true),
		// A connector that comes back after a long pause presents an EXPIRED
		// token first; the relay refuses it, and nats.go's default is to stop
		// reconnecting after repeated refusals from one server — which left the
		// answering side dead (every tool call a 503) while the main link had
		// long since refreshed its token and carried on. Keep trying: the token
		// handler above hands over the fresh token on the next attempt.
		nats.IgnoreAuthErrorAbort(),
	}
	if proxyPath != "" {
		opts = append(opts, nats.ProxyPath(proxyPath))
	}
	nc, err := nats.Connect(dial, opts...)
	if err != nil {
		return nil, fmt.Errorf("подключение как коннектор: %w", err)
	}
	r.nc = nc
	return r, nil
}

// Serve answers requests addressed to this connector until the context ends.
func (r *Responder) Serve(ctx context.Context, h func(ctx context.Context, method string, params json.RawMessage) (any, error)) error {
	sub, err := r.subscribe(ctx, h)
	if err != nil {
		return err
	}
	<-ctx.Done()
	_ = sub.Unsubscribe()
	return nil
}

// subscribe joins the QUEUE named after this connector's identity rather than
// subscribing plainly. Two processes with one identity — a stand brought up
// twice, a customer's botched restart — would otherwise both receive every
// request and both execute it: a writing tool acting twice in the customer's
// tracker, and the ledger showing two rows per call with the second answer
// silently dropped by the relay. In a queue group the relay hands each
// request to exactly one member, so a duplicate process is idle, not double.
func (r *Responder) subscribe(ctx context.Context, h func(ctx context.Context, method string, params json.RawMessage) (any, error)) (*nats.Subscription, error) {
	r.mu.Lock()
	r.handler = h
	r.mu.Unlock()

	subject := "events.connector." + r.cid + ".>"
	sub, err := r.nc.QueueSubscribe(subject, r.cid, func(msg *nats.Msg) {
		go r.answer(ctx, msg)
	})
	if err != nil {
		return nil, fmt.Errorf("подписка на %s: %w", subject, err)
	}
	return sub, nil
}

// answer handles one request. Every outcome produces a reply — a request left
// unanswered would hold a pipeline stage until its timeout with nothing to show
// for it.
func (r *Responder) answer(ctx context.Context, msg *nats.Msg) {
	if msg.Reply == "" {
		return
	}
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		r.replyError(msg, "request is not readable: "+err.Error())
		return
	}
	r.mu.RLock()
	h := r.handler
	r.mu.RUnlock()
	if h == nil {
		r.replyError(msg, "connector is not ready to answer yet")
		return
	}
	result, err := h(ctx, req.Method, req.Params)
	if err != nil {
		r.replyError(msg, err.Error())
		return
	}
	body, err := json.Marshal(map[string]any{"ok": true, "result": result})
	if err != nil {
		r.replyError(msg, "result is not encodable: "+err.Error())
		return
	}
	_ = msg.Respond(body)
}

func (r *Responder) replyError(msg *nats.Msg, text string) {
	body, _ := json.Marshal(map[string]any{"ok": false, "error": text})
	_ = msg.Respond(body)
}

// Close ends the connection.
func (r *Responder) Close() {
	if r.nc != nil {
		r.nc.Close()
	}
}

// Connected reports whether the answering connection is live, so the report can
// say "the connector is reachable" rather than leaving the platform to guess.
func (r *Responder) Connected() bool { return r.nc != nil && r.nc.IsConnected() }
