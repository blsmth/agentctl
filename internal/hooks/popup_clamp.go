package hooks

import (
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
)

// popupMargin is how much smaller than the attached tmux client a clamped
// popup is made, so its border never touches the edge of the terminal.
const popupMargin = 2

// --- Dispatch-preview fit inputs -------------------------------------------
//
// These size the dispatch-preview popup's INPUTS -- content width and max
// viewport height -- from the attached tmux client's dimensions, so the
// popup can grow to fit a large terminal as well as shrink to fit a small
// one. The popup's actual dimensions still come from
// tui.DispatchPreviewModel.PopupSize() (see dispatchPreviewFit's doc
// comment) -- this file never computes a popup size on its own.

const (
	// dpFallbackContentWidth / dpFallbackVPHeight are the values used when
	// the client's dimensions can't be queried at all (not attached, tmux
	// not running, unparseable output). These intentionally match
	// tui.dispatchPreviewContentWidth / tui.dpMaxVPHeight -- the fixed
	// values the popup shipped with before responsive sizing existed. Do
	// NOT invent a new fallback number here and do NOT grow blindly on
	// failure -- that would reintroduce the exact bug that clamping
	// fixes: trusting an unqueryable client.
	dpFallbackContentWidth = 88
	dpFallbackVPHeight     = 22

	// dpMinContentWidth is the floor below which the popup's prose becomes
	// unreadable regardless of how small the terminal is -- there's a point
	// where shrinking further does more harm than a slightly-too-wide popup
	// would. dpMaxContentWidth is the READABILITY cap in the other
	// direction: markdown prose wrapped at, say, 340 columns is not more
	// readable for being wider, just harder to scan line to line. Width
	// growth up to this cap is otherwise a pure win -- glamour rewraps
	// prose to fill whatever width it's given -- so this is the point, not
	// clientWidth itself.
	dpMinContentWidth = 60
	dpMaxContentWidth = 132

	// dpMinVPHeight is the floor below which the viewport can no longer
	// usefully show more than a line or two of context.
	//
	// dpMaxVPHeightCap is a UX cap, deliberately distinct from -- and much
	// smaller relative to client height than -- dpMaxContentWidth is
	// relative to client width. HEIGHT IS A TRADEOFF THAT WIDTH IS NOT: a
	// tmux popup's size is fixed at launch and can never be resized, so it
	// must be sized for the fully EXPANDED view up front. But the popup's
	// default state is COLLAPSED, showing only a few lines of metadata --
	// every row of height beyond what that collapsed content needs is
	// visible dead space below it until the user presses 'v'. Width has no
	// such penalty (glamour/highlight simply rewrap into the extra space),
	// so width is allowed to grow much closer to its readability ceiling
	// while height is deliberately kept moderate. Do not "fix" this to
	// scale like width does in either direction -- growing it further
	// enlarges the common-case dead space, shrinking it back to a flat
	// constant reintroduces the underuse bug this file fixes.
	dpMinVPHeight     = 8
	dpMaxVPHeightCap  = 40
	heightBudgetShare = 0.6 // see dispatchPreviewFit
)

// clampInt clamps v into [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// fitToClient derives one popup dimension (content width or viewport height)
// from one client dimension: subtract the model's own chrome overhead and
// popupMargin, then clamp into [minVal, maxVal]. This single function is
// what lets a popup grow as well as shrink -- shared by every popup model's
// fit function so that logic lives in exactly one place.
func fitToClient(clientDim, overhead, minVal, maxVal int) int {
	return clampInt(clientDim-overhead-popupMargin, minVal, maxVal)
}

// dispatchPreviewFit resolves the (contentWidth, vpHeight) INPUTS for a
// dispatch-preview popup from the attached tmux client's dimensions. These
// are inputs to tui.DispatchPreviewModel.PopupSize() -- via
// tui.WithContentWidth / tui.WithMaxViewportHeight -- not a competing size
// computation; see that function's doc comment for why the hook must never
// compute the popup's size independently of the model.
//
// On any failure to query the client, both return values are the historical
// fixed defaults (88, 22) -- see dpFallbackContentWidth / dpFallbackVPHeight.
func dispatchPreviewFit() (contentWidth, vpHeight int) {
	width, height, ok := clientDimensions()
	if !ok {
		return dpFallbackContentWidth, dpFallbackVPHeight
	}

	// Width: content + border(2) + padding(4) + tmux border(2) = content + 8
	// (see tui.DispatchPreviewModel.PopupSize()).
	contentWidth = fitToClient(width, 8, dpMinContentWidth, dpMaxContentWidth)

	// Height: budget off a FRACTION of the client's height, not the full
	// height -- see dpMaxVPHeightCap's doc comment on why height doesn't get
	// the same treatment as width. total popup height = vpHeight + 16 (see
	// PopupSize()), so solve for vpHeight against the budgeted height.
	budget := int(float64(height) * heightBudgetShare)
	vpHeight = fitToClient(budget, 16, dpMinVPHeight, dpMaxVPHeightCap)

	return contentWidth, vpHeight
}

