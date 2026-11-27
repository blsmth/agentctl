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
	"github.com/blsmth/agentctl/internal/panestate"
	"github.com/blsmth/agentctl/internal/prompttemplate"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/uploads"
)

const (
	// maxReplyRunes bounds a reply (characters, not bytes).
	maxReplyRunes = 4000
	// maxPromptRunes bounds a dispatch prompt; the body limit caps it too.
	maxPromptRunes = 32000
	// maxInstructionsRunes bounds a dispatch's additional instructions.
	maxInstructionsRunes = 4000
	// escKey is the answer key that dismisses a dialog.
	escKey = "esc"
	// submitKey is the answer key that confirms a multi-select question: it
	// ticks the boxes named in answerIn.Select, then moves to and presses its
	// Submit/Next row.
	submitKey = "submit"
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
	if rt.allowWrite {
		rt.Feature("command")
	}
	rt.Write("POST "+win+"/command", writeRoute(s, "command", s.handleCommand))
	if rt.allowWrite {
		rt.Feature("upload")
	}
	rt.Write("POST "+win+"/upload", s.handleUpload)
	rt.Write("POST "+win+"/interrupt", writeRoute(s, "interrupt", s.handleInterrupt))
	rt.Write("POST "+win+"/close", writeRoute(s, "close", s.handleClose))
	rt.Write("POST /v1/sessions/{session}/shells/{window}/close", writeRoute(s, "close-shell", s.handleCloseShell))
	rt.Write("POST /v1/group-order", writeRoute(s, "group-order", s.handleGroupOrder))
	if rt.allowWrite {
		rt.Feature("dispatch-upload")
		rt.Feature("dispatch-instructions")
		rt.Feature("dispatch-skip-review")
		rt.Feature("dispatch-templates")
	}
	rt.Write("POST /v1/dispatch/upload", s.handleDispatchUpload)
	rt.Write("POST /v1/dispatch", writeRoute(s, "dispatch", s.handleDispatch))
	if rt.allowWrite {
		rt.Feature("shell")
	}
	rt.Write("POST /v1/shell", writeRoute(s, "shell", s.handleShell))
	// The preview only reads, but it runs a subprocess that shells out to
	// git and gh for every finished window, and is only useful with apply.
	rt.Write("POST /v1/sweep/preview", sweepDeadline(writeRoute(s, "sweep-preview", s.handleSweepPreview)))
	rt.Write("POST /v1/sweep", sweepDeadline(writeRoute(s, "sweep", s.handleSweep)))
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
	return s.resolveWindowAt(r.PathValue("session"), normalizeWindowID(r.PathValue("window")))
}

// resolveWindowAt is resolveWindow for an explicit session and window ID.
func (s *Server) resolveWindowAt(session, id string) (resolved, outcome, bool) {
	groups, err := s.fleet.Fleet()
	if err != nil {
		logging.Warn("serve: loading fleet: %v", err)
		return resolved{}, failure(http.StatusInternalServerError, codeInternal, "loading fleet"), false
	}
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
	return s.lockWindowAt(r.PathValue("session"), normalizeWindowID(r.PathValue("window")), a)
}

