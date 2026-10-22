// Package agent orchestrates the connector: pairing (one-time) and running
// (heartbeat + repo reporting + token refresh). It ties together config,
// identity, and the middleware link.
package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/buffsystems/buff-connector/internal/catalog"
	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/devgate"
	"github.com/buffsystems/buff-connector/internal/gitutil"
	"github.com/buffsystems/buff-connector/internal/identity"
	"github.com/buffsystems/buff-connector/internal/natslink"
	"github.com/buffsystems/buff-connector/internal/plugin"
	"github.com/buffsystems/buff-connector/internal/provider"
	"github.com/buffsystems/buff-connector/internal/transport"
)

// Repo is one served repo reported to the registry (json shape matches the backend).
type Repo struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	Repo     string `json:"repo"`
	HasCreds bool   `json:"has_creds"`
}

const (
	refreshMarginSec = 60 // refresh this long before the access token expires
	minRefreshSec    = 5  // never schedule a refresh sooner than this
	heartbeatEvery   = 30 * time.Second
	jobsEvery        = 15 * time.Second
	watchEvery       = 3 * time.Second // config file watch → live plug-and-play reload
	reqTimeout       = 30 * time.Second
)

// decodeClaims decodes (WITHOUT verifying) a JWT's payload. The agent only ever
// decodes its OWN tokens, to read cid/jti/exp.
func decodeClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}
	return m, nil
}

// reposFromConfig flattens the provider allowlists into the report shape. A
// configured provider always carries a token, so has_creds is true here; real
// validity is refined by the provider adapter (checkAccess) in a later milestone.
func reposFromConfig(cfg *config.Config) []Repo {
	var out []Repo
	for _, p := range cfg.Providers {
		for _, r := range p.Repos {
			out = append(out, Repo{Provider: p.Kind, BaseURL: p.BaseURL, Repo: r, HasCreds: true})
		}
	}
	return out
}

func buildClaimPayload(id identity.Identity, label string) map[string]any {
	return map[string]any{"op": "connector_claim", "pubkey": id.PubB64(), "label": label}
}

func buildReportPayload(access string, repos []Repo, version string) map[string]any {
	return map[string]any{"op": "connector_report", "token": access, "repos": repos, "version": version}
}

// buildFullReportPayload adds what the connector runs, not just what it serves
// (#279): the plugin connections, the installed code, and the tool calls made
// since the last report.
//
// Fields are OMITTED when there is nothing to say, never sent empty. An older
// agent sends none of them, and the platform must not read a missing field as
// "the operator deleted everything".
func buildFullReportPayload(access string, repos []Repo, version string,
	pm *PluginManager, mcp *MCPSurface) map[string]any {
	payload := buildReportPayload(access, repos, version)
	if pm == nil {
		return payload
	}
	// The connector's own version is checked against the same signed catalogue:
	// an operator running a core with a known defect is worth telling, and
	// telling them should not need a second thing to trust.
	if idx := pm.index(); idx != nil {
		if newer := idx.CoreUpdate(version); newer != "" {
			payload["core_latest"] = newer
		}
	}
	if reports := pm.Reports(); len(reports) > 0 {
		payload["instances"] = reports
	}
	if list, err := pm.Store().List(); err == nil && len(list) > 0 {
		plugins := make([]map[string]string, 0, len(list))
		for _, in := range list {
			plugins = append(plugins, map[string]string{
				"name": in.Manifest.Name, "version": in.Manifest.Version, "digest": in.Digest,
			})
		}
		payload["plugins"] = plugins
	}
	if mcp != nil {
		if calls := mcp.TakeCalls(); len(calls) > 0 {
			payload["tool_calls"] = calls
		}
	}
	return payload
}

func buildJobsPayload(access, version string) map[string]any {
	return map[string]any{"op": "connector_jobs", "token": access, "version": version}
}

func buildUploadPayload(access, jobID string, c transport.Chunk) map[string]any {
	return map[string]any{"op": "connector_upload", "token": access, "job_id": jobID, "seq": c.Seq, "data": c.Data, "last": c.Last}
}

func buildDownloadPayload(access, jobID string, seq int) map[string]any {
	return map[string]any{"op": "connector_download", "token": access, "job_id": jobID, "seq": seq}
}

func buildJobResultPayload(access, jobID, status, prURL, errStr string) map[string]any {
	return map[string]any{"op": "connector_job_result", "token": access, "job_id": jobID, "status": status, "pr_url": prURL, "error": errStr}
}

