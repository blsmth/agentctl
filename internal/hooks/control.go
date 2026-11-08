package hooks

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// CheckDone is called by Claude Code's Stop hook.
// For ticket-launched agents, it verifies the work is complete before allowing
// the agent to stop. Checks: git status clean, build passes.
// Exit 2 = force continuation. Exit 0 = allow stop.
func CheckDone() {
	if !guardAgentctl() {
		return
	}

	doneTimer := logging.Timing("hook/check-done")
	defer doneTimer()

	var input struct {
		LastAssistantMessage string `json:"last_assistant_message"`
		StopHookActive       bool   `json:"stop_hook_active"`
	}
	if !decodeInput(&input) {
		return
	}

	// Set idle immediately -- the turn's text output is complete, so stop the
	// spinner. If checks below fail (exit 2), UserPromptSubmit will set working
	// again when the continuation turn starts.
	// The state snapshot is captured inside the Update closure (after State is
	// mutated but still reflecting everything Read() loaded, including
	// SpawnedBy/SpawnedBySession/Mode) so the auto-emit decision below doesn't
	// need a second windowstate.Read.
	windowID := resolveHookWindowID()
	var ws windowstate.WindowState
	if windowID != "" {
		if err := windowstate.Update(windowID, func(w *windowstate.WindowState) {
			w.State = "idle"
			ws = *w
		}); err != nil {
			logging.Log("hook-check-done: windowstate.Update error: %v", err)
		}
		logging.Log("hook-check-done: set state=idle window=%s", windowID)
	}

	// Avoid infinite loops: if a stop hook already ran this turn, allow stop.
	if input.StopHookActive {
		logging.Log("hook-check-done: stop_hook_active=true, allowing stop to prevent loop")
		return
	}

	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	logging.Log("hook-check-done: agent=%s", agentName)

	blockReason := computeBlockReason(ws.Mode, windowID, agentName)
	if blockReason != "" {
		fmt.Fprint(os.Stderr, blockReason)
		os.Exit(2)
	}

	logging.Log("hook-check-done: agent=%s all checks passed, allowing stop", agentName)

	// Mark the window Done=true on a genuine turn-end completion. This is a
	// second windowstate.Update (the first, above, only sets State=idle):
	// blockReason isn't known until checkDoneReason runs, which happens
	// after that first Update, so Done cannot be folded into it.
	if shouldMarkDone(ws.Mode, input.StopHookActive, blockReason) {
		if windowID != "" {
			if err := windowstate.Update(windowID, func(w *windowstate.WindowState) {
				w.Done = true
			}); err != nil {
				logging.Log("hook-check-done: windowstate.Update(Done) error: %v", err)
			} else {
				logging.Log("hook-check-done: set done=true window=%s agent=%s", windowID, agentName)
			}
		}
	}

	// Auto-emit a "done" message to whoever dispatched this window (see
	// docs/agent-messaging-design.md §4). This only runs on the genuine
	// turn-end path: input.StopHookActive and blockReason are both
	// guaranteed false/"" here (we would have returned/exited above
	// otherwise), but shouldEmitDone still takes them as explicit
	// parameters so every branch stays table-driven testable without
	// re-deriving them.
	if shouldEmitDone(ws.SpawnedBy, ws.Mode, input.StopHookActive, blockReason) {
		emitDoneMessage(windowID, ws, agentName, input.LastAssistantMessage)
	}

	// Fire user lifecycle hooks (async, non-blocking). Only fires when all
	// built-in checks pass -- not when exit 2 is about to be called above.
	runUserHooks("stop", map[string]string{
		"AGENTCTL_AGENT_NAME": agentName,
		"AGENTCTL_WINDOW_ID":  windowID,
		"AGENTCTL_TICKET":     os.Getenv("AGENTCTL_TICKET"),
		"AGENTCTL_MODE":       os.Getenv("AGENTCTL_MODE"),
	})
}

