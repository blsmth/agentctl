package serve

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/cleanup"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/launchopts"
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
	// answeredTTL is how long an answered dialog's hash is remembered per
	// window. Claude Code redraws some time after the key lands; until then
	// a second answer (a double tap, a second phone) would re-capture the
	// same dialog and its key would land in the input box as stray text.
	answeredTTL = 5 * time.Second
	// answerSettleTimeout caps how long an answer waits, under the window
	// lock, for the answered dialog to leave the pane; answerSettlePoll is
	// the re-capture interval.
	answerSettleTimeout = time.Second
	answerSettlePoll    = 100 * time.Millisecond
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
	if rt.allowWrite {
		rt.Feature("queue")
	}
	rt.Write("POST "+win+"/queue", writeRoute(s, "queue", s.handleQueue))
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
	window fleet.Window
	// pane is the window's main pane, set by lockWindow.
	pane string
}

// harness is the window's recorded harness name ("" = claude).
func (rw resolved) harness() string {
	if rw.index < len(rw.group.Rows) {
		return rw.group.Rows[rw.index].Harness
	}
	return ""
}

func (rw resolved) target() WindowTarget {
	return WindowTarget{Session: rw.group.Session, RuntimeDir: rw.group.RuntimeDir, WindowID: rw.window.WindowID, Pane: rw.pane}
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
// every check after the lock sees state no older than the lock. It also
// resolves the window's main pane: every check and keystroke after it
// targets that one pane.
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
	meta := rw.group.Meta[rw.window.WindowID]
	pane, err := s.mainPanes.MainPane(rw.group.Session, rw.window.WindowID, meta.ChromeBarPane, meta.HudPane)
	if err == nil && !tmux.IsPaneID(pane) {
		err = fmt.Errorf("not a pane id: %q", pane)
	}
	if err != nil {
		unlock()
		logging.Warn("serve: finding main pane of %s:%s: %v", rw.group.Session, rw.window.WindowID, err)
		return resolved{}, nil, failure(http.StatusBadGateway, codePaneUnavailable, "the agent pane could not be found"), false
	}
	rw.pane = pane
	return rw, unlock, outcome{}, true
}

// capturePane captures the window's main pane (rw.pane, the pane the write
// types into) and classifies it as GET …/pane does (buildPaneBody), so a
// write decides on what the phone was shown.
func (s *Server) capturePane(rw resolved) (paneBody, outcome, bool) {
	text, err := s.mainPanes.CapturePane(rw.pane, defaultPaneLines)
	if err != nil {
		logging.Warn("serve: capturing pane: %v", err)
		return paneBody{}, failure(http.StatusBadGateway, codePaneUnavailable, "pane could not be captured"), false
	}
	return buildPaneBodyFor(rw.harness(), text), outcome{}, true
}

type answerIn struct {
	Key        string `json:"key"`
	DialogHash string `json:"dialog_hash"`
}

// handleAnswer answers a permission dialog. The pane is re-captured and
// re-classified under the window lock immediately before the key is sent;
// unless it still shows the very dialog the phone answered (same hash) and
// the key is one of its options or esc, nothing is sent.
//
// The lock alone does not stop a double answer: send-keys returns before
// Claude Code redraws, so a second request can re-capture the dialog that
// was just answered and its key would land in the input box. So an answered
// hash is remembered per window for answeredTTL and a repeat is refused, and
// the answer waits (up to answerSettle) for the dialog to leave the pane
// before releasing the lock.
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
	winKey := rw.group.Session + "\x00" + rw.window.WindowID
	if s.answered.recent(winKey, in.DialogHash, s.now()) {
		return failure(http.StatusConflict, codeAlreadyAnswered, "this dialog was already answered; refresh and try again")
	}
	key := in.Key
	if key == escKey {
		key = "Escape"
	} else if !hasOption(pane, key) {
		return failure(http.StatusConflict, codeDialogChanged, fmt.Sprintf("%q is not an option of this dialog", in.Key))
	}
	s.auditAttempt(*a)
	if err := s.keys.SendKey(rw.target(), key); err != nil {
		logging.Warn("serve: answer %s:%s: %v", rw.group.Session, rw.window.WindowID, err)
		return failure(http.StatusBadGateway, codeActionFailed, "sending the key failed")
	}
	s.answered.remember(winKey, in.DialogHash, s.now())
	s.awaitDialogGone(rw, in.DialogHash)
	return success(okBody)
}

