package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// bitbucket adapts Bitbucket Cloud (bitbucket.org), REST API 2.0.
type bitbucket struct {
	client  *http.Client
	apiBase string // e.g. https://api.bitbucket.org/2.0
	owner   string
	repo    string
}

func newBitbucket(base, owner, repo string, client *http.Client) (Provider, error) {
	if base == "" {
		base = "https://api.bitbucket.org"
	}
	return &bitbucket{client: client, apiBase: base + "/2.0", owner: owner, repo: repo}, nil
}

func (b *bitbucket) PushRemoteURL(token string) string {
	return fmt.Sprintf("https://x-token-auth:%s@bitbucket.org/%s/%s.git", token, b.owner, b.repo)
}

func (b *bitbucket) GitConfig(string) []string { return nil }

func (b *bitbucket) headers(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func (b *bitbucket) CheckAccess(ctx context.Context, token string) error {
	q := url.Values{}
	q.Set("q", fmt.Sprintf(`repository.full_name="%s/%s"`, b.owner, b.repo))
	reqURL := b.apiBase + "/user/permissions/repositories?" + q.Encode()
	resp, err := doJSON(ctx, b.client, http.MethodGet, reqURL, nil, b.headers(token))
	if err != nil {
		return err
	}
	if err := accessErrForStatus(resp); err != nil {
		return err
	}
	var body struct {
		Values []struct {
			Permission string `json:"permission"`
		} `json:"values"`
	}
	if err := decode(resp.body, &body); err != nil {
		return err
	}
	for _, v := range body.Values {
		if v.Permission == "write" || v.Permission == "admin" {
			return nil
		}
	}
	return ErrNoAccess
}

func (b *bitbucket) CreatePR(ctx context.Context, token string, spec PRSpec) (PRResult, error) {
	reqURL := fmt.Sprintf("%s/repositories/%s/%s/pullrequests", b.apiBase, b.owner, b.repo)
	payload := map[string]any{
		"title":       spec.Title,
		"description": spec.Body,
		"source":      map[string]any{"branch": map[string]string{"name": spec.Head}},
		"destination": map[string]any{"branch": map[string]string{"name": spec.Base}},
	}
	resp, err := doJSON(ctx, b.client, http.MethodPost, reqURL, payload, b.headers(token))
	if err != nil {
		return PRResult{}, err
	}
	if resp.status >= 200 && resp.status < 300 {
		var body struct {
			ID    int `json:"id"`
			Links struct {
				HTML struct {
					Href string `json:"href"`
				} `json:"html"`
			} `json:"links"`
		}
		if err := decode(resp.body, &body); err != nil {
			return PRResult{}, err
		}
		return PRResult{URL: body.Links.HTML.Href, Number: body.ID}, nil
	}
	if isAlreadyExists(resp) {
		return PRResult{AlreadyExists: true}, nil
	}
	return PRResult{}, fmt.Errorf("провайдер — PR не создан (%d): %s", resp.status, providerMessage(resp.body))
}

func (b *bitbucket) ListRepos(ctx context.Context, token string) ([]RepoRef, error) {
	reqURL := b.apiBase + "/repositories?role=member&pagelen=100"
	resp, err := doJSON(ctx, b.client, http.MethodGet, reqURL, nil, b.headers(token))
	if err != nil {
		return nil, err
	}
	if err := accessErrForStatus(resp); err != nil {
		return nil, err
	}
	var body struct {
		Values []struct {
			FullName   string `json:"full_name"`
			MainBranch struct {
				Name string `json:"name"`
			} `json:"mainbranch"`
			IsPrivate bool `json:"is_private"`
		} `json:"values"`
	}
	if err := decode(resp.body, &body); err != nil {
		return nil, err
	}
	repos := make([]RepoRef, 0, len(body.Values))
	for _, r := range body.Values {
		repos = append(repos, RepoRef{Repo: r.FullName, DefaultBranch: r.MainBranch.Name, Private: r.IsPrivate})
	}
	return repos, nil
}
