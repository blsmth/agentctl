package serve

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/fleet"
)

func (s *Server) registerHandles(rt *Router) {
	rt.Feature("fleet")
	rt.Device("GET /v1/handles/{handle}", s.handleHandle)
}

// handleBody is the answer of GET /v1/handles/{handle}: what a handle (or a
// full session ID) names. Live sessions carry the window to open; closed ones
// say so, with the time they closed.
type handleBody struct {
	Handle    string `json:"handle"`
	SessionID string `json:"session_id"`
	Label     string `json:"label,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Project   string `json:"project,omitempty"`
	Live      bool   `json:"live"`
	Session   string `json:"session,omitempty"`
	WindowID  string `json:"window_id,omitempty"`
	// Closed is true for a session in the closed ledger.
	Closed   bool   `json:"closed"`
	ClosedAt string `json:"closed_at,omitempty"`
}

type handleCandidate struct {
	Handle string `json:"handle"`
	Label  string `json:"label,omitempty"`
}

type ambiguousBody struct {
	Error      string            `json:"error"`
	Code       string            `json:"code"`
	Candidates []handleCandidate `json:"candidates"`
}

const codeAmbiguous = "ambiguous"

// handleRef is what a handle or full session ID looks like: a hex prefix or a
// UUID-style ID, bounded, so nothing else is scanned against the index.
var handleRef = regexp.MustCompile(`^[0-9a-f][0-9a-f-]{3,63}$`)

func (s *Server) handleHandle(w http.ResponseWriter, r *http.Request) {
	ref := strings.ToLower(strings.TrimSpace(r.PathValue("handle")))
	if !handleRef.MatchString(ref) {
		writeError(w, http.StatusNotFound, codeNotFound, "unknown session")
		return
	}
	sess, err := s.handles().Resolve(ref)
	var amb *fleet.AmbiguousError
	switch {
	case errors.As(err, &amb):
		body := ambiguousBody{Error: amb.Error(), Code: codeAmbiguous, Candidates: []handleCandidate{}}
		for _, c := range amb.Candidates {
			body.Candidates = append(body.Candidates, handleCandidate{Handle: c.Handle, Label: c.Label})
		}
		writeJSON(w, http.StatusConflict, body)
		return
	case err != nil:
		writeError(w, http.StatusNotFound, codeNotFound, "unknown session")
		return
	}
	body := handleBody{
		Handle:    sess.Handle,
		SessionID: sess.SessionID,
		Label:     sess.Label,
		Agent:     sess.Row.AgentName,
		Project:   sess.Row.Project,
	}
	// A window record can outlive its window; the live fleet decides.
	if groups, err := s.fleet.Fleet(); err == nil {
		for _, g := range groups {
			for i, row := range g.Rows {
				if row.SessionID == sess.SessionID && i < len(g.Windows) {
					body.Live, body.Session, body.WindowID = true, g.Session, g.Windows[i].WindowID
					body.Project = g.Project
				}
			}
		}
	}
	if !body.Live && sess.Closed != nil {
		body.Closed = true
		if !sess.Closed.ClosedAt.IsZero() {
			body.ClosedAt = sess.Closed.ClosedAt.UTC().Format(time.RFC3339)
		}
	}
	writeJSON(w, http.StatusOK, body)
}
