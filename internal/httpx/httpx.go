// Package httpx holds the small REST/JSON plumbing shared by the connector's
// outbound adapters — the git providers and the issue trackers. It knows how to
// send a JSON request, read a response whole, map a status code to an access
// error, and dig a human message out of whatever error shape the remote uses.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// UserAgent identifies the connector to every remote it talks to.
const UserAgent = "buff-connector"

// Sentinel errors returned by AccessErrForStatus.
var (
	// ErrNoAccess maps to a 401/403 from the remote.
	ErrNoAccess = errors.New("нет доступа")
	// ErrNotFound maps to a 404 from the remote.
	ErrNotFound = errors.New("не найдено")
)

// Resp is the decoded shape of a response we care about.
type Resp struct {
	Status int
	Body   []byte
}

// OK reports whether the status is a 2xx.
func (r Resp) OK() bool { return r.Status >= 200 && r.Status < 300 }

// DoJSON performs an HTTP request. When body is non-nil it is JSON-encoded and a
// Content-Type header is set. headers are applied verbatim. It reads the full
// response body and never panics on a nil body.
func DoJSON(ctx context.Context, client *http.Client, method, url string, body any, headers map[string]string) (Resp, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return Resp{}, fmt.Errorf("кодирование тела запроса: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return Resp{}, fmt.Errorf("подготовка запроса: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return do(client, req, method, url)
}

// DoRaw performs an HTTP request with a caller-supplied body and headers — used
// where the remote demands something other than JSON, such as a multipart upload.
func DoRaw(ctx context.Context, client *http.Client, method, url string, body io.Reader, headers map[string]string) (Resp, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return Resp{}, fmt.Errorf("подготовка запроса: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return do(client, req, method, url)
}

func do(client *http.Client, req *http.Request, method, url string) (Resp, error) {
	resp, err := client.Do(req)
	if err != nil {
		return Resp{}, fmt.Errorf("запрос %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return Resp{}, fmt.Errorf("чтение ответа: %w", err)
	}
	return Resp{Status: resp.StatusCode, Body: buf}, nil
}

// AccessErrForStatus maps a status code to a sentinel access error, or nil when
// the status is a success. Any other non-2xx yields a generic error that
// includes the remote's message when one can be extracted.
func AccessErrForStatus(r Resp) error {
	switch {
	case r.OK():
		return nil
	case r.Status == http.StatusUnauthorized || r.Status == http.StatusForbidden:
		return ErrNoAccess
	case r.Status == http.StatusNotFound:
		return ErrNotFound
	default:
		return fmt.Errorf("неожиданный ответ %d: %s", r.Status, Message(r.Body))
	}
}

// Message best-effort extracts a human message from an error body across the
// several JSON shapes used by the remotes we support.
func Message(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	// "errors" is an array of objects on Bitbucket but an object on Jira, so it
	// is held raw: decoding it into a fixed shape would fail the whole struct and
	// lose the sibling fields that did parse.
	var m struct {
		Message  string          `json:"message"`
		Error    string          `json:"error"`
		Messages []string        `json:"errorMessages"`
		Errors   json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &m); err == nil {
		switch {
		case m.Message != "":
			return m.Message
		case m.Error != "":
			return m.Error
		case len(m.Messages) > 0 && m.Messages[0] != "":
			return m.Messages[0]
		}
		var nested []struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(m.Errors, &nested) == nil && len(nested) > 0 && nested[0].Message != "" {
			return nested[0].Message
		}
	}
	return string(bytes.TrimSpace(body))
}

// Decode unmarshals a JSON body into v, wrapping errors.
func Decode(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("разбор ответа: %w", err)
	}
	return nil
}
