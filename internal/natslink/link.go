// Package natslink is the connector agent's transport to the middleware. It
// connects as a locked-down GUEST (exactly like the browser SPA) and exchanges
// commands via publish + a gid-scoped reply inbox.
package natslink

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

// Link is a guest connection to the middleware relay.
type Link struct {
	nc      *nats.Conn
	gid     string
	seq     atomic.Uint64
	timeout time.Duration
}

// ConnectGuest dials the middleware as a guest. `url` is the middleware's NATS
// (ws://, wss://, or nats://) endpoint. The connection auto-reconnects.
func ConnectGuest(rawURL string, timeout time.Duration) (*Link, error) {
	gid := newGid()
	opts := []nats.Option{
		nats.Token("guest." + gid),
		// The guest may only sub `_INBOX.<gid>.>`, so keep every inbox under it.
		nats.CustomInboxPrefix("_INBOX." + gid),
		nats.Name("buff-connector"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.RetryOnFailedConnect(true),
	}
	dial, proxyPath := wsDial(rawURL)
	if proxyPath != "" {
		opts = append(opts, nats.ProxyPath(proxyPath))
	}
	nc, err := nats.Connect(dial, opts...)
	if err != nil {
		return nil, fmt.Errorf("подключение к relay: %w", err)
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Link{nc: nc, gid: gid, timeout: timeout}, nil
}

// wsDial turns a ws:// / wss:// middleware URL into what nats.go can actually dial.
// nats.go neither defaults the port for websocket schemes (a bare `wss://host/nats`
// dials host:0) nor sends the URL's path (it always requests "/", which the relay behind
// the public edge does not upgrade). So the port is made explicit (443 / 80) and the path
// travels separately as nats.ProxyPath. Anything else (nats://, a malformed string) is
// returned untouched with no path.
func wsDial(rawURL string) (dial string, proxyPath string) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") {
		return rawURL, ""
	}
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "wss" {
			port = "443"
		}
		u.Host = u.Hostname() + ":" + port
	}
	if u.Path != "" && u.Path != "/" {
		proxyPath = u.Path
	}
	u.Path, u.RawPath = "", ""
	return u.String(), proxyPath
}

// Request publishes a command and awaits its reply on a gid-scoped inbox. It
// mirrors the SPA: the reply subject is carried in the body (the backend replies
// to cmd.reply). A reply with ok:false / an error field becomes a Go error.
func (l *Link) Request(ctx context.Context, subject string, payload map[string]any) (map[string]any, error) {
	seq := l.seq.Add(1)
	reply := replySubject(l.gid, seq)
	sub, err := l.nc.SubscribeSync(reply)
	if err != nil {
		return nil, fmt.Errorf("подписка на ответ: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	body, err := envelope(payload, reply)
	if err != nil {
		return nil, err
	}
	if err := l.nc.Publish(subject, body); err != nil {
		return nil, fmt.Errorf("отправка %s: %w", subject, err)
	}
	_ = l.nc.Flush()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), l.timeout)
		defer cancel()
	}
	// A big answer comes in size chunks (#337): read until it is whole.
	var a assembler
	for {
		msg, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("ожидание ответа на %s: %w", subject, err)
		}
		ev, err := a.feed(msg.Data)
		if err != nil {
			return nil, err
		}
		switch ev.kind {
		case evNone:
			continue
		case evWhole:
			return decodeReply(subject, ev.body)
		default:
			return nil, fmt.Errorf("%s: ожидался ответ, пришёл поток", subject)
		}
	}
}

// decodeReply turns a whole answer into a map; ok:false / an error field becomes a Go error.
func decodeReply(subject string, body []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("разбор ответа: %w", err)
	}
	if e, ok := m["error"]; ok && e != nil {
		return m, fmt.Errorf("%s: %v", subject, e)
	}
	if ok, present := m["ok"].(bool); present && !ok {
		return m, fmt.Errorf("%s не удалось", subject)
	}
	return m, nil
}

