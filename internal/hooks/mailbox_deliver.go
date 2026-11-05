package hooks

// mailbox_deliver.go wires mailbox.DrainFor into the two hooks that can
// reach a running Claude Code session in-band: PostToolUse (the primary
// path -- it fires on every tool call, so it reaches a *working* agent) and
// UserPromptSubmit (covers interactive/bare-mode windows, which the old
// tmux send-keys design excluded entirely). See
// docs/agent-messaging-design.md §3.
//
// The prior delivery mechanism (tmux send-keys from the monitor) never
// delivered a single message in production -- see the removed
// monitor_mailbox.go's shouldDeliver gate, which required a live tmux pane
// running "claude"/"node" but every agentctl pane reports "zsh" (claude
// runs as a child of the pane's shell). This replaces it with output a hook
// writes directly to its own stdout, which Claude Code injects as
// additionalContext -- no tmux pane inspection required.

import (
	"encoding/json"
	"os"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/mailbox"
)

// deliverMailbox drains windowID's pending mailbox (scoped to sessionID via
// mailbox.DrainFor) and, if there is anything to deliver, writes the
// hookSpecificOutput JSON to stdout so Claude Code injects the coalesced
// body as additionalContext for eventName ("PostToolUse" or
// "UserPromptSubmit" -- must exactly match the firing hook's event name).
// Writes nothing at all when there is no mail: this runs on the hottest
// hook path in the whole system (every tool call, every prompt), and
// emitting an empty hookSpecificOutput on every invocation would be noise
// Claude Code has to parse for no benefit.
//
// A mailbox failure must never break the hook that called it -- recovers
// from any panic in the drain and logs instead of propagating, so a bug in
// mailbox delivery can never turn into a broken tool call or a stuck
// prompt submission.
func deliverMailbox(eventName, windowID, sessionID string) {
	defer func() {
		if r := recover(); r != nil {
			logging.Error("hook-mailbox-deliver: panic window=%s event=%s: %v", windowID, eventName, r)
		}
	}()

	if windowID == "" {
		return
	}

	body, n := mailbox.DrainFor(windowID, sessionID, mailbox.DefaultTTL)
	if n == 0 {
		return
	}

	output := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     eventName,
			"additionalContext": body,
		},
	}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		logging.Warn("hook-mailbox-deliver: encode output window=%s event=%s: %v", windowID, eventName, err)
		return
	}
	logging.Log("hook-mailbox-deliver: delivered %d message(s) to window=%s via %s", n, windowID, eventName)
}
