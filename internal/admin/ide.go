package admin

import (
	"net/http"
	"time"
)

// DeveloperCalls is one developer's use of the company's tools from Buff Code (#368).
type DeveloperCalls struct {
	Developer string    `json:"developer"`
	Calls     int       `json:"calls"`
	Errors    int       `json:"errors"`
	Last      time.Time `json:"last"`
}

// IDEJournal is what the running agent knows about those calls (agent.MCPSurface).
type IDEJournal interface {
	DeveloperCalls() []DeveloperCalls
}

// WithIDE wires the «Buff Code» section of the page.
func (s *Server) WithIDE(j IDEJournal) *Server {
	s.ide = j
	return s
}

func (s *Server) ideState(w http.ResponseWriter, _ *http.Request) {
	rows := []DeveloperCalls{}
	if s.ide != nil {
		rows = append(rows, s.ide.DeveloperCalls()...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"developers": rows})
}
