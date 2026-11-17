// Package winname keeps a tmux window's name in sync with its windowstate.
//
// Window glyphs (● working, ⏸ blocked, ✓ done and unseen, bare name
// otherwise) are written by whichever hook or command changes State or Done,
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
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// Glyph prefixes used in window names.
const (
	glyphWorking = "● "
	glyphBlocked = "⏸ "
	glyphDone    = "✓ "
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
	for _, p := range []string{glyphWorking, glyphBlocked, glyphDone} {
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
	case ws.Done && !ws.Seen:
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
// Seen is cleared whenever State changes or Done goes true; if Done went true
// on the window the user is looking at, Seen is set at once so a focused
// window never shows ✓. Done itself is never cleared here.
func Apply(windowID string, ws *windowstate.WindowState, fn func(*windowstate.WindowState)) {
	prevState, prevDone := ws.State, ws.Done
	fn(ws)
	doneEdge := ws.Done && !prevDone
	if ws.State != prevState || doneEdge {
		ws.Seen = false
	}
	if doneEdge && windowID == activeWindow() {
		ws.Seen = true
	}
	SyncName(windowID, ws)
}

// Transact is windowstate.Transact with Apply wrapped around fn.
func Transact(windowID string, fn func(*windowstate.WindowState)) (old, updated windowstate.WindowState, err error) {
	return windowstate.Transact(windowID, func(ws *windowstate.WindowState) {
		Apply(windowID, ws, fn)
	})
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
