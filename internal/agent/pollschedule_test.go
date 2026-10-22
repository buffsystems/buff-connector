package agent

import (
	"testing"
	"time"

	"github.com/buffsystems/buff-connector/internal/config"
)

// Three places can say how often a connection is read, and they rank: what the
// operator set on the connection, then what the plugin suggests, then the
// connector's own default.
func TestPollIntervalRanksConnectionOverPluginOverDefault(t *testing.T) {
	p := config.PluginInstance{ID: "7QK3M9XBWZ0FVT2H"}
	if got := pollEvery(&p, 0); got != defaultPollEvery {
		t.Errorf("nothing set: %v, want the default %v", got, defaultPollEvery)
	}
	if got := pollEvery(&p, 5*time.Second); got != 5*time.Second {
		t.Errorf("plugin suggests 5s: %v", got)
	}
	p.PollSeconds = 45
	if got := pollEvery(&p, 5*time.Second); got != 45*time.Second {
		t.Errorf("operator set 45s: %v — the connection's own setting must win", got)
	}
}

// The schedule reads each connection on ITS interval, from one clock: a
// connection asking for five seconds is not held to another's twenty, and a
// removed connection is forgotten rather than booked for ever.
func TestPollScheduleReadsEachConnectionOnItsOwnInterval(t *testing.T) {
	fast := config.PluginInstance{ID: "7QK3M9XBWZ0FVT2H", Plugin: "notion", Slug: "notion", PollSeconds: 5}
	slow := config.PluginInstance{ID: "J4RD8N1VC6PQY5ZE", Plugin: "jira", Slug: "jira"}
	cfg := &config.Config{Plugins: []config.PluginInstance{fast, slow}}
	every := func(p *config.PluginInstance) time.Duration { return pollEvery(p, 0) }

	s := newPollSchedule()
	t0 := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	first := s.due(cfg, every, t0)
	if !first[fast.ID] || !first[slow.ID] {
		t.Fatalf("the first tick must read every connection, got %v", first)
	}
	if again := s.due(cfg, every, t0.Add(time.Second)); len(again) != 0 {
		t.Fatalf("one second later nothing is due, got %v", again)
	}
	at5 := s.due(cfg, every, t0.Add(5*time.Second))
	if !at5[fast.ID] || at5[slow.ID] {
		t.Fatalf("at 5s only the fast connection is due, got %v", at5)
	}
	at20 := s.due(cfg, every, t0.Add(20*time.Second))
	if !at20[fast.ID] || !at20[slow.ID] {
		t.Fatalf("at 20s both are due, got %v", at20)
	}

	// The operator switches the fast one off: it is not read, and its booking
	// does not linger.
	off := false
	cfg.Plugins[0].Enabled = &off
	if later := s.due(cfg, every, t0.Add(25*time.Second)); later[fast.ID] {
		t.Fatalf("a disabled connection was read: %v", later)
	}
	if _, booked := s.next[fast.ID]; booked {
		t.Fatal("a connection no longer read must not stay booked")
	}
}

// A late tick — the machine slept, the loop was busy — reads the connection
// once and books the next read from NOW, not from the missed moment: catching
// up by reading five times in a row is five bills for one sweep.
func TestPollScheduleDoesNotReplayMissedTicks(t *testing.T) {
	p := config.PluginInstance{ID: "7QK3M9XBWZ0FVT2H", Plugin: "notion", Slug: "notion", PollSeconds: 5}
	cfg := &config.Config{Plugins: []config.PluginInstance{p}}
	every := func(p *config.PluginInstance) time.Duration { return pollEvery(p, 0) }
	s := newPollSchedule()
	t0 := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s.due(cfg, every, t0)

	late := t0.Add(2 * time.Minute)
	if got := s.due(cfg, every, late); !got[p.ID] {
		t.Fatal("a late tick must still read the connection")
	}
	if got := s.due(cfg, every, late.Add(time.Second)); got[p.ID] {
		t.Fatal("the read after a late tick must wait a whole interval again")
	}
	if got := s.due(cfg, every, late.Add(5*time.Second)); !got[p.ID] {
		t.Fatal("five seconds after the late read the connection is due again")
	}
}
