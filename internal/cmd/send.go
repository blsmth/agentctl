package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/signal"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// resolveSendAddress resolves addr (a window ID like "@41", or an agent
// name) to a tmux window ID. runningAgents is the same shape as
// tmux.RunningAgents()'s return value (window ID -> stripped instance name)
// and is a parameter rather than a direct call so this stays a pure,
// tmux-free function for testing.
//
// If addr matches more than one running window's base agent name, this is an
// error listing every candidate window ID -- it deliberately does NOT
// silently pick the first match. That arbitrary-first-match behavior is the
// existing bug in tmux.InjectPromptToAgent and must not be replicated here.
func resolveSendAddress(addr string, runningAgents map[string]string) (string, error) {
	if strings.HasPrefix(addr, "@") {
		return addr, nil
	}

	var candidates []string
	for id, name := range runningAgents {
		if strings.EqualFold(tmux.BaseAgentName(name), addr) {
			candidates = append(candidates, id)
		}
	}
	sort.Strings(candidates)

	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("no running agent window matches %q", addr)
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("agent name %q is ambiguous, matches windows: %s", addr, strings.Join(candidates, ", "))
	}
}

// RunSend implements `agentctl send <addr> <message> [--kind fyi] [--subject
// "..."] [--wait-reply] [--timeout <dur>]`. --wait-reply is sugar: enqueue
// as normal, then perform exactly the same wait as `inbox --wait --from
// <addr>` (see mailbox.WaitAndClaim) -- an orchestrator sending a message
// and wanting to block for the reply no longer needs a separate `inbox
// --wait` call, let alone a `sleep 40` guess.
func RunSend(addr, message, kind, subject string, waitReply bool, timeout time.Duration) {
	if kind == "" {
		kind = mailbox.KindFYI
	}

	windowID, err := resolveSendAddress(addr, tmux.RunningAgents())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ws, wsErr := windowstate.Read(windowID)
	if wsErr != nil || ws.SessionID == "" {
		logging.Warn("send: target window %s has no recorded session ID; message will be enqueued without session verification", windowID)
	}
	// A window with no live windowstate at all (never ran a hook, or its
	// state file was removed) has nothing to deliver into: the message will
	// sit pending until a windowstate record eventually appears for that
	// window ID, if ever.
	if wsErr == nil && ws.UpdatedAt.IsZero() {
		fmt.Fprintf(os.Stderr, "Warning: window %s has no recorded state -- it may not be a live agentctl window; the message will remain queued until (and unless) one appears.\n", windowID)
	}

	m := mailbox.Message{
		From:      signal.ResolveWindowID(),
		To:        windowID,
		ToSession: ws.SessionID,
		Kind:      kind,
		Subject:   subject,
		Body:      message,
	}

	id, err := mailbox.Enqueue(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Delivery is in-band via Claude Code hooks (PostToolUse / UserPromptSubmit),
	// not immediate -- there is no tmux injection to report as already having
	// happened. Report honestly: the message is queued and will be delivered
	// the next time that window's hooks fire.
	fmt.Printf("Queued message %s for %s -- will be delivered at its next tool call or prompt submission.\n", id, linkWindow(windowID, windowID))

	// Notify only when the target will NOT get prompt delivery: PostToolUse
	// fires within one tool call while an agent is working, so a desktop
	// notification on top of that would be pure noise. A window that is
	// anything other than "working" (idle, blocked, gone, or has no
	// recorded state at all) has no hook about to fire on its own -- a
	// notification is the only way its human or dispatcher finds out mail
	// is waiting.
	if shouldNotifyOnEnqueue(ws.State) {
		senderName := m.From
		if running := tmux.RunningAgents(); running != nil {
			if name, ok := running[m.From]; ok {
				senderName = tmux.BaseAgentName(name)
			}
		}
		if senderName == "" {
			senderName = "agentctl"
		}
		notifyMsg := subject
		if notifyMsg == "" {
			notifyMsg = message
		}
		notify.Notify(fmt.Sprintf("Message from %s", senderName), notifyMsg, map[string]string{
			"AGENTCTL_AGENT_NAME":        windowID,
			"AGENTCTL_NOTIFICATION_TYPE": "mail",
		})
	}

	if !waitReply {
		return
	}

	if timeout <= 0 {
		timeout = mailbox.DefaultWaitTimeout
	}

	myWindowID := signal.ResolveWindowID()
	if myWindowID == "" {
		fmt.Fprintln(os.Stderr, "Error: --wait-reply requires a resolvable window ID for this window")
		os.Exit(1)
	}

	matched, timedOut, err := mailbox.WaitAndClaim(myWindowID, windowID, timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if timedOut {
		fmt.Printf("(timed out after %s, no messages)\n", timeout)
		return
	}
	printInboxTable(matched, nil)
}

// shouldNotifyOnEnqueue reports whether enqueuing a message to a window
// whose recorded windowstate.State is state should fire a desktop
// notification. It is a pure function so the notify gate is testable
// without a real notifier: PostToolUse/UserPromptSubmit deliver in-band
// within one tool call while an agent is "working", so notifying on top of
// that is pure noise; any other state (including the zero value for a
// window with no recorded state at all) has no hook about to fire on its
// own.
func shouldNotifyOnEnqueue(state string) bool {
	return state != "working"
}
