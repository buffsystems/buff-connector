package mcpsource

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// GitHubAPI is where repositories are downloaded from. The API's zipball is
// used for public and private alike: one path, and a token that travels in a
// header rather than in the URL.
const GitHubAPI = "https://api.github.com"

// Repo names a GitHub repository and, optionally, the branch, tag or commit.
type Repo struct {
	Owner, Name, Ref string
}

var githubRe = regexp.MustCompile(`^(?:https?://)?(?:www\.)?github\.com/([^/\s]+)/([^/\s]+?)(?:\.git)?(?:/(?:tree|commit|releases/tag)/([^/\s]+))?(?:/.*)?$`)
var githubSSHRe = regexp.MustCompile(`^git@github\.com:([^/\s]+)/([^/\s]+?)(?:\.git)?$`)

// ParseGitHub reads the repository out of any spelling an operator pastes.
func ParseGitHub(raw string) (Repo, error) {
	raw = strings.TrimSpace(raw)
	if m := githubSSHRe.FindStringSubmatch(raw); m != nil {
		return Repo{Owner: m[1], Name: m[2]}, nil
	}
	m := githubRe.FindStringSubmatch(strings.TrimSuffix(raw, "/"))
	if m == nil {
		return Repo{}, fmt.Errorf("%q — не адрес репозитория GitHub (https://github.com/<владелец>/<репозиторий>)", raw)
	}
	return Repo{Owner: m[1], Name: m[2], Ref: m[3]}, nil
}

// ArchiveURL is the API download for the repository at its ref, or its default
// branch when none was named.
func (r Repo) ArchiveURL(api string) string {
	u := fmt.Sprintf("%s/repos/%s/%s/zipball", strings.TrimSuffix(api, "/"), r.Owner, r.Name)
	if r.Ref != "" {
		u += "/" + r.Ref
	}
	return u
}

// FetchGitHub downloads the repository as a zip. The name of the archive is
// "<repo>-<ref>.zip", which Unpack strips the top folder of.
func FetchGitHub(ctx context.Context, client *http.Client, api string, r Repo, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.ArchiveURL(api), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "buff-connector")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("скачивание %s/%s: %w", r.Owner, r.Name, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("скачивание %s/%s — GitHub ответил %d; проверьте адрес, ветку и токен для закрытого репозитория", r.Owner, r.Name, res.StatusCode)
	}
	return io.ReadAll(res.Body)
}
