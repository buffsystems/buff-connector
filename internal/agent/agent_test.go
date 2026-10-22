package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/identity"
	"github.com/buffsystems/buff-connector/internal/transport"
)

func mkToken(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","typ":"JWT"}`))
	pb, _ := json.Marshal(claims)
	p := base64.RawURLEncoding.EncodeToString(pb)
	return h + "." + p + ".sig"
}

func TestDecodeClaims(t *testing.T) {
	tok := mkToken(map[string]any{"cid": "c1", "jti": "j1", "exp": float64(123)})
	c, err := decodeClaims(tok)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c["cid"] != "c1" || c["jti"] != "j1" {
		t.Fatalf("claims = %v", c)
	}
}

func TestDecodeClaimsRejectsMalformed(t *testing.T) {
	if _, err := decodeClaims("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestReposFromConfig(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Kind: "gitlab", BaseURL: "https://gl", Repos: []string{"g/a", "g/b"}},
			{Kind: "bitbucket-server", BaseURL: "https://bb", Repos: []string{"P/s"}},
		},
	}
	repos := reposFromConfig(cfg)
	if len(repos) != 3 {
		t.Fatalf("repos = %d, want 3", len(repos))
	}
	if repos[0].Provider != "gitlab" || repos[0].BaseURL != "https://gl" || repos[0].Repo != "g/a" {
		t.Fatalf("repo0 = %+v", repos[0])
	}
	if !repos[0].HasCreds {
		t.Fatal("a configured provider should report has_creds=true")
	}
	// json tags must match the backend's expected shape
	b, _ := json.Marshal(repos[2])
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, key := range []string{"provider", "base_url", "repo", "has_creds"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("missing json key %q in %s", key, b)
		}
	}
}

func TestBuildClaimPayload(t *testing.T) {
	id := identity.Generate()
	p := buildClaimPayload(id, "dc-1")
	if p["op"] != "connector_claim" || p["pubkey"] != id.PubB64() || p["label"] != "dc-1" {
		t.Fatalf("payload = %v", p)
	}
}

func TestBuildReportPayload(t *testing.T) {
	repos := []Repo{{Provider: "gitea", BaseURL: "https://g", Repo: "a/b", HasCreds: true}}
	p := buildReportPayload("ACCESS", repos, "1.0.0")
	if p["op"] != "connector_report" || p["token"] != "ACCESS" || p["version"] != "1.0.0" {
		t.Fatalf("payload = %v", p)
	}
}

func TestBuildRefreshPayloadSignsPoP(t *testing.T) {
	id := identity.Generate()
	refresh := mkToken(map[string]any{"cid": "c1", "jti": "j9", "ptype": "connector"})
	p, err := buildRefreshPayload(id, refresh, "1.0.0")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if p["op"] != "connector_refresh" || p["refresh"] != refresh {
		t.Fatalf("payload = %v", p)
	}
	// The sig must verify against the agent's pubkey over popMessage(cid, jti).
	sig, _ := base64.RawURLEncoding.DecodeString(p["sig"].(string))
	der, _ := base64.RawURLEncoding.DecodeString(id.PubB64())
	pk, _ := x509.ParsePKIXPublicKey(der)
	msg := identity.PopMessage("c1", "j9")
	if !ed25519.Verify(pk.(ed25519.PublicKey), []byte(msg), sig) {
		t.Fatal("refresh PoP signature did not verify")
	}
}

func TestTokenForJob(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Kind: "gitlab", BaseURL: "https://gl", Repos: []string{"g/a"}, Token: "gltok"},
		{Kind: "gitea", BaseURL: "https://gt", Repos: []string{"o/r"}, Token: "gttok"},
	}}
	if tok, ok := tokenForJob(cfg, "gitlab", "https://gl", "g/a"); !ok || tok != "gltok" {
		t.Fatalf("gitlab token = %q, ok=%v", tok, ok)
	}
	if _, ok := tokenForJob(cfg, "gitlab", "https://gl", "not/served"); ok {
		t.Fatal("should not match an unserved repo")
	}
	if _, ok := tokenForJob(cfg, "github", "https://gh", "x/y"); ok {
		t.Fatal("should not match an unknown provider")
	}
}

func TestBuildJobsAndUploadPayloads(t *testing.T) {
	jp := buildJobsPayload("ACCESS", "1.0.0")
	if jp["op"] != "connector_jobs" || jp["token"] != "ACCESS" || jp["version"] != "1.0.0" {
		t.Fatalf("jobs payload = %v", jp)
	}
	up := buildUploadPayload("ACCESS", "job-1", transport.Chunk{Seq: 2, Data: "DATA", Last: true})
	if up["op"] != "connector_upload" || up["job_id"] != "job-1" || up["seq"] != 2 || up["data"] != "DATA" || up["last"] != true {
		t.Fatalf("upload payload = %v", up)
	}
}

func TestBuildDownloadAndResultPayloads(t *testing.T) {
	dp := buildDownloadPayload("ACCESS", "job-1", 3)
	if dp["op"] != "connector_download" || dp["token"] != "ACCESS" || dp["job_id"] != "job-1" || dp["seq"] != 3 {
		t.Fatalf("download payload = %v", dp)
	}
	rp := buildJobResultPayload("ACCESS", "job-1", "done", "https://pr/1", "")
	if rp["op"] != "connector_job_result" || rp["status"] != "done" || rp["pr_url"] != "https://pr/1" {
		t.Fatalf("result payload = %v", rp)
	}
}

func TestNextRefreshDelay(t *testing.T) {
	// refresh a margin before expiry
	if d := nextRefreshDelaySec(1000, 900, 60); d != 40 {
		t.Fatalf("delay = %d, want 40", d)
	}
	// never negative / never below the floor
	if d := nextRefreshDelaySec(1000, 995, 60); d != 5 {
		t.Fatalf("floor delay = %d, want 5 (min)", d)
	}
}

// recordingLink stands in for the link: it answers the job poll with whatever
// jobs a test wants and keeps the last payload sent on every subject.
type recordingLink struct {
	jobs []any
	sent map[string]map[string]any
}

func (l *recordingLink) Request(_ context.Context, subject string, payload map[string]any) (map[string]any, error) {
	if l.sent == nil {
		l.sent = map[string]map[string]any{}
	}
	l.sent[subject] = payload
	if subject == "data.cmd.connector_jobs" {
		return map[string]any{"jobs": l.jobs}, nil
	}
	return map[string]any{}, nil
}

// A job that fails has to SAY so. The platform holds a claimed job until the
// connector answers, so an import that dies on the connector's side leaves the
// customer watching an import that never finishes and never fails either. Found
// by the live gitlab.com run: the path was refused, the failure was logged, and
// nobody was told (#296).
func TestAFailedImportJobIsReportedBack(t *testing.T) {
	link := &recordingLink{jobs: []any{map[string]any{
		"id": "job-1", "kind": "import", "provider": "gitlab",
		"base_url": "https://gitlab.com", "repo": "grp/sub/app",
	}}}
	pollJobs(context.Background(), link, &config.Config{}, nil, "ACCESS", "1.2.3")

	res, ok := link.sent["data.cmd.connector_job_result"]
	if !ok {
		t.Fatal("a failed import told the platform nothing")
	}
	if res["job_id"] != "job-1" || res["status"] != "error" {
		t.Fatalf("result = %v", res)
	}
	if s, _ := res["error"].(string); s == "" {
		t.Fatal("the report does not say what went wrong")
	}
}