// autonomousModeFn decides whether mode is autonomous (dispatch-like: one
// kickoff prompt, nobody types after it, so turn-end IS task-end). Production
// code wires this to dispatch.IsAutonomous (see main.go's hook fast path).
// It cannot be called directly here: internal/dispatch already imports
// internal/hooks (for EnsureShellPrefixScript), so a direct import in the
// other direction would be a circular dependency. The indirection is the
// least invasive way to satisfy "reuse dispatch.IsAutonomous, don't invent a
// second predicate" without restructuring package boundaries.
//
// Defaults to "not autonomous" -- the safe answer -- so a hook invoked before
// SetAutonomousModeFn runs (or a test that forgets to call it) never
// over-emits into an interactive window.
var autonomousModeFn = func(mode string) bool { return false }

// SetAutonomousModeFn overrides the mode-autonomy predicate used by
// shouldEmitDone. Production code calls this once at startup with
// dispatch.IsAutonomous; tests inject a stub.
func SetAutonomousModeFn(fn func(mode string) bool) {
	autonomousModeFn = fn
}

// shouldMarkDone is the pure decision for whether a Stop hook invocation
// represents a genuine turn-end completion: autonomous mode, not a
// stop-hook-active retry, and not blocked by checkDoneReason. It
// deliberately does NOT require SpawnedBy -- a dispatched window with no
// parent (e.g. launched with nobody tracking it) still deserves the ✓
// done-unseen badge and still needs HandleComplete to fire.
func shouldMarkDone(mode string, stopHookActive bool, blockReason string) bool {
	if stopHookActive {
		// A blocked-stop retry, not a genuine completion.
		return false
	}
	if blockReason != "" {
		// checkDoneReason blocked the stop (exit 2 path) -- not done.
		return false
	}
	return autonomousModeFn(mode)
}

// shouldEmitDone is the pure decision for whether CheckDone should auto-emit
// a mailbox "done" message to the window's dispatching parent. It has no
// hooks/tmux/filesystem dependency, so every branch is independently
// testable without invoking CheckDone (which can os.Exit(2)). It is exactly
// shouldMarkDone plus the additional requirement of a parent to report to.
func shouldEmitDone(spawnedBy, mode string, stopHookActive bool, blockReason string) bool {
	return shouldMarkDone(mode, stopHookActive, blockReason) && spawnedBy != ""
}

// maxDoneMessageBody caps the length of the done message body. The body is
// the turn's last_assistant_message, which can be long; it is truncated with
// textutil.Truncate.
const maxDoneMessageBody = 4000

// emitDoneMessage enqueues a "done" mailbox message addressed to ws.SpawnedBy
// (paired with ws.SpawnedBySession so the monitor's session-verification
// check can discard it if the window has since been recycled -- see
// docs/agent-messaging-design.md §1). Best-effort: any failure is logged and
// swallowed, never surfaced to the caller, since a mailbox problem must never
// affect whether the agent is allowed to stop.
func emitDoneMessage(windowID string, ws windowstate.WindowState, agentName, lastAssistantMessage string) {
	body := textutil.Truncate(strings.TrimSpace(lastAssistantMessage), maxDoneMessageBody, "...")
	if body == "" {
		body = fmt.Sprintf("%s finished its turn with no final message.", agentName)
	}
	_, err := deps.EnqueueMailboxMessage(mailbox.Message{
		From:      windowID,
		To:        ws.SpawnedBy,
		ToSession: ws.SpawnedBySession,
		Kind:      mailbox.KindDone,
		Subject:   fmt.Sprintf("%s done", agentName),
		Body:      body,
	})
	if err != nil {
		logging.Warn("hook-check-done: mailbox enqueue(done) window=%s spawned_by=%s: %v", windowID, ws.SpawnedBy, err)
		return
	}
	logging.Log("hook-check-done: enqueued done message window=%s -> %s", windowID, ws.SpawnedBy)
}

// computeBlockReason is the gating decision for whether CheckDone should
// invoke the completion checks (checkDoneReason) at all. The checks
// (uncommitted changes / build failure) only make sense for autonomous
// (dispatch-like) agents that were sent to produce committed work: one
// kickoff prompt, nobody types after it, so turn-end IS task-end. An
// interactive/bare agent working in a repo that simply has uncommitted
// files must be allowed to stop -- forcing continuation here traps it in a
// loop and, worse, prevents Done from ever being set since shouldMarkDone
// requires blockReason == "". Extracted so the gate can be exercised without
// os.Exit -- CheckDone (the real caller) still owns the exit(2) side effect.
func computeBlockReason(mode, windowID, agentName string) string {
	if !autonomousModeFn(mode) {
		logging.Log("hook-check-done: skipping completion checks (non-autonomous mode=%q) window=%s", mode, windowID)
		return ""
	}
	return checkDoneReason(agentName)
}