// tokenForJob finds the git token the operator configured for a job's
// (provider, base_url, repo). Returns ("", false) if this agent doesn't serve it.
func tokenForJob(cfg *config.Config, provider, baseURL, repo string) (string, bool) {
	for _, p := range cfg.Providers {
		if p.Kind != provider || p.BaseURL != baseURL {
			continue
		}
		for _, r := range p.Repos {
			if r == repo {
				return p.Token, true
			}
		}
	}
	return "", false
}

func buildRefreshPayload(id identity.Identity, refresh, version string) (map[string]any, error) {
	claims, err := decodeClaims(refresh)
	if err != nil {
		return nil, err
	}
	cid, _ := claims["cid"].(string)
	jti, _ := claims["jti"].(string)
	if cid == "" || jti == "" {
		return nil, fmt.Errorf("refresh token missing cid/jti")
	}
	sig := id.Sign(identity.PopMessage(cid, jti))
	return map[string]any{"op": "connector_refresh", "refresh": refresh, "sig": sig, "version": version}, nil
}

// nextRefreshDelaySec = exp - now - margin, floored at minRefreshSec.
func nextRefreshDelaySec(exp, now, margin int64) int64 {
	d := exp - now - margin
	if d < minRefreshSec {
		return minRefreshSec
	}
	return d
}

func refreshDelay(access string) time.Duration {
	claims, err := decodeClaims(access)
	if err != nil {
		return minRefreshSec * time.Second
	}
	exp, _ := claims["exp"].(float64)
	return time.Duration(nextRefreshDelaySec(int64(exp), time.Now().Unix(), refreshMarginSec)) * time.Second
}

// requester is all the job path needs from the link: send a command, read the
// answer. Naming it here is what lets a test watch what a failing job reports —
// and a job that reports nothing is the bug this interface was added for.
type requester interface {
	Request(ctx context.Context, subject string, payload map[string]any) (map[string]any, error)
}

// reportJobResult tells the platform how a job ended. Until it arrives the job
// stays claimed: the platform cannot tell a connector still working from one
// that gave up, so every path out of a job goes through here.
func reportJobResult(ctx context.Context, link requester, access, jobID, status, prURL, errStr string) {
	rctx, cancel := context.WithTimeout(ctx, reqTimeout)
	defer cancel()
	_, _ = link.Request(rctx, "data.cmd.connector_job_result",
		buildJobResultPayload(access, jobID, status, prURL, errStr))
}

// pollJobs fetches pending jobs and runs each. Import jobs clone the provider
// repo locally and stream a bundle up; other kinds are logged and skipped for now.
func pollJobs(ctx context.Context, link requester, cfg *config.Config, m *PluginManager, access, version string) {
	rctx, cancel := context.WithTimeout(ctx, reqTimeout)
	resp, err := link.Request(rctx, "data.cmd.connector_jobs", buildJobsPayload(access, version))
	cancel()
	if err != nil {
		log.Printf("poll jobs failed: %v", err)
		return
	}
	jobs, _ := resp["jobs"].([]any)
	for _, j := range jobs {
		job, ok := j.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := job["kind"].(string)
		id, _ := job["id"].(string)
		switch kind {
		case "import":
			if err := runImportJob(ctx, link, cfg, access, job); err != nil {
				// Logging it is not telling anyone: the customer is watching an
				// import that would otherwise never finish and never fail.
				log.Printf("import job %s failed: %v", id, err)
				reportJobResult(ctx, link, access, id, "error", "", err.Error())
			} else {
				log.Printf("import job %s: bundle uploaded", id)
			}
		case "publish":
			runPublishJob(ctx, link, cfg, access, job)
		case "jira_reply":
			runReply(ctx, link, cfg, m, access, job)
		default:
			log.Printf("job %s: unsupported kind %q (skipped)", id, kind)
		}
	}
}

// cachedCatalogue reads the catalogue this connector last verified — the same
// file the plugin store installs from, re-verified on every read because it sits
// on the customer's disk. Nothing is fetched here: a connector that has never
// reached a catalogue, or one running from an offline kit, simply claims nothing
// about updates (#302).
func cachedCatalogue(stateDir string) func() *catalog.Index {
	return func() *catalog.Index {
		idx, err := catalog.New("", plugin.TrustedKeys, filepath.Join(stateDir, "catalog")).Cached()
		if err != nil {
			return nil
		}
		return idx
	}
}

