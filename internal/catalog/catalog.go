// Package catalog reads the plugin catalogue we publish and fetches bundles from
// it.
//
// The catalogue is signed files, not a relay message: signed end to end means
// the transport does not have to be trusted, and the relay is a message bus that
// every organisation shares — widening it for software distribution would be the
// wrong trade.
//
// Two transports carry those same files. A container REGISTRY is what a
// published catalogue uses: a customer with a closed perimeter already knows how
// to mirror one into their own Harbor or Nexus, allow it, and scan it. Plain
// HTTPS files stay supported because the offline kit is a directory of exactly
// these files, and a self-hosted mirror may just serve them. The FORMAT does not
// differ between the two — only how the bytes arrive — so one signed catalogue
// works everywhere.
//
// The catalogue INDEX is signed as well as each bundle. Signing only bundles
// would leave the list itself forgeable, and a forged list is enough to talk a
// connector into installing an older version we have since withdrawn.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/buffsystems/buff-connector/internal/i18n"
	"github.com/buffsystems/buff-connector/internal/oci"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// Index is the published catalogue.
type Index struct {
	GeneratedAt time.Time `json:"generated_at"`
	Plugins     []Entry   `json:"plugins"`
	// The newest connector we publish. It travels in the catalogue because that
	// is the one channel a connector already verifies with its baked-in keys —
	// telling an operator to update should not need a second thing to trust
	// (#302). An older publisher leaves it empty, and then nothing is claimed.
	Connector string `json:"connector,omitempty"`
}

// Entry is one plugin with every version we still serve.
type Entry struct {
	Name     string `json:"name"`
	Summary  string `json:"summary"`
	Category string `json:"category"`
	// The English of Summary and Category (#565), from the bundle's
	// manifest.en.yaml. An older publisher leaves them out, and a reader
	// that does not know them — an older connector, the site — never sees them.
	SummaryEN  string    `json:"summary_en,omitempty"`
	CategoryEN string    `json:"category_en,omitempty"`
	Publisher  string    `json:"publisher"`
	DocsURL    string    `json:"docs_url,omitempty"`
	SourceURL  string    `json:"source_url,omitempty"`
	Versions   []Version `json:"versions"`
}

// Version is one published bundle. It carries what the consent screen needs, so
// an operator sees what a plugin will ask for BEFORE anything is installed.
type Version struct {
	Version   string   `json:"version"`
	HostAPI   int      `json:"host_api"`
	Released  string   `json:"released"`
	Size      int64    `json:"size"`
	Bundle    string   `json:"bundle"`
	Signature string   `json:"signature"`
	Changelog string   `json:"changelog,omitempty"`
	Platforms []string `json:"platforms,omitempty"`
	Status    string   `json:"status,omitempty"` // "", deprecated, withdrawn
	Reason    string   `json:"reason,omitempty"`
	Webhook   string   `json:"webhook,omitempty"`
	Tools     []Tool   `json:"tools,omitempty"`
	Creds     []string `json:"credentials,omitempty"`
	// Handed names the credentials the plugin RECEIVES (kind password) — the
	// consent screen says so, because "values stay in the connector" is
	// exactly what is not true of these.
	Handed     []string `json:"handed,omitempty"`
	EgressFrom []string `json:"egress_from,omitempty"`
	// Web marks a plugin that reaches the public internet at the model's
	// direction, within the operator's lists — the consent screen says so in
	// those words rather than listing a field.
	Web bool `json:"web,omitempty"`
	// Network lists the connections the plugin opens ITSELF (SMB to the
	// shares in a setting), which the host shows but cannot guard.
	Network []Network `json:"network,omitempty"`
}

// Network is one protocol a plugin speaks on its own, to hosts named in a
// list setting of the connection.
type Network struct {
	Protocol  string `json:"protocol"`
	HostsFrom string `json:"hosts_from"`
	Ports     []int  `json:"ports"`
}

// Tool is the catalogue's view of what a plugin offers.
type Tool struct {
	Name     string `json:"name"`
	ReadOnly bool   `json:"read_only"`
	// Answers with a picture: shown in the catalogue so a buyer on a text-only
	// model knows this one will not be offered to their model.
	Vision    bool   `json:"vision,omitempty"`
	Summary   string `json:"summary"`
	SummaryEN string `json:"summary_en,omitempty"`
}

