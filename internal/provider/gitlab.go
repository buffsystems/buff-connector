package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// gitLab adapts GitLab (gitlab.com or self-hosted). Projects are addressed by
// their URL-encoded "namespace/project" path.
type gitLab struct {
	client  *http.Client
	base    string // scheme://host root, e.g. https://gitlab.com
	encPath string // URL-encoded "owner/repo"
	owner   string
	repo    string
}

func newGitLab(base, owner, repo string, client *http.Client) (Provider, error) {
	if base == "" {
		base = "https://gitlab.com"
	}
	return &gitLab{
		client:  client,
		base:    base,
		encPath: url.PathEscape(owner + "/" + repo),
		owner:   owner,
		repo:    repo,
	}, nil
}

func (g *gitLab) PushRemoteURL(token string) string {
	return fmt.Sprintf(schemeOf(g.base)+"://oauth2:%s@%s/%s/%s.git", token, hostOf(g.base), g.owner, g.repo)
}

func (g *gitLab) GitConfig(string) []string { return nil }

func (g *gitLab) headers(token string) map[string]string {
	return map[string]string{"PRIVATE-TOKEN": token}
}

func (g *gitLab) CheckAccess(ctx context.Context, token string) error {
	url := fmt.Sprintf("%s/api/v4/projects/%s", g.base, g.encPath)
	resp, err := doJSON(ctx, g.client, http.MethodGet, url, nil, g.headers(token))
	if err != nil {
		return err
	}
	if err := accessErrForStatus(resp); err != nil {
		return err
	}
	var body struct {
		Permissions struct {
			ProjectAccess *struct {
				AccessLevel int `json:"access_level"`
			} `json:"project_access"`
			GroupAccess *struct {
				AccessLevel int `json:"access_level"`
			} `json:"group_access"`
		} `json:"permissions"`
	}
	if err := decode(resp.body, &body); err != nil {
		return err
	}
	level := 0
	if pa := body.Permissions.ProjectAccess; pa != nil && pa.AccessLevel > level {
		level = pa.AccessLevel
	}
	if ga := body.Permissions.GroupAccess; ga != nil && ga.AccessLevel > level {
		level = ga.AccessLevel
	}
	// GitLab access level 30 == Developer, the minimum for push + MR.
	if level < 30 {
		return ErrNoAccess
	}
	return nil
}

func (g *gitLab) CreatePR(ctx context.Context, token string, spec PRSpec) (PRResult, error) {
	url := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests", g.base, g.encPath)
	payload := map[string]string{
		"source_branch": spec.Head,
		"target_branch": spec.Base,
		"title":         spec.Title,
		"description":   spec.Body,
	}
	resp, err := doJSON(ctx, g.client, http.MethodPost, url, payload, g.headers(token))
	if err != nil {
		return PRResult{}, err
	}
	if resp.status >= 200 && resp.status < 300 {
		var body struct {
			WebURL string `json:"web_url"`
			IID    int    `json:"iid"`
		}
		if err := decode(resp.body, &body); err != nil {
			return PRResult{}, err
		}
		return PRResult{URL: body.WebURL, Number: body.IID}, nil
	}
	if isAlreadyExists(resp) {
		return PRResult{AlreadyExists: true}, nil
	}
	return PRResult{}, fmt.Errorf("провайдер — MR не создан (%d): %s", resp.status, providerMessage(resp.body))
}

func (g *gitLab) ListRepos(ctx context.Context, token string) ([]RepoRef, error) {
	url := g.base + "/api/v4/projects?membership=true&simple=true&per_page=100"
	resp, err := doJSON(ctx, g.client, http.MethodGet, url, nil, g.headers(token))
	if err != nil {
		return nil, err
	}
	if err := accessErrForStatus(resp); err != nil {
		return nil, err
	}
	var body []struct {
		PathWithNamespace string `json:"path_with_namespace"`
		DefaultBranch     string `json:"default_branch"`
		Visibility        string `json:"visibility"`
	}
	if err := decode(resp.body, &body); err != nil {
		return nil, err
	}
	repos := make([]RepoRef, 0, len(body))
	for _, r := range body {
		repos = append(repos, RepoRef{Repo: r.PathWithNamespace, DefaultBranch: r.DefaultBranch, Private: r.Visibility != "public"})
	}
	return repos, nil
}
