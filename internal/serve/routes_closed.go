package serve

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/tmux"
)

// The phone's "Recently closed" view, the hub's Closed section (C) over
// HTTP: GET /v1/closed lists the closed ledger (internal/closedlog), POST
// /v1/closed/{handle}/resume relaunches one through the same hidden
// `agentctl hub-resume` helper the hub and `agentctl resume` run.

// closedWindow is how far back /v1/closed looks by default, as the hub does.
const closedWindow = 7 * 24 * time.Hour

const (
	// codeNotResumable is a closed record that cannot be resumed (no
	// session ID, transcript or directory gone).
	codeNotResumable = "not_resumable"
	// codeLive is a resume of a session that is open again.
	codeLive = "live"
)

// reasonLive is the resume reason of a session open in a window again.
const reasonLive = "already open"

// closedResumeSpawn is the production resume: the hub-resume subprocess. A
// var so TestMain can make an unstubbed test fail instead of launching.
var closedResumeSpawn = runHubResume

func spawnHubResume(r closedlog.Record) (string, error) { return closedResumeSpawn(r) }

func (s *Server) registerClosed(rt *Router) {
	rt.Feature("closed")
	rt.Device("GET /v1/closed", s.handleClosed)
	if rt.allowWrite {
		rt.Feature("resume")
	}
	rt.Write("POST /v1/closed/{handle}/resume", writeRoute(s, "closed-resume", s.handleClosedResume))
}

// closedItem is one row of GET /v1/closed.
type closedItem struct {
	// Handle addresses the record for resume; "" when no session ID was
	// recorded.
	Handle     string `json:"handle"`
	SessionID  string `json:"session_id,omitempty"`
	Title      string `json:"title"`
	Label      string `json:"label,omitempty"`
	Agent      string `json:"agent,omitempty"`
	Instance   string `json:"instance,omitempty"`
	Project    string `json:"project"`
	ProjectDir string `json:"project_dir,omitempty"`
	Account    string `json:"account,omitempty"`
	ClosedAt   string `json:"closed_at"`
	// CloseReason is closedlog's reason (closed, cascade, improve);
	// ClosedBy is the dispatcher window whose close cascaded to this one.
	CloseReason string `json:"close_reason,omitempty"`
	ClosedBy    string `json:"closed_by,omitempty"`
	FinalState  string `json:"final_state"`
	PRNumber    int    `json:"pr_number,omitempty"`
	PRURL       string `json:"pr_url,omitempty"`
	// Resumable is false when the record cannot be resumed; Reason says why.
	Resumable bool   `json:"resumable"`
	Reason    string `json:"reason,omitempty"`
	// Live is true when the session is open in a window again; Session and
	// WindowID name it.
	Live     bool   `json:"live,omitempty"`
	Session  string `json:"session,omitempty"`
	WindowID string `json:"window_id,omitempty"`
}

// closedBody is the answer of GET /v1/closed. Error is set, with an empty
// list, when the ledger could not be read.
type closedBody struct {
	Since  string       `json:"since"`
	Closed []closedItem `json:"closed"`
	Error  string       `json:"error,omitempty"`
}

// parseClosedSince reads ?since=: an RFC 3339 time or a Go duration back
// from now ("24h"); empty is closedWindow. It never reaches past the
// ledger's retention.
func parseClosedSince(v string, now time.Time) (time.Time, error) {
	since := now.Add(-closedWindow)
	if v = strings.TrimSpace(v); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t
		} else if d, err := time.ParseDuration(v); err == nil && d > 0 {
			since = now.Add(-d)
		} else {
			return time.Time{}, errors.New("since must be an RFC 3339 time or a duration such as 24h")
		}
	}
	if floor := now.Add(-closedlog.MaxAge); since.Before(floor) {
		since = floor
	}
	return since, nil
}

// liveSessions maps the session IDs of the fleet's agent windows to their
// window. A fleet that cannot be loaded has none.
func (s *Server) liveSessions() (map[string][2]string, error) {
	groups, err := s.fleet.Fleet()
	if err != nil {
		return nil, err
	}
	out := map[string][2]string{}
	for _, g := range groups {
		for i, row := range g.Rows {
			if row.SessionID != "" && i < len(g.Windows) {
				out[row.SessionID] = [2]string{g.Session, g.Windows[i].WindowID}
			}
		}
	}
	return out, nil
}

func (s *Server) handleClosed(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	since, err := parseClosedSince(r.URL.Query().Get("since"), now)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	body := closedBody{Since: since.UTC().Format(time.RFC3339), Closed: []closedItem{}}
	recs, err := s.closedRecs(since)
	if err != nil {
		logging.Warn("serve: reading the closed ledger: %v", err)
		body.Error = "the closed ledger could not be read"
		writeJSON(w, http.StatusOK, body)
		return
	}
	live, err := s.liveSessions()
	if err != nil {
		logging.Warn("serve: loading fleet for /v1/closed: %v", err)
	}
	ix := s.handles()
	for _, rec := range closedlog.LatestPerSession(recs) {
		body.Closed = append(body.Closed, closedItemFor(rec, ix, live))
	}
	writeJSON(w, http.StatusOK, body)
}

