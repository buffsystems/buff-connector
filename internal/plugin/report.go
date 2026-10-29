package plugin

import "github.com/buffsystems/buff-connector/internal/problem"

// InstanceReport is what an operator, the local UI and the platform report all
// read. It never carries a secret.
type InstanceReport struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	Alias      string `json:"alias,omitempty"`
	Plugin     string `json:"plugin"`
	Version    string `json:"version"`
	DevProject string `json:"dev_project,omitempty"`
	Status     string `json:"status"`
	// LastError is the problem as the connector's own Russian text. Deprecated:
	// kept for the cabinets that predate Problem, which read it as it is.
	LastError string `json:"last_error,omitempty"`
	// Problem is what went wrong as a status — a code, its values, its cause —
	// for the reader's own client to write in the reader's language (#718).
	Problem  *problem.Problem `json:"problem,omitempty"`
	Restarts int              `json:"restarts"`
	Egress   []string         `json:"egress,omitempty"`
	Tools    []string         `json:"tools,omitempty"`
	ReadOnly []string         `json:"read_only_tools,omitempty"`
	// Tools that answer with a picture. The platform withholds them from a
	// text-only model, so it has to know which they are without asking the plugin.
	Vision    []string `json:"vision_tools,omitempty"`
	Withdrawn string   `json:"withdrawn,omitempty"`
	// The newer version the catalogue offers for this plugin, when there is one.
	// A connection is pinned on purpose, so this is a MESSAGE, never an act: the
	// operator installs it when they choose (#302).
	Latest string `json:"latest,omitempty"`
	// What the operator wrote FOR THE MODEL about this connection, and whether
	// they lifted the length bound to write it. Travels with the tools so the
	// platform's relay hands it over at the same handshake, and so the
	// platform can show the same warning the local page shows.
	AINotes          string `json:"ai_notes,omitempty"`
	AINotesUnbounded bool   `json:"ai_notes_unbounded,omitempty"`

	// Custom marks a server the operator brought (#351): nobody signed it and
	// its network use is not guarded. Every surface that shows the connection
	// says so, which is why it travels in the report rather than being inferred.
	// Source is where it came from (archive, github, url, command).
	Custom bool   `json:"custom,omitempty"`
	Source string `json:"source,omitempty"`
	// ReadOnlyHint is what the server CLAIMS only reads. For a custom server the
	// claim is shown to the operator, who decides; ReadOnly above is the decision.
	ReadOnlyHint []string `json:"read_only_hint,omitempty"`
}