// awaitDialogGone re-captures the pane until it no longer shows the dialog
// with hash, for at most s.answerSettle. It only narrows the window in which
// a later request could see the answered dialog; answeredDialogs is the
// guard.
func (s *Server) awaitDialogGone(rw resolved, hash string) {
	deadline := time.Now().Add(s.answerSettle)
	for time.Now().Before(deadline) {
		text, err := s.mainPanes.CapturePane(rw.pane, defaultPaneLines)
		if err != nil {
			return
		}
		if p := buildPaneBodyFor(rw.harness(), text); p.Dialog == nil || p.Dialog.Hash != hash {
			return
		}
		time.Sleep(answerSettlePoll)
	}
}

// answeredDialogs remembers the last answered dialog hash per window.
type answeredDialogs struct {
	mu sync.Mutex
	m  map[string]answeredDialog
}

type answeredDialog struct {
	hash string
	at   time.Time
}

// recent reports whether window key answered the dialog hash within
// answeredTTL of now.
func (d *answeredDialogs) recent(key, hash string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.m[key]
	return ok && e.hash == hash && now.Sub(e.at) < answeredTTL
}

// remember records that window key answered hash at now, dropping expired
// entries so the map stays bounded by the windows answered recently.
func (d *answeredDialogs) remember(key, hash string, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.m == nil {
		d.m = make(map[string]answeredDialog)
	}
	for k, e := range d.m {
		if now.Sub(e.at) >= answeredTTL {
			delete(d.m, k)
		}
	}
	d.m[key] = answeredDialog{hash: hash, at: now}
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
	return s.typeText(r, in, a, false)
}

// handleQueue types a message into a working agent and submits it, as a
// person typing mid-turn would: Claude Code queues the input and delivers it
// after the current step. It is a separate, explicitly requested action (and
// audit kind) from reply. It also accepts an idle agent, since the turn may
// end between the phone's tap and the send, and the message then simply
// lands as a reply. A blocked record, a dialog or an unrecognised pane is
// refused exactly as for reply.
func (s *Server) handleQueue(r *http.Request, in replyIn, a *auditEntry) outcome {
	return s.typeText(r, in, a, true)
}

// typeText is reply and queue: the shared text checks, the state and pane
// guards under the window lock, then the guarded inject. allowWorking admits
// a working record and a busy pane (queue).
func (s *Server) typeText(r *http.Request, in replyIn, a *auditEntry, allowWorking bool) outcome {
	if utf8.RuneCountInString(in.Text) > maxReplyRunes {
		return failure(http.StatusRequestEntityTooLarge, codeTooLarge, fmt.Sprintf("reply exceeds %d characters", maxReplyRunes))
	}
	if out, ok := checkText("text", in.Text); !ok {
		return out
	}
	if out, ok := checkReplyMode(in.Text); !ok {
		return out
	}
	rw, unlock, out, ok := s.lockWindow(r, a)
	if !ok {
		return out
	}
	defer unlock()

	st := rw.window.State
	if st == "blocked" || (st == "working" && !allowWorking) {
		return failure(http.StatusConflict, codeNotIdle, "the agent is "+st)
	}
	target := rw.target()
	if harness.For(rw.harness()).Caps().PaneClassifier {
		pane, out, ok := s.capturePane(rw)
		if !ok {
			return out
		}
		if !textAccepted(pane.Verdict, allowWorking) {
			if allowWorking {
				return failure(http.StatusConflict, codeNotIdle, "the agent is not at a prompt that accepts input")
			}
			return failure(http.StatusConflict, codeNotIdle, "the agent is not at an idle prompt")
		}
	} else {
		// The pane cannot be read for this harness: the recorded state
		// (checked above) is all there is. An empty state is a fresh agent
		// whose bridge has not written idle yet.
		if !recordedAccepts(st, allowWorking) {
			return failure(http.StatusConflict, codeNotIdle, "the agent is not at an idle prompt")
		}
		target.Unclassified = true
	}
	s.auditAttempt(*a)
	// The inject path re-checks the pane before every keystroke and refuses
	// a dialog (tmux.ErrDialogBlocking).
	if err := s.inject(target, in.Text, allowWorking); err != nil {
		logging.Warn("serve: %s %s:%s: %v", a.Action, rw.group.Session, rw.window.WindowID, err)
		if errors.Is(err, tmux.ErrDialogBlocking) {
			return failure(http.StatusConflict, codeNotIdle, "a dialog appeared; refresh and try again")
		}
		return failure(http.StatusBadGateway, codeActionFailed, "the message could not be delivered")
	}
	return success(okBody)
}

