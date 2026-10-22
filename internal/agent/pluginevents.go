package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/natslink"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// Events from plugins.
//
// A plugin reports what happened in the customer's system; this file turns that
// into the event the platform already understands. The platform is
// source-agnostic — it takes an integration, an event id, a ticket and an
// instruction — so nothing about Jira, or about any future tracker, lives up
// there. That is what makes a new integration a bundle rather than a release.
//
// Attribution and the watermark are keyed on the instance GUID, never on the
// alias. An operator renaming a connection must not split their spend history
// in two, and must not make the connector replay a tracker's back catalogue at
// everyone in it.

// How often each instance is read lives in pollschedule.go: per connection,
// from the operator's setting, the plugin's suggestion or the default. Where a
// webhook is configured events arrive sooner and the poll only catches what the
// webhook missed.

// pluginActions are the actions the platform will act on. Refusing an unknown
// one here — beside the plugin that produced it — beats a rejection at the far
// end of a relay, where nobody is reading.
var pluginActions = map[string]bool{"ask": true, "research": true, "task": true}

// PluginEvent is one thing a plugin reports. It matches the SDK's Event.
type PluginEvent struct {
	ID     string `json:"id"`
	At     string `json:"at"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Author string `json:"author"`
	// Who asked, as the system identifies them — the display name above is
	// for people and anyone may set it to anything. Either may be empty when
	// the system does not say (cloud-front#476).
	AuthorLogin string `json:"author_login"`
	AuthorEmail string `json:"author_email"`
	// Visibility is how the request was written — "internal" for a note only
	// the team sees; empty is an ordinary comment. The reply keeps it.
	Visibility string         `json:"visibility"`
	URL        string         `json:"url"`
	Action     string         `json:"action"`
	Extra      map[string]any `json:"extra"`
}

// issue is the ticket the event is about. It travels in extra because it is the
// plugin's own vocabulary: another tracker may key on something else, and the
// platform only needs a stable identifier.
func (e PluginEvent) issue() string { return e.extraString("issue") }

func (e PluginEvent) trigger() string { return e.extraString("trigger") }

// Where the request was written. A comment in a pull request is about the code
// ON that pull request, so the repository and its branch travel with the event
// and the platform answers on that branch instead of the default one. A ticket
// carries neither, and so does an older plugin — both mean "the default branch".
func (e PluginEvent) repo() string { return e.extraString("repo") }

func (e PluginEvent) ref() string { return e.extraString("ref") }

func (e PluginEvent) context() string {
	if c := e.extraString("context"); c != "" {
		return c
	}
	return "issue"
}

// eventContexts are the places a request can be written. Unknown means a plugin
// bug, and the platform is not the place to discover it.
var eventContexts = map[string]bool{"issue": true, "pull": true, "commit": true}

func (e PluginEvent) extraString(k string) string {
	v, _ := e.Extra[k].(string)
	return v
}

func (e PluginEvent) validate() error {
	switch {
	case e.ID == "":
		return fmt.Errorf("event has no id")
	case e.At == "":
		return fmt.Errorf("event %s has no timestamp", e.ID)
	case e.issue() == "":
		return fmt.Errorf("event %s names no ticket (extra.issue)", e.ID)
	case !pluginActions[e.Action]:
		return fmt.Errorf("event %s asks for %q, which is not an action we run", e.ID, e.Action)
	case strings.TrimSpace(e.Body) == "":
		return fmt.Errorf("event %s carries no instruction", e.ID)
	case !eventContexts[e.context()]:
		return fmt.Errorf("event %s comes from %q, which is not a place we read", e.ID, e.context())
	case e.ref() != "" && e.repo() == "":
		return fmt.Errorf("event %s names a branch but no repository", e.ID)
	}
	return nil
}

func buildPluginEventPayload(access string, p config.PluginInstance, e PluginEvent, version string) map[string]any {
	return map[string]any{
		"op":      "connector_event",
		"token":   access,
		"version": version,
		"source":  p.Plugin,
		// The GUID, not the alias: see the note at the top of this file.
		"integration":  p.ID,
		"dev_project":  p.DevProject,
		"event_id":     e.ID,
		"issue_key":    e.issue(),
		"summary":      e.Title,
		"author":       e.Author,
		"author_login": e.AuthorLogin,
		"author_email": e.AuthorEmail,
		"visibility":   e.Visibility,
		// What the connection's list said; the platform refuses a denied one and
		// checks membership for a «members» one before anything runs.
		"askers":      string(config.AskerVerdict(p.Askers, p.AskersMembers, e.AuthorLogin, e.AuthorEmail)),
		"trigger":     e.trigger(),
		"action":      e.Action,
		"instruction": e.Body,
		"url":         e.URL,
		"at":          e.At,
		// Empty for a ticket: the platform then answers on the default branch.
		"context": e.context(),
		"repo":    e.repo(),
		"ref":     e.ref(),
	}
}

// pollPluginEvents reads every instance that offers buff/poll and forwards what
// it reports. `only` narrows the read to the instances a webhook just woke; nil
// reads them all, which is the timed sweep.
func pollPluginEvents(ctx context.Context, link *natslink.Link, m *PluginManager, cfg *config.Config,
	stateDir, access, version string, only map[string]bool) {
	if m == nil {
		return
	}
	for _, p := range cfg.Plugins {
		if !p.IsEnabled() || (only != nil && !only[p.ID]) {
			continue
		}
		inst := m.Instance(p.Slug)
		if inst == nil || !inst.Info().Polls {
			continue
		}
		since := loadInstanceWatermark(stateDir, p.ID)

		pctx, cancel := context.WithTimeout(ctx, reqTimeout)
		var out struct {
			Events []PluginEvent `json:"events"`
		}
		err := inst.Call(pctx, "buff/poll", map[string]any{"since": since}, &out)
		cancel()
		if err != nil {
			log.Printf("plugin %s: poll failed: %v", p.Slug, err)
			continue
		}

		delivered := map[string]bool{}
		for _, e := range out.Events {
			if err := e.validate(); err != nil {
				// Not delivered, and deliberately not retried: a malformed event
				// will be malformed again. Say so once and move on.
				log.Printf("plugin %s: %v (skipped)", p.Slug, err)
				delivered[e.ID] = true
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, reqTimeout)
			_, err := link.Request(rctx, "data.cmd.connector_event", buildPluginEventPayload(access, p, e, version))
			cancel()
			if err != nil {
				log.Printf("plugin %s: report %s: %v", p.Slug, e.ID, err)
				continue
			}
			delivered[e.ID] = true
		}
		if n := len(delivered); n > 0 {
			log.Printf("plugin %s: %d request(s) forwarded", p.Slug, n)
		}
		saveInstanceWatermark(stateDir, p.ID, highWatermark(since, out.Events, delivered))
	}
}

// highWatermark is how far reading may safely resume: the last event that was
// delivered BEFORE the first one that was not. Advancing past a failed event
// would drop a customer's request without a trace, which is the one failure here
// nobody can notice from the outside.
//
// Moments are compared as moments and kept in one spelling, RFC 3339 in UTC:
// trackers spell them their own way — Jira's `…56.846+0000` has no colon in the
// offset — and a watermark that cannot be read back restarts reading from now,
// dropping whatever was written in between (#476).
func highWatermark(since string, events []PluginEvent, delivered map[string]bool) string {
	out, ok := parseMoment(since)
	if !ok {
		return since
	}
	for _, e := range events {
		if !delivered[e.ID] {
			break
		}
		if at, ok := parseMoment(e.At); ok && at.After(out) {
			out = at
		}
	}
	return out.UTC().Format(time.RFC3339Nano)
}

// momentLayouts are the spellings trackers use for a moment: RFC 3339, and the
// same without the colon in the offset (Jira Data Center and Server).
var momentLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700"}

func parseMoment(s string) (time.Time, bool) {
	for _, layout := range momentLayouts {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Watermarks live beside the instance's other state, under its GUID.

func instanceWatermarkPath(stateDir, id string) string {
	return filepath.Join(stateDir, "instances", id, "watermark")
}

// seedInstanceWatermark marks a new connection as caught up, at the moment it
// STARTS.
//
// Called from the manager rather than left to the first poll, and that is the
// whole point: a watermark invented at first-poll time is later than the moment
// the operator switched the connection on, so anything written in between — the
// request they wrote to try it out — is silently swallowed. It happened in the
// end-to-end run, and from the outside it looks exactly like the bot ignoring
// somebody.
func seedInstanceWatermark(stateDir, id string) {
	if _, err := os.Stat(instanceWatermarkPath(stateDir, id)); err == nil {
		return // already reading from somewhere; never move it backwards
	}
	saveInstanceWatermark(stateDir, id, time.Now().UTC().Format(time.RFC3339Nano))
}

// loadInstanceWatermark reads how far a connection has been read. One with no
// watermark at all starts from NOW: the tracker's back catalogue was never
// addressed to it.
func loadInstanceWatermark(stateDir, id string) string {
	b, err := os.ReadFile(instanceWatermarkPath(stateDir, id))
	if err != nil {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	// Read in any spelling an earlier connector may have saved, returned in one.
	at, ok := parseMoment(string(b))
	if !ok {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return at.UTC().Format(time.RFC3339Nano)
}

func saveInstanceWatermark(stateDir, id, at string) {
	path := instanceWatermarkPath(stateDir, id)
	// 0711 on instances/ is what makes an instance's directory traversable but
	// not listable; the per-instance directory itself stays private.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("plugin %s: persist watermark: %v", id, err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(at), 0o600); err != nil {
		log.Printf("plugin %s: persist watermark: %v", id, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("plugin %s: persist watermark: %v", id, err)
	}
}

// deliverCallback hands a validated callback to the plugin that parses payloads,
// and forwards whatever events it makes of it.
//
// The events go through the SAME validation and the same reporting as a poll's:
// a source that can only push must not get a shortcut past the checks that
// protect the platform. The watermark is deliberately left alone — a callback is
// out of band, and moving the mark on it would skip whatever a poll has not read
// yet.
func deliverCallback(ctx context.Context, link *natslink.Link, m *PluginManager, cfg *config.Config,
	access, version string, c webhookCall) {
	if m == nil {
		return
	}
	p := instanceOf(cfg, c.instanceID)
	if p == nil || !p.IsEnabled() {
		return
	}
	inst := m.Instance(p.Slug)
	if inst == nil {
		log.Printf("plugin %s: callback arrived while the connection is down", p.Slug)
		return
	}
	pctx, cancel := context.WithTimeout(ctx, reqTimeout)
	defer cancel()
	var out struct {
		Events []PluginEvent `json:"events"`
	}
	err := inst.Call(pctx, "buff/webhook", map[string]any{
		"headers": c.headers,
		"body":    string(c.body),
	}, &out)
	if err != nil {
		log.Printf("plugin %s: callback refused: %v", p.Slug, err)
		return
	}
	delivered := 0
	for _, e := range out.Events {
		if verr := e.validate(); verr != nil {
			log.Printf("plugin %s: %v (skipped)", p.Slug, verr)
			continue
		}
		rctx, rcancel := context.WithTimeout(ctx, reqTimeout)
		_, rerr := link.Request(rctx, "data.cmd.connector_event", buildPluginEventPayload(access, *p, e, version))
		rcancel()
		if rerr != nil {
			log.Printf("plugin %s: report %s: %v", p.Slug, e.ID, rerr)
			continue
		}
		delivered++
	}
	if delivered > 0 {
		log.Printf("plugin %s: %d request(s) from a callback", p.Slug, delivered)
	}
}

func instanceOf(cfg *config.Config, id string) *config.PluginInstance {
	for i := range cfg.Plugins {
		if cfg.Plugins[i].ID == id {
			return &cfg.Plugins[i]
		}
	}
	return nil
}

// pluginWebhookRoutes marks which routes carry the payload. The manifest is the
// authority — the connection's settings say WHERE to listen, the plugin says
// what it can do with what arrives.
func pluginWebhookRoutes(cfg *config.Config, m *PluginManager) []webhookRoute {
	routes := webhookRoutes(cfg)
	if m == nil {
		return routes
	}
	for i := range routes {
		p := instanceOf(cfg, routes[i].instanceID)
		if p == nil {
			continue
		}
		if in, err := m.Store().Resolve(p.Plugin, p.Version); err == nil {
			routes[i].deliver = in.Manifest.Webhook == plugin.WebhookDeliver
			routes[i].signed = in.Manifest.Signature
			routes[i].stateDir = cfg.StateDir
		}
	}
	return routes
}
