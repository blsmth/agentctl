package hooks

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/tui"
)

// This file holds the human-approval gate machinery shared by BOTH kinds of
// PreToolUse popup ValidateBash can show: the always-on "agentctl dispatch"
// preview (dispatchPreviewGate, dispatch_preview.go) and the opt-in
// bash_preview gate for every other Bash command (bashPreviewGate, below).
// The lock is command-neutral by design -- see acquireGateLock's doc
// comment -- so there is exactly one dedupe mechanism, not two competing
// ones.

// gateStaleAfter is how old an orphaned lock file must be before a
// contending invocation is allowed to reclaim it. Fifteen minutes comfortably
// exceeds how long a human could plausibly be staring at the popup, so a
// lock older than this can only be a leftover from a process that crashed or
// was killed before its deferred release ran.
const gateStaleAfter = 15 * time.Minute

// gateLockPath returns the path of the exclusive lock file that arbitrates
// which of the (typically two) duplicate hook invocations for the same
// command gets to show the popup. The hash is derived from the session ID
// plus the command so that concurrent, unrelated popups (different
// sessions, or different commands in the same session) never contend with
// each other. Shared by both the dispatch gate and the general bash_preview
// gate -- a dispatch command and a plain Bash command never collide because
// their command strings differ.
func gateLockPath(command string) string {
	sessionID := os.Getenv("AGENTCTL_SESSION_ID")
	sum := sha256.Sum256([]byte(sessionID + "\x00" + command))
	return filepath.Join(os.TempDir(), fmt.Sprintf("agentctl-dispatch-gate-%x.lock", sum))
}

// acquireGateLock attempts to exclusively claim the preview-popup gate for
// command. Only one caller can hold the lock at a time; the loser gets
// ok=false and must not show a popup or write any output. Callers that get
// ok=true must call release (typically via defer) once they're done, whether
// or not they end up showing the popup.
func acquireGateLock(command string) (release func(), ok bool) {
	path := gateLockPath(command)

	if createGateLockFile(path) {
		return func() { os.Remove(path) }, true
	}

	// Contended -- if the existing lock is stale (left behind by a process
	// that never reached its deferred release, e.g. a crash), reclaim it and
	// retry exactly once. A second failure means a live invocation genuinely
	// holds it.
	if reclaimStaleGateLock(path) && createGateLockFile(path) {
		return func() { os.Remove(path) }, true
	}

	return nil, false
}

// createGateLockFile attempts to exclusively create the lock file at path,
// returning true only if this call created it.
func createGateLockFile(path string) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	f.Close() //nolint:errcheck
	return true
}

// reclaimStaleGateLock removes path if it exists and is older than
// gateStaleAfter. Returns true only if it removed the file.
func reclaimStaleGateLock(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if time.Since(info.ModTime()) < gateStaleAfter {
		return false
	}
	return os.Remove(path) == nil
}

// bashPreviewGate shows the preview popup for an ordinary (non-dispatch)
// Bash command, gated behind the bash_preview config setting. It mirrors
// dispatchPreviewGate's contract exactly: returns (approved, decided), where
// decided is false whenever no popup was shown and the caller must stay
// silent so Claude Code's normal permission flow decides.
//
// It adds ONE rail dispatchPreviewGate does not have: requiring an ATTACHED
// tmux client, not merely os.Getenv("TMUX") != "". A dispatched agent runs
// inside its own tmux pane (TMUX is set) with nobody watching it -- showing
// a popup there would either deadlock the agent forever, or draw the popup
// on whatever client IS attached to the session, hijacking a human's screen
// with an approval prompt for a window they aren't even looking at. See
// hasAttachedClient's doc comment (popup_clamp.go) for why clientDimensions
// can't be reused for this check. This rail is scoped to the general
// bash_preview path deliberately -- the "agentctl dispatch" gate's existing
// TMUX-only check is left unchanged (see ValidateBash's doc comment on why
// dispatch gating is independent of this setting).
func bashPreviewGate(agentName, command string) (approved, decided bool, denyReason string) {
	if os.Getenv("TMUX") == "" {
		logging.Log("hook-validate-bash: not in tmux session, passing through bash preview")
		return false, false, ""
	}

	if !hasAttachedClient() {
		logging.Log("hook-validate-bash: no attached tmux client, passing through bash preview")
		return false, false, ""
	}

	// Same dedupe rationale as dispatchPreviewGate: PreToolUse fires twice per
	// tool call, and the loser must produce no output at all.
	release, ok := acquireGateLock(command)
	if !ok {
		logging.Log("hook-validate-bash: bash preview gate already owned by another invocation, staying silent")
		return false, false, ""
	}
	defer release()

	contentWidth, vpHeight := dispatchPreviewFit()

	args := DispatchPreviewArgs{
		AgentName:    agentName,
		Command:      command,
		Kind:         PreviewKindBash,
		ContentWidth: contentWidth,
		VPHeight:     vpHeight,
	}

	tmpFile, err := os.CreateTemp("", "agentctl-bash-preview-*")
	if err != nil {
		logging.Log("hook-validate-bash: failed to create bash preview temp file: %v", err)
		return false, false, ""
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if err := json.NewEncoder(tmpFile).Encode(args); err != nil {
		tmpFile.Close()
		logging.Log("hook-validate-bash: failed to write bash preview temp file: %v", err)
		return false, false, ""
	}
	tmpFile.Close()

	// Same sizing/clamping contract as dispatchPreviewGate -- see
	// DispatchPreviewMaxPopupSize's doc comment for why the hook must never
	// compute a competing popup size.
	sz := clampPopupToClient(tui.DispatchPreviewMaxPopupSize(
		tui.WithContentWidth(contentWidth),
		tui.WithMaxViewportHeight(vpHeight),
	))
	popupW := sz.Width
	popupH := sz.Height

	logging.Log("hook-validate-bash: launching bash preview popup agent=%s cmd=%s", agentName, textutil.Truncate(command, 100, "..."))

	return runPopupWithDeadline("bash command", "hook-validate-bash: bash preview", popupW, popupH, tmpPath)
}
