package provider

import (
	"context"
	"fmt"
	"net/http"
)

// bitbucketServer adapts Bitbucket Server / Data Center, REST API 1.0. Repos are
// addressed as PROJECTKEY/slug and the git remote lives under /scm.
type bitbucketServer struct {
	client  *http.Client
	base    string
	project string
	slug    string
}

func newBitbucketServer(base, project, slug string, client *http.Client) (Provider, error) {
	if base == "" {
		return nil, fmt.Errorf("провайдер — bitbucket-server нужен base URL")
	}
	return &bitbucketServer{client: client, base: base, project: project, slug: slug}, nil
}

func (b *bitbucketServer) PushRemoteURL(token string) string {
	// No userinfo: Data Center refuses an HTTP access token in the URL whatever
	// username it is paired with — only the account's OWN name works, and the
	// connector does not know it. The token goes in a bearer header instead
	// (GitConfig), which is what Atlassian documents for these tokens.
	return fmt.Sprintf(schemeOf(b.base)+"://%s/scm/%s/%s.git", hostOf(b.base), b.project, b.slug)
}

func (b *bitbucketServer) GitConfig(token string) []string {
	return []string{"http.extraHeader=Authorization: Bearer " + token}
}

func (b *bitbucketServer) headers(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func (b *bitbucketServer) CheckAccess(ctx context.Context, token string) error {
	url := fmt.Sprintf("%s/rest/api/1.0/projects/%s/repos/%s", b.base, b.project, b.slug)
	resp, err := doJSON(ctx, b.client, http.MethodGet, url, nil, b.headers(token))
	if err != nil {
		return err
	}
	// A 200 means the token can at least read the repo; 401/403 and 404 map to
	// the sentinels via accessErrForStatus.
	return accessErrForStatus(resp)
}

func (b *bitbucketServer) CreatePR(ctx context.Context, token string, spec PRSpec) (PRResult, error) {
	url := fmt.Sprintf("%s/rest/api/1.0/projects/%s/repos/%s/pull-requests", b.base, b.project, b.slug)
	payload := map[string]any{
		"title":       spec.Title,
		"description": spec.Body,
		"fromRef":     map[string]string{"id": "refs/heads/" + spec.Head},
		"toRef":       map[string]string{"id": "refs/heads/" + spec.Base},
	}
	resp, err := doJSON(ctx, b.client, http.MethodPost, url, payload, b.headers(token))
	if err != nil {
		return PRResult{}, err
	}
	if resp.status >= 200 && resp.status < 300 {
		var body struct {
			ID    int `json:"id"`
			Links struct {
				Self []struct {
					Href string `json:"href"`
				} `json:"self"`
			} `json:"links"`
		}
		if err := decode(resp.body, &body); err != nil {
			return PRResult{}, err
		}
		prURL := ""
		if len(body.Links.Self) > 0 {
			prURL = body.Links.Self[0].Href
		}
		return PRResult{URL: prURL, Number: body.ID}, nil
	}
	if isAlreadyExists(resp) {
		return PRResult{AlreadyExists: true}, nil
	}
	return PRResult{}, fmt.Errorf("провайдер — PR не создан (%d): %s", resp.status, providerMessage(resp.body))
}

func (b *bitbucketServer) ListRepos(ctx context.Context, token string) ([]RepoRef, error) {
	url := b.base + "/rest/api/1.0/repos?limit=100"
	resp, err := doJSON(ctx, b.client, http.MethodGet, url, nil, b.headers(token))
	if err != nil {
		return nil, err
	}
	if err := accessErrForStatus(resp); err != nil {
		return nil, err
	}
	var body struct {
		Values []struct {
			Project struct {
				Key string `json:"key"`
			} `json:"project"`
			Slug   string `json:"slug"`
			Public bool   `json:"public"`
		} `json:"values"`
	}
	if err := decode(resp.body, &body); err != nil {
		return nil, err
	}
	repos := make([]RepoRef, 0, len(body.Values))
	for _, r := range body.Values {
		repos = append(repos, RepoRef{Repo: r.Project.Key + "/" + r.Slug, Private: !r.Public})
	}
	return repos, nil
}
