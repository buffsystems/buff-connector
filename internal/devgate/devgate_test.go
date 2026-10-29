package devgate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type sent struct {
	subject string
	payload map[string]any
}

type fakeLink struct {
	calls []sent
	reply map[string]any
	err   error
}

func (f *fakeLink) Request(_ context.Context, subject string, payload map[string]any) (map[string]any, error) {
	f.calls = append(f.calls, sent{subject, payload})
	return f.reply, f.err
}

func post(t *testing.T, h http.Handler, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

func TestEachRouteRelaysOnlyItsOwnFields(t *testing.T) {
	cases := []struct {
		path, body, subject string
		want                map[string]any
	}{
		{"/v1/developer/device/start", `{"device_name":"ноутбук","pubkey":"K","op":"members_list","token":"stolen"}`,
			"data.cmd.device_start", map[string]any{"op": "device_start", "device_name": "ноутбук", "pubkey": "K"}},
		{"/v1/developer/device/poll", `{"device_code":"bufd_X","org":"acme"}`,
			"data.cmd.device_poll", map[string]any{"op": "device_poll", "device_code": "bufd_X"}},
		{"/v1/developer/refresh", `{"refresh":"R","sig":"S","token":"T"}`,
			"data.cmd.developer_refresh", map[string]any{"op": "developer_refresh", "refresh": "R", "sig": "S"}},
		{"/v1/developer/workspace", `{"token":"T","org":"acme","op":"members_list"}`,
			"data.cmd.list", map[string]any{"op": "list", "token": "T"}},
		{"/v1/developer/billing", `{"token":"T","org":"acme","login":"other","op":"billing_topup"}`,
			"data.cmd.developer_billing", map[string]any{"op": "developer_billing", "token": "T", "org": "acme"}},
		{"/v1/developer/mobile-home", `{"token":"T","org":"acme","op":"mobile_policy_set","pairing_disabled":false}`,
			"data.cmd.developer_mobile_home", map[string]any{"op": "developer_mobile_home", "token": "T", "org": "acme"}},
	}
	for _, c := range cases {
		link := &fakeLink{reply: map[string]any{"ok": true, "status": "pending"}}
		rec, got := post(t, Handler(link), c.path, c.body)
		if rec.Code != http.StatusOK || got["status"] != "pending" {
			t.Fatalf("%s: %d %v", c.path, rec.Code, got)
		}
		if len(link.calls) != 1 || link.calls[0].subject != c.subject {
			t.Fatalf("%s: calls %+v", c.path, link.calls)
		}
		if len(link.calls[0].payload) != len(c.want) {
			t.Fatalf("%s: relayed %v, want %v", c.path, link.calls[0].payload, c.want)
		}
		for k, v := range c.want {
			if link.calls[0].payload[k] != v {
				t.Fatalf("%s: %s = %v, want %v", c.path, k, link.calls[0].payload[k], v)
			}
		}
	}
}

func TestABackendRefusalIsPassedOnInItsOwnWords(t *testing.T) {
	link := &fakeLink{reply: map[string]any{"ok": false, "error": "Устройство отозвано — войдите заново."}, err: errors.New("refused")}
	rec, got := post(t, Handler(link), "/v1/developer/refresh", `{"refresh":"R","sig":"S"}`)
	if rec.Code != http.StatusForbidden || got["error"] != "Устройство отозвано — войдите заново." {
		t.Fatalf("%d %v", rec.Code, got)
	}
}

func TestAnUnreachableBackendIsABadGateway(t *testing.T) {
	link := &fakeLink{err: errors.New("await reply: timeout")}
	rec, got := post(t, Handler(link), "/v1/developer/device/poll", `{"device_code":"d"}`)
	if rec.Code != http.StatusBadGateway || got["ok"] != false || got["error"] == "" {
		t.Fatalf("%d %v", rec.Code, got)
	}
}

func TestNothingElseIsReachable(t *testing.T) {
	link := &fakeLink{reply: map[string]any{"ok": true}}
	h := Handler(link)
	for _, path := range []string{"/v1/developer/devices", "/v1/cmd/members_list", "/", "/v1/developer/device/approve"} {
		if rec, _ := post(t, h, path, `{}`); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/developer/device/start", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}
	if rec, _ := post(t, h, "/v1/developer/device/start", `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}
	if len(link.calls) != 0 {
		t.Fatalf("relayed %+v", link.calls)
	}
}

func TestTheAddressDevelopersAreGiven(t *testing.T) {
	cases := []struct {
		s    Settings
		host string
		want string
	}{
		{Settings{Listen: "0.0.0.0:8767", TLSCert: "c", TLSKey: "k"}, "https://connector.corp.local:8767", "https://connector.corp.local:8767"},
		{Settings{Listen: "10.0.0.5:8767", TLSCert: "c", TLSKey: "k"}, "", "https://10.0.0.5:8767"},
		{Settings{Listen: "10.0.0.5:8767"}, "", "http://10.0.0.5:8767"},
		{Settings{}, "https://ignored.example", ""},
	}
	for _, c := range cases {
		if got := Address(c.s, c.host); got != c.want {
			t.Fatalf("Address(%+v, %q) = %q, want %q", c.s, c.host, got, c.want)
		}
	}
}
