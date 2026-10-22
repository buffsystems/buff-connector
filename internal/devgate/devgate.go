// Package devgate is the connector's door for developers (#366). Buff Code on a
// developer's machine signs in through the company's connector, which relays the
// sign-in to the backend: start, wait for the cabinet's answer, refresh — and, signed
// in, reads what it may work in (#391). The key and the
// tokens stay on the developer's machine; the connector only carries the commands.
//
// Each route relays a fixed set of fields to one fixed command, so nothing a client
// sends can name another command or ride on someone else's session.
package devgate

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// Requester sends one command to the backend over the middleware (natslink.Link does).
type Requester interface {
	Request(ctx context.Context, subject string, payload map[string]any) (map[string]any, error)
}

type route struct {
	op     string
	fields []string
}

var routes = map[string]route{
	"/v1/developer/device/start": {op: "device_start", fields: []string{"device_name", "pubkey"}},
	"/v1/developer/device/poll":  {op: "device_poll", fields: []string{"device_code"}},
	"/v1/developer/refresh":      {op: "developer_refresh", fields: []string{"refresh", "sig"}},
	// The organisations, projects and spaces to pick the work context from (#391),
	// read with the developer's own token and narrowed by the backend to it.
	"/v1/developer/workspace": {op: "list", fields: []string{"token"}},
	// The organisation's balance and the developer's own spend, for Buff Code to show
	// (cloud-front#455): a read; whose spend it is the backend takes from the token.
	"/v1/developer/billing": {op: "developer_billing", fields: []string{"token", "org"}},
}

// commandTimeout: how long one relayed command waits for the backend's answer.
const commandTimeout = 30 * time.Second

// Handler serves the developer routes over link.
func Handler(link Requester) http.Handler { return HandlerWithTools(link, nil, "") }

// HandlerWithTools also serves the connector's tools to Buff Code at /v1/mcp (#368) and
// the organisation's knowledge at /v1/knowledge/mcp (#371) and the project's repositories at
// /v1/repos/mcp (#372), for the developers of org.
func HandlerWithTools(link Requester, tools Tools, org string) http.Handler {
	var mcp, knowledge *mcpServer
	if tools != nil {
		mcp = newMCPServer(link, toolsSurface{tools}, org)
	}
	// The organisation's knowledge (#371) needs nothing of the connector but the line to the
	// backend: it is served wherever the gateway knows its organisation.
	var repos *mcpServer
	if org != "" {
		knowledge = newMCPServer(link, knowledgeSurface{link: link}, org)
		repos = newMCPServer(link, reposSurface{link: link}, org)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mcp != nil && r.URL.Path == "/v1/mcp" {
			mcp.ServeHTTP(w, r)
			return
		}
		if knowledge != nil && r.URL.Path == "/v1/knowledge/mcp" {
			knowledge.ServeHTTP(w, r)
			return
		}
		if repos != nil && r.URL.Path == "/v1/repos/mcp" {
			repos.ServeHTTP(w, r)
			return
		}
		if serveModels(w, r, link) {
			return
		}
		if serveEngineRows(w, r, link) {
			return
		}
		rt, ok := routes[r.URL.Path]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "нет такого адреса"})
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "только POST"})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "тело запроса — не JSON"})
			return
		}
		payload := map[string]any{"op": rt.op}
		for _, f := range rt.fields {
			if v, ok := body[f]; ok {
				payload[f] = v
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), commandTimeout)
		defer cancel()
		reply, err := link.Request(ctx, "data.cmd."+rt.op, payload)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, reply)
		case reply != nil: // the backend answered: a refusal, passed on in its own words
			writeJSON(w, http.StatusForbidden, reply)
		default:
			log.Printf("developer gateway: %s: %v", rt.op, err)
			writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "коннектор не достучался до Buff — попробуйте ещё раз"})
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Settings is the part of the connector's config the gateway reads.
type Settings struct {
	Listen, TLSCert, TLSKey string
	// Org is the connector's organisation: only its developers get the tools (#368).
	Org string
}

// Serve starts the gateway on s.Listen (nothing when empty) and stops it with ctx.
// A gateway that cannot bind is logged: sign-in through it is visibly broken, the rest
// of the connector keeps working.
func Serve(ctx context.Context, s Settings, link Requester, tools Tools) *http.Server {
	if s.Listen == "" {
		return nil
	}
	srv := &http.Server{Addr: s.Listen, Handler: HandlerWithTools(link, tools, s.Org), ReadHeaderTimeout: 10 * time.Second}
	tls := s.TLSCert != "" && s.TLSKey != ""
	go func() {
		var err error
		if tls {
			log.Printf("developer gateway: listening on %s (https)", s.Listen)
			err = srv.ListenAndServeTLS(s.TLSCert, s.TLSKey)
		} else {
			log.Printf("developer gateway: listening on %s WITHOUT TLS — developers' tokens cross the network in the clear; set developer_gateway.tls_cert and tls_key", s.Listen)
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Printf("developer gateway on %s: %v", s.Listen, err)
		}
	}()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return srv
}

// Address is what developers are told to enter in Buff Code: the configured host when
// set (behind NAT, in a container, or bound to 0.0.0.0 the listen address cannot be
// dialled), else the listen address with the scheme it is served on; "" — no gateway.
func Address(s Settings, host string) string {
	if s.Listen == "" {
		return ""
	}
	if host != "" {
		return host
	}
	if s.TLSCert != "" && s.TLSKey != "" {
		return "https://" + s.Listen
	}
	return "http://" + s.Listen
}