// runImportJob clones the provider repo (mirror), bundles all refs, and streams the
// bundle to the backend in chunks. The backend applies it to our gitea mirror.
func runImportJob(ctx context.Context, link requester, cfg *config.Config, access string, job map[string]any) error {
	kind, _ := job["provider"].(string)
	baseURL, _ := job["base_url"].(string)
	repo, _ := job["repo"].(string)
	jobID, _ := job["id"].(string)
	tok, ok := tokenForJob(cfg, kind, baseURL, repo)
	if !ok {
		return fmt.Errorf("no configured token for %s %s", kind, repo)
	}
	p, err := provider.New(provider.Kind(kind), baseURL, repo, nil)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "conn-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	mirror := filepath.Join(dir, "repo.git")
	if err := gitutil.MirrorClone(ctx, p.PushRemoteURL(tok), mirror, p.GitConfig(tok)...); err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	bundle := filepath.Join(dir, "repo.bundle")
	if err := gitutil.BundleAll(ctx, mirror, bundle); err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	return transport.ChunkFile(bundle, transport.DefaultChunkSize, func(c transport.Chunk) error {
		rctx, cancel := context.WithTimeout(ctx, reqTimeout)
		defer cancel()
		_, e := link.Request(rctx, "data.cmd.connector_upload", buildUploadPayload(access, jobID, c))
		return e
	})
}

// downloadBundle streams a prepared bundle from the backend (us → agent) chunk by
// chunk into outPath.
func downloadBundle(ctx context.Context, link requester, access, jobID, outPath string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	for seq := 0; ; seq++ {
		rctx, cancel := context.WithTimeout(ctx, reqTimeout)
		resp, err := link.Request(rctx, "data.cmd.connector_download", buildDownloadPayload(access, jobID, seq))
		cancel()
		if err != nil {
			return err
		}
		if s, _ := resp["data"].(string); s != "" {
			raw, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				return fmt.Errorf("decode chunk %d: %w", seq, err)
			}
			if _, err := f.Write(raw); err != nil {
				return err
			}
		}
		if last, _ := resp["last"].(bool); last {
			return nil
		}
	}
}

// runPublishJob downloads a prepared branch bundle, pushes the branch to the
// provider repo (with the agent's local token), and opens a PR/MR. The result
// (PR url or error) is reported back so the job's status updates.
func runPublishJob(ctx context.Context, link requester, cfg *config.Config, access string, job map[string]any) {
	jobID, _ := job["id"].(string)
	res, err := doPublish(ctx, link, cfg, access, job)
	if err != nil {
		log.Printf("publish job %s failed: %v", jobID, err)
		reportJobResult(ctx, link, access, jobID, "error", "", err.Error())
		return
	}
	log.Printf("publish job %s: PR %s", jobID, res)
	reportJobResult(ctx, link, access, jobID, "done", res, "")
}

func doPublish(ctx context.Context, link requester, cfg *config.Config, access string, job map[string]any) (string, error) {
	kind, _ := job["provider"].(string)
	baseURL, _ := job["base_url"].(string)
	repo, _ := job["repo"].(string)
	jobID, _ := job["id"].(string)
	spec, _ := job["spec"].(map[string]any)
	if spec == nil {
		return "", fmt.Errorf("publish job missing spec")
	}
	branch, _ := spec["branch"].(string)
	target, _ := spec["target"].(string)
	title, _ := spec["title"].(string)
	body, _ := spec["body"].(string)
	if branch == "" || target == "" {
		return "", fmt.Errorf("publish job missing branch/target")
	}
	tok, ok := tokenForJob(cfg, kind, baseURL, repo)
	if !ok {
		return "", fmt.Errorf("no configured token for %s %s", kind, repo)
	}
	p, err := provider.New(provider.Kind(kind), baseURL, repo, nil)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "conn-publish-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	bundle := filepath.Join(dir, "publish.bundle")
	if err := downloadBundle(ctx, link, access, jobID, bundle); err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	repoDir := filepath.Join(dir, "repo")
	if err := gitutil.Clone(ctx, p.PushRemoteURL(tok), repoDir, p.GitConfig(tok)...); err != nil {
		return "", fmt.Errorf("clone their repo: %w", err)
	}
	if err := gitutil.FetchRefspec(ctx, repoDir, bundle, "+refs/heads/"+branch+":refs/heads/"+branch); err != nil {
		return "", fmt.Errorf("fetch bundle: %w", err)
	}
	if err := gitutil.Push(ctx, repoDir, "origin", branch, p.GitConfig(tok)...); err != nil {
		return "", fmt.Errorf("push branch: %w", err)
	}
	pr, err := p.CreatePR(ctx, tok, provider.PRSpec{Head: branch, Base: target, Title: title, Body: body})
	if err != nil {
		return "", fmt.Errorf("create PR: %w", err)
	}
	return pr.URL, nil
}

