package serve

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/cleanup"
	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
	"github.com/blsmth/agentctl/internal/tmux"
)

const (
	// maxReplyRunes bounds a reply (characters, not bytes).
	maxReplyRunes = 4000
	// maxPromptRunes bounds a dispatch prompt; the body limit caps it too.
	maxPromptRunes = 32000
	// escKey is the answer key that dismisses a dialog.
	escKey = "esc"
)

var (
	// answerKeyRE matches a dialog option key (panestate option lines are
	// numbered).
	answerKeyRE = regexp.MustCompile(`^[0-9]{1,2}$`)
	// dialogHashRE matches panestate.HashDialogText output.
	dialogHashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// modelRE bounds a dispatch model override to model-name characters.
	modelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,99}$`)
)

// registerWrite registers the write API. Every route is registered whether
// or not serve.allow_write is on (rt.Write returns 403 when it is off); the
// "write" feature is advertised only when it is on.
func (s *Server) registerWrite(rt *Router) {
	if rt.allowWrite {
		rt.Feature("write")
	}
	const win = "/v1/sessions/{session}/windows/{window}"
	rt.Write("POST "+win+"/answer", writeRoute(s, "answer", s.handleAnswer))
	rt.Write("POST "+win+"/reply", writeRoute(s, "reply", s.handleReply))
	rt.Write("POST "+win+"/interrupt", writeRoute(s, "interrupt", s.handleInterrupt))
	rt.Write("POST "+win+"/close", writeRoute(s, "close", s.handleClose))
	rt.Write("POST /v1/dispatch", writeRoute(s, "dispatch", s.handleDispatch))
}

// windowLocks serializes write actions per window, so two phones cannot
// interleave a re-check and a send on the same pane.
type windowLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (l *windowLocks) lock(session, windowID string) (unlock func()) {
	key := session + "\x00" + windowID
	l.mu.Lock()
	m, ok := l.m[key]
	if !ok {
		if l.m == nil {
			l.m = make(map[string]*sync.Mutex)
		}
		m = &sync.Mutex{}
		l.m[key] = m
	}
	l.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// resolved is a window found in a fresh fleet load.
type resolved struct {
	group  fleetwatch.Group
	index  int
	window herd.Window
}

func (rw resolved) target() WindowTarget {
	return WindowTarget{Session: rw.group.Session, RuntimeDir: rw.group.RuntimeDir, WindowID: rw.window.WindowID}
}

// resolveWindow finds the request's {session}/{window} in the fleet. Only a
// window the fleet knows can be targeted; its IDs, not the request's, are
// what later reach tmux.
func (s *Server) resolveWindow(r *http.Request) (resolved, outcome, bool) {
	groups, err := s.fleet.Fleet()
	if err != nil {
		logging.Warn("serve: loading fleet: %v", err)
		return resolved{}, failure(http.StatusInternalServerError, codeInternal, "loading fleet"), false
	}
	session, id := r.PathValue("session"), normalizeWindowID(r.PathValue("window"))
	g, i, found := fleetwatch.Find(groups, session, id)
	if !found {
		return resolved{}, failure(http.StatusNotFound, codeNotFound, fmt.Sprintf("no window %s in session %s", id, session)), false
	}
	return resolved{group: g, index: i, window: g.Windows[i]}, outcome{}, true
}

// lockWindow resolves the window, takes its lock, then resolves it again so
// every check after the lock sees state no older than the lock.
func (s *Server) lockWindow(r *http.Request, a *auditEntry) (resolved, func(), outcome, bool) {
	rw, out, ok := s.resolveWindow(r)
	if !ok {
		return resolved{}, nil, out, false
	}
	a.Session, a.Window = rw.group.Session, rw.window.WindowID
	unlock := s.winLocks.lock(rw.group.Session, rw.window.WindowID)
	rw, out, ok = s.resolveWindow(r)
	if !ok {
		unlock()
		return resolved{}, nil, out, false
	}
	return rw, unlock, outcome{}, true
}

// capturePane captures and classifies a window's pane exactly as GET …/pane
// does (buildPaneBody), so a write decides on what the phone was shown.
func (s *Server) capturePane(rw resolved) (paneBody, outcome, bool) {
	text, err := s.panes.Capture(rw.group.Session, rw.window.WindowID, defaultPaneLines)
	if err != nil {
		logging.Warn("serve: capturing pane: %v", err)
		return paneBody{}, failure(http.StatusBadGateway, codePaneUnavailable, "pane could not be captured"), false
	}
	return buildPaneBody(text), outcome{}, true
}

type answerIn struct {
	Key        string `json:"key"`
	DialogHash string `json:"dialog_hash"`
}

// handleAnswer answers a permission dialog. The pane is re-captured and
// re-classified under the window lock immediately before the key is sent;
// unless it still shows the very dialog the phone answered (same hash) and
// the key is one of its options or esc, nothing is sent.
func (s *Server) handleAnswer(r *http.Request, in answerIn, a *auditEntry) outcome {
	if in.Key != escKey && !answerKeyRE.MatchString(in.Key) {
		return failure(http.StatusBadRequest, codeBadRequest, `key must be a dialog option key or "esc"`)
	}
	if !dialogHashRE.MatchString(in.DialogHash) {
		return failure(http.StatusBadRequest, codeBadRequest, "dialog_hash must be a sha256 hex digest")
	}
	rw, unlock, out, ok := s.lockWindow(r, a)
	if !ok {
		return out
	}
	defer unlock()

	pane, out, ok := s.capturePane(rw)
	if !ok {
		return out
	}
	if pane.Verdict != "awaiting_input" || pane.Dialog == nil || pane.Dialog.Hash != in.DialogHash {
		return failure(http.StatusConflict, codeDialogChanged, "the dialog changed; refresh and try again")
	}
	key := in.Key
	if key == escKey {
		key = "Escape"
	} else if !hasOption(pane, key) {
		return failure(http.StatusConflict, codeDialogChanged, fmt.Sprintf("%q is not an option of this dialog", in.Key))
	}
	if err := s.keys.SendKey(rw.target(), key); err != nil {
		logging.Warn("serve: answer %s:%s: %v", rw.group.Session, rw.window.WindowID, err)
		return failure(http.StatusBadGateway, codeActionFailed, "sending the key failed")
	}
	return success(okBody)
}

func hasOption(pane paneBody, key string) bool {
	for _, o := range pane.Dialog.Options {
		if o.Key == key {
			return true
		}
	}
	return false
}

type replyIn struct {
	Text string `json:"text"`
}

// handleReply types a reply into an idle agent and submits it.
func (s *Server) handleReply(r *http.Request, in replyIn, a *auditEntry) outcome {
	if utf8.RuneCountInString(in.Text) > maxReplyRunes {
		return failure(http.StatusRequestEntityTooLarge, codeTooLarge, fmt.Sprintf("reply exceeds %d characters", maxReplyRunes))
	}
	if out, ok := checkText("text", in.Text); !ok {
		return out
	}
	rw, unlock, out, ok := s.lockWindow(r, a)
	if !ok {
		return out
	}
	defer unlock()

	if st := rw.window.State; st == "working" || st == "blocked" {
		return failure(http.StatusConflict, codeNotIdle, "the agent is "+st)
	}
	pane, out, ok := s.capturePane(rw)
	if !ok {
		return out
	}
	if pane.Verdict != "idle" {
		return failure(http.StatusConflict, codeNotIdle, "the agent is not at an idle prompt")
	}
	// The inject path re-checks the pane before every keystroke and refuses
	// a dialog (tmux.ErrDialogBlocking).
	if err := s.injector.Inject(rw.target(), in.Text); err != nil {
		logging.Warn("serve: reply %s:%s: %v", rw.group.Session, rw.window.WindowID, err)
		if errors.Is(err, tmux.ErrDialogBlocking) {
			return failure(http.StatusConflict, codeNotIdle, "a dialog appeared; refresh and try again")
		}
		return failure(http.StatusBadGateway, codeActionFailed, "the reply could not be delivered")
	}
	return success(okBody)
}

type emptyIn struct{}

// handleInterrupt sends Escape, in any state.
func (s *Server) handleInterrupt(r *http.Request, _ emptyIn, a *auditEntry) outcome {
	rw, unlock, out, ok := s.lockWindow(r, a)
	if !ok {
		return out
	}
	defer unlock()
	if err := s.keys.SendKey(rw.target(), "Escape"); err != nil {
		logging.Warn("serve: interrupt %s:%s: %v", rw.group.Session, rw.window.WindowID, err)
		return failure(http.StatusBadGateway, codeActionFailed, "sending Escape failed")
	}
	return success(okBody)
}

// sweepPane maps a pane verdict to cleanup.SweepFacts.Pane.
func sweepPane(verdict string) string {
	switch verdict {
	case "awaiting_input":
		return "awaiting"
	case "working":
		return "busy"
	case "idle":
		return "idle"
	}
	return ""
}

// closable applies sweep's window rules (cleanup.ClassifyWindow) to a close
// the user asked for. Sweep's exclusions all hold: shell, improve~* and
// untracked windows are skipped, working and blocked windows are left alone.
// Unlike an unattended sweep, an idle or done window the user chose to close
// may be closed even when sweep would only list it for attention.
func closable(rw resolved, paneVerdict string) (cleanup.SweepVerdict, bool) {
	meta, tracked := rw.group.Meta[rw.window.WindowID]
	name := rw.window.Agent
	if rw.index < len(rw.group.Rows) {
		name = rw.group.Rows[rw.index].Name
	}
	v := cleanup.ClassifyWindow(cleanup.SweepFacts{
		NonAgent:          herd.IsNonAgentWindow(name) || herd.IsNonAgentWindow(rw.window.Agent),
		Tracked:           tracked,
		IncludeBare:       true,
		State:             rw.window.State,
		Done:              rw.window.Done,
		Mode:              rw.window.Mode,
		PendingTool:       meta.PendingTool,
		PendingToolDetail: meta.PendingToolDetail,
		Pane:              sweepPane(paneVerdict),
	})
	switch v.Kind {
	case cleanup.SweepClose:
		return v, true
	case cleanup.SweepAction:
		return v, v.Reason == "idle" || v.Reason == "done"
	}
	return v, false
}

// handleClose kills a done or idle agent window.
func (s *Server) handleClose(r *http.Request, _ emptyIn, a *auditEntry) outcome {
	rw, unlock, out, ok := s.lockWindow(r, a)
	if !ok {
		return out
	}
	defer unlock()
	if rw.group.Session == tmux.HubSessionName {
		return failure(http.StatusConflict, codeNotClosable, "hub windows cannot be closed")
	}
	pane, out, ok := s.capturePane(rw)
	if !ok {
		return out
	}
	if v, ok := closable(rw, pane.Verdict); !ok {
		return failure(http.StatusConflict, codeNotClosable, "window cannot be closed: "+v.Reason)
	}
	if err := s.closer.CloseWindow(rw.target()); err != nil {
		logging.Warn("serve: close %s:%s: %v", rw.group.Session, rw.window.WindowID, err)
		return failure(http.StatusBadGateway, codeActionFailed, "closing the window failed")
	}
	return success(okBody)
}

type dispatchIn struct {
	Agent      string `json:"agent"`
	ProjectDir string `json:"project_dir"`
	Prompt     string `json:"prompt"`
	Model      string `json:"model,omitempty"`
}

// checkText rejects empty text and control characters other than newline
// and tab: text reaches a terminal, where an escape sequence is a keystroke.
func checkText(field, s string) (outcome, bool) {
	if strings.TrimSpace(s) == "" {
		return failure(http.StatusBadRequest, codeBadRequest, field+" is required"), false
	}
	if !utf8.ValidString(s) {
		return failure(http.StatusBadRequest, codeBadRequest, field+" must be valid UTF-8"), false
	}
	for _, c := range s {
		if c != '\n' && c != '\t' && (unicode.IsControl(c) || c == '\u2028' || c == '\u2029') {
			return failure(http.StatusBadRequest, codeBadRequest, field+" contains control characters"), false
		}
	}
	return outcome{}, true
}

// handleDispatch launches an ad-hoc agent. The phone's confirm sheet stands
// in for the tmux approval popup, and the dispatch hook gates that run inside
// Claude Code do not apply here, so every input is checked strictly: the
// project dir must be exactly one of /v1/projects' dirs and the agent one of
// /v1/agents' for that dir.
func (s *Server) handleDispatch(_ *http.Request, in dispatchIn, a *auditEntry) outcome {
	if in.Agent == "" || !agent.ValidLaunchName(in.Agent) {
		return failure(http.StatusBadRequest, codeUnknownAgent, "invalid agent name")
	}
	if !filepath.IsAbs(in.ProjectDir) {
		return failure(http.StatusBadRequest, codeUnknownProject, "project_dir must be an absolute path from /v1/projects")
	}
	if utf8.RuneCountInString(in.Prompt) > maxPromptRunes {
		return failure(http.StatusRequestEntityTooLarge, codeTooLarge, fmt.Sprintf("prompt exceeds %d characters", maxPromptRunes))
	}
	if out, ok := checkText("prompt", in.Prompt); !ok {
		return out
	}
	if in.Model != "" && !modelRE.MatchString(in.Model) {
		return failure(http.StatusBadRequest, codeBadRequest, "invalid model")
	}

	groups, err := s.fleet.Fleet()
	if err != nil {
		logging.Warn("serve: loading fleet: %v", err)
		return failure(http.StatusInternalServerError, codeInternal, "loading fleet")
	}
	var g fleetwatch.Group
	found := false
	for _, cand := range groups {
		if cand.Dir != "" && cand.Dir == in.ProjectDir {
			g, found = cand, true
			break
		}
	}
	if !found {
		return failure(http.StatusBadRequest, codeUnknownProject, "project_dir is not a known project")
	}
	a.Session = g.Session

	agents, err := s.agents.Agents([]string{g.Dir})
	if err != nil {
		logging.Warn("serve: discovering agents: %v", err)
		return failure(http.StatusInternalServerError, codeInternal, "discovering agents")
	}
	known := false
	for _, ag := range agents {
		if ag.Name == in.Agent {
			known = true
			break
		}
	}
	if !known {
		return failure(http.StatusBadRequest, codeUnknownAgent, fmt.Sprintf("unknown agent %q for this project", in.Agent))
	}

	res, err := s.dispatcher.Dispatch(DispatchRequest{
		Agent: in.Agent, Prompt: in.Prompt, Model: in.Model,
		Dir: g.Dir, Session: g.Session, RuntimeDir: g.RuntimeDir,
	})
	if err != nil {
		logging.Warn("serve: dispatch %s in %s: %v", in.Agent, g.Dir, err)
		return failure(http.StatusBadGateway, codeActionFailed, "dispatch failed; see serve.log")
	}
	a.Window = res.WindowID
	return success(res)
}
