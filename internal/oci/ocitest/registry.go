// Package ocitest is a stand-in registry for tests: just enough of the
// distribution API for a push and a pull. It lives in its own package so the
// catalogue's tests can drive a registry without a container, and so the
// connector's binary never carries it.
package ocitest

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/oci"
)

// Registry holds everything pushed to it, in memory.
type Registry struct {
	srv       *httptest.Server
	blobs     map[string][]byte
	manifests map[string][]byte
	pushed    []string
}

// New starts a registry that stops with the test.
func New(t *testing.T) *Registry {
	t.Helper()
	r := &Registry{blobs: map[string][]byte{}, manifests: map[string][]byte{}}
	r.srv = httptest.NewServer(r)
	t.Cleanup(r.srv.Close)
	return r
}

// Host is the host:port to build an oci+http:// address from.
func (r *Registry) Host() string { return strings.TrimPrefix(r.srv.URL, "http://") }

// Client talks to this registry.
func (r *Registry) Client() *oci.Client {
	c := oci.New(r.Host())
	c.Plain = true
	return c
}

// Pushed lists the artifacts written, newest last, as "repo:tag".
func (r *Registry) Pushed() []string { return r.pushed }

// Manifest returns the raw manifest JSON stored for an artifact.
func (r *Registry) Manifest(repo, ref string) []byte { return r.manifests[repo+":"+ref] }

// Corrupt replaces a blob's content while keeping its address, which is what a
// registry that lies (or a broken mirror) looks like from the outside.
func (r *Registry) Corrupt(body, replacement []byte) {
	sum := sha256.Sum256(body)
	r.blobs["sha256:"+hex.EncodeToString(sum[:])] = replacement
}

func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	p := strings.TrimPrefix(req.URL.Path, "/v2/")
	switch {
	case strings.Contains(p, "/blobs/uploads/") && req.Method == http.MethodPost:
		repo := strings.Split(p, "/blobs/")[0]
		w.Header().Set("Location", "/v2/"+repo+"/blobs/uploads/1")
		w.WriteHeader(http.StatusAccepted)
	case strings.Contains(p, "/blobs/uploads/") && req.Method == http.MethodPut:
		body, _ := io.ReadAll(req.Body)
		r.blobs[req.URL.Query().Get("digest")] = body
		w.WriteHeader(http.StatusCreated)
	case strings.Contains(p, "/blobs/"):
		body, ok := r.blobs[p[strings.LastIndex(p, "/")+1:]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	case strings.Contains(p, "/manifests/") && req.Method == http.MethodPut:
		repo, ref := split(p)
		body, _ := io.ReadAll(req.Body)
		r.manifests[repo+":"+ref] = body
		r.pushed = append(r.pushed, repo+":"+ref)
		w.WriteHeader(http.StatusCreated)
	case strings.Contains(p, "/manifests/"):
		repo, ref := split(p)
		body, ok := r.manifests[repo+":"+ref]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		_, _ = w.Write(body)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func split(p string) (string, string) {
	i := strings.LastIndex(p, "/manifests/")
	return p[:i], p[i+len("/manifests/"):]
}
