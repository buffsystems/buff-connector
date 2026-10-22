package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"time"

	"github.com/buffsystems/buff-connector/internal/config"
)

// replyJob is what to write back into a ticket. Every part is optional, so one
// job can be an acknowledgement, a finished answer, a link, a document, or any
// combination of them.
//
// The job's name on the wire is still "jira_reply". It is a contract with the
// platform, and a connector already in the field answers to that name — so it
// outlives the built-in integration it was named after. Renaming it is a
// two-sided change, tracked in #285.
type replyJob struct {
	Integration string
	IssueKey    string
	Comment     string
	LinkURL     string
	LinkTitle   string
	LinkIcon    string
	FileName    string
	// HasFile marks a document that must be streamed down before it can be
	// attached, rather than travelling inside the job.
	HasFile bool
	// Document is the finished text itself, as markdown, with its title — for a
	// source that can hold a PAGE rather than only a comment and a file (Notion
	// publishes a research report as a child page). Optional; a plugin that has
	// nowhere to put a page ignores it.
	Document      string
	DocumentTitle string
	// Visibility is the request's own — "internal" answers an internal note
	// with an internal note (cloud-front#476). Empty: an ordinary comment.
	Visibility string
}

func parseReplyJob(job map[string]any) replyJob {
	spec, _ := job["spec"].(map[string]any)
	str := func(k string) string {
		v, _ := spec[k].(string)
		return v
	}
	r := replyJob{
		Integration:   str("integration"),
		IssueKey:      str("issue_key"),
		Comment:       str("comment"),
		LinkURL:       str("link_url"),
		LinkTitle:     str("link_title"),
		LinkIcon:      str("link_icon"),
		FileName:      str("file_name"),
		Document:      str("document"),
		DocumentTitle: str("document_title"),
		Visibility:    str("visibility"),
	}
	r.HasFile, _ = spec["has_file"].(bool)
	return r
}

// runReply writes a result back into a ticket, through the plugin connection
// that raised the request.
//
// The parts are attempted in the order a reader wants them: the document first,
// then the link that points at it, then the prose that explains it — so the
// comment never arrives promising an attachment that failed to upload.
func runReply(ctx context.Context, link requester, cfg *config.Config, m *PluginManager, access string, job map[string]any) {
	jobID, _ := job["id"].(string)
	report := func(status, errStr string) {
		reportJobResult(ctx, link, access, jobID, status, "", errStr)
	}
	r := parseReplyJob(job)
	if err := r.validate(); err != nil {
		log.Printf("reply job %s refused: %v", jobID, err)
		report("error", err.Error())
		return
	}
	// A job is addressed by the connection that raised it. There is only one kind
	// of connection now — an integration from the catalogue — so a job naming
	// anything else is refused rather than guessed at.
	p, ok := instanceByID(cfg, r.Integration)
	if !ok {
		err := fmt.Errorf("this connector does not serve the integration %q", r.Integration)
		log.Printf("reply job %s: %v", jobID, err)
		report("error", err.Error())
		return
	}
	if err := doPluginReply(ctx, link, m, p, access, jobID, r); err != nil {
		log.Printf("reply job %s failed: %v", jobID, err)
		report("error", err.Error())
		return
	}
	report("done", "")
}

// validate refuses a job that would put nothing, or nothing addressable, into a
// customer's ticket.
func (r replyJob) validate() error {
	if r.IssueKey == "" {
		return fmt.Errorf("no issue to answer in")
	}
	if r.Comment == "" && r.LinkURL == "" && !r.HasFile && r.Document == "" {
		return fmt.Errorf("nothing to say: no comment, no link, no document")
	}
	return nil
}

// instanceByID finds the plugin connection a job names. Jobs are addressed by
// GUID, so a renamed connection still receives its own answers.
func instanceByID(cfg *config.Config, id string) (config.PluginInstance, bool) {
	for _, p := range cfg.Plugins {
		if p.ID == id {
			return p, true
		}
	}
	return config.PluginInstance{}, false
}

// pluginReplyParams turns a reply job into the plugin's own vocabulary. The
// document travels base64 because JSON-RPC carries text; the plugin decodes it.
func pluginReplyParams(r replyJob, file []byte) map[string]any {
	params := map[string]any{"issue": r.IssueKey}
	if r.Comment != "" {
		params["text"] = r.Comment
	}
	if r.LinkURL != "" {
		params["link"] = map[string]any{"url": r.LinkURL, "title": r.LinkTitle, "icon": r.LinkIcon}
	}
	if len(file) > 0 {
		params["attachment"] = map[string]any{
			"filename": r.FileName,
			"base64":   base64.StdEncoding.EncodeToString(file),
		}
	}
	if r.Document != "" {
		params["document"] = map[string]any{"title": r.DocumentTitle, "markdown": r.Document}
	}
	if r.Visibility != "" {
		params["visibility"] = r.Visibility
	}
	return params
}

// replyTimeout is longer than an ordinary request: a reply may carry a document
// the plugin has to upload to the tracker, and that upload is the slow part.
const replyTimeout = 3 * time.Minute

// doPluginReply hands the answer to the plugin connection that raised the
// request. The connector never learns the tracker's API: that is the plugin's
// job, and keeping it there is what makes a new tracker a bundle.
func doPluginReply(ctx context.Context, link requester, m *PluginManager,
	p config.PluginInstance, access, jobID string, r replyJob) error {
	if m == nil {
		return fmt.Errorf("plugins are not running on this connector")
	}
	inst := m.Instance(p.Slug)
	if inst == nil {
		return fmt.Errorf("connection %s is not running", p.Slug)
	}
	if !inst.Info().Replies {
		return fmt.Errorf("plugin %s cannot write answers back", p.Plugin)
	}
	var file []byte
	if r.HasFile {
		var err error
		if file, err = downloadBlob(ctx, link, access, jobID); err != nil {
			return fmt.Errorf("fetch %s: %w", r.FileName, err)
		}
	}
	rctx, cancel := context.WithTimeout(ctx, replyTimeout)
	defer cancel()
	var out map[string]any
	return inst.Call(rctx, "buff/reply", pluginReplyParams(r, file), &out)
}

// downloadBlob streams a job's payload down in chunks, reusing the same
// transfer the publish path uses for branch bundles.
func downloadBlob(ctx context.Context, link requester, access, jobID string) ([]byte, error) {
	var out []byte
	for seq := 0; ; seq++ {
		rctx, cancel := context.WithTimeout(ctx, reqTimeout)
		resp, err := link.Request(rctx, "data.cmd.connector_download", buildDownloadPayload(access, jobID, seq))
		cancel()
		if err != nil {
			return nil, err
		}
		if s, _ := resp["data"].(string); s != "" {
			raw, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				return nil, fmt.Errorf("decode chunk %d: %w", seq, err)
			}
			out = append(out, raw...)
		}
		if last, _ := resp["last"].(bool); last {
			return out, nil
		}
	}
}
