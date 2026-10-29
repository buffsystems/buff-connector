// Package provider is a provider-agnostic adapter for opening a pull/merge
// request on an upstream git host and pre-flighting write access.
//
// Supported provider kinds: GitHub (github.com), GitHub Enterprise Server,
// GitLab (gitlab.com or self-hosted), Gitea/Forgejo, Bitbucket Cloud, and
// Bitbucket Server / Data Center. Each speaks its own REST dialect; this package
// hides those differences behind a single Provider interface.
package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/buffsystems/buff-connector/internal/httpx"
)

// Kind identifies the upstream provider dialect.
type Kind string

const (
	KindGitHub          Kind = "github"
	KindGHE             Kind = "ghe"
	KindGitLab          Kind = "gitlab"
	KindGitea           Kind = "gitea"
	KindBitbucket       Kind = "bitbucket"
	KindBitbucketServer Kind = "bitbucket-server"
)

// PRSpec describes the pull/merge request to open.
type PRSpec struct {
	Head  string // source branch
	Base  string // target branch
	Title string
	Body  string
}

// PRResult is the outcome of CreatePR.
type PRResult struct {
	URL           string
	Number        int
	AlreadyExists bool
}

// RepoRef is one repo the token can access.
type RepoRef struct {
	Repo          string `json:"repo"` // path used elsewhere: owner/name (or PROJECT/slug for bitbucket-server)
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// Provider opens PRs and pre-flights write access on one upstream repository.
type Provider interface {
	// PushRemoteURL returns a git remote URL with the token embedded in userinfo.
	PushRemoteURL(token string) string
	// GitConfig is the `-c key=value` settings git needs to authenticate where
	// the URL cannot carry it. Empty for every host that takes a token in the
	// userinfo; Bitbucket Data Center takes an HTTP access token only as a
	// bearer header and refuses it anywhere else.
	GitConfig(token string) []string
	// CheckAccess returns nil when the token has push + PR rights on the repo.
	CheckAccess(ctx context.Context, token string) error
	// CreatePR opens a pull/merge request; an already-existing PR is success.
	CreatePR(ctx context.Context, token string, spec PRSpec) (PRResult, error)
	// ListRepos lists repos the token can access (first page / a reasonable cap is fine;
	// prefer membership/affiliated repos). Used to populate the setup UI's repo picker.
	ListRepos(ctx context.Context, token string) ([]RepoRef, error)
}

// Sentinel errors returned by CheckAccess (and CreatePR pre-flight). They wrap
// the shared httpx sentinels so a caller can match on either layer.
var (
	// ErrNoAccess maps to a 401/403 from the provider.
	ErrNoAccess = fmt.Errorf("провайдер — нет доступа к репозиторию: %w", httpx.ErrNoAccess)
	// ErrNotFound maps to a 404 from the provider.
	ErrNotFound = fmt.Errorf("провайдер — репозиторий не найден: %w", httpx.ErrNotFound)
)

// New builds a Provider for the given kind. repoPath is "owner/repo" for every
// kind except bitbucket-server, where it is "PROJECTKEY/slug". baseURL selects a
// self-hosted instance (and, in tests, points the adapter at an httptest server);
// an empty baseURL uses the public cloud endpoints where one exists. A nil
// httpClient falls back to a client with a sane timeout.
func New(kind Kind, baseURL, repoPath string, httpClient *http.Client) (Provider, error) {
	// GitLab is the one kind whose namespace nests, so it gets the parser that
	// allows it; everywhere else a third segment is a typo, not a subgroup.
	split := parseRepoPath
	if kind == KindGitLab {
		split = parseNestedRepoPath
	}
	a, b, err := split(repoPath)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	base := strings.TrimRight(baseURL, "/")

	switch kind {
	case KindGitHub, KindGHE:
		return newGitHub(kind, base, a, b, httpClient)
	case KindGitLab:
		return newGitLab(base, a, b, httpClient)
	case KindGitea:
		return newGitea(base, a, b, httpClient)
	case KindBitbucket:
		return newBitbucket(base, a, b, httpClient)
	case KindBitbucketServer:
		return newBitbucketServer(base, a, b, httpClient)
	default:
		return nil, fmt.Errorf("провайдер — неизвестный вид %q", kind)
	}
}

// parseRepoPath splits "owner/repo" (or "PROJECT/slug") into its two segments,
// tolerating a leading slash and a trailing ".git" or slash. It never panics.
func parseRepoPath(repoPath string) (string, string, error) {
	parts := strings.Split(trimRepoPath(repoPath), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("провайдер — путь репозитория %q должен быть \"владелец/репозиторий\"", repoPath)
	}
	return parts[0], parts[1], nil
}

// parseNestedRepoPath splits a path whose NAMESPACE may nest: everything up to
// the last slash names the namespace, the last segment the repository. GitLab
// needs it — a project can live in a subgroup, and on gitlab.com it usually has
// to, because the API refuses to create a top-level group at all. A plain
// "owner/repo" parses exactly as before.
func parseNestedRepoPath(repoPath string) (string, string, error) {
	p := trimRepoPath(repoPath)
	i := strings.LastIndex(p, "/")
	if i <= 0 || i == len(p)-1 {
		return "", "", fmt.Errorf("провайдер — путь репозитория %q должен быть \"пространство/репозиторий\"", repoPath)
	}
	return p[:i], p[i+1:], nil
}

// trimRepoPath strips what people paste around a path: a leading slash, a
// trailing slash, and the ".git" a clone URL carries.
func trimRepoPath(repoPath string) string {
	p := strings.Trim(repoPath, "/")
	p = strings.TrimSuffix(p, ".git")
	return strings.Trim(p, "/")
}

// schemeOf returns the URL scheme of a base ("http" or "https"), defaulting to
// "https" when absent — so a self-hosted instance served over plain HTTP (common
// on an internal network) produces an http:// git remote, not a broken https one.
func schemeOf(base string) string {
	if u, err := url.Parse(base); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return u.Scheme
	}
	return "https"
}

// hostOf returns the host[:port] of a base URL, falling back to the raw string
// with any scheme prefix stripped. It never panics.
func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	s := strings.TrimPrefix(base, "https://")
	s = strings.TrimPrefix(s, "http://")
	return strings.TrimRight(s, "/")
}