// InLang is the catalogue as a person reading l sees it: in English, each
// summary and category its English where the publisher gave one. The index
// itself is not changed.
func (idx *Index) InLang(l i18n.Lang) *Index {
	if idx == nil || l != i18n.EN {
		return idx
	}
	out := *idx
	out.Plugins = make([]Entry, len(idx.Plugins))
	for i, e := range idx.Plugins {
		e.Summary = orElse(e.SummaryEN, e.Summary)
		e.Category = orElse(e.CategoryEN, e.Category)
		versions := make([]Version, len(e.Versions))
		for j, v := range e.Versions {
			tools := make([]Tool, len(v.Tools))
			for k, t := range v.Tools {
				t.Summary = orElse(t.SummaryEN, t.Summary)
				tools[k] = t
			}
			v.Tools = tools
			versions[j] = v
		}
		e.Versions = versions
		out.Plugins[i] = e
	}
	return &out
}

func orElse(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// source is where the bytes come from. It knows nothing about signatures: every
// transport hands back a payload and the detached signature beside it, and the
// Client is the single place that decides whether to believe them.
type source interface {
	catalog(ctx context.Context) (body, sig []byte, err error)
	revocations(ctx context.Context) (body, sig []byte, err error)
	bundle(ctx context.Context, name string, v Version) (body, sig []byte, err error)
}

// Client reads a catalogue and downloads from it.
type Client struct {
	BaseURL string
	Keys    []string
	CacheIn string
	HTTP    *http.Client
	src     source
}

// New reads the catalogue at baseURL:
//
//	oci://host/prefix       a container registry — what a published catalogue uses
//	oci+http://host/prefix  the same, without TLS (a registry on the loopback)
//	file:///path            an offline kit: the same signed files, from a directory
//	https://host/path       a self-hosted mirror serving the files directly
func New(baseURL string, keys []string, cacheDir string) *Client {
	c := &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Keys:    keys,
		CacheIn: cacheDir,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
	if dir, ok := strings.CutPrefix(c.BaseURL, "file://"); ok {
		c.src = &dirSource{dir: dir}
	} else if plain := strings.HasPrefix(c.BaseURL, "oci+http://"); plain || strings.HasPrefix(c.BaseURL, "oci://") {
		rest := strings.TrimPrefix(strings.TrimPrefix(c.BaseURL, "oci+http://"), "oci://")
		host, prefix, _ := strings.Cut(rest, "/")
		cl := oci.New(host)
		cl.Plain = plain
		c.src = &ociSource{cl: cl, prefix: strings.Trim(prefix, "/")}
	} else {
		c.src = &httpSource{c: c}
	}
	return c
}

// ociSource maps our four logical artifacts onto repositories. The plugin name
// is the repository and the version is the tag, so `docker`-shaped tooling and
// `tags/list` show a customer what exists without any of our code.
type ociSource struct {
	cl     *oci.Client
	prefix string
}

func (o *ociSource) repo(name string) string {
	if o.prefix == "" {
		return name
	}
	return o.prefix + "/" + name
}

func (o *ociSource) catalog(ctx context.Context) ([]byte, []byte, error) {
	return o.cl.Pull(ctx, o.repo("catalog"), "v1")
}

func (o *ociSource) revocations(ctx context.Context) ([]byte, []byte, error) {
	return o.cl.Pull(ctx, o.repo("revocations"), "v1")
}

func (o *ociSource) bundle(ctx context.Context, name string, v Version) ([]byte, []byte, error) {
	return o.cl.Pull(ctx, o.repo(name), v.Version)
}

// dirSource reads the offline kit: the same signed files, from a directory
// somebody carried in.
//
// A machine inside a closed perimeter may have no route anywhere — not to our
// registry, not to a mirror. The kit is how it gets a plugin at all, and the
// signature is what makes that safe: a directory on a USB stick is exactly the
// transport a signature exists for. Nothing here trusts the disk.
type dirSource struct{ dir string }

func (d *dirSource) catalog(context.Context) ([]byte, []byte, error) {
	return d.pair("catalog.json")
}

func (d *dirSource) revocations(context.Context) ([]byte, []byte, error) {
	return d.pair("revocations.json")
}

func (d *dirSource) bundle(_ context.Context, _ string, v Version) ([]byte, []byte, error) {
	return d.pair(v.Bundle)
}

func (d *dirSource) pair(name string) ([]byte, []byte, error) {
	// path.Clean and the base name together keep a catalogue from naming a file
	// outside the kit — the kit is data, and data does not choose paths.
	safe := filepath.Base(path.Clean(name))
	body, err := os.ReadFile(filepath.Join(d.dir, safe))
	if err != nil {
		return nil, nil, fmt.Errorf("офлайн-набор: %w", err)
	}
	sig, err := os.ReadFile(filepath.Join(d.dir, safe+".sig"))
	if err != nil {
		return nil, nil, fmt.Errorf("офлайн-набор — %s не подписан: %w", safe, err)
	}
	return body, sig, nil
}

// httpSource is a directory of files over HTTP — a self-hosted mirror's shape.
type httpSource struct{ c *Client }

func (h *httpSource) catalog(ctx context.Context) ([]byte, []byte, error) {
	return h.c.pair(ctx, "catalog.json", "catalog.json.sig")
}

func (h *httpSource) revocations(ctx context.Context) ([]byte, []byte, error) {
	return h.c.pair(ctx, "revocations.json", "revocations.json.sig")
}

func (h *httpSource) bundle(ctx context.Context, _ string, v Version) ([]byte, []byte, error) {
	return h.c.pair(ctx, v.Bundle, v.Signature)
}

func (c *Client) pair(ctx context.Context, body, sig string) ([]byte, []byte, error) {
	b, err := c.get(ctx, body)
	if err != nil {
		return nil, nil, err
	}
	s, err := c.get(ctx, sig)
	if err != nil {
		return nil, nil, err
	}
	return b, s, nil
}

// Fetch reads the catalogue, verifies it and caches it.
func (c *Client) Fetch(ctx context.Context) (*Index, error) {
	body, sig, err := c.src.catalog(ctx)
	if err != nil {
		return nil, err
	}
	if err := plugin.Verify(body, sig, c.Keys); err != nil {
		return nil, fmt.Errorf("подпись каталога: %w", err)
	}
	var idx Index
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("каталог не читается: %w", err)
	}
	c.cache(body, sig)
	return &idx, nil
}