// textAccepted reports whether a pane verdict admits typed text: idle for a
// reply, idle or working for a queued message. A dialog or an unknown pane
// never does.
func textAccepted(verdict string, allowWorking bool) bool {
	return verdict == "idle" || (allowWorking && verdict == "working")
}

// recordedAccepts is textAccepted for a harness whose pane cannot be read:
// the windowstate record decides. "" counts as idle.
func recordedAccepts(state string, allowWorking bool) bool {
	return state == "idle" || state == "" || (allowWorking && state == "working")
}

// inject delivers text through the Injector, or its Queue method for a
// queued message when the Injector distinguishes the two.
func (s *Server) inject(t WindowTarget, text string, queued bool) error {
	if q, ok := s.injector.(Queuer); ok && queued {
		return q.Queue(t, text)
	}
	return s.injector.Inject(t, text)
}

type emptyIn struct{}

// handleInterrupt sends Escape, in any state.
func (s *Server) handleInterrupt(r *http.Request, _ emptyIn, a *auditEntry) outcome {
	rw, unlock, out, ok := s.lockWindow(r, a)
	if !ok {
		return out
	}
	defer unlock()
	s.auditAttempt(*a)
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
		NonAgent:          fleet.IsNonAgentWindow(name) || fleet.IsNonAgentWindow(rw.window.Agent),
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
	s.auditAttempt(*a)
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
	Backend    string `json:"backend,omitempty"`
	Effort     string `json:"effort,omitempty"`
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

// replyModePrefixes are the runes that, first in Claude Code's input box,
// leave plain prompting: "!" runs the rest as a shell command (bash mode, no
// permission dialog), "/" runs a slash command (/permissions, /hooks,
// /add-dir change what the agent may do) and "#" writes to memory. A reply
// is text for the agent, so it must not start with any of them.
const replyModePrefixes = "!/#"

// checkReplyMode rejects a reply whose first non-space rune would switch
// Claude Code out of prompt mode (see replyModePrefixes).
func checkReplyMode(text string) (outcome, bool) {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	if r, _ := utf8.DecodeRuneInString(trimmed); strings.ContainsRune(replyModePrefixes, r) {
		return failure(http.StatusBadRequest, codeReplyIsCommand,
			fmt.Sprintf("a reply cannot start with %q: Claude Code would run it as a command, not send it to the agent", r)), false
	}
	return outcome{}, true
}

// handleDispatch launches an ad-hoc agent. The phone's confirm sheet stands
// in for the tmux approval popup, and the dispatch hook gates that run inside
// Claude Code do not apply here, so every input is checked strictly: the
// project dir must be exactly one of /v1/projects' dirs and the agent one of
// /v1/agents' for that dir.
func (s *Server) handleDispatch(_ *http.Request, in dispatchIn, a *auditEntry) outcome {
	if utf8.RuneCountInString(in.Prompt) > maxPromptRunes {
		return failure(http.StatusRequestEntityTooLarge, codeTooLarge, fmt.Sprintf("prompt exceeds %d characters", maxPromptRunes))
	}
	if out, ok := checkText("prompt", in.Prompt); !ok {
		return out
	}
	if in.Model != "" && !modelRE.MatchString(in.Model) {
		return failure(http.StatusBadRequest, codeBadRequest, "invalid model")
	}
	if in.Effort != "" {
		if err := launchopts.ValidateEffort(in.Effort); err != nil {
			return failure(http.StatusBadRequest, codeBadRequest, err.Error())
		}
	}
	g, out, ok := s.dispatchTarget(in.Agent, in.ProjectDir, a)
	if !ok {
		return out
	}
	backend, model, effort := in.Backend, in.Model, in.Effort
	if in.Backend != "" || in.Model != "" || in.Effort != "" {
		c, err := s.launchChoices(g.Session, g.RuntimeDir, in.Agent)
		if err != nil {
			logging.Warn("serve: dispatch launch options: %v", err)
			return failure(http.StatusInternalServerError, codeInternal, "reading launch options")
		}
		backend, model, effort, out, ok = resolveLaunchChoice(c, in)
		if !ok {
			return out
		}
	}
	a.Backend, a.Model, a.Effort = clip(in.Backend), clip(in.Model), clip(in.Effort)
	s.auditAttempt(*a)

	res, err := s.dispatcher.Dispatch(DispatchRequest{
		Agent: in.Agent, Prompt: in.Prompt, Model: model, Backend: backend, Effort: effort,
		Dir: g.Dir, Session: g.Session, RuntimeDir: g.RuntimeDir,
	})
	if err != nil {
		logging.Warn("serve: dispatch %s in %s: %v", in.Agent, g.Dir, err)
		return failure(http.StatusBadGateway, codeActionFailed, "dispatch failed; see serve.log")
	}
	a.Window = res.WindowID
	return success(res)
}

// resolveLaunchChoice checks the requested backend, model and effort against
// what the agent's launch options offer and maps them to the launch overrides
// exactly as the tmux popup does. An empty backend selects the agent's
// default; "none" is accepted as an alias of "anthropic".
func resolveLaunchChoice(c launchChoices, in dispatchIn) (backend, model, effort string, out outcome, ok bool) {
	bad := func(msg string) (string, string, string, outcome, bool) {
		return "", "", "", failure(http.StatusBadRequest, codeBadRequest, msg), false
	}
	names, def := c.in.BackendNames()
	sel := in.Backend
	switch {
	case sel == "":
		sel = def
	case sel == launchopts.BackendNone:
		sel = launchopts.BackendAnthropic
	case !slices.Contains(names, sel):
		return bad("unknown backend")
	}
	if in.Model != "" && in.Model != launchopts.Inherit {
		offered := slices.ContainsFunc(c.in.ModelChoices(sel), func(m launchopts.ModelChoice) bool {
			if c.in.IsNous(sel) || sel == launchopts.BackendAnthropic {
				return m.Value == in.Model
			}
			return launchopts.SameModel(m.Value, in.Model)
		})
		// An Ollama server that could not be asked cannot say what it has: the
		// charset check done earlier is all that applies.
		_, listFailed := c.listErrors[sel]
		if !offered && !listFailed {
			return bad("model is not offered for this backend")
		}
	}
	if in.Model == launchopts.Inherit && sel != launchopts.BackendAnthropic {
		return bad("model is not offered for this backend")
	}
	model = in.Model
	if model == "" {
		model = c.in.DefaultModel(sel)
	}
	return c.in.ResultBackend(sel), c.in.ResultModel(sel, model), c.in.ResultEffort(effortOrInherit(in.Effort, c)), outcome{}, true
}

func effortOrInherit(e string, c launchChoices) string {
	if e == "" {
		return c.in.DefaultEffort()
	}
	return e
}

// dispatchTarget validates an agent name and project dir as a dispatch (and
// its launch options) require: the dir must be exactly a fleet project's dir
// and the agent one of the agents discoverable there. a, when non-nil,
// receives the audit fields.
func (s *Server) dispatchTarget(agentName, projectDir string, a *auditEntry) (fleetwatch.Group, outcome, bool) {
	if agentName == "" || !agent.ValidLaunchName(agentName) {
		return fleetwatch.Group{}, failure(http.StatusBadRequest, codeUnknownAgent, "invalid agent name"), false
	}
	if !filepath.IsAbs(projectDir) {
		return fleetwatch.Group{}, failure(http.StatusBadRequest, codeUnknownProject, "project_dir must be an absolute path from /v1/projects"), false
	}
	groups, err := s.fleet.Fleet()
	if err != nil {
		logging.Warn("serve: loading fleet: %v", err)
		return fleetwatch.Group{}, failure(http.StatusInternalServerError, codeInternal, "loading fleet"), false
	}
	var g fleetwatch.Group
	found := false
	for _, cand := range groups {
		if cand.Dir != "" && cand.Dir == projectDir {
			g, found = cand, true
			break
		}
	}
	if !found {
		return fleetwatch.Group{}, failure(http.StatusBadRequest, codeUnknownProject, "project_dir is not a known project"), false
	}
	if a != nil {
		a.Session = g.Session
		a.Project = clip(g.Project)
	}
	agents, err := s.agents.Agents([]string{g.Dir})
	if err != nil {
		logging.Warn("serve: discovering agents: %v", err)
		return fleetwatch.Group{}, failure(http.StatusInternalServerError, codeInternal, "discovering agents"), false
	}
	if !slices.ContainsFunc(agents, func(ag AgentInfo) bool { return ag.Name == agentName }) {
		return fleetwatch.Group{}, failure(http.StatusBadRequest, codeUnknownAgent, fmt.Sprintf("unknown agent %q for this project", agentName)), false
	}
	if a != nil {
		a.Agent = agentName
	}
	return g, outcome{}, true
}