// --- File-preview fit inputs ------------------------------------------------
//
// Same underuse problem as dispatch-preview (fixed 108x32 regardless of
// terminal size), but the file-preview popup has no collapsed state -- it
// always shows the full viewport immediately, so there is no "dead space
// below collapsed content" tradeoff. Growing height is therefore a pure win
// here too, same as width, and both simply reuse fitToClient directly
// against the client's raw dimensions -- no heightBudgetShare needed.

const (
	// fpFallbackContentWidth / fpFallbackVPHeight mirror
	// tui.filePreviewContentWidth / tui.filePreviewMaxVPHeight, the fixed
	// values shipped before responsive sizing existed. Same rule as the
	// dispatch-preview fallbacks: never invent a new number, never grow
	// blindly when the client can't be queried.
	fpFallbackContentWidth = 108
	fpFallbackVPHeight     = 32

	fpMinContentWidth = 60
	fpMaxContentWidth = 140 // readability cap; code can usefully use a bit more than prose

	fpMinVPHeight    = 8
	fpMaxVPHeightCap = 50
)

// filePreviewFit resolves the (contentWidth, vpHeight) INPUTS for a
// file-preview popup from the attached tmux client's dimensions -- inputs to
// tui.FilePreviewModel.PopupSize() via tui.WithFileContentWidth /
// tui.WithFileMaxViewportHeight, not a competing size computation. Mirrors
// dispatchPreviewFit; see its doc comment for the invariant this preserves.
//
// On any failure to query the client, both return values are the historical
// fixed defaults (108, 32) -- see fpFallbackContentWidth / fpFallbackVPHeight.
func filePreviewFit() (contentWidth, vpHeight int) {
	width, height, ok := clientDimensions()
	if !ok {
		return fpFallbackContentWidth, fpFallbackVPHeight
	}

	// Width: content + border(2) + padding(4) + tmux border(2) = content + 8
	// (see tui.FilePreviewModel.PopupSize()).
	contentWidth = fitToClient(width, 8, fpMinContentWidth, fpMaxContentWidth)

	// Height: content + border(2) + padding(2) + hint(1) + blank(1) + tmux
	// border(2) = content + 12 (see tui.FilePreviewModel.PopupSize()). No
	// budget fraction -- see this section's header comment.
	vpHeight = fitToClient(height, 12, fpMinVPHeight, fpMaxVPHeightCap)

	return contentWidth, vpHeight
}

// clientDimensions queries the attached tmux client's width and height.
// It is a package-level var so tests can fake it instead of shelling out to
// a real tmux client -- see clampPopupToClient's doc comment for why this
// seam exists.
var clientDimensions = tmuxClientDimensions

// clampPopupToClient clamps a popup size derived from a model's own chrome
// arithmetic (e.g. tui.DispatchPreviewMaxPopupSize, tui.FilePreviewMaxPopupSize)
// down to the attached tmux client's actual dimensions. A worst-case derived
// size can legitimately exceed a small terminal -- the hook has to fix the
// popup's size before the real content is known, so it sizes for the worst
// case, and the worst case does not know how big the user's terminal is.
//
// On any failure to query the client (not attached, tmux not running,
// unparseable output) the derived size is returned UNCLAMPED. Never abort
// the popup over a clamp failure, and never substitute a new hardcoded
// fallback number -- that would just reintroduce the bug this helper fixes.
func clampPopupToClient(sz tui.PopupSize) tui.PopupSize {
	width, height, ok := clientDimensions()
	if !ok {
		return sz
	}

	if maxW := width - popupMargin; maxW > 0 && sz.Width > maxW {
		sz.Width = maxW
	}
	if maxH := height - popupMargin; maxH > 0 && sz.Height > maxH {
		sz.Height = maxH
	}
	return sz
}

