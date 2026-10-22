package natslink

import (
	"context"
	"encoding/json"
	"testing"
)

// Two processes with one identity — a stand brought up twice, a customer's
// botched restart — must not both execute every tool call. A plain
// subscription hands the request to each of them; a queue group hands it to
// exactly one. The relay does not have to be reachable to see which one the
// responder asks for: the client queues the subscription until it connects.
func TestResponderSubscribesAsAQueueGroupNamedAfterItself(t *testing.T) {
	r, err := Connect("nats://127.0.0.1:1", "cid-42", func() string { return "token" })
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer r.Close()

	sub, err := r.subscribe(context.Background(), func(context.Context, string, json.RawMessage) (any, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()
	if sub.Subject != "events.connector.cid-42.>" {
		t.Fatalf("subject = %q", sub.Subject)
	}
	if sub.Queue != "cid-42" {
		t.Fatalf("queue = %q, want the connector's own identity — a second process must share it", sub.Queue)
	}
}
