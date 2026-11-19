package serve

import (
	"context"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/sweeprun"
)

// maxSessionBytes caps the session name in a sweep request.
const maxSessionBytes = 256

// sweepIn names the project group to sweep by its tmux session. It is only
// ever matched against the fleet; it is never passed through as is.
type sweepIn struct {
	Session string `json:"session"`
}

// sweepOut is the sweep report plus the group's project name.
type sweepOut struct {
	Project string `json:"project"`
	sweeprun.Report
}

// sweepWriteDeadline is how long a sweep request may take to answer: a sweep
// runs for up to sweeprun.Timeout, past the server's 30s WriteTimeout.
const sweepWriteDeadline = sweeprun.Timeout + 15*time.Second

// sweepDeadline extends the write deadline for a sweep route, so a slow sweep
// (gh and git lookups per finished window) still delivers its response
// instead of the phone seeing a network error after the apply ran.
func sweepDeadline(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(sweepWriteDeadline)); err != nil {
			logging.Debug("serve: extending sweep write deadline: %v", err)
		}
		h(w, r)
	}
}

// sweepGuard allows one sweep (preview or apply) per session at a time, so a
// double tap cannot start a second subprocess.
type sweepGuard struct {
	mu      sync.Mutex
	running map[string]bool
}

func (g *sweepGuard) acquire(session string) (release func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running[session] {
		return nil, false
	}
	if g.running == nil {
		g.running = make(map[string]bool)
	}
	g.running[session] = true
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		delete(g.running, session)
	}, true
}

// handleSweepPreview runs `agentctl sweep --dry-run` for a fleet group.
func (s *Server) handleSweepPreview(_ *http.Request, in sweepIn, a *auditEntry) outcome {
	return s.runSweep(in, a, true)
}

// handleSweep applies a sweep. Sweep re-classifies every window at apply
// time, so the response reports what actually closed, which may differ from
// the preview.
func (s *Server) handleSweep(_ *http.Request, in sweepIn, a *auditEntry) outcome {
	return s.runSweep(in, a, false)
}

func (s *Server) runSweep(in sweepIn, a *auditEntry, dryRun bool) outcome {
	t, project, out, ok := s.sweepTarget(in.Session, a)
	if !ok {
		return out
	}
	release, ok := s.sweeps.acquire(t.Session)
	if !ok {
		return failure(http.StatusConflict, codeBusy, "a sweep is already running for this project")
	}
	defer release()
	// Not the request context: an apply cut short by a dropped connection
	// would leave the sweep half done. sweeprun bounds the run.
	rep, err := s.sweep(context.Background(), t, dryRun)
	if err != nil {
		logging.Warn("serve: sweep %s (dry run %v): %v", t.Session, dryRun, err)
		return failure(http.StatusBadGateway, codeActionFailed, err.Error())
	}
	if !dryRun {
		s.bus.Refresh()
	}
	return success(sweepOut{Project: project, Report: rep})
}

// sweepTarget resolves a session name from the client to a fleet group's
// sweep target. Only a session the fleet lists, with a runtime dir that
// passes sweeprun's checks, is accepted.
func (s *Server) sweepTarget(session string, a *auditEntry) (sweeprun.Target, string, outcome, bool) {
	if session == "" || len(session) > maxSessionBytes || !utf8.ValidString(session) || hasControl(session) {
		return sweeprun.Target{}, "", failure(http.StatusBadRequest, codeBadRequest, "invalid session"), false
	}
	groups, err := s.fleet.Fleet()
	if err != nil {
		logging.Warn("serve: loading fleet: %v", err)
		return sweeprun.Target{}, "", failure(http.StatusInternalServerError, codeInternal, "loading fleet"), false
	}
	for _, g := range groups {
		if g.Session != session {
			continue
		}
		a.Session, a.Project = g.Session, clip(g.Project)
		t := sweeprun.Target{Session: g.Session, RuntimeDir: g.RuntimeDir, ProjectDir: g.Dir}
		if err := t.Validate(); err != nil {
			return sweeprun.Target{}, "", failure(http.StatusBadRequest, codeUnknownSession, "not an agentctl session"), false
		}
		return t, g.Project, outcome{}, true
	}
	return sweeprun.Target{}, "", failure(http.StatusNotFound, codeUnknownSession, "unknown session"), false
}