// Cached returns the last catalogue we successfully read, so the list is still
// visible without the network — with its date, so nobody mistakes it for live.
func (c *Client) Cached() (*Index, error) {
	if c.CacheIn == "" {
		return nil, os.ErrNotExist
	}
	body, err := os.ReadFile(filepath.Join(c.CacheIn, "catalog.json"))
	if err != nil {
		return nil, err
	}
	sig, err := os.ReadFile(filepath.Join(c.CacheIn, "catalog.json.sig"))
	if err != nil {
		return nil, err
	}
	// The cache is on the customer's disk; re-verify rather than trust it.
	if err := plugin.Verify(body, sig, c.Keys); err != nil {
		return nil, fmt.Errorf("подпись сохранённого каталога: %w", err)
	}
	var idx Index
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// Download fetches a bundle and its signature. The plugin NAME is passed
// alongside the version because a registry addresses a bundle by repository and
// tag, not by file name. Verification happens in the store, which is the only
// place that decides what may be installed.
func (c *Client) Download(ctx context.Context, name string, v Version) (bundle, sig []byte, err error) {
	return c.src.bundle(ctx, name, v)
}

// Revocations reads the withdrawal list. It is fetched alongside the catalogue
// and belongs in the offline kit too — otherwise installing from a file would
// simply route around it.
func (c *Client) Revocations(ctx context.Context) (*plugin.Revocation, error) {
	body, sig, err := c.src.revocations(ctx)
	if err != nil {
		return nil, err
	}
	if err := plugin.Verify(body, sig, c.Keys); err != nil {
		return nil, fmt.Errorf("подпись списка отзыва: %w", err)
	}
	var rev plugin.Revocation
	if err := json.Unmarshal(body, &rev); err != nil {
		return nil, err
	}
	return &rev, nil
}

func (c *Client) get(ctx context.Context, name string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/"+path.Clean(name), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("обращение к каталогу: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("каталог ответил %s на %s", res.Status, name)
	}
	return io.ReadAll(io.LimitReader(res.Body, plugin.MaxBundleSize+1))
}

func (c *Client) cache(body, sig []byte) {
	if c.CacheIn == "" {
		return
	}
	if err := os.MkdirAll(c.CacheIn, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(c.CacheIn, "catalog.json"), body, 0o644)
	_ = os.WriteFile(filepath.Join(c.CacheIn, "catalog.json.sig"), sig, 0o644)
}

// Find locates a version in the index.
func (i *Index) Find(name, version string) (Entry, Version, bool) {
	for _, e := range i.Plugins {
		if e.Name != name {
			continue
		}
		for _, v := range e.Versions {
			if v.Version == version {
				return e, v, true
			}
		}
	}
	return Entry{}, Version{}, false
}

// LatestCompatible is the newest version this build can actually run. Showing
// only "latest" would send an operator to a version their connector refuses,
// with no way to tell why.
func (e Entry) LatestCompatible(hostAPI int) (Version, bool) {
	var best Version
	var found bool
	for _, v := range e.Versions {
		if v.HostAPI > hostAPI || v.Status == "withdrawn" {
			continue
		}
		if !found || v.Version > best.Version {
			best, found = v, true
		}
	}
	return best, found
}