// Pair performs the one-time device pairing: generate an identity, redeem the
// pairing code, persist state, and do an initial report.
func Pair(cfg *config.Config, code, label, version string) error {
	id := identity.Generate()
	link, err := natslink.ConnectGuest(cfg.MiddlewareURL, reqTimeout)
	if err != nil {
		return err
	}
	defer link.Close()

	ctx, cancel := context.WithTimeout(context.Background(), reqTimeout)
	defer cancel()
	claim := buildClaimPayload(id, label)
	claim["code"] = code
	resp, err := link.Request(ctx, "data.cmd.connector_claim", claim)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	cid, _ := resp["connector_id"].(string)
	org, _ := resp["org"].(string)
	access, _ := resp["access"].(string)
	refresh, _ := resp["refresh"].(string)
	if cid == "" || access == "" || refresh == "" {
		return fmt.Errorf("incomplete claim response")
	}
	st := identity.State{
		Seed:        base64.RawURLEncoding.EncodeToString(id.Seed()),
		ConnectorID: cid,
		Org:         org,
		Access:      access,
		Refresh:     refresh,
	}
	if err := identity.Save(cfg.StateDir, st); err != nil {
		return err
	}
	if _, err := link.Request(ctx, "data.cmd.connector_report", buildReportPayload(access, reposFromConfig(cfg), version)); err != nil {
		log.Printf("initial report failed (will retry on run): %v", err)
	}
	log.Printf("paired: connector %s in org %s", cid, org)
	return nil
}

