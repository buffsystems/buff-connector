package plugin

// ProtocolVersion is the protocol revision the connector speaks to ITS plugins:
// MCP framing plus the buff/* surface.
const ProtocolVersion = "2026-01-01"

// MCPProtocolVersion is the published MCP revision the connector offers a server
// the operator brought (#351). Such a server knows nothing of buff/*, so it is
// spoken to by the book — and the book is versioned.
const MCPProtocolVersion = "2025-06-18"

// SupportedHostAPI is the buff/* surface this build implements. A plugin pinned
// to an older version keeps working; one that needs a newer host is refused with
// a message naming both numbers, because instances stay pinned for a long time
// and "it just stopped" is the worst possible failure mode.
//
//	1 — buff/http to configured hosts, buff/poll, buff/reply, buff/check, buff/webhook.
//	2 — the web egress rule (a destination the model names, held by the operator's
//	    lists and a public-only pinned dial) and buff/url_allowed.
//	3 — poll_seconds (the plugin's suggested read interval), a webhook signing
//	    secret the source introduces (signature.secret_from), the finished
//	    document handed to buff/reply as `document`, and a byte-exact request
//	    body for buff/http (body_base64).
//	4 — credentials the host MINTS: service_account (a JSON key, traded for a
//	    bearer through a signed JWT) and oauth2 (the operator's own client plus
//	    a refresh token from a sign-in on the local page), declared with an
//	    `oauth` block naming the endpoints and scopes.
//	5 — a credential the host HANDS to the plugin: kind `password` (an account
//	    and a password) read through buff/credential, for a protocol the host
//	    does not speak on the plugin's behalf; and the `network` declaration of
//	    connections the plugin opens itself, shown before install.
const SupportedHostAPI = 5
