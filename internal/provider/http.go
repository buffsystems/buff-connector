package provider

import (
	"context"
	"net/http"
	"regexp"

	"github.com/buffsystems/buff-connector/internal/httpx"
)

// httpResp is the decoded shape of a provider HTTP response we care about.
type httpResp struct {
	status int
	body   []byte
}

// doJSON performs an HTTP request. When body is non-nil it is JSON-encoded and a
// Content-Type header is set. headers are applied verbatim.
func doJSON(ctx context.Context, client *http.Client, method, url string, body any, headers map[string]string) (httpResp, error) {
	r, err := httpx.DoJSON(ctx, client, method, url, body, headers)
	if err != nil {
		return httpResp{}, err
	}
	return httpResp{status: r.Status, body: r.Body}, nil
}

// accessErrForStatus maps a status code to a sentinel access error, or nil when
// the status is a success (2xx). Any other non-2xx yields a generic error that
// includes the provider's message when one can be extracted.
func accessErrForStatus(r httpResp) error {
	switch err := httpx.AccessErrForStatus(httpx.Resp{Status: r.status, Body: r.body}); {
	case err == nil:
		return nil
	case err == httpx.ErrNoAccess:
		return ErrNoAccess
	case err == httpx.ErrNotFound:
		return ErrNotFound
	default:
		return err
	}
}

var alreadyExistsRe = regexp.MustCompile(`(?i)exist|duplicate|already`)

// isAlreadyExists reports whether a non-2xx create response should be treated as
// an idempotent success: HTTP 409/422, or 400 whose body hints at a duplicate.
func isAlreadyExists(r httpResp) bool {
	switch r.status {
	case http.StatusConflict, http.StatusUnprocessableEntity:
		return true
	case http.StatusBadRequest:
		return alreadyExistsRe.Match(r.body)
	default:
		return false
	}
}

// providerMessage best-effort extracts a human message from a provider error body.
func providerMessage(body []byte) string { return httpx.Message(body) }

// decode unmarshals a JSON body into v, wrapping errors.
func decode(body []byte, v any) error { return httpx.Decode(body, v) }