// StreamSilence is how long a stream may be completely silent — not even a beat — before
// the reader takes the line as dead. The producer beats every 15 s (#365).
var StreamSilence = 60 * time.Second

// streamAckEvery is how many pieces the reader takes before acknowledging them; well under
// the producer's window of 64, so it never stalls on a reader that is keeping up.
const streamAckEvery = 16

// Stream publishes a command whose answer is streamed (#365) and hands each piece to
// onPiece as it arrives. It returns nil when the stream ends normally, the stream's own
// error when it ends abnormally, and the refusal when the backend answers plainly instead.
// Cancelling ctx cancels the stream at the producer.
func (l *Link) Stream(ctx context.Context, subject string, payload map[string]any, onPiece func([]byte) error) error {
	seq := l.seq.Add(1)
	reply := replySubject(l.gid, seq)
	sub, err := l.nc.SubscribeSync(reply)
	if err != nil {
		return fmt.Errorf("подписка на ответ: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	body, err := envelope(payload, reply)
	if err != nil {
		return err
	}
	if err := l.nc.Publish(subject, body); err != nil {
		return fmt.Errorf("отправка %s: %w", subject, err)
	}
	_ = l.nc.Flush()

	var a assembler
	taken := 0
	asked := -1 // the frame a resume was last asked from — one request per gap (#404)
	resume := func() {
		if asked != a.nextSeq {
			asked = a.nextSeq
			log.Print(i18n.Sprintf("%s: кадр %d потока не пришёл — прошу дослать", subject, a.nextSeq))
			l.control(a.streamID, map[string]any{"resume": a.nextSeq})
		}
	}
	for {
		wait, cancel := context.WithTimeout(ctx, StreamSilence)
		msg, err := sub.NextMsgWithContext(wait)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				l.control(a.streamID, map[string]any{"cancel": true})
				return ctx.Err()
			}
			// Silence may be a lost frame with nothing after it: ask once before giving up.
			if a.streamID != "" && asked != a.nextSeq {
				resume()
				continue
			}
			return fmt.Errorf("%s: бэкенд замолчал — ни кадра за %s", subject, StreamSilence)
		}
		ev, err := a.feed(msg.Data)
		if err != nil {
			l.control(a.streamID, map[string]any{"cancel": true})
			return err
		}
		switch ev.kind {
		case evGap:
			resume()
		case evWhole:
			_, err := decodeReply(subject, ev.body)
			if err == nil {
				err = fmt.Errorf("%s: ожидался поток, пришёл обычный ответ", subject)
			}
			return err
		case evPiece:
			if err := onPiece(ev.body); err != nil {
				l.control(a.streamID, map[string]any{"cancel": true})
				return err
			}
			if taken++; taken%streamAckEvery == 0 {
				l.control(a.streamID, map[string]any{"ack": a.lastSeq})
			}
		case evEnd:
			l.control(a.streamID, map[string]any{"ack": a.endSeq})
			return nil
		case evFail:
			l.control(a.streamID, map[string]any{"ack": a.endSeq})
			return fmt.Errorf("%s", ev.err)
		}
	}
}

// control tells a running stream the reader's ack or cancel (#365). Fire-and-forget: the
// backend answers into an inbox nobody reads.
func (l *Link) control(id string, body map[string]any) {
	if id == "" {
		return
	}
	payload := map[string]any{"op": "stream_ctl", "id": id}
	for k, v := range body {
		payload[k] = v
	}
	msg, err := envelope(payload, replySubject(l.gid, l.seq.Add(1)))
	if err != nil {
		return
	}
	_ = l.nc.Publish("data.cmd.stream_ctl", msg)
}

// Conn exposes the underlying connection (for subscriptions in later milestones).
func (l *Link) Conn() *nats.Conn { return l.nc }

// Gid returns this link's guest id.
func (l *Link) Gid() string { return l.gid }

// Close drains and closes the connection.
func (l *Link) Close() {
	if l.nc != nil {
		_ = l.nc.Drain()
	}
}
