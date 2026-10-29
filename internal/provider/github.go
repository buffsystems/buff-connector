package provider

import (
	"context"
	"fmt"
	"net/http"
)

// gitHub adapts GitHub.com and GitHub Enterprise Server, which share a REST API
// (v3). The only differences are the API base URL and the git host.
type gitHub struct {
	client    *http.Client
	apiBase   string // e.g. https://api.github.com or https://ghe.example.com/api/v3
	gitHost   string // e.g. github.com or ghe.example.com
	gitScheme string // http or https (GHE may be plain http on an internal network)
	owner     string
	repo      string
}

func newGitHub(kind Kind, base, owner, repo string, client *http.Client) (Provider, error) {
	g := &gitHub{client: client, owner: owner, repo: repo, gitScheme: "https"}
	switch kind {
	case KindGHE:
		if base == "" {
			return nil, fmt.Errorf("провайдер — ghe нужен base URL")
		}
		g.apiBase = base + "/api/v3"
		g.gitHost = hostOf(base)
		g.gitScheme = schemeOf(base)
	default: // KindGitHub
		if base == "" {
			g.apiBase = "https://api.github.com"
		} else {
			g.apiBase = base // self-hosted override / test server
		}
		g.gitHost = "github.com"
	}
	return g, nil
}

func (g *gitHub) PushRemoteURL(token string) string {
	return fmt.Sprintf("%s://x-access-token:%s@%s/%s/%s.git", g.gitScheme, token, g.gitHost, g.owner, g.repo)
}

func (g *gitHub) GitConfig(string) []string { return nil }

func (g *gitHub) headers(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/vnd.github+json",
	}
}

func (g *gitHub) CheckAccess(ctx context.Context, token string) error {
	url := fmt.Sprintf("%s/repos/%s/%s", g.apiBase, g.owner, g.repo)
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

func (g *gitHub) CreatePR(ctx context.Context, token string, spec PRSpec) (PRResult, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls", g.apiBase, g.owner, g.repo)
	payload := map[string]string{
		"title": spec.Title,
		"head":  spec.Head,
		"base":  spec.Base,
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
	return PRResult{}, fmt.Errorf("провайдер — PR не создан (%d): %s", resp.status, providerMessage(resp.body))
}

func (g *gitHub) ListRepos(ctx context.Context, token string) ([]RepoRef, error) {
	url := g.apiBase + "/user/repos?per_page=100&affiliation=owner,collaborator,organization_member"
	resp, err := doJSON(ctx, g.client, http.MethodGet, url, nil, g.headers(token))
	if err != nil {
		return nil, err
	}
	if err := accessErrForStatus(resp); err != nil {
		return nil, err
	}
	var body []struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
	}
	if err := decode(resp.body, &body); err != nil {
		return nil, err
	}
	repos := make([]RepoRef, 0, len(body))
	for _, r := range body {
		repos = append(repos, RepoRef{Repo: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private})
	}
	return repos, nil
}
