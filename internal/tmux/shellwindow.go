package tmux

import (
	"fmt"
	"strings"
)

// ShellWindowName is the window name for plain shell windows. It is special-cased
// across agentctl (sweep never closes it, it is not an agent).
const ShellWindowName = "shell"

// newShellWindowArgs builds the argv for `tmux new-window` that opens a plain
// shell (no command: the user's default shell) in dir, detached, at the end of
// session. It prints the new window ID. The window env comes from the target
// tmux session, never from the calling process.
func newShellWindowArgs(session, dir string) []string {
	args := []string{
		"new-window", "-d", "-P", "-F", "#{window_id}",
		"-t", session + ":",
	}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	return append(args, "-n", ShellWindowName, "-e", "AGENTCTL=1")
}

// NewShellWindow opens a detached shell window named "shell" in session, rooted
// at dir, and returns its window ID.
func NewShellWindow(session, dir string) (string, error) {
	if session == "" || session == HubSessionName {
		return "", fmt.Errorf("refusing to open a shell in session %q", session)
	}
	out, err := tmuxRun(newShellWindowArgs(session, dir)...)
	if err != nil {
		return "", fmt.Errorf("opening shell window: %w", err)
	}
	return strings.TrimSpace(out), nil
}
