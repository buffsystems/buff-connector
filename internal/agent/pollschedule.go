package agent

import (
	"time"

	"github.com/buffsystems/buff-connector/internal/config"
)

// How often a connection is read.
//
// One clock, one tick, and a schedule that says which connections are due on
// it. Three places may set the interval and they rank: the connection's own
// setting (the operator knows whether this is a tracker watched all day or an
// archive), then what the plugin suggests (a comment feed with no callback of
// its own wants seconds), then the connector's default. Where a webhook is
// configured events arrive sooner still, and the poll catches what it missed.

// defaultPollEvery is the interval when neither the operator nor the plugin
// says otherwise.
const defaultPollEvery = 20 * time.Second

// pollTickEvery is the clock's resolution — the finest interval a connection
// can ask for.
const pollTickEvery = time.Second

// pollEvery is the effective interval for one connection.
func pollEvery(p *config.PluginInstance, suggested time.Duration) time.Duration {
	if p.PollSeconds > 0 {
		return time.Duration(p.PollSeconds) * time.Second
	}
	if suggested > 0 {
		return suggested
	}
	return defaultPollEvery
}

// pollSchedule books the next read of every enabled connection.
type pollSchedule struct {
	next map[string]time.Time
}

func newPollSchedule() *pollSchedule { return &pollSchedule{next: map[string]time.Time{}} }

// due returns the enabled connections whose interval has elapsed at `now`, and
// books each one's next read from now — a late tick is one read, not a replay
// of every read that was missed. Connections no longer in the config, or
// switched off, are forgotten.
func (s *pollSchedule) due(cfg *config.Config, every func(*config.PluginInstance) time.Duration,
	now time.Time) map[string]bool {
	seen := map[string]bool{}
	out := map[string]bool{}
	for i := range cfg.Plugins {
		p := &cfg.Plugins[i]
		if !p.IsEnabled() {
			continue
		}
		seen[p.ID] = true
		if at, booked := s.next[p.ID]; booked && now.Before(at) {
			continue
		}
		out[p.ID] = true
		s.next[p.ID] = now.Add(every(p))
	}
	for id := range s.next {
		if !seen[id] {
			delete(s.next, id)
		}
	}
	return out
}