// tmuxClientDimensions is the production implementation of clientDimensions.
// It shells out to `tmux display -p` twice (once per dimension) rather than
// a single call with both formats concatenated, so a failure or garbage
// value on one query doesn't have to be disentangled from the other.
func tmuxClientDimensions() (width, height int, ok bool) {
	w, wOK := tmuxDisplayInt("#{client_width}")
	h, hOK := tmuxDisplayInt("#{client_height}")
	if !wOK || !hOK {
		return 0, 0, false
	}
	return w, h, true
}

// tmuxDisplayInt runs `tmux display -p <format>` and parses the result as a
// positive integer. Returns ok=false on any error, empty output, or
// unparseable/non-positive output.
func tmuxDisplayInt(format string) (int, bool) {
	out, err := exec.Command("tmux", "display", "-p", format).Output()
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// tmuxDisplayString runs `tmux display -p <format>` and returns the trimmed
// output. Returns ok=false on any error or empty output. Same implicit
// target-resolution semantics as tmuxDisplayInt (current pane/session via
// $TMUX_PANE) -- see that function's doc comment.
func tmuxDisplayString(format string) (string, bool) {
	out, err := exec.Command("tmux", "display", "-p", format).Output()
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", false
	}
	return s, true
}

// hasAttachedClient reports whether a human is attached to the current tmux
// session AND is currently looking at the window that is calling this
// function -- as opposed to merely running inside a tmux pane somewhere,
// which is all os.Getenv("TMUX") != "" tells you, or merely having SOME
// client attached to the session, which is not enough on its own (see
// below).
//
// Both halves matter because every agentctl agent window -- including a
// dispatched, unattended one -- is created with `new-window -t
// <sessionTarget>` (tmux.LaunchAgent), i.e. it lives in THE SAME SESSION the
// human is attached to, just in a different window. Checking only "is a
// client attached to this session" is true whenever the human is attached to
// ANY window in the session, including window 0 while an unattended
// dispatched agent in window 5 is asking. Requiring the calling window to
// also be the session's ACTIVE window is what actually distinguishes "a
// human is looking at this" from "a human is looking at something else in
// the same session" -- without it, a global bash_preview setting deadlocks
// every unattended dispatch forever on a modal nobody can see.
//
// clientDimensions() (above) is NOT a substitute for this check: per its own
// doc comment, it deliberately FALLS BACK to fixed defaults (88/22) on any
// query failure rather than reporting one, specifically so popup sizing
// never aborts over an unqueryable client. That fallback makes it
// indistinguishable from "small but present" client, which is exactly the
// ambiguity this function exists to resolve.
//
// It is a package-level var so tests can fake the answer without shelling
// out to a real tmux server -- same seam pattern as clientDimensions.
var hasAttachedClient = tmuxHasAttachedClient

// tmuxHasAttachedClient is the production implementation of
// hasAttachedClient. It resolves the calling window via
// signal.ResolveWindowID's TMUX_PANE path specifically -- NOT the full
// resolution chain, whose AGENTCTL_WINDOW_ID fallback is session-global and
// last-writer-wins and can therefore name the wrong window entirely. A hook
// invocation always runs inside its own pane, so TMUX_PANE is always
// available and always correct for it; if it can't be resolved this way, the
// window is treated as NOT attached (fail closed to pass-through, which
// emits nothing -- the safe direction, since ValidateBash's caller falls
// back to Claude Code's normal permission flow).
//
// It then requires BOTH:
//  1. at least one client attached to the session (scoped to the CURRENT
//     session, resolved via tmux's implicit target -- a client attached to
//     some other project's session must not count as "attached" here), and
//  2. the calling window is that session's ACTIVE window.
//
// Any error, empty output, or unresolvable window makes this false. There is
// deliberately no fallback default the way clientDimensions has one -- an
// unqueryable client here is never "safe to proceed", it's "assume nobody is
// watching."
func tmuxHasAttachedClient() bool {
	windowID := resolveCallingWindowID()
	if windowID == "" {
		return false
	}

	session, ok := tmuxDisplayString("#{session_name}")
	if !ok {
		return false
	}

	out, err := exec.Command("tmux", "list-clients", "-t", session).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return false
	}

	return tmux.DisplayTarget(windowID, "#{window_active}") == "1"
}

// resolveCallingWindowID resolves the tmux window ID of the pane this hook
// process is running in, via TMUX_PANE only -- see tmuxHasAttachedClient's
// doc comment for why the rest of signal.ResolveWindowID's fallback chain is
// deliberately not used here.
func resolveCallingWindowID() string {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return ""
	}
	return tmux.DisplayTarget(pane, "#{window_id}")
}
