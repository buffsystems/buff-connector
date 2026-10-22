package provider

import (
	"context"
	"fmt"
	"net/http"
)

// gitea adapts Gitea and Forgejo, which are always self-hosted and share a REST
// API (v1) closely modelled on GitHub's.
type gitea struct {
	client *http.Client
	base   string
	owner  string
	repo   string
}

func newGitea(base, owner, repo string, client *http.Client) (Provider, error) {
	if base == "" {
		return nil, fmt.Errorf("provider: gitea requires a base URL")
	}
	return &gitea{client: client, base: base, owner: owner, repo: repo}, nil
}

func (g *gitea) PushRemoteURL(token string) string {
	return fmt.Sprintf(schemeOf(g.base)+"://oauth2:%s@%s/%s/%s.git", token, hostOf(g.base), g.owner, g.repo)
}

func (g *gitea) GitConfig(string) []string { return nil }

func (g *gitea) headers(token string) map[string]string {
	return map[string]string{"Authorization": "token " + token}
}

func (g *gitea) CheckAccess(ctx context.Context, token string) error {
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s", g.base, g.owner, g.repo)
	resp, err := doJSON(ctx, g.client, http.MethodGet, url, nil, g.headers(token))
	if err != nil {
		return err
	}
	if err := accessErrForStatus(resp); err != nil {
		return err
	}
	var body struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := decode(resp.body, &body); err != nil {
		return err
	}
	if !body.Permissions.Push {
		return ErrNoAccess
	}
	return nil
}

func (g *gitea) CreatePR(ctx context.Context, token string, spec PRSpec) (PRResult, error) {
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls", g.base, g.owner, g.repo)
	payload := map[string]string{
		"head":  spec.Head,
		"base":  spec.Base,
		"title": spec.Title,
		"body":  spec.Body,
	}
	resp, err := doJSON(ctx, g.client, http.MethodPost, url, payload, g.headers(token))
	if err != nil {
		return PRResult{}, err
	}
	if resp.status >= 200 && resp.status < 300 {
		var body struct {
			HTMLURL string `json:"html_url"`
			Number  int    `json:"number"`
		}
		if err := decode(resp.body, &body); err != nil {
			return PRResult{}, err
		}
		return PRResult{URL: body.HTMLURL, Number: body.Number}, nil
	}
	if isAlreadyExists(resp) {
		return PRResult{AlreadyExists: true}, nil
	}
	return PRResult{}, fmt.Errorf("provider: create PR failed (%d): %s", resp.status, providerMessage(resp.body))
}

func (g *gitea) ListRepos(ctx context.Context, token string) ([]RepoRef, error) {
	url := g.base + "/api/v1/repos/search?limit=100"
	resp, err := doJSON(ctx, g.client, http.MethodGet, url, nil, g.headers(token))
	if err != nil {
		return nil, err
	}
	if err := accessErrForStatus(resp); err != nil {
		return nil, err
	}
	var body struct {
		Data []struct {
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
			Private       bool   `json:"private"`
		} `json:"data"`
	}
	if err := decode(resp.body, &body); err != nil {
		return nil, err
	}
	repos := make([]RepoRef, 0, len(body.Data))
	for _, r := range body.Data {
		repos = append(repos, RepoRef{Repo: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private})
	}
	return repos, nil
}
