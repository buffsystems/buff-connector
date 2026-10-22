package config

import (
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// How often a connection is read is the operator's decision, per connection: a
// tracker somebody watches all day is worth reading every few seconds, an
// archive wiki is not. Zero means "whatever the plugin suggests".
func TestPollSecondsIsPerConnectionAndOptional(t *testing.T) {
	var cfg Config
	err := yaml.Unmarshal([]byte(`
middleware_url: wss://relay.example/nats
state_dir: /tmp/state
plugins:
  - id: 7QK3M9XBWZ0FVT2H
    plugin: notion
    version: 1.0.0
    slug: notion
    poll_seconds: 5
  - id: J4RD8N1VC6PQY5ZE
    plugin: jira
    version: 1.0.0
    slug: jira
`), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a connection with its own interval must validate: %v", err)
	}
	if cfg.Plugins[0].PollSeconds != 5 || cfg.Plugins[1].PollSeconds != 0 {
		t.Fatalf("poll_seconds = %d/%d, want 5/0", cfg.Plugins[0].PollSeconds, cfg.Plugins[1].PollSeconds)
	}
	out, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), "poll_seconds") != 1 {
		t.Errorf("an unset interval must not be written as 0:\n%s", out)
	}
}

// A negative interval is a typo, and a zero-or-less schedule would spin.
func TestNegativePollSecondsIsRefused(t *testing.T) {
	cfg := Config{MiddlewareURL: "wss://relay.example/nats", StateDir: "/tmp/state",
		Plugins: []PluginInstance{{ID: "7QK3M9XBWZ0FVT2H", Plugin: "notion", Version: "1.0.0",
			Slug: "notion", PollSeconds: -1}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "poll_seconds") {
		t.Fatalf("err = %v, want a refusal naming poll_seconds", err)
	}
}
