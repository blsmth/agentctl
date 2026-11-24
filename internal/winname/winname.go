// Package winname keeps a tmux window's name in sync with its windowstate.
//
// Window glyphs (● working, ⏸ blocked, ! needs attention, ✓ done and
// unseen, bare name otherwise) are written by whichever hook or command changes State or Done,
// inside the same windowstate transaction, so two racing writers cannot leave
// a glyph that disagrees with the persisted state. The monitor does not
// rename windows.
//
// Import direction: winname imports windowstate and tmux; neither imports
// winname.
package winname

import (
	"os/exec"
	"strings"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/metrics/events"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// Glyph prefixes used in window names.
const (
	glyphWorking = "● "
	glyphBlocked = "⏸ "
	glyphDone    = "✓ "
	// glyphAttention marks a window that needs the human (windowstate
	// Attention). Precedence: ⏸ > ● > ! > ✓ > bare.
	glyphAttention = "! "
)

// Seams, replaced in tests via SetSeamsForTest.
var (
	renameFn      = tmuxRename
	currentNameFn = tmuxCurrentName
	activeWindow  = func() string { return tmux.ActiveWindowIDFast(tmux.ActiveWindowPath()) }
)

// SetSeamsForTest overrides the tmux seams and returns a restore func. A nil
// argument leaves that seam unchanged. Only for tests: the defaults talk to
// whatever tmux server the test happens to run under.
func SetSeamsForTest(rename func(windowID, name string) error, currentName func(windowID string) string, active func() string) (restore func()) {
	oldR, oldC, oldA := renameFn, currentNameFn, activeWindow
	if rename != nil {
		renameFn = rename
	}
	if currentName != nil {
		currentNameFn = currentName
	}
	if active != nil {
		activeWindow = active
	}
	return func() { renameFn, currentNameFn, activeWindow = oldR, oldC, oldA }
}

// tmuxRename renames by bare window ID: @N is unique across the tmux server,
// and hook processes do not know the session name.
func tmuxRename(windowID, name string) error {
	// rename-window format-expands the name; it is literal here.
	return exec.Command("tmux", "rename-window", "-t", windowID, tmux.FormatEscape(name)).Run()
}

func tmuxCurrentName(windowID string) string {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", windowID, "#{window_name}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// stripGlyph removes a leading status glyph prefix.
func stripGlyph(name string) string {
	for _, p := range []string{glyphWorking, glyphBlocked, glyphAttention, glyphDone} {
		if s, ok := strings.CutPrefix(name, p); ok {
			return s
		}
	}
	return name
}

// Compute returns the window name ws wants. The base is ws.Instance; when
// that is empty the last name written (RenderedName) with any glyph stripped.
// It returns "" when no base is known.
func Compute(ws windowstate.WindowState) string {
	base := ws.Instance
	if base == "" {
		base = stripGlyph(ws.RenderedName)
	}
	if base == "" {
		return ""
	}
	switch {
	case ws.State == "working":
		return glyphWorking + base
	case ws.State == "blocked":
		return glyphBlocked + base
	case ws.Attention != nil:
		// Not gated by Seen: looking at the window does not answer it.
		return glyphAttention + base
	case (ws.Done || ws.TurnDone) && !ws.Seen:
		return glyphDone + base
	}
	return base
}

// SyncName renames the tmux window when Compute differs from ws.RenderedName
// and records the new RenderedName. Call it inside the Transact/Update
// closure that changed State or Done. It never forks tmux when the name is
// unchanged. A rename failure is logged at debug and leaves RenderedName
// alone so the next writer retries.
func SyncName(windowID string, ws *windowstate.WindowState) {
	probe := *ws
	if probe.Instance == "" && probe.RenderedName == "" {
		// Untracked or pre-Instance window: adopt whatever it is called now.
		probe.Instance = stripGlyph(currentNameFn(windowID))
	}
	want := Compute(probe)
	if want == "" || want == ws.RenderedName {
		return
	}
	if err := renameFn(windowID, want); err != nil {
		logging.Debug("winname: rename %s to %q: %v", windowID, want, err)
		return
	}
	ws.RenderedName = want
}

// Apply runs fn on ws, then enforces the Seen rules and syncs the window name.
// Seen is cleared whenever State changes or Done (or a bare window's
// TurnDone) goes true; if Done went true
// on the window the user is looking at, Seen is set at once so a focused
// window never shows ✓. Done itself is never cleared here. A window that
// goes working loses its Attention: whatever it needed is being dealt with.
func Apply(windowID string, ws *windowstate.WindowState, fn func(*windowstate.WindowState)) {
	prevState, prevDone := ws.State, ws.Done || ws.TurnDone
	fn(ws)
	if ws.State == "working" && prevState != "working" {
		ws.Attention = nil
	}
	doneEdge := (ws.Done || ws.TurnDone) && !prevDone
	if ws.State != prevState || doneEdge {
		ws.Seen = false
	}
	if doneEdge && windowID == activeWindow() {
		ws.Seen = true
	}
	SyncName(windowID, ws)
}

// Transact is windowstate.Transact with Apply wrapped around fn. A State
// change is recorded in the metrics event log only after the transaction has
// committed, so a failed write never emits an event for a change that did not
// happen. The state writers decide transitions; the metrics serve reads them
// from that log. No-op unless metrics are enabled; never fails.
func Transact(windowID string, fn func(*windowstate.WindowState)) (old, updated windowstate.WindowState, err error) {
	old, updated, err = windowstate.Transact(windowID, func(ws *windowstate.WindowState) {
		Apply(windowID, ws, fn)
	})
	if err == nil {
		events.RecordStateChange(windowID, old.State, updated)
	}
	return old, updated, err
}

// Update is windowstate.Update with Apply wrapped around fn.
func Update(windowID string, fn func(*windowstate.WindowState)) error {
	_, _, err := Transact(windowID, fn)
	return err
}

// MarkSeen records that the user focused windowID and clears its ✓. It does
// nothing for a window with no windowstate record (shell, untracked).
func MarkSeen(windowID string) error {
	if !windowstate.Exists(windowID) {
		return nil
	}
	err := Update(windowID, func(ws *windowstate.WindowState) { ws.Seen = true })
	if err == windowstate.ErrGone {
		return nil
	}
	return err
}

// ReleaseTurnDone sets the held-back TurnDone of parentID (see
// windowstate.TurnDoneHeld) once none of its child windows is working or
// blocked any more. Call it after a child window went idle or away. It is a
// no-op unless the parent is idle, still holds a TurnDone, and is the same
// session that dispatched the child (parentSession).
func ReleaseTurnDone(parentID, parentSession string) {
	if parentID == "" || parentSession == "" || !windowstate.Exists(parentID) {
		return
	}
	all := windowstate.All()
	if p, ok := all[parentID]; !ok || !p.TurnDoneHeld || p.SessionID != parentSession {
		return
	}
	if windowstate.HasBusyChild(parentID, parentSession, all) {
		return
	}
	err := Update(parentID, func(w *windowstate.WindowState) {
		if !w.TurnDoneHeld || w.SessionID != parentSession {
			return
		}
		w.TurnDoneHeld = false
		if w.State == "idle" {
			w.TurnDone = true
		}
	})
	if err != nil && err != windowstate.ErrGone {
		logging.Debug("winname: releasing turn done of %s: %v", parentID, err)
	}
}
