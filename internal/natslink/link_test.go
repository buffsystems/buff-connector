package natslink

import "testing"

// nats.go dials a bare `wss://host/nats` at host:0 (no default port for websocket
// schemes) and always requests "/" (the URL's path is dropped) — the relay behind the
// public edge upgrades only "/nats". So the dial URL gets an explicit port and the path
// travels as ProxyPath; nats:// and malformed input pass through untouched.
func TestWsDial(t *testing.T) {
	cases := []struct{ in, dial, path string }{
		{"wss://buff.systems/nats", "wss://buff.systems:443", "/nats"},
		{"ws://relay.local/nats", "ws://relay.local:80", "/nats"},
		{"ws://10.0.0.1:8138/nats", "ws://10.0.0.1:8138", "/nats"},
		{"wss://relay.example:8443/deep/nats", "wss://relay.example:8443", "/deep/nats"},
		{"wss://buff.systems/", "wss://buff.systems:443", ""},
		{"wss://buff.systems", "wss://buff.systems:443", ""},
		{"nats://10.0.0.1:4222", "nats://10.0.0.1:4222", ""},
		{"nats://10.0.0.1:4222/ignored", "nats://10.0.0.1:4222/ignored", ""},
		{"not a url", "not a url", ""},
	}
	for _, c := range cases {
		dial, path := wsDial(c.in)
		if dial != c.dial || path != c.path {
			t.Errorf("wsDial(%q) = (%q, %q), want (%q, %q)", c.in, dial, path, c.dial, c.path)
		}
	}
}
