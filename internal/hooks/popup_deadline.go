package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// closeDisplayPopup runs `tmux display-popup -C` on the client the popup was
// opened on (empty client: tmux's default, matching an open without -c).
func closeDisplayPopup(client string) error {
	args := append([]string{"display-popup"}, popupTarget{Client: client}.clientArgs()...)
	return exec.Command("tmux", append(args, "-C")...).Run()
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
	// Remote, when set, also offers the approval to the phone through
	// agentctl serve (see raceRemote). nil means the popup alone.
	Remote *remoteDetails
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

	if stateSet && ws.SpawnedBy != "" && prevState != "blocked" {
		emitBlockedMessage(windowID, ws, n.Agent)
	}
	title, titled := alertTitle(ws, n.Agent, windowID)
	// notify.NotifyWithOptions is fire-and-forget, so this does not delay
	// the popup.
	sendAlert(n.Agent, approvalNotificationType, notify.Options{
		Title:    title,
		Subtitle: alertSubtitle("Approval needed", titled, n.Agent, strings.TrimPrefix(sessionName, "agentctl-")),
		Message:  approvalMessage(n),
		Env: map[string]string{
			"AGENTCTL_AGENT_NAME":        n.Agent,
			"AGENTCTL_NOTIFICATION_TYPE": approvalNotificationType,
			"AGENTCTL_WINDOW_ID":         windowID,
			"AGENTCTL_TOOL_NAME":         n.Tool,
			"AGENTCTL_SESSION_NAME":      sessionName,
			"AGENTCTL_SWITCH_TARGET":     tmuxSwitchTarget(sessionName, windowID),
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

// popupRetryInterval is how long to wait before re-opening a popup that did
// not run because another popup or menu was already open on the client. A var
// so tests can shrink it.
var popupRetryInterval = 750 * time.Millisecond

// popupEnd is how the tmux popup loop ended.
type popupEnd int

const (
	// popupVerdict: approved/decided carry the human's answer, or a deny
	// for a popup that ran and failed (exit 1, no result).
	popupVerdict popupEnd = iota
	// popupPassThrough: the popup could not be shown; without another
	// answer source the gate stays silent.
	popupPassThrough
	// popupExpired: the context ended first. popupOpen says whether a popup
	// may still be on screen and must be closed.
	popupExpired
)

type popupResult struct {
	end               popupEnd
	approved, decided bool
	popupOpen         bool
}

// runPopupWithDeadline runs the popup under popupDeadline and hook-cancelling
// signals. Returns (approved, decided, denyReason) like the gates: a non-empty
// denyReason means the popup went unanswered (or the hook was cancelled), the
// popup was closed, and the command must be denied.
//
// The verdict comes only from the result file the popup process writes
// (popupResultPath(argsFile)), never from display-popup's exit code: tmux
// returns 0 without running the command when the client already has a popup or
// menu open. No result file therefore means the popup never ran, and it is
// re-opened until it does or the deadline expires.
//
// When notice.Remote is set and agentctl serve is running (and passes
// remoteapproval.Guard), the approval is also opened on the phone and the
// first answer wins; see raceRemote. Otherwise behavior is exactly the popup
// alone.
func runPopupWithDeadline(target popupTarget, kind, logPrefix string, popupW, popupH int, argsFile string, notice popupNotice) (approved, decided bool, denyReason string) {
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	ctx, cancel := context.WithTimeout(sigCtx, popupDeadline)
	defer cancel()

	end := beginApproval(notice)
	defer end()

	resultFile := popupResultPath(argsFile)
	defer os.Remove(resultFile)

	begin := time.Now()
	deny := func(closePopup bool) (bool, bool, string) {
		if closePopup {
			// Killing the tmux client does not necessarily close the popup.
			if cerr := popupCloser(target.Client); cerr != nil {
				logging.Log("%s: failed to close popup: %v", logPrefix, cerr)
			}
		}
		outcome := "timeout"
		if sigCtx.Err() != nil {
			outcome = "cancelled by signal"
		}
		logging.Log("%s: approval outcome=%s source=timeout after %s (%v), denying",
			logPrefix, outcome, time.Since(begin).Round(time.Millisecond), ctx.Err())
		return false, false, popupTimeoutReason(kind, popupDeadline)
	}
	run := func(c context.Context) popupResult {
		return runPopupLoop(c, target, logPrefix, popupW, popupH, argsFile)
	}

	if remote := startRemote(notice.Remote, logPrefix, begin.Add(popupDeadline)); remote != nil {
		return raceRemote(ctx, target, logPrefix, remote, run, deny)
	}

	pr := run(ctx)
	switch pr.end {
	case popupVerdict:
		logging.Log("%s: approval source=laptop approved=%v", logPrefix, pr.approved)
		return pr.approved, pr.decided, ""
	case popupPassThrough:
		return false, false, ""
	}
	return deny(pr.popupOpen)
}

// runPopupLoop shows the popup until it records a verdict, fails, or ctx ends.
func runPopupLoop(ctx context.Context, target popupTarget, logPrefix string, popupW, popupH int, argsFile string) popupResult {
	resultFile := popupResultPath(argsFile)
	for attempt := 1; ; attempt++ {
		os.Remove(resultFile) // a stale verdict must never be read
		start := time.Now()
		err := dispatchPopupRunner(ctx, target.Client, popupW, popupH, argsFile)
		elapsed := time.Since(start).Round(time.Millisecond)

		switch readPopupResult(resultFile) {
		case "allow":
			logging.Log("%s: popup outcome=approved after %s", logPrefix, elapsed)
			return popupResult{end: popupVerdict, approved: true, decided: true}
		case "deny":
			logging.Log("%s: popup outcome=denied after %s", logPrefix, elapsed)
			return popupResult{end: popupVerdict, decided: true}
		}
		if ctx.Err() != nil {
			return popupResult{end: popupExpired, popupOpen: true}
		}
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				switch code := exitErr.ExitCode(); code {
				case 1:
					// The popup ran and exited 1 but recorded nothing (a
					// startup error); retrying would loop on a broken popup.
					logging.Log("%s: popup exited 1 without a verdict after %s, denying", logPrefix, elapsed)
					return popupResult{end: popupVerdict, decided: true}
				default:
					logging.Log("%s: popup outcome=other exit code=%d after %s, passing through", logPrefix, code, elapsed)
					return popupResult{end: popupPassThrough}
				}
			}
			logging.Log("%s: popup outcome=error after %s: %v, passing through", logPrefix, elapsed, err)
			return popupResult{end: popupPassThrough}
		}

		// No verdict: the popup never ran (another popup or menu was open).
		logging.Log("%s: popup did not run (no result, exit err=%v after %s), retrying in %s (attempt %d)",
			logPrefix, err, elapsed, popupRetryInterval, attempt)
		select {
		case <-ctx.Done():
			return popupResult{end: popupExpired}
		case <-time.After(popupRetryInterval):
		}
	}
}