// providersSig is a stable signature of the provider config, so a change (added
// repo, changed token, new provider) is detected for a live re-report.
func providersSig(cfg *config.Config) string {
	var sb strings.Builder
	for _, p := range cfg.Providers {
		sb.WriteString(p.Kind)
		sb.WriteByte(0)
		sb.WriteString(p.BaseURL)
		sb.WriteByte(0)
		sb.WriteString(strings.Join(p.Repos, ","))
		sb.WriteByte(0)
		sb.WriteString(p.Token)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// Run loads the persisted identity and runs the agent loop: an immediate report,
// then periodic heartbeats, token refreshes, and a config WATCH. Editing the
// config (by hand, or via the local setup UI/CLI) — adding a repo or changing a
// token — is applied LIVE and re-reported with NO restart (plug-and-play).
// `cfgPath` is the config file; it is re-read on change.
// Plugins is the running plugin manager, exposed so the local admin surface can
// show and change what is installed. It exists only while the agent runs.
var Plugins *PluginManager

func Run(ctx context.Context, cfgPath, version string) error {
	cur, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log.Printf("connector %s starting; middleware=%s", version, cur.MiddlewareURL)
	st, err := identity.Load(cur.StateDir)
	if err != nil {
		return fmt.Errorf("load state (pair first?): %w", err)
	}
	id, err := st.Identity()
	if err != nil {
		return err
	}
	// The state directory has to be laid out before anything runs under a
	// plugin's uid — see plugin.PrepareStateDir for what each mode is for.
	if err := plugin.PrepareStateDir(cur.StateDir); err != nil {
		return fmt.Errorf("prepare state dir: %w", err)
	}
	pm := NewPluginManager(ctx, cur.StateDir, plugin.NewStore(cur.StateDir, plugin.TrustedKeys, nil)).
		WithCatalogue(cachedCatalogue(cur.StateDir))
	Plugins = pm
	defer pm.StopAll()
	pm.Sync(ctx, cur)
	if priv := pm.Privilege(); !priv.CanSwitchUID && len(cur.Plugins) > 0 {
		log.Printf("plugins will NOT run: %s", priv.Reason)
	}

	link, err := natslink.ConnectGuest(cur.MiddlewareURL, reqTimeout)
	if err != nil {
		return err
	}
	defer link.Close()

	access, refresh := st.Access, st.Refresh

	// The ANSWERING connection. The guest link above can only ask; a connector
	// that offers tools to a run has to be askable, which needs its own identity.
	// The token is read through a callback because it is refreshed while the
	// process runs — capturing it here would make the first reconnect after a
	// refresh present an expired token, and the connector would quietly become
	// unreachable.
	var tokenMu sync.RWMutex
	currentToken := func() string {
		tokenMu.RLock()
		defer tokenMu.RUnlock()
		return access
	}
	mcp := NewMCPSurface(pm, toolCallsPerMinute)
	MCP = mcp
	if claims, cerr := decodeClaims(access); cerr == nil {
		if cid, _ := claims["cid"].(string); cid != "" {
			resp, rerr := natslink.Connect(cur.MiddlewareURL, cid, currentToken)
			if rerr != nil {
				log.Printf("connector responder: %v (tool calls from runs will not arrive)", rerr)
			} else {
				defer resp.Close()
				go func() {
					own := NewOwnModel(cur.OwnModel.BaseURL, cur.OwnModel.APIKey, nil)
					if err := resp.Serve(ctx, relayHandler(mcp.Handle, own.Chat)); err != nil {
						log.Printf("connector responder stopped: %v", err)
					}
				}()
				log.Printf("answering tool calls on events.connector.%s.>", cid)
			}
		}
	}

	sig := providersSig(cur)
	var lastMod time.Time
	if fi, serr := os.Stat(cfgPath); serr == nil {
		lastMod = fi.ModTime()
	}

	// A section this build cannot read is silently doing nothing. Saying so once
	// at startup is the difference between "the agent is old" and "the
	// integration is broken".
	for _, line := range ignoredSectionNotices(cur.UnknownSections()) {
		log.Print(line)
	}

	report := func() {
		rctx, cancel := context.WithTimeout(ctx, reqTimeout)
		defer cancel()
		payload := buildFullReportPayload(access, reposFromConfig(cur), version, pm, mcp)
		// Where developers' Buff Code signs in (#366); "" says «no gateway», so the
		// cabinet stops showing an address that is gone.
		payload["developer_gateway"] = devgate.Address(gatewaySettings(cur), cur.DeveloperGateway.Host)
		if _, err := link.Request(rctx, "data.cmd.connector_report", payload); err != nil {
			log.Printf("report failed: %v", err)
		}
	}
	report()

	hb := time.NewTicker(heartbeatEvery)
	defer hb.Stop()
	jobs := time.NewTicker(jobsEvery)
	defer jobs.Stop()
	watch := time.NewTicker(watchEvery)
	defer watch.Stop()
	// One clock for every connection, each read on its own interval: the tick is
	// the resolution, the schedule says who is due.
	eventTick := time.NewTicker(pollTickEvery)
	defer eventTick.Stop()
	sched := newPollSchedule()
	pollOf := func(p *config.PluginInstance) time.Duration { return pollEvery(p, pm.PollSuggested(p.ID)) }
	// A waking webhook does not parse the tracker's payload; it only says "read
	// now", so one path still produces every event. What it buys is latency.
	hooks := newWaker()
	// A callback either wakes a poll or is handed to the plugin, depending on
	// what its manifest declares; the validation before that is the same.
	onCallback := func(c webhookCall) {
		deliverCallback(ctx, link, pm, cur, access, version, c)
	}
	routes := pluginWebhookRoutes(cur, pm)
	hookServers := serveWebhooks(ctx, routes, hooks.wake, onCallback)
	// Developers' Buff Code signs in through this connector (#366) and reaches the
	// company's tools through it (#368) — only this organisation's developers.
	gateway := gatewaySettings(cur)
	gateway.Org = st.Org
	devgate.Serve(ctx, gateway, link, mcp)
	refreshTimer := time.NewTimer(refreshDelay(access))
	defer refreshTimer.Stop()

	pollJobs(ctx, link, cur, pm, access, version)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-hb.C:
			report()
		case <-jobs.C:
			pollJobs(ctx, link, cur, pm, access, version)
		case now := <-eventTick.C:
			due := sched.due(cur, pollOf, now)
			if len(due) == 0 {
				continue
			}
			// Reconciled per instance: only the connection the operator touched
			// is restarted.
			pm.Sync(ctx, cur)
			pollPluginEvents(ctx, link, pm, cur, cur.StateDir, access, version, due)
		case <-hooks.signal():
			woken := hooks.drain()
			pm.Sync(ctx, cur)
			pollPluginEvents(ctx, link, pm, cur, cur.StateDir, access, version, woken)
		case <-watch.C:
			fi, serr := os.Stat(cfgPath)
			if serr != nil || fi.ModTime().Equal(lastMod) {
				continue
			}
			lastMod = fi.ModTime()
			nc, lerr := config.Load(cfgPath)
			if lerr != nil {
				log.Printf("config reload failed (keeping current): %v", lerr)
				continue
			}
			ns := providersSig(nc)
			if ns == sig && pluginsSig(nc) == pluginsSig(cur) {
				continue
			}
			cur, sig = nc, ns
			log.Printf("config changed — applied live (%d providers, %d integrations)", len(cur.Providers), len(cur.Plugins))
			// Reconciled per instance: only the connection the operator touched
			// is restarted.
			pm.Sync(ctx, cur)
			// The operator changed something — the platform hears it now, not at
			// the next heartbeat: a connection's notes for the model, its name,
			// its tools after a restart are all read from the report up there.
			report()
			// Rebind the webhook listeners to the new set of connections.
			for _, s := range hookServers {
				_ = s.Close()
			}
			hookServers = serveWebhooks(ctx, pluginWebhookRoutes(cur, pm), hooks.wake, onCallback)
		case <-refreshTimer.C:
			payload, err := buildRefreshPayload(id, refresh, version)
			if err != nil {
				log.Printf("build refresh: %v", err)
				refreshTimer.Reset(minRefreshSec * time.Second)
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, reqTimeout)
			resp, err := link.Request(rctx, "data.cmd.connector_refresh", payload)
			cancel()
			if err != nil {
				// A revoked connector fails permanently — surface it so the
				// operator sees the agent stop rather than spin silently.
				if strings.Contains(err.Error(), "revoked") {
					return fmt.Errorf("connector revoked — re-pair required")
				}
				log.Printf("refresh failed (will retry): %v", err)
				refreshTimer.Reset(30 * time.Second)
				continue
			}
			if a, ok := resp["access"].(string); ok && a != "" {
				// Under the lock: the answering connection reads this on every
				// reconnect, and a torn read there would present a broken token.
				tokenMu.Lock()
				access = a
				tokenMu.Unlock()
			}
			if r, ok := resp["refresh"].(string); ok && r != "" {
				refresh = r
			}
			st.Access, st.Refresh = access, refresh
			if err := identity.Save(cur.StateDir, st); err != nil {
				log.Printf("persist refreshed tokens: %v", err)
			}
			refreshTimer.Reset(refreshDelay(access))
		}
	}
}

