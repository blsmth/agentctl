package hooks

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/approval"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/winname"
)

const (
	// validateBashTimeoutSeconds is the explicit timeout registered for the
	// hook-validate-bash hook. Claude Code kills the hook after this long and
	// treats it as "no decision", which auto mode allows -- so the popup
	// gates must finish (and deny) before it fires.
	validateBashTimeoutSeconds = 600

	// popupDeadlineMargin is how much earlier than the registered hook
	// timeout the popup gives up, leaving room to close it and emit a deny.
	popupDeadlineMargin = 60 * time.Second
)

// popupDeadline is how long an approval popup may wait for an answer.
// Derived from the registered hook timeout so the two cannot drift; a var so
// tests can inject a tiny value.
var popupDeadline = time.Duration(validateBashTimeoutSeconds)*time.Second - popupDeadlineMargin

// popupCloser closes the open display-popup on the client the popup was
// opened for. Seam for tests.
var popupCloser = closeDisplayPopup

// closeDisplayPopup runs `tmux display-popup -C`. The popup is opened without
// -t (it targets the hook's current client), so closing uses the same default.
func closeDisplayPopup() error {
	return exec.Command("tmux", "display-popup", "-C").Run()
}

// popupTimeoutReason is the deny reason shown to the model when nobody
// answered the popup in time.
func popupTimeoutReason(kind string, d time.Duration) string {
	return fmt.Sprintf("%s approval timed out after %s with no answer; nobody approved it. "+
		"Do not retry automatically; tell your dispatcher or the user.", kind, d)
}

// popupNotice describes a pending approval for the blocked state and the
// desktop notification. Tool/Detail fill windowstate.PendingTool/Detail;
// Action completes "<agent> wants to <Action>, approve in <session>".
type popupNotice struct {
	Agent  string
	Tool   string
	Detail string
	Action string
}

// approvalNotificationType is the sendAlert type for a pending approval popup.
const approvalNotificationType = "approval_pending"

// approvalSeams are the process-environment lookups beginApproval needs, as
// vars so tests need no tmux.
var (
	approvalWindowID    = resolveHookWindowID
	approvalSessionName = resolveInstanceSessionName
	approvalRuntimeDir  = paths.RuntimeDir
)

// beginApproval marks the issuing window blocked, drops the focus-guard
// marker, and notifies the human. The returned func undoes the state and
// marker changes; callers must defer it so every exit path restores. If the
// window cannot be resolved the state change is skipped, but the notification
// still fires.
func beginApproval(n popupNotice) (end func()) {
	windowID := approvalWindowID()
	sessionName := approvalSessionName()
	clearMarker := approval.Mark(approvalRuntimeDir(), windowID)

	var ws windowstate.WindowState
	prevState := ""
	stateSet := false
	var prevTool, prevDetail string
	var prevAt time.Time
	if windowID != "" {
		err := winname.Update(windowID, func(w *windowstate.WindowState) {
			prevState = w.State
			prevTool, prevDetail, prevAt = w.PendingTool, w.PendingToolDetail, w.PendingToolAt
			w.State = "blocked"
			if prevState != "blocked" || w.BlockedSince.IsZero() {
				w.BlockedSince = time.Now()
			}
			w.PendingTool, w.PendingToolDetail, w.PendingToolAt = n.Tool, n.Detail, time.Now()
			ws = *w
		})
		if err != nil {
			logging.Log("hook-approval: windowstate.Update(blocked) error: %v", err)
		} else {
			stateSet = true
		}
	}

	message := fmt.Sprintf("%s wants to %s, approve in %s", n.Agent, n.Action, sessionName)
	if stateSet && ws.SpawnedBy != "" && prevState != "blocked" {
		emitBlockedMessage(windowID, ws, n.Agent)
	}
	title := n.Agent
	if d := strings.TrimPrefix(sessionName, "agentctl-"); d != "" {
		title = n.Agent + " \u00b7 " + d
	}
	sendAlert(n.Agent, approvalNotificationType, notify.Options{
		Title:    title,
		Subtitle: "Approval needed",
		Message:  message,
		Env: map[string]string{
			"AGENTCTL_AGENT_NAME":        n.Agent,
			"AGENTCTL_NOTIFICATION_TYPE": approvalNotificationType,
			"AGENTCTL_WINDOW_ID":         windowID,
			"AGENTCTL_TOOL_NAME":         n.Tool,
			"AGENTCTL_SESSION_NAME":      sessionName,
		},
	})

	return func() {
		clearMarker()
		if !stateSet {
			return
		}
		if err := winname.Update(windowID, func(w *windowstate.WindowState) {
			w.State = "working"
			w.BlockedSince = time.Time{}
			// Restore the PreToolUse-written pending tool (the real Bash
			// call) so a later permission prompt describes it, not us.
			w.PendingTool, w.PendingToolDetail, w.PendingToolAt = prevTool, prevDetail, prevAt
		}); err != nil {
			logging.Log("hook-approval: windowstate.Update(restore) error: %v", err)
		}
	}
}

// runPopupWithDeadline runs the popup under popupDeadline and hook-cancelling
// signals. Returns (approved, decided, denyReason) like the gates: a non-empty
// denyReason means the popup went unanswered (or the hook was cancelled), the
// popup was closed, and the command must be denied.
func runPopupWithDeadline(kind, logPrefix string, popupW, popupH int, argsFile string, notice popupNotice) (approved, decided bool, denyReason string) {
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	ctx, cancel := context.WithTimeout(sigCtx, popupDeadline)
	defer cancel()

	end := beginApproval(notice)
	defer end()

	start := time.Now()
	err := dispatchPopupRunner(ctx, popupW, popupH, argsFile)
	elapsed := time.Since(start).Round(time.Millisecond)
	if ctx.Err() != nil {
		// Killing the tmux client does not necessarily close the popup.
		if cerr := popupCloser(); cerr != nil {
			logging.Log("%s: failed to close popup: %v", logPrefix, cerr)
		}
		outcome := "timeout"
		if sigCtx.Err() != nil {
			outcome = "cancelled by signal"
		}
		logging.Log("%s: popup outcome=%s after %s (%v), denying", logPrefix, outcome, elapsed, ctx.Err())
		return false, false, popupTimeoutReason(kind, popupDeadline)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code := exitErr.ExitCode()
			if code == 1 {
				logging.Log("%s: popup outcome=denied after %s", logPrefix, elapsed)
				return false, true, ""
			}
			logging.Log("%s: popup outcome=other exit code=%d after %s, passing through", logPrefix, code, elapsed)
			return false, false, ""
		}
		logging.Log("%s: popup outcome=error after %s: %v, passing through", logPrefix, elapsed, err)
		return false, false, ""
	}
	logging.Log("%s: popup outcome=approved after %s", logPrefix, elapsed)
	return true, true, ""
}