// checkDoneReason runs the pre-stop validation checks (git clean, build passes)
// and returns a non-empty human-readable block reason if the agent should be
// prevented from stopping. Returns "" when all checks pass.
// Extracted from CheckDone so the logic can be unit-tested without os.Exit.
func checkDoneReason(agentName string) string {
	// Check for uncommitted changes -- agent may have forgotten to commit.
	gitStatus, err := deps.RunCommand("git", "--no-optional-locks", "status", "--porcelain", "-uno")
	if err == nil && len(strings.TrimSpace(string(gitStatus))) > 0 {
		logging.Log("hook-check-done: BLOCKED agent=%s reason=uncommitted_changes", agentName)
		return fmt.Sprintf("You have uncommitted changes. Please review and commit your work before stopping:\n%s", string(gitStatus))
	}

	// Check if the build passes (Go projects only).
	if _, err := deps.StatFile("go.mod"); err == nil {
		buildOut, buildErr := deps.RunCommand("go", "build", "./...")
		if buildErr != nil {
			logging.Log("hook-check-done: BLOCKED agent=%s reason=build_failure", agentName)
			return fmt.Sprintf("Build fails. Please fix compilation errors before stopping:\n%s", string(buildOut))
		}
	}

	return ""
}

// StopFailure is called by Claude Code's StopFailure hook.
// Fires when a turn ends due to API error. Sets state=idle and alerts the user.
func StopFailure() {
	if !guardAgentctl() {
		return
	}

	doneTimer := logging.Timing("hook/stop-failure")
	defer doneTimer()

	var input struct {
		ErrorType    string `json:"error_type"`
		ErrorMessage string `json:"error_message"`
	}
	decodeInput(&input) // non-fatal: ignore return value

	agentName := resolveAgentName()

	windowID := resolveHookWindowID()
	if windowID != "" {
		errType := input.ErrorType
		if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
			ws.State = "idle"
			ws.Error = errType
		}); err != nil {
			logging.Log("hook-stop-failure: windowstate.Update error: %v", err)
		} else {
			logging.Log("hook-stop-failure: set state=idle error=%s window=%s agent=%s", errType, windowID, agentName)
		}
	}

	// Map error type to human-friendly message.
	var message string
	switch input.ErrorType {
	case "rate_limit":
		message = "Rate limited"
	case "authentication_failed":
		message = "Authentication failed"
	case "billing_error":
		message = "Billing error"
	case "server_error":
		message = "Server error"
	case "max_output_tokens":
		message = "Hit max output tokens"
	case "invalid_request":
		message = "Invalid request"
	default:
		if input.ErrorType != "" {
			message = fmt.Sprintf("API error: %s", input.ErrorType)
		} else {
			message = "API error"
		}
	}

	logging.Log("hook-stop-failure: firing alert agent=%s error=%s", agentName, input.ErrorType)
	sendAlert(agentName, "stop_failure", notify.Options{
		Title:   agentName,
		Message: message,
		Env: map[string]string{
			"AGENTCTL_AGENT_NAME":        agentName,
			"AGENTCTL_NOTIFICATION_TYPE": "stop_failure",
			"AGENTCTL_WINDOW_ID":         windowID,
		},
	})

	// Fire user lifecycle hooks (async, non-blocking).
	runUserHooks("stop_failure", map[string]string{
		"AGENTCTL_AGENT_NAME":    agentName,
		"AGENTCTL_WINDOW_ID":     windowID,
		"AGENTCTL_ERROR_TYPE":    input.ErrorType,
		"AGENTCTL_ERROR_MESSAGE": input.ErrorMessage,
	})
}