// closedItemFor renders one record; live are the open sessions.
func closedItemFor(rec closedlog.Record, ix *fleet.Index, live map[string][2]string) closedItem {
	it := closedItem{
		Handle:      ix.HandleFor(rec.SessionID),
		SessionID:   rec.SessionID,
		Title:       rec.Title(),
		Label:       fleet.Label(fleet.RecordRow(rec), fleet.Scope{}),
		Agent:       rec.Agent,
		Instance:    rec.Instance,
		Project:     rec.Project(),
		ProjectDir:  rec.ProjectDir,
		Account:     rec.Account,
		ClosedAt:    rec.ClosedAt.UTC().Format(time.RFC3339),
		CloseReason: rec.Reason,
		ClosedBy:    rec.ClosedBy,
		FinalState:  rec.FinalState(),
		PRNumber:    rec.PRNumber,
		PRURL:       safePRURL(rec.PRURL),
	}
	if win, ok := live[rec.SessionID]; ok && rec.SessionID != "" {
		it.Live, it.Session, it.WindowID, it.Reason = true, win[0], win[1], reasonLive
		return it
	}
	if err := rec.ResumeCheck(os.Stat); err != nil {
		it.Reason = err.Error()
		return it
	}
	it.Resumable = true
	return it
}

// safePRURL keeps a PR URL only when it is an https link, so the app never
// renders a javascript: or other scheme from the ledger as a link.
func safePRURL(u string) string {
	if strings.HasPrefix(u, "https://") {
		return u
	}
	return ""
}

// resumeIn is the (empty) body of POST /v1/closed/{handle}/resume: the
// record is resolved from the handle alone, never from the request.
type resumeIn struct{}

// ResumeResult is the answer of a resume: the new window, when the helper
// reported it, so the app can open it.
type ResumeResult struct {
	Session  string `json:"session"`
	WindowID string `json:"window_id,omitempty"`
	Handle   string `json:"handle"`
}

func (s *Server) handleClosedResume(r *http.Request, _ resumeIn, a *auditEntry) outcome {
	ref := strings.ToLower(strings.TrimSpace(r.PathValue("handle")))
	if !handleRef.MatchString(ref) {
		return failure(http.StatusNotFound, codeNotFound, "unknown session")
	}
	sess, err := s.handles().Resolve(ref)
	var amb *fleet.AmbiguousError
	switch {
	case errors.As(err, &amb):
		return failure(http.StatusConflict, codeAmbiguous, amb.Error())
	case err != nil:
		return failure(http.StatusNotFound, codeNotFound, "unknown session")
	}
	rec, ok := s.closedRecord(sess.SessionID)
	if !ok {
		if sess.Live {
			return failure(http.StatusConflict, codeLive, "this session is open in a window; open it instead")
		}
		return failure(http.StatusNotFound, codeNotFound, "no closed session "+sess.Handle)
	}
	a.Agent, a.Project = rec.Agent, rec.Project()
	live, err := s.liveSessions()
	if err != nil {
		logging.Warn("serve: loading fleet: %v", err)
		return failure(http.StatusInternalServerError, codeInternal, "loading fleet")
	}
	if _, open := live[rec.SessionID]; open || sess.Live {
		return failure(http.StatusConflict, codeLive, "this session is open in a window again; open it instead")
	}
	if err := rec.ResumeCheck(os.Stat); err != nil {
		return failure(http.StatusConflict, codeNotResumable, "cannot resume: "+err.Error())
	}
	release, ok := s.resumes.acquire(rec.SessionID)
	if !ok {
		return failure(http.StatusConflict, codeBusy, "this session is already being resumed")
	}
	defer release()
	session := rec.Session
	if session == "" {
		session = tmux.SessionNameForProjectRoot(rec.ProjectDir)
	}
	a.Session = session
	s.auditAttempt(*a)
	stdout, err := s.resumeClosed(rec)
	if err != nil {
		logging.Warn("serve: resuming %s: %v", sess.Handle, err)
		return failure(http.StatusBadGateway, codeActionFailed, "resume failed: "+err.Error())
	}
	a.Window = lastWindowID(stdout)
	s.bus.Refresh()
	return success(ResumeResult{Session: session, WindowID: a.Window, Handle: sess.Handle})
}

// closedRecord is the newest ledger record of sessionID.
func (s *Server) closedRecord(sessionID string) (closedlog.Record, bool) {
	if sessionID == "" {
		return closedlog.Record{}, false
	}
	recs, err := s.closedRecs(s.now().Add(-closedlog.MaxAge))
	if err != nil {
		logging.Warn("serve: reading the closed ledger: %v", err)
		return closedlog.Record{}, false
	}
	for _, r := range recs { // newest first
		if r.SessionID == sessionID {
			return r, true
		}
	}
	return closedlog.Record{}, false
}

// lastWindowID is the window ID hub-resume prints as its last stdout line
// ("@12"), or "".
func lastWindowID(stdout string) string {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if len(last) < 2 || last[0] != '@' {
		return ""
	}
	for _, c := range last[1:] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return last
}

// runHubResume runs `agentctl hub-resume` for r with cwd = the project and
// the environment pinned to it (closedlog.ResumeEnv), as the hub does, and
// returns its stdout.
func runHubResume(r closedlog.Record) (string, error) {
	exe, err := selfpath.Spawnable()
	if err != nil {
		return "", fmt.Errorf("locating agentctl: %w", err)
	}
	c := exec.Command(exe, closedlog.ResumeArgs(r)...)
	c.Dir = r.ProjectDir
	c.Env = closedlog.ResumeEnv(r.ProjectDir, os.Environ())
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		if msg = strings.TrimPrefix(msg, "Error: "); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return stdout.String(), nil
}
