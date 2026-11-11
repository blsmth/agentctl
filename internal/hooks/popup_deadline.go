package hooks

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
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

// runPopupWithDeadline runs the popup under popupDeadline and hook-cancelling
// signals. Returns (approved, decided, denyReason) like the gates: a non-empty
// denyReason means the popup went unanswered (or the hook was cancelled), the
// popup was closed, and the command must be denied.
func runPopupWithDeadline(kind, logPrefix string, popupW, popupH int, argsFile string) (approved, decided bool, denyReason string) {
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	ctx, cancel := context.WithTimeout(sigCtx, popupDeadline)
	defer cancel()

	err := dispatchPopupRunner(ctx, popupW, popupH, argsFile)
	if ctx.Err() != nil {
		// Killing the tmux client does not necessarily close the popup.
		if cerr := popupCloser(); cerr != nil {
			logging.Log("%s: failed to close popup: %v", logPrefix, cerr)
		}
		logging.Log("%s: popup unanswered (%v), denying", logPrefix, ctx.Err())
		return false, false, popupTimeoutReason(kind, popupDeadline)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code := exitErr.ExitCode()
			logging.Log("%s: popup exit code=%d", logPrefix, code)
			if code == 1 {
				return false, true, ""
			}
		}
		logging.Log("%s: popup error: %v, passing through", logPrefix, err)
		return false, false, ""
	}
	return true, true, ""
}