// UserPrompt is called by Claude Code's UserPromptSubmit hook.
// Sets @agentctl_state=working immediately when the user submits a prompt,
// eliminating the gap between "user presses enter" and "first PreToolUse fires".
func UserPrompt() {
	if !guardAgentctl() {
		return
	}

	// Consume stdin -- we don't need the prompt text.
	decodeInput(&struct{}{}) // non-fatal: ignore return value

	monitorWatchdog()

	windowID := resolveHookWindowID()
	if windowID == "" {
		logging.Log("hook-user-prompt: cannot determine window ID, skipping")
		return
	}

	// Reset last_tool_time to now so the 5s stuck-state heuristic gets a
	// fresh clock reference for this new prompt turn. A new user turn also
	// means the window is no longer done -- Done is a per-turn fact, cleared
	// here and re-set (if warranted) by CheckDone at the next turn's end.
	nowUnix := time.Now().Unix()
	var ws windowstate.WindowState
	if err := windowstate.Update(windowID, func(w *windowstate.WindowState) {
		w.State = "working"
		w.LastToolTime = nowUnix
		w.Done = false
		ws = *w
	}); err != nil {
		logging.Log("hook-user-prompt: windowstate.Update error: %v", err)
	}

	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	logging.Log("hook-user-prompt: set state=working window=%s agent=%s", windowID, agentName)

	// Deliver any pending mailbox messages as additionalContext.
	// UserPromptSubmit covers interactive/bare-mode windows, which the old
	// tmux send-keys delivery design (removed) excluded entirely since it
	// only ever fired into windows it believed were running Claude
	// autonomously (see docs/agent-messaging-design.md §3).
	deliverMailbox("UserPromptSubmit", windowID, ws.SessionID)
}

// Notify is called by Claude Code's Notification hook.
// It fires macOS notifications immediately for permission prompts and idle events,
// bypassing the monitor's polling cycle for lower latency.
func Notify() {
	if !guardAgentctl() {
		return
	}

	doneTimer := logging.Timing("hook/notify")
	defer doneTimer()

	var input struct {
		NotificationType string `json:"notification_type"`
		Message          string `json:"message"`
	}
	if !decodeInput(&input) {
		return
	}

	if input.NotificationType != "permission_prompt" && input.NotificationType != "idle_prompt" {
		// Don't notify for other types (auth_success, etc.)
		logging.Log("hook-notify: ignoring type %q", input.NotificationType)
		return
	}

	agentName := resolveAgentName()

	// Set hook-driven state so the monitor picks it up instantly.
	// This must happen before alerter, which blocks until the user interacts with
	// the notification (potentially 20+ seconds). Without this ordering, the spinner
	// keeps spinning while the alert is pending.
	// Visual updates (window renaming, tab style) are owned by the Bubble Tea monitor view.
	windowID := resolveHookWindowID()
	var ws windowstate.WindowState
	// prevState is the window's State *before* this hook invocation mutates
	// it, captured inside the Update closure (which hands us the struct as
	// Read() loaded it, before fn's assignment below runs). It gates
	// shouldEmitBlocked on a genuine transition into "blocked" -- see
	// shouldEmitBlocked's doc comment for why this matters (dedup of the
	// per-invocation, not per-window-lifetime, permission_prompt firing).
	// The same prevState also gates BlockedSince: it is set only on the
	// transition into "blocked" so elapsed time accumulates across the
	// repeat firings of this hook while a prompt stays pending, instead of
	// resetting on every invocation.
	prevState := ""
	updateErr := error(nil)
	if windowID != "" {
		var newState string
		switch input.NotificationType {
		case "permission_prompt":
			newState = "blocked"
		case "idle_prompt":
			newState = "idle"
		}
		if newState != "" {
			updateErr = windowstate.Update(windowID, func(w *windowstate.WindowState) {
				prevState = w.State
				w.State = newState
				switch input.NotificationType {
				case "permission_prompt":
					if prevState != "blocked" {
						w.BlockedSince = time.Now()
					}
				case "idle_prompt":
					w.BlockedSince = time.Time{}
				}
				ws = *w
			})
			if updateErr != nil {
				logging.Log("hook-notify: windowstate.Update error: %v", updateErr)
			} else {
				logging.Log("hook-notify: set state=%s window=%s agent=%s", newState, windowID, agentName)
			}
		}
	}

	// Auto-emit a "blocked" message to whoever dispatched this window (see
	// docs/agent-messaging-design.md §4). Only for permission_prompt -- an
	// idle_prompt is not a blockage worth escalating to the parent. Fails
	// closed on a windowstate.Update error: if the prior state is unknown, a
	// missed notification is far cheaper than resuming the per-invocation
	// spam this gate exists to prevent.
	if updateErr == nil && shouldEmitBlocked(ws.SpawnedBy, input.NotificationType, prevState) {
		emitBlockedMessage(windowID, ws, agentName, input.Message)
	}

	sessionName := resolveInstanceSessionName()
	alert := buildAlertContent(input.NotificationType, agentName, sessionName, windowID, input.Message, ws, time.Now())

	logging.Log("hook-notify: firing alert type=%s agent=%s", input.NotificationType, agentName)
	sendAlert(agentName, input.NotificationType, notify.Options{
		Title:    alert.Title,
		Subtitle: alert.Subtitle,
		Message:  alert.Message,
		Env: map[string]string{
			"AGENTCTL_AGENT_NAME":        agentName,
			"AGENTCTL_NOTIFICATION_TYPE": input.NotificationType,
			"AGENTCTL_WINDOW_ID":         windowID,
			"AGENTCTL_TOOL_NAME":         ws.PendingTool,
			"AGENTCTL_TICKET":            ws.Ticket,
			"AGENTCTL_PROJECT":           paths.DirName(),
			"AGENTCTL_SESSION_NAME":      sessionName,
		},
	})

	// Fire user lifecycle hooks (async, non-blocking).
	runUserHooks("notification", map[string]string{
		"AGENTCTL_AGENT_NAME":        agentName,
		"AGENTCTL_WINDOW_ID":         windowID,
		"AGENTCTL_NOTIFICATION_TYPE": input.NotificationType,
	})
}

