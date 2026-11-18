package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/shellquote"
	"github.com/blsmth/agentctl/internal/tmux"
)

// RunLogs displays a formatted session log report for the named agent or window
// in a temporary tmux window using less.
func RunLogs(nameOrID string) {
	// Resolve window ID (e.g. "@3") to agent name, or strip prefixes from window name.
	name := nameOrID
	if strings.HasPrefix(nameOrID, "@") {
		// Try windowstate first for fast resolution without a tmux fork.
		if _, base := fleet.LocalWindowNames(nameOrID); base != "" {
			name = base
		}
		if name == nameOrID {
			// Record miss — fall back to tmux.
			if resolved := tmux.ResolveAgentByWindowID(nameOrID); resolved != "" {
				name = resolved
			}
		}
	} else {
		name = tmux.StripAgentPrefix(name)
	}
	baseName := tmux.BaseAgentName(name)

	if name == "" || name == "shell" {
		tmux.DisplayMessage("Switch to an agent window first")
		return
	}

	sess, err := findRecentSessions(baseName)
	if err != nil || len(sess) == 0 {
		tmux.DisplayMessage(fmt.Sprintf("No sessions found for %s", baseName))
		return
	}

	report := buildReport(baseName, sess)
	tmpFile, err := os.CreateTemp("", "agentctl-log-*.txt")
	if err != nil {
		return
	}
	tmpFile.WriteString(report)
	tmpFile.Close()

	tmux.NewTempWindow("logs-"+baseName, lessThenRemove(tmpFile.Name()))
}

func findRecentSessions(agentName string) ([]sessions.Session, error) {
	return sessions.FindSessionsForAgent(agentName, 5)
}

func buildReport(agentName string, sess []sessions.Session) string {
	return sessions.BuildReport(agentName, sess)
}

// lessThenRemove returns the shell command that pages path with less and then
// deletes it. path is shell-quoted: TMPDIR may contain spaces or
// metacharacters.
func lessThenRemove(path string) string {
	q := shellquote.Quote(path)
	return "less -R " + q + "; rm -f " + q
}