// pluginsSig folds every instance's fingerprint, so a change to any connection
// wakes the reload — the manager then works out which one it was.
// pluginsSig decides whether a reload has anything to APPLY. That is a different
// question from whether a plugin must RESTART, which is what
// PluginInstance.Fingerprint answers — so the webhook settings are added here
// and deliberately not there: moving a listener rebinds a port, and the plugin
// process could not care less.
//
// Leaving them out is the bug this comment exists to prevent, and it is not
// hypothetical: turning a webhook on looked applied — the file WAS reloaded, the
// page showed the URL — while nothing was listening on the port.
func pluginsSig(cfg *config.Config) string {
	var b strings.Builder
	// The listener is one address for the whole connector, so it belongs to the
	// signature once rather than per connection.
	fmt.Fprintf(&b, "listen=%s|%s\n", cfg.Webhooks.Listen, cfg.Webhooks.Host)
	for i := range cfg.Plugins {
		p := &cfg.Plugins[i]
		b.WriteString(p.Fingerprint())
		// The interval and the listener are read live by the loop, not by the
		// plugin: a change to either is applied, and restarts nothing.
		// The notes are read by the model, not the plugin: same rule. So is who
		// may ask (#476), read by the connector as it forwards each event.
		fmt.Fprintf(&b, "|hook=%t|poll=%d|notes=%t:%s|askers=%t:%q\n", p.WebhookEnabled(), p.PollSeconds,
			p.AINotesUnbounded, p.AINotes, p.AskersMembers, p.Askers)
	}
	return b.String()
}

// MCP is the running answering surface, exposed so the local admin page can show
// the recent tool calls.
var MCP *MCPSurface

// toolCallsPerMinute bounds how often one connection is called from runs. It
// slows a burst; it never discards a result.
const toolCallsPerMinute = 120

// gatewaySettings is the developer gateway's part of the connector's config (#366).
func gatewaySettings(cfg *config.Config) devgate.Settings {
	g := cfg.DeveloperGateway
	return devgate.Settings{Listen: g.Listen, TLSCert: g.TLSCert, TLSKey: g.TLSKey}
}