// pendingToolFreshWindow bounds how recent windowstate.PendingToolAt must be
// for buildAlertContent to still treat it as describing the CURRENT
// permission prompt. PreToolUse fires immediately before Claude Code shows
// the prompt, so a fresh value is almost certainly the tool being asked
// about; a stale one is leftover from an earlier, now-irrelevant call.
const pendingToolFreshWindow = 10 * time.Minute

// minBlockedDurationForDisplay is the minimum elapsed time before
// buildAlertContent appends a "blocked <duration>" fragment to the message.
// A block that has been pending for a few seconds isn't worth reporting an
// age for.
const minBlockedDurationForDisplay = time.Minute

// alertContent is the fully-assembled, platform-agnostic content of a
// desktop notification.
type alertContent struct {
	Title, Subtitle, Message string
}

// buildAlertContent assembles the title/subtitle/message for a Notify alert
// from agentctl's own state, since Claude Code's own payload carries no
// useful content for a permission prompt (its message field is the literal,
// constant string "Claude needs your permission" -- no tool name, no
// argument, see the design doc for this feature). Pure and unit-testable:
// all I/O (resolving sessionName, reading ws) is the caller's job.
//
// sessionName is the full tmux session name (e.g. "agentctl-acme"), resolved
// by the caller via resolveInstanceSessionName.
func buildAlertContent(notificationType, agentName, sessionName, windowID, fallbackMessage string, ws windowstate.WindowState, now time.Time) alertContent {
	// Title deliberately does NOT vary with fresh vs. stale pending-tool
	// state or with elapsed blocked time -- only with agent+instance -- so a
	// configured notify_cmd using `--group {{title}}` for notification
	// replacement keeps grouping correctly across repeat firings of the same
	// prompt, while still distinguishing this agentctl instance from others.
	displaySession := strings.TrimPrefix(sessionName, "agentctl-")
	title := agentName
	if displaySession != "" {
		title = agentName + " · " + displaySession
	}

	toolFresh := ws.PendingTool != "" && !ws.PendingToolAt.IsZero() && now.Sub(ws.PendingToolAt) <= pendingToolFreshWindow

	var subtitle string
	switch notificationType {
	case "permission_prompt":
		subtitle = "Needs permission"
		if toolFresh {
			subtitle += " — " + ws.PendingTool
		}
	case "idle_prompt":
		subtitle = "Idle — waiting for input"
	}

	var parts []string
	if toolFresh && ws.PendingToolDetail != "" {
		parts = append(parts, ws.PendingToolDetail)
	}
	switch {
	case ws.Ticket != "":
		parts = append(parts, "#"+ws.Ticket)
	case ws.Mode != "":
		parts = append(parts, ws.Mode)
	}
	if target := tmuxSwitchTarget(sessionName, windowID); target != "" {
		parts = append(parts, target)
	}
	if notificationType == "permission_prompt" && !ws.BlockedSince.IsZero() {
		if elapsed := now.Sub(ws.BlockedSince); elapsed >= minBlockedDurationForDisplay {
			parts = append(parts, "blocked "+compactDuration(elapsed))
		}
	}

	message := strings.Join(parts, " · ")
	if message == "" {
		message = fallbackMessage
	}
	if message == "" {
		// Message must never be empty -- an empty message means an empty
		// macOS banner body.
		message = subtitle
	}

	return alertContent{Title: title, Subtitle: subtitle, Message: message}
}

