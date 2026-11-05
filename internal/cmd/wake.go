package cmd

// wake.go implements `agentctl wake <addr>` -- the explicit send-keys wake
// path for an agent that has gone fully idle (see
// docs/agent-messaging-design.md §3's "known gap"). PostToolUse and
// UserPromptSubmit deliver mail in-band while an agent is working or a
// human is typing, but nothing fires either hook for a window that has
// already gone idle and is just sitting there -- mail queued for it would
// otherwise sit pending until something else wakes the agent up. For an
// idle agent, send-keys IS the delivery: it injects the message as a fresh
// prompt, exactly like a human typing it in. That is the one case where
// send-keys is both safe (nothing is streaming) and necessary (no hook will
// ever fire on its own).

import (
	"fmt"
	"os"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/reaper"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// Seams for testing -- these shell out to real tmux/ps in production; tests
// substitute fakes rather than spawning real processes or a real tmux
// session. Mirrors the clientDimensions pattern in
// internal/hooks/popup_clamp.go.
var (
	wakeMainPanePID  = tmux.MainPanePID
	wakeHasClaude    = reaper.HasClaudeDescendant
	wakeInjectPrompt = tmux.InjectPromptToWindow
	wakeCheckInject  = tmux.CheckWindowInjectable
)

// RunWake implements `agentctl wake <addr>`. All decision logic lives in
// doWake, which returns rather than exits, so it can be unit tested; this
// wrapper is the only place that prints and calls os.Exit, matching
// RunSend's style in this package.
func RunWake(addr string) {
	msg, err := doWake(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(msg)
}

// doWake resolves addr, applies the two safety refusals (working, no live
// Claude process), and either delivers pending mail via send-keys or
// reports there was nothing to deliver. It returns the message RunWake
// should print on success, or an error RunWake should print to stderr
// before exiting nonzero.
func doWake(addr string) (message string, err error) {
	windowID, err := resolveSendAddress(addr, tmux.RunningAgents())
	if err != nil {
		return "", err
	}

	ws, err := windowstate.Read(windowID)
	if err != nil {
		return "", fmt.Errorf("reading state for window %s: %w", windowID, err)
	}

	// Refuse when working: this is the single most important safety rule
	// here. Injecting keystrokes into Claude Code's input box while it is
	// mid-stream types into a box that is actively being redrawn and
	// corrupts it -- there is no safe way to wake a working agent, because
	// "working" means it isn't waiting for input in the first place.
	if ws.State == "working" {
		return "", fmt.Errorf(
			"%s is still working -- wait for it to go idle before waking it.\n"+
				"Injecting text while Claude Code is streaming a response corrupts its input box",
			windowID)
	}

	// Liveness check: a live claude process must actually be running under
	// the pane, or injecting text would just type the message into a bare
	// shell prompt and execute it as a command. Deliberately NOT
	// #{pane_current_command} -- Claude runs as a *child*
	// of the pane's shell, so every agentctl pane reports "zsh" regardless
	// of whether Claude is alive underneath it. That exact mistake is what
	// broke tmux send-keys delivery for months (see
	// docs/agent-messaging-design.md §3).
	pid, ok := wakeMainPanePID(windowID)
	if !ok || !wakeHasClaude(pid) {
		return "", fmt.Errorf(
			"no live Claude process found in %s's pane -- it looks like the agent has exited to a shell.\n"+
				"Waking it would type the message text into that shell and execute it as a command",
			windowID)
	}

	// Refuse BEFORE claiming mail: if a permission dialog is up (or the pane
	// can't be read), typing into it would approve an unreviewed tool call and
	// lose the message. Leaving the mail unclaimed lets a later wake deliver it.
	if err := wakeCheckInject(windowID); err != nil {
		return "", fmt.Errorf("not delivering to %s, mail left pending: %w", windowID, err)
	}

	body, ids := mailbox.DrainForIDs(windowID, ws.SessionID, mailbox.DefaultTTL)
	if len(ids) == 0 {
		return fmt.Sprintf("No pending mail for %s -- nothing to deliver.", windowID), nil
	}

	if err := wakeInjectPrompt(windowID, body); err != nil {
		// DrainForIDs already claimed (moved to read/) every one of these
		// messages before returning them -- that's load-bearing for the
		// hook-driven delivery paths, which would otherwise re-deliver the
		// same mail on every subsequent tool call. It means a failure right
		// here loses the mail for good unless it's recovered from this log
		// line, which is why the IDs and the full body are both logged at
		// Warn rather than just the error.
		logging.Warn("wake: injection into window=%s failed after claiming %d message(s) ids=%v: %v\nbody:\n%s",
			windowID, len(ids), ids, err, body)
		return "", fmt.Errorf("failed to inject message into %s: %w", windowID, err)
	}

	return fmt.Sprintf("Delivered %d message(s) to %s.", len(ids), windowID), nil
}
