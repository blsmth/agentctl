package hooks

import (
	"fmt"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/panestate"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/winname"
)

// Launch watcher timing: one pane read a second, for at most
// launchWatchFor. Claude Code draws the trust dialog within a second or two
// of starting; the deadline covers a slow start (cold npm cache, a loaded
// machine) without leaving watchers around.
const (
	launchWatchFor   = 90 * time.Second
	launchWatchEvery = time.Second
	// launchWatchMaxFails is how many failed pane reads in a row end the
	// watch: the window (or the tmux server) is gone.
	launchWatchMaxFails = 2
)

// trustNotificationType is the sendAlert type for a window stopped on the
// folder-trust dialog. It is gated by alert_config.blocked.
const trustNotificationType = "trust_prompt"

// launchWatchSeams are the clock and tmux reads LaunchWatch needs, as vars so
// tests need no tmux and no real time.
var (
	lwNow     = time.Now
	lwSleep   = time.Sleep
	lwPaneOf  = func(windowID string) string { return tmux.DisplayTarget(windowID, "#{pane_id}") }
	lwCapture = func(pane string) (string, error) { return tmux.CapturePaneByIDANSI(pane, 0) }
	lwSession = func(pane string) string { return tmux.DisplayTarget(pane, "#{session_name}") }
)

// LaunchWatch is the body of the hidden `agentctl launch-watch <windowID>`
// (spawned by internal/launchwatch right after a launch). Claude Code asks
// whether to trust a new folder before it loads any hooks, so a window stuck
// on that dialog would otherwise sit as a quiet idle row. LaunchWatch reads
// the window's pane about once a second until the session starts (the
// window gets a SessionID), the window or its record goes away, or
// launchWatchFor passes. On the trust dialog it reports the window once
// (reportTrustPrompt) and returns.
//
// It never answers the dialog: trusting a folder is the human's call.
func LaunchWatch(windowID string) {
	deadline := lwNow().Add(launchWatchFor)
	pane := ""
	fails := 0
	for {
		if !windowstate.Exists(windowID) {
			logging.Debug("launch-watch: %s has no record, stopping", windowID)
			return
		}
		ws, err := windowstate.Read(windowID)
		if err == nil {
			if ws.SessionID != "" || ws.PendingTool == windowstate.PendingTrust {
				return
			}
			if pane == "" && tmux.IsPaneID(ws.MainPane) {
				pane = ws.MainPane
			}
		}
		if pane == "" {
			pane = lwPaneOf(windowID)
		}
		text, cerr := "", fmt.Errorf("no pane for %s", windowID)
		if tmux.IsPaneID(pane) {
			text, cerr = lwCapture(pane)
		}
		if cerr != nil {
			if fails++; fails >= launchWatchMaxFails {
				logging.Debug("launch-watch: %s: %v, stopping", windowID, cerr)
				return
			}
		} else {
			fails = 0
			if panestate.IsTrustDialog(text) {
				reportTrustPrompt(windowID, pane, trustFolder(text, ws))
				return
			}
		}
		if !lwNow().Before(deadline) {
			return
		}
		lwSleep(launchWatchEvery)
	}
}

// trustFolder is the folder the dialog names, or the window's own dir when
// the pane cut the path off.
func trustFolder(text string, ws windowstate.WindowState) string {
	if f := panestate.TrustFolder(text); f != "" && !strings.HasSuffix(f, "…") {
		return f
	}
	if ws.WorktreeDir != "" {
		return ws.WorktreeDir
	}
	return ws.WorkDir
}

// reportTrustPrompt marks windowID blocked on the folder-trust dialog
// (PendingTool windowstate.PendingTrust, the folder as its detail, ⏸ in the
// window name), then sends the blocked desktop notification and queues an
// agentctl notice for the dispatcher. The mark is made at most once: a
// window whose session already started, or already marked, is left alone
// and nothing is sent. The first hook of the session clears it
// (windowstate.ClearPendingTrust).
func reportTrustPrompt(windowID, pane, folder string) {
	marked := false
	var ws windowstate.WindowState
	_, _, err := winname.Transact(windowID, func(w *windowstate.WindowState) {
		if w.SessionID != "" || w.PendingTool == windowstate.PendingTrust {
			return
		}
		if w.State != "blocked" || w.BlockedSince.IsZero() {
			w.BlockedSince = lwNow()
		}
		w.State = "blocked"
		w.PendingTool, w.PendingToolDetail, w.PendingToolAt = windowstate.PendingTrust, folder, lwNow()
		marked = true
		ws = *w
	})
	if err != nil {
		logging.Warn("launch-watch: marking %s blocked on folder trust: %v", windowID, err)
		return
	}
	if !marked {
		return
	}
	logging.Log("launch-watch: %s is waiting on the folder trust prompt for %s", windowID, folder)
	agent := ws.AgentName
	sessionName := lwSession(pane)
	title, titled := alertTitle(ws, agent, windowID)
	sendAlert(agent, trustNotificationType, notify.Options{
		Title:    title,
		Subtitle: alertSubtitle("Trust this folder?", titled, agent, strings.TrimPrefix(sessionName, "agentctl-")),
		Message:  alertMessage("Claude Code is asking whether to trust " + folder + ". Answer it in the window."),
		Env: map[string]string{
			"AGENTCTL_AGENT_NAME":        agent,
			"AGENTCTL_NOTIFICATION_TYPE": trustNotificationType,
			"AGENTCTL_WINDOW_ID":         windowID,
			"AGENTCTL_TOOL_NAME":         windowstate.PendingTrust,
			"AGENTCTL_SESSION_NAME":      sessionName,
			"AGENTCTL_SWITCH_TARGET":     tmuxSwitchTarget(sessionName, windowID),
		},
	})
	if ws.SpawnedBy != "" {
		emitTrustMessage(windowID, ws, folder)
	}
}

// emitTrustMessage queues the agentctl notice telling the dispatcher its
// worker has not started and needs a human at the trust prompt.
func emitTrustMessage(windowID string, ws windowstate.WindowState, folder string) {
	agent := ws.AgentName
	if agent == "" {
		agent = "the agent"
	}
	body := fmt.Sprintf("agentctl notice: %s (window %s) has not started: it is waiting on Claude Code's folder trust prompt for %s. A human must answer it in that window.", agent, windowID, folder)
	if _, err := deps.EnqueueMailboxMessage(mailbox.Message{
		From:      windowID,
		To:        ws.SpawnedBy,
		ToSession: ws.SpawnedBySession,
		Kind:      mailbox.KindBlocked,
		Subject:   agent + " waiting on folder trust",
		Body:      body,
	}); err != nil {
		logging.Warn("launch-watch: mailbox enqueue(trust) window=%s spawned_by=%s: %v", windowID, ws.SpawnedBy, err)
	}
}