// tmuxSwitchTarget builds the copy-pasteable `tmux switch-client -t` target
// fragment for a notification: the full (unstripped) session name plus the
// window ID, e.g. "agentctl-acme:@61". Degrades to a bare windowID when
// sessionName is unavailable, and to "" when both are.
func tmuxSwitchTarget(sessionName, windowID string) string {
	switch {
	case sessionName != "" && windowID != "":
		return sessionName + ":" + windowID
	case windowID != "":
		return windowID
	default:
		return ""
	}
}

// compactDuration renders d as a short human string: "12m", "1h5m", "2d".
// Deliberately local to this file rather than folded into one of the
// repo's several existing near-duplicate duration formatters -- out of
// scope for this change (see spec).
func compactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalMinutes := int(d / time.Minute)
	days := totalMinutes / (24 * 60)
	hours := (totalMinutes / 60) % 24
	minutes := totalMinutes % 60

	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

// shouldEmitBlocked is the pure decision for whether Notify should auto-emit
// a mailbox "blocked" message to the window's dispatching parent. No mailbox
// message is worth sending for idle_prompt -- an agent waiting on its next
// prompt is not "blocked" in any sense the parent needs to hear about.
//
// prevState gates this to the *transition* into blocked, not merely "is this
// a permission_prompt": the Notification hook fires on every invocation
// while a prompt is pending, so gating on notificationType alone re-emits an
// identical message once per invocation for as long as the window stays
// stuck (observed: 18 byte-identical messages from one blocked window in a
// real incident). Only fire when prevState != "blocked" -- i.e. the window
// was NOT already blocked coming into this hook call.
//
// Accepted trade-off: a *different* permission prompt arriving while the
// window is already blocked is now silent too (prevState == "blocked"
// regardless of which prompt is pending). The parent already knows the child
// is blocked from the first message, so this is not treated as a regression
// worth message-content hashing to work around.
func shouldEmitBlocked(spawnedBy, notificationType, prevState string) bool {
	return spawnedBy != "" && notificationType == "permission_prompt" && prevState != "blocked"
}

// maxBlockedMessageBody caps the length of the blocked message body.
const maxBlockedMessageBody = 2000

// emitBlockedMessage enqueues a "blocked" mailbox message addressed to
// ws.SpawnedBy (paired with ws.SpawnedBySession, mirroring emitDoneMessage).
// Best-effort: any failure is logged and swallowed -- a mailbox problem must
// never affect notification delivery to the operator.
func emitBlockedMessage(windowID string, ws windowstate.WindowState, agentName, notifyMessage string) {
	body := fmt.Sprintf("%s is blocked on a permission prompt: %s", agentName, textutil.Truncate(strings.TrimSpace(notifyMessage), maxBlockedMessageBody, "..."))
	_, err := deps.EnqueueMailboxMessage(mailbox.Message{
		From:      windowID,
		To:        ws.SpawnedBy,
		ToSession: ws.SpawnedBySession,
		Kind:      mailbox.KindBlocked,
		Subject:   fmt.Sprintf("%s blocked", agentName),
		Body:      body,
	})
	if err != nil {
		logging.Warn("hook-notify: mailbox enqueue(blocked) window=%s spawned_by=%s: %v", windowID, ws.SpawnedBy, err)
		return
	}
	logging.Log("hook-notify: enqueued blocked message window=%s -> %s", windowID, ws.SpawnedBy)
}
