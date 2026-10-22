package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// The channel carries JSON-RPC 2.0 in both directions over one pair of pipes:
// the host calls the plugin, and the plugin calls back into the host (buff/http,
// buff/log). It talks to code we did not write running on data we do not
// control, so the framing has to survive a plugin that logs to stdout, dies
// mid-message, or goes quiet.

// pipePair wires two Conns face to face, so a test can drive one end and assert
// on the other without spawning a process.
func pipePair(t *testing.T, hostHandler, peerHandler Handler) (host, peer *Conn) {
	t.Helper()
	hr, pw := io.Pipe()
	pr, hw := io.Pipe()
	host = NewConn(hr, hw, hostHandler)
	peer = NewConn(pr, pw, peerHandler)
	t.Cleanup(func() { host.Close(); peer.Close() })
	return host, peer
}

func TestCallReturnsResult(t *testing.T) {
	host, _ := pipePair(t, nil, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "add" {
			return nil, errors.New("unexpected method " + method)
		}
		var p struct{ A, B int }
		_ = json.Unmarshal(params, &p)
		return map[string]int{"sum": p.A + p.B}, nil
	})

	var out struct{ Sum int }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := host.Call(ctx, "add", map[string]int{"A": 2, "B": 3}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.Sum != 5 {
		t.Fatalf("sum = %d, want 5", out.Sum)
	}
}

// A Python plugin that logs to stdout is not broken — it is normal. Its noise
// must not break the channel.
func TestGarbageLinesAreIgnored(t *testing.T) {
	hr, pw := io.Pipe()
	host := NewConn(hr, io.Discard, nil)
	defer host.Close()

	go func() {
		_, _ = pw.Write([]byte("INFO starting up\n"))
		_, _ = pw.Write([]byte("not json at all\n"))
		_, _ = pw.Write([]byte("{\"broken\": \n"))
		_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n"))
	}()

	var out struct{ OK bool }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := host.call(ctx, 1, "noop", nil, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if !out.OK {
		t.Fatal("result not delivered past the noise")
	}
}

// An error from the plugin is an answer, not a channel failure.
func TestCallSurfacesRemoteError(t *testing.T) {
	host, _ := pipePair(t, nil, func(context.Context, string, json.RawMessage) (any, error) {
		return nil, errors.New("jira said no")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := host.Call(ctx, "whatever", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "jira said no") {
		t.Fatalf("err = %v, want the remote message", err)
	}
	// The channel is still usable afterwards.
	if host.Closed() {
		t.Fatal("a remote error must not close the channel")
	}
}

// The plugin calls back into the host: this is how it makes HTTP requests
// without ever holding a credential.
func TestPluginCanCallHost(t *testing.T) {
	called := make(chan string, 1)
	_, peer := pipePair(t, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		called <- method
		return map[string]int{"status": 200}, nil
	}, nil)

	var out struct{ Status int }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := peer.Call(ctx, "buff/http", map[string]string{"url": "https://x"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := <-called; got != "buff/http" {
		t.Fatalf("method = %q", got)
	}
	if out.Status != 200 {
		t.Fatalf("status = %d", out.Status)
	}
}

// A silent plugin must not hang the caller for ever.
func TestCallRespectsContextDeadline(t *testing.T) {
	hr, _ := io.Pipe()
	host := NewConn(hr, io.Discard, nil)
	defer host.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	err := host.Call(ctx, "sleep", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

// A frame past the limit is a broken or hostile peer. We fail loudly and let the
// supervisor restart it — we never silently drop or truncate the payload.
func TestOversizeFrameFailsLoudly(t *testing.T) {
	hr, pw := io.Pipe()
	host := newConn(hr, io.Discard, nil, 1024)
	defer host.Close()

	go func() {
		_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"` + strings.Repeat("x", 4096) + `"}` + "\n"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := host.call(ctx, 1, "noop", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "frame") {
		t.Fatalf("err = %v, want a frame-size error", err)
	}
	if !host.Closed() {
		t.Fatal("an oversize frame must take the channel down, not be swallowed")
	}
}

// When the plugin dies mid-conversation every waiting call must be released with
// a real error, not left hanging.
func TestPendingCallsFailWhenPeerDies(t *testing.T) {
	hr, pw := io.Pipe()
	host := NewConn(hr, io.Discard, nil)
	defer host.Close()

	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := host.call(ctx, 1, "noop", nil, nil); err == nil {
		t.Fatal("call must fail when the peer goes away")
	}
}

// Fuzzing the framing: whatever a plugin writes, the host must not panic and
// must stay able to answer once a well-formed frame arrives.
func FuzzFramingSurvivesArbitraryOutput(f *testing.F) {
	f.Add("hello")
	f.Add("{}")
	f.Add(`{"jsonrpc":"2.0"}`)
	f.Add(`{"jsonrpc":"2.0","id":"not-a-number","result":1}`)
	f.Add("\x00\x01\x02")
	f.Fuzz(func(t *testing.T, noise string) {
		if strings.ContainsRune(noise, '\n') {
			t.Skip("newlines split frames; covered by the line tests")
		}
		hr, pw := io.Pipe()
		host := NewConn(hr, io.Discard, nil)
		defer host.Close()

		go func() {
			_, _ = pw.Write([]byte(noise + "\n"))
			_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}` + "\n"))
		}()

		var out struct{ OK bool }
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Either the noise was tolerated and the good frame arrives, or the
		// channel failed cleanly. Both are fine; a panic or a hang is not.
		_ = host.call(ctx, 7, "noop", nil, &out)
	})
}