// lockWindowAt is lockWindow for an explicit session and window ID.
func (s *Server) lockWindowAt(session, window string, a *auditEntry) (resolved, func(), outcome, bool) {
	rw, out, ok := s.resolveWindowAt(session, window)
	if !ok {
		return resolved{}, nil, out, false
	}
	a.Session, a.Window = rw.group.Session, rw.window.WindowID
	unlock := s.winLocks.lock(rw.group.Session, rw.window.WindowID)
	rw, out, ok = s.resolveWindowAt(session, window)
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
	// Select is the full set of option keys that should end up ticked when
	// Key is "submit" on a multi-select question.
	Select []string `json:"select,omitempty"`
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
	if in.Key != escKey && in.Key != submitKey && !isTrustKey(in.Key) && !answerKeyRE.MatchString(in.Key) {
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
	keys, out, ok := answerKeys(pane.Dialog, in)
	if !ok {
		return out
	}
	s.auditAttempt(*a)
	if pane.Dialog.Kind == panestate.KindTrust {
		if out, ok := s.sendTrust(rw, *pane.Dialog, in.Key); !ok {
			return out
		}
	} else if in.Key == submitKey {
		if out, ok := s.sendChecked(rw, *pane.Dialog, keys); !ok {
			return out
		}
	} else {
		for _, key := range keys {
			if err := s.keys.SendKey(rw.target(), key); err != nil {
				logging.Warn("serve: answer %s:%s: %v", rw.group.Session, rw.window.WindowID, err)
				return failure(http.StatusBadGateway, codeActionFailed, "sending the key failed")
			}
		}
	}
	if pane.Dialog.Multi && in.Key != escKey && in.Key != submitKey {
		// A lone toggle: toggling back restores an earlier hash, which must
		// not read as "already answered", and the dialog stays up.
		return success(okBody)
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

// sendChecked sends a multi-select submit sequence (submitKeys: toggles,
// arrow moves, Enter) one key at a time. After every key but the final
// Enter it re-captures and re-parses the pane and sends the next key only
// when that key landed: the same question is still up (QuestionKey) and a
// toggle flipped its box, or an arrow moved the cursor one row (keyLanded).
// Enter goes out only with the cursor on the confirm row. Anything else
// stops the sequence with a 409, so no key is ever sent into a screen the
// sequence was not computed for. d is the dialog the sequence was built from.
func (s *Server) sendChecked(rw resolved, d panestate.Dialog, keys []string) (outcome, bool) {
	question := panestate.QuestionKey(d)
	for i, key := range keys {
		if i == len(keys)-1 && !confirmReady(d) {
			// Every move landed, yet the cursor is not on the confirm row:
			// Enter would pick whatever row it is on.
			logging.Warn("serve: answer %s:%s: cursor not on the confirm row; stopped before Enter", rw.group.Session, rw.window.WindowID)
			return failure(http.StatusConflict, codeDialogChanged, "the question did not take the answer; refresh and try again"), false
		}
		if err := s.keys.SendKey(rw.target(), key); err != nil {
			logging.Warn("serve: answer %s:%s: %v", rw.group.Session, rw.window.WindowID, err)
			return failure(http.StatusBadGateway, codeActionFailed, "sending the key failed"), false
		}
		if i == len(keys)-1 {
			return outcome{}, true // Enter: the caller waits for the dialog to go
		}
		landed := keyLanded(d, key)
		next, ok := s.awaitDialog(rw, func(n *panestate.Dialog) bool {
			return n == nil || panestate.QuestionKey(*n) != question || landed(*n)
		})
		if !ok || next == nil || panestate.QuestionKey(*next) != question || !landed(*next) {
			logging.Warn("serve: answer %s:%s: key %d (%s) did not land; stopped", rw.group.Session, rw.window.WindowID, i+1, key)
			return failure(http.StatusConflict, codeDialogChanged, "the question did not take the answer; refresh and try again"), false
		}
		d = *next
	}
	return outcome{}, true
}

// isTrustKey reports whether key is one of the folder-trust dialog's
// answers.
func isTrustKey(key string) bool {
	return key == panestate.TrustKeyTrust || key == panestate.TrustKeyExit
}

// sendTrust answers the folder-trust dialog d with key: it moves the ❯
// cursor to the chosen row one arrow at a time, re-capturing after each and
// going on only when the same dialog (hash) shows the cursor one row further,
// then presses Enter with the cursor on that row. Number keys are never used
// (the current layout is unnumbered), and the arrows never rely on wrapping
// (Claude Code wraps Up from the top row to the bottom one). Anything else
// stops with a 409 before Enter, so Enter never lands on the other row.
func (s *Server) sendTrust(rw resolved, d panestate.Dialog, key string) (outcome, bool) {
	target := panestate.TrustRow(d, key)
	for d.Cursor != target {
		if d.Cursor < 0 || target < 0 {
			logging.Warn("serve: answer %s:%s: no cursor on the trust dialog; nothing sent", rw.group.Session, rw.window.WindowID)
			return failure(http.StatusConflict, codeDialogChanged, "the trust dialog shows no cursor; answer it on the terminal"), false
		}
		move, want := "Down", d.Cursor+1
		if target < d.Cursor {
			move, want = "Up", d.Cursor-1
		}
		if err := s.keys.SendKey(rw.target(), move); err != nil {
			logging.Warn("serve: answer %s:%s: %v", rw.group.Session, rw.window.WindowID, err)
			return failure(http.StatusBadGateway, codeActionFailed, "sending the key failed"), false
		}
		hash := d.Hash
		next, ok := s.awaitDialog(rw, func(n *panestate.Dialog) bool {
			return n == nil || n.Hash != hash || n.Cursor == want
		})
		if !ok || next == nil || next.Kind != panestate.KindTrust || next.Hash != hash || next.Cursor != want {
			logging.Warn("serve: answer %s:%s: %s did not move the trust cursor; stopped before Enter", rw.group.Session, rw.window.WindowID, move)
			return failure(http.StatusConflict, codeDialogChanged, "the trust dialog did not take the answer; refresh and try again"), false
		}
		d = *next
	}
	if err := s.keys.SendKey(rw.target(), "Enter"); err != nil {
		logging.Warn("serve: answer %s:%s: %v", rw.group.Session, rw.window.WindowID, err)
		return failure(http.StatusBadGateway, codeActionFailed, "sending the key failed"), false
	}
	return outcome{}, true
}

// keyLanded is the check for one key of a submit sequence sent to d: a
// number flips that option's box and leaves the cursor where it was, Down/Up
// moves the cursor one row.
func keyLanded(d panestate.Dialog, key string) func(panestate.Dialog) bool {
	switch key {
	case "Down", "Up":
		want := d.Cursor + 1
		if key == "Up" {
			want = d.Cursor - 1
		}
		return func(n panestate.Dialog) bool { return n.Cursor == want }
	}
	// A number toggles without moving the cursor: a moved cursor means the
	// key did something else, and the arrow count is no longer right.
	was, cursor := isChecked(d, key), d.Cursor
	return func(n panestate.Dialog) bool { return isChecked(n, key) != was && n.Cursor == cursor }
}

// confirmReady reports whether d's cursor is on its confirm (Next/Submit)
// row, where Enter submits the question.
func confirmReady(d panestate.Dialog) bool { return d.Submit != nil && d.Submit.Moves == 0 }

// isChecked reports whether option key of d shows a ticked box.
func isChecked(d panestate.Dialog, key string) bool {
	for _, o := range d.Options {
		if o.Key == key {
			return panestate.Checked(o.Label)
		}
	}
	return false
}

// awaitDialog re-captures the pane until done reports true for its dialog
// (nil when none is showing), for at most s.answerSettle. It returns the
// last dialog seen and whether done was reached.
func (s *Server) awaitDialog(rw resolved, done func(*panestate.Dialog) bool) (*panestate.Dialog, bool) {
	deadline := time.Now().Add(s.answerSettle)
	for {
		text, err := s.mainPanes.CapturePane(rw.pane, defaultPaneLines)
		if err != nil {
			return nil, false
		}
		p := buildPaneBodyFor(rw.harness(), text)
		if done(p.Dialog) {
			return p.Dialog, true
		}
		if !time.Now().Before(deadline) {
			return p.Dialog, false
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

// answerKeys is the tmux key sequence for an answer to dialog d, or a 409
// when the answer does not fit it.
func answerKeys(d *panestate.Dialog, in answerIn) ([]string, outcome, bool) {
	switch {
	case d.Kind == panestate.KindTrust:
		// Esc on the trust dialog exits Claude Code, so it is not an answer
		// here; the arrow moves are computed by sendTrust from the live pane.
		if !isTrustKey(in.Key) || panestate.TrustRow(*d, in.Key) < 0 {
			return nil, failure(http.StatusConflict, codeDialogChanged, `the folder-trust dialog is answered with "trust" or "exit"`), false
		}
		return nil, outcome{}, true
	case isTrustKey(in.Key):
		return nil, failure(http.StatusConflict, codeDialogChanged, fmt.Sprintf("%q is not an option of this dialog", in.Key)), false
	case in.Key == escKey:
		return []string{"Escape"}, outcome{}, true
	case in.Key == submitKey:
		return submitKeys(d, in.Select)
	case d.Multi && !isToggleKey(d, in.Key):
		return nil, failure(http.StatusConflict, codeDialogChanged, fmt.Sprintf("%q is not a checkbox of this dialog", in.Key)), false
	case !hasOptionIn(d, in.Key):
		return nil, failure(http.StatusConflict, codeDialogChanged, fmt.Sprintf("%q is not an option of this dialog", in.Key)), false
	}
	return []string{in.Key}, outcome{}, true
}

// submitKeys toggles every checkbox whose state differs from want, then moves
// the cursor to the question's Submit/Next row and presses Enter. A number key
// toggles without moving the cursor, so the arrow count read from the pane
// stays right after the toggles.
func submitKeys(d *panestate.Dialog, want []string) ([]string, outcome, bool) {
	if !d.Multi || d.Submit == nil {
		return nil, failure(http.StatusConflict, codeDialogChanged, "this question has no submit row to confirm remotely"), false
	}
	desired := make(map[string]bool, len(want))
	for _, k := range want {
		desired[k] = true
	}
	var keys []string
	for _, o := range d.Options {
		if !strings.HasPrefix(o.Label, "[") || panestate.IsFreeText(o.Label) {
			// Free text ("Type something") is never toggled remotely.
			continue
		}
		if desired[o.Key] != panestate.Checked(o.Label) {
			keys = append(keys, o.Key)
		}
		delete(desired, o.Key)
	}
	if len(desired) > 0 {
		return nil, failure(http.StatusConflict, codeDialogChanged, "selection names an option this dialog does not have"), false
	}
	move, n := "Down", d.Submit.Moves
	if n < 0 {
		move, n = "Up", -n
	}
	for i := 0; i < n; i++ {
		keys = append(keys, move)
	}
	return append(keys, "Enter"), outcome{}, true
}

// isToggleKey reports whether key is a checkbox option other than free text.
func isToggleKey(d *panestate.Dialog, key string) bool {
	for _, o := range d.Options {
		if o.Key == key {
			return strings.HasPrefix(o.Label, "[") && !panestate.IsFreeText(o.Label)
		}
	}
	return false
}

func hasOptionIn(d *panestate.Dialog, key string) bool {
	for _, o := range d.Options {
		if o.Key == key {
			return true
		}
	}
	return false
}

type replyIn struct {
	Text string `json:"text"`
	// Images are IDs returned by POST …/upload for this window; their paths
	// are appended to the text.
	Images []string `json:"images,omitempty"`
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
// lands as a reply. A dialog or an unrecognised pane is refused exactly as
// for reply, and so is a blocked record unless the pane is idle at the prompt.
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
	if len(in.Images) > maxImages {
		return failure(http.StatusBadRequest, codeBadRequest, fmt.Sprintf("at most %d images per message", maxImages))
	}
	// An image-only message is allowed; otherwise the text is required.
	if len(in.Images) == 0 || strings.TrimSpace(in.Text) != "" {
		if out, ok := checkText("text", in.Text); !ok {
			return out
		}
		if out, ok := checkReplyMode(in.Text); !ok {
			return out
		}
	}
	return s.deliverText(r.PathValue("session"), normalizeWindowID(r.PathValue("window")), in, a, allowWorking, false)
}

// deliverText is the part of typeText after the text checks: lock the window,
// guard on its state and pane, then inject. claudeOnly refuses a harness
// whose pane cannot be classified (slash commands are typed blind there), and
// delivers once, without the staged-input retry (see Commander).
func (s *Server) deliverText(session, window string, in replyIn, a *auditEntry, allowWorking, command bool) outcome {
	rw, unlock, out, ok := s.lockWindowAt(session, window, a)
	if !ok {
		return out
	}
	defer unlock()
	if command && !harness.For(rw.harness()).Caps().PaneClassifier {
		return failure(http.StatusConflict, codeUnsupportedHarness, "slash commands are only supported for Claude Code windows")
	}
	text, out, ok := withImages(rw, in)
	if !ok {
		return out
	}

	st := rw.window.State
	classified := harness.For(rw.harness()).Caps().PaneClassifier
	// A blocked record is only trusted alone when the pane cannot be read.
	// With a pane classifier the pane decides: a live permission dialog
	// classifies as awaiting_input and is refused below, while a stale block
	// (e.g. a cron timeout's C-c left the prompt idle) accepts a reply, as
	// the phone's actionsFor offers one. Its UserPromptSubmit hook then
	// clears the record.
	if (st == "blocked" && !classified) || (st == "working" && !allowWorking) {
		return failure(http.StatusConflict, codeNotIdle, "the agent is "+st)
	}
	target := rw.target()
	if classified {
		pane, out, ok := s.capturePane(rw)
		if !ok {
			return out
		}
		if st == "blocked" && !staleBlockAccepts(pane) {
			return failure(http.StatusConflict, codeNotIdle, "the agent is blocked")
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
	if err := s.deliver(target, text, allowWorking, command); err != nil {
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

// staleBlockAccepts reports whether a window recorded as blocked is in fact
// idle at its prompt with no dialog, the only pane that admits text for a
// blocked record (a busy pane is refused even for queue, matching the phone,
// which offers only Reply there).
func staleBlockAccepts(pane paneBody) bool {
	return pane.Verdict == "idle" && pane.Dialog == nil
}

// recordedAccepts is textAccepted for a harness whose pane cannot be read:
// the windowstate record decides. "" counts as idle.
func recordedAccepts(state string, allowWorking bool) bool {
	return state == "idle" || state == "" || (allowWorking && state == "working")
}

// deliver is inject, or the send-once Commander path for a slash command.
func (s *Server) deliver(t WindowTarget, text string, queued, command bool) error {
	if c, ok := s.injector.(Commander); ok && command {
		return c.Command(t, text)
	}
	return s.inject(t, text, queued)
}

// inject delivers text through the Injector, or its Queue method for a
// queued message when the Injector distinguishes the two.
func (s *Server) inject(t WindowTarget, text string, queued bool) error {
	if q, ok := s.injector.(Queuer); ok && queued {
		return q.Queue(t, text)
	}
	return s.injector.Inject(t, text)
}

// commandNameRE matches the word after "/" in a slash command; plugin
// commands look like /plugin:skill.
var commandNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_-]*$`)

// maxCommandRunes bounds a slash command line.
const maxCommandRunes = 2000

type commandIn struct {
	Text string `json:"text"`
}

// checkCommand validates a slash command and returns it trimmed. "!" and "#"
// stay refused: only "/" is accepted, as one line with a well-formed name.
func checkCommand(text string) (string, outcome, bool) {
	if out, ok := checkText("text", text); !ok {
		return "", out, false
	}
	t := strings.TrimSpace(text)
	if utf8.RuneCountInString(t) > maxCommandRunes {
		return "", failure(http.StatusRequestEntityTooLarge, codeTooLarge, fmt.Sprintf("command exceeds %d characters", maxCommandRunes)), false
	}
	if !strings.HasPrefix(t, "/") {
		return "", failure(http.StatusBadRequest, codeBadRequest, "a command must start with /"), false
	}
	if strings.ContainsAny(t, "\n\r") {
		return "", failure(http.StatusBadRequest, codeBadRequest, "a command must be a single line"), false
	}
	name, _, _ := strings.Cut(t[1:], " ")
	if !commandNameRE.MatchString(name) {
		return "", failure(http.StatusBadRequest, codeBadRequest, "invalid command name"), false
	}
	return t, outcome{}, true
}

// handleCommand runs a Claude Code slash command in an idle agent: an
// explicitly requested action, separate from reply (which refuses a leading
// "/"). Interactive commands open a menu in the pane; the phone shows it.
func (s *Server) handleCommand(r *http.Request, in commandIn, a *auditEntry) outcome {
	text, out, ok := checkCommand(in.Text)
	if !ok {
		return out
	}
	return s.deliverText(r.PathValue("session"), normalizeWindowID(r.PathValue("window")), replyIn{Text: text}, a, false, true)
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
// the user asked for. Sweep's exclusions all hold: shell and
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
	// Sub-sessions go first, so the phone never sees the parent vanish while
	// its children linger.
	closedKids := s.children.CloseChildren(rw.window.WindowID, rw.group.Meta[rw.window.WindowID].SessionID)
	if err := s.closer.CloseWindow(rw.target()); err != nil {
		logging.Warn("serve: close %s:%s: %v", rw.group.Session, rw.window.WindowID, err)
		return failure(http.StatusBadGateway, codeActionFailed, "closing the window failed")
	}
	if closedKids == nil {
		closedKids = []WindowKey{}
	}
	return success(closeBody{OK: true, Closed: closedKids})
}

// handleCloseShell kills one plain shell window. It accepts a window only when
// the session's tmux reports it as a shell (a renamed window is not one), takes
// no pane check and cascades to no children: a shell is not an agent.
func (s *Server) handleCloseShell(r *http.Request, _ emptyIn, a *auditEntry) outcome {
	groups, err := s.fleet.Fleet()
	if err != nil {
		logging.Warn("serve: loading fleet: %v", err)
		return failure(http.StatusInternalServerError, codeInternal, "loading fleet")
	}
	session, id := r.PathValue("session"), normalizeWindowID(r.PathValue("window"))
	var g fleetwatch.Group
	for _, c := range groups {
		if c.Session == session {
			g = c
			break
		}
	}
	if g.Session == "" {
		return failure(http.StatusNotFound, codeNotFound, fmt.Sprintf("no session %s", session))
	}
	if g.Session == tmux.HubSessionName {
		return failure(http.StatusConflict, codeNotClosable, "hub windows cannot be closed")
	}
	a.Session, a.Window = g.Session, id
	unlock := s.winLocks.lock(g.Session, id)
	defer unlock()
	if !s.shells.IsShell(g.Session, id) {
		return failure(http.StatusConflict, codeNotClosable, "window is not a shell")
	}
	s.auditAttempt(*a)
	if err := s.closer.CloseWindow(WindowTarget{Session: g.Session, RuntimeDir: g.RuntimeDir, WindowID: id}); err != nil {
		logging.Warn("serve: close shell %s:%s: %v", g.Session, id, err)
		return failure(http.StatusBadGateway, codeActionFailed, "closing the shell failed")
	}
	return success(okBody)
}

// closeBody is the close response: the ok flag every window action returns,
// plus the sub-sessions closed with the window (older clients ignore it).
type closeBody struct {
	OK     bool        `json:"ok"`
	Closed []WindowKey `json:"closed_children"`
}

type dispatchIn struct {
	Agent      string `json:"agent"`
	ProjectDir string `json:"project_dir"`
	Prompt     string `json:"prompt"`
	// Instructions is optional text for the agent's system prompt.
	Instructions string `json:"instructions,omitempty"`
	// SkipReview launches with pr_review off for this dispatch only.
	SkipReview bool   `json:"skip_review,omitempty"`
	Model      string `json:"model,omitempty"`
	Backend    string `json:"backend,omitempty"`
	Effort     string `json:"effort,omitempty"`
	// Template names the prompt template the sheet was filled from. It is
	// display-only metadata: the prompt sent is the edited text.
	Template string `json:"template,omitempty"`
	// Images are IDs from POST /v1/dispatch/upload for the same project.
	Images []string `json:"images,omitempty"`
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
			fmt.Sprintf("a reply cannot start with %q: Claude Code would run it as a command, not send it to the agent; use the command action to run a slash command", r)), false
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
	instructions := strings.TrimSpace(in.Instructions)
	if utf8.RuneCountInString(instructions) > maxInstructionsRunes {
		return failure(http.StatusRequestEntityTooLarge, codeTooLarge, fmt.Sprintf("instructions exceed %d characters", maxInstructionsRunes))
	}
	if instructions != "" {
		if out, ok := checkText("instructions", instructions); !ok {
			return out
		}
	}
	if len(in.Images) > maxImages {
		return failure(http.StatusBadRequest, codeBadRequest, fmt.Sprintf("at most %d images per dispatch", maxImages))
	}
	if in.Model != "" && !launchopts.ValidModel(in.Model) {
		return failure(http.StatusBadRequest, codeBadRequest, "invalid model")
	}
	if in.Effort != "" {
		if err := launchopts.ValidateEffort(in.Effort); err != nil {
			return failure(http.StatusBadRequest, codeBadRequest, err.Error())
		}
	}
	if in.Template != "" && !prompttemplate.ValidName(in.Template) {
		return failure(http.StatusBadRequest, codeBadRequest, "invalid template name")
	}
	g, out, ok := s.dispatchTarget(in.Agent, in.ProjectDir, a)
	if !ok {
		return out
	}
	// The prompt text is required above; images only add screenshot lines to it.
	prompt, out, ok := appendImages(in.Prompt, g.RuntimeDir, uploads.DispatchKey, in.Images)
	if !ok {
		return out
	}
	if utf8.RuneCountInString(prompt) > maxPromptRunes {
		return failure(http.StatusRequestEntityTooLarge, codeTooLarge, fmt.Sprintf("prompt exceeds %d characters", maxPromptRunes))
	}
	if out, ok := checkText("prompt", prompt); !ok {
		return out
	}
	backend, model, effort := in.Backend, in.Model, in.Effort
	if in.Backend != "" || in.Model != "" || in.Effort != "" {
		c, err := s.launchChoices(g.Session, g.RuntimeDir, in.Agent, g.Dir)
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
		Agent: in.Agent, Prompt: prompt, Instructions: instructions, SkipReview: in.SkipReview, Template: in.Template, Model: model, Backend: backend, Effort: effort,
		Dir: g.Dir, Session: g.Session, RuntimeDir: g.RuntimeDir,
	})
	if err != nil {
		logging.Warn("serve: dispatch %s in %s: %v", in.Agent, g.Dir, err)
		return failure(http.StatusBadGateway, codeActionFailed, "dispatch failed; see serve.log")
	}
	a.Window = res.WindowID
	return success(res)
}

type shellIn struct {
	ProjectDir string `json:"project_dir"`
}

// ShellResult is the body of POST /v1/shell.
type ShellResult struct {
	Session  string `json:"session"`
	WindowID string `json:"window_id"`
}

// handleShell opens a plain shell window in a project's running session, as
// the hub's shell selector does. The project resolves exactly as for dispatch
// (projectGroup). It never creates a session.
func (s *Server) handleShell(_ *http.Request, in shellIn, a *auditEntry) outcome {
	g, out, ok := s.projectGroup(in.ProjectDir, a)
	if !ok {
		return out
	}
	if g.Session == "" || g.Session == tmux.HubSessionName {
		return failure(http.StatusBadRequest, codeBadRequest, "a shell cannot be opened in this session")
	}
	if !s.shells.SessionRunning(g.Session) {
		return failure(http.StatusConflict, codeSessionNotRunning, "the project's tmux session is not running; start it on the laptop first")
	}
	s.auditAttempt(*a)
	id, err := s.shells.NewShell(g.Session, g.Dir)
	if err != nil {
		logging.Warn("serve: shell in %s: %v", g.Session, err)
		return failure(http.StatusBadGateway, codeActionFailed, "opening the shell failed; see serve.log")
	}
	a.Window = id
	return success(ShellResult{Session: g.Session, WindowID: id})
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

// projectGroup finds the fleet project whose dir is exactly projectDir (one of
// /v1/projects' dirs). a, when non-nil, receives the audit fields.
func (s *Server) projectGroup(projectDir string, a *auditEntry) (fleetwatch.Group, outcome, bool) {
	if !filepath.IsAbs(projectDir) {
		return fleetwatch.Group{}, failure(http.StatusBadRequest, codeUnknownProject, "project_dir must be an absolute path from /v1/projects"), false
	}
	groups, err := s.fleet.Fleet()
	if err != nil {
		logging.Warn("serve: loading fleet: %v", err)
		return fleetwatch.Group{}, failure(http.StatusInternalServerError, codeInternal, "loading fleet"), false
	}
	for _, g := range groups {
		if g.Dir != "" && g.Dir == projectDir {
			if a != nil {
				a.Session = g.Session
				a.Project = clip(g.Project)
			}
			return g, outcome{}, true
		}
	}
	return fleetwatch.Group{}, failure(http.StatusBadRequest, codeUnknownProject, "project_dir is not a known project"), false
}

// dispatchTarget validates an agent name and project dir as a dispatch (and
// its launch options) require: the dir must be exactly a fleet project's dir
// and the agent one of the agents discoverable there. a, when non-nil,
// receives the audit fields.
func (s *Server) dispatchTarget(agentName, projectDir string, a *auditEntry) (fleetwatch.Group, outcome, bool) {
	if agentName == "" || !agent.ValidLaunchName(agentName) {
		return fleetwatch.Group{}, failure(http.StatusBadRequest, codeUnknownAgent, "invalid agent name"), false
	}
	g, out, ok := s.projectGroup(projectDir, a)
	if !ok {
		return fleetwatch.Group{}, out, false
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
