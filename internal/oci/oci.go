// Package oci moves signed files through a container registry.
//
// Why a registry at all: a customer with a closed perimeter already knows what
// to do with one. They mirror it into their own Harbor or Nexus, they have an
// admission policy for it, they scan it. "Download a signed tarball from our
// HTTPS host" is a conversation their security team has not had; "pull from a
// registry" is one they have. It also means one less public host of ours to run.
//
// What this package is NOT: trust. The registry is transport. Every payload
// carries our own detached signature, verified by the connector against a key
// baked into the binary — a customer pulling from their OWN mirror must still be
// able to prove the bundle is ours, and a registry's authentication cannot say
// anything about that.
//
// Deliberately hand-written against the distribution API rather than pulling in
// an OCI library: the connector has two direct dependencies and that number is
// part of what makes it auditable. A pull is two GETs.
package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	manifestMediaType  = "application/vnd.oci.image.manifest.v1+json"
	configMediaType    = "application/vnd.oci.image.config.v1+json"
	payloadMediaType   = "application/vnd.buff.plugin.payload.v1"
	signatureMediaType = "application/vnd.buff.plugin.signature.v1"

	// A ceiling on anything we read from a registry. Bundles are hundreds of
	// kilobytes; this is room to grow and still a refusal rather than a machine
	// that fills its disk because a registry answered oddly.
	maxBlob = 64 << 20
)

// Client talks to one registry.
type Client struct {
	Host string
	// Plain sends http:// instead of https://. For tests and for a registry on
	// the loopback; never for a published one.
	Plain bool
	// User and Pass are needed only to PUSH. Pulling our catalogue is anonymous
	// on purpose: a customer must not need an account with us to check what a
	// plugin is allowed to do.
	User, Pass string
	HTTP       *http.Client
}

func New(host string) *Client {
	return &Client{Host: strings.TrimRight(host, "/"), HTTP: &http.Client{Timeout: 120 * time.Second}}
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        descriptor        `json:"config"`
	Layers        []descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// Pull fetches one artifact: its payload and the detached signature beside it.
func (c *Client) Pull(ctx context.Context, repo, ref string) (payload, sig []byte, err error) {
	raw, err := c.get(ctx, c.url(repo, "manifests", ref), manifestMediaType)
	if err != nil {
		return nil, nil, fmt.Errorf("%s:%s: %w", repo, ref, err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("%s:%s — манифест не читается: %w", repo, ref, err)
	}
	if len(m.Layers) < 2 {
		return nil, nil, fmt.Errorf("%s:%s — ждали содержимое и подпись, а слоёв %d", repo, ref, len(m.Layers))
	}
	if payload, err = c.blob(ctx, repo, m.Layers[0]); err != nil {
		return nil, nil, err
	}
	if sig, err = c.blob(ctx, repo, m.Layers[1]); err != nil {
		return nil, nil, err
	}
	return payload, sig, nil
}

// Push uploads a payload and its signature as one artifact.
func (c *Client) Push(ctx context.Context, repo, ref string, payload, sig []byte) error {
	// A registry requires a config descriptor even when there is nothing to
	// configure, so an empty JSON object is uploaded as the config blob.
	cfg := []byte("{}")
	for _, b := range [][]byte{cfg, payload, sig} {
		if err := c.putBlob(ctx, repo, b); err != nil {
			return err
		}
	}
	m := manifest{
		SchemaVersion: 2,
		MediaType:     manifestMediaType,
		Config:        descriptor{MediaType: configMediaType, Digest: digestOf(cfg), Size: int64(len(cfg))},
		Layers: []descriptor{
			{MediaType: payloadMediaType, Digest: digestOf(payload), Size: int64(len(payload))},
			{MediaType: signatureMediaType, Digest: digestOf(sig), Size: int64(len(sig))},
		},
		Annotations: map[string]string{
			"org.opencontainers.image.created": time.Now().UTC().Format(time.RFC3339),
			"systems.buff.signature":           base64.StdEncoding.EncodeToString(sig),
		},
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := c.request(ctx, http.MethodPut, c.url(repo, "manifests", ref), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", manifestMediaType)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("отправка %s:%s: %w", repo, ref, err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("отправка %s:%s — %s (%s)", repo, ref, res.Status, snippet(res.Body))
	}
	return nil
}

// Tags lists the versions a repository holds, which is how the catalogue can be
// checked against what is actually published.
func (c *Client) Tags(ctx context.Context, repo string) ([]string, error) {
	raw, err := c.get(ctx, c.url(repo, "tags", "list"), "application/json")
	if err != nil {
		return nil, err
	}
	var out struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Tags, nil
}

// blob fetches a layer and checks it hashes to the address it was fetched by.
// Without this the registry would be trusted to return what it was asked for,
// and a substituted blob would reach the signature check wearing the right name.
func (c *Client) blob(ctx context.Context, repo string, d descriptor) ([]byte, error) {
	body, err := c.get(ctx, c.url(repo, "blobs", d.Digest), "")
	if err != nil {
		return nil, err
	}
	if got := digestOf(body); got != d.Digest {
		return nil, fmt.Errorf("%s — хеш не совпал, в манифесте %s, у содержимого %s", repo, d.Digest, got)
	}
	return body, nil
}

func (c *Client) putBlob(ctx context.Context, repo string, body []byte) error {
	req, err := c.request(ctx, http.MethodPost, c.url(repo, "blobs", "uploads/"), nil)
	if err != nil {
		return err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("начало выгрузки в %s: %w", repo, err)
	}
	loc := res.Header.Get("Location")
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted || loc == "" {
		return fmt.Errorf("начало выгрузки в %s — %s", repo, res.Status)
	}
	put, err := c.request(ctx, http.MethodPut, c.absolute(loc)+separator(loc)+"digest="+url.QueryEscape(digestOf(body)), bytes.NewReader(body))
	if err != nil {
		return err
	}
	put.Header.Set("Content-Type", "application/octet-stream")
	up, err := c.HTTP.Do(put)
	if err != nil {
		return fmt.Errorf("выгрузка в %s: %w", repo, err)
	}
	defer up.Body.Close()
	if up.StatusCode/100 != 2 {
		return fmt.Errorf("выгрузка в %s — %s (%s)", repo, up.Status, snippet(up.Body))
	}
	return nil
}

func (c *Client) get(ctx context.Context, url, accept string) ([]byte, error) {
	req, err := c.request(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		// Registries still serve the docker media type for images pushed by
		// docker; asking for both keeps this usable against either.
		req.Header.Set("Accept", accept+", application/vnd.docker.distribution.manifest.v2+json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("обращение к реестру: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("не опубликовано")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("реестр ответил %s", res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, maxBlob))
}

func (c *Client) request(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if c.User != "" {
		req.SetBasicAuth(c.User, c.Pass)
	}
	return req, nil
}

func (c *Client) scheme() string {
	if c.Plain {
		return "http://"
	}
	return "https://"
}

func (c *Client) url(repo, kind, ref string) string {
	return c.scheme() + c.Host + "/v2/" + repo + "/" + kind + "/" + ref
}

// absolute resolves the Location a registry hands back, which the spec allows to
// be either a full URL or a path on the same host.
func (c *Client) absolute(loc string) string {
	if strings.HasPrefix(loc, "http://") || strings.HasPrefix(loc, "https://") {
		return loc
	}
	return c.scheme() + c.Host + loc
}

func separator(loc string) string {
	if strings.Contains(loc, "?") {
		return "&"
	}
	return "?"
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func snippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 300))
	return strings.TrimSpace(string(b))
}
