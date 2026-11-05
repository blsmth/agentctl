package state

// Transition describes a state change for an agent window.
type Transition struct {
	Agent          string // agent base name
	WindowID       string // tmux window ID
	From           AgentStatus
	To             AgentStatus
	Active         bool   // true if this window is currently focused
	LastToolName   string // e.g. "Bash", "Edit", "Write"
	LastToolDetail string // e.g. "go build ./...", "internal/tmux/tmux.go" (truncated ~80 chars)
	LastText       string // last assistant text snippet, truncated ~100 chars
	CtxPct         int    // context window usage percentage at transition time
	HookHandled    bool   // true if a hook already set this state (alert already fired)
	Done           bool   // true when agent explicitly signaled completion (not just idle)
	Mode           string // "dispatch", "discuss", "triage", "custom", ""
	TicketNumber   int    // from @agentctl_ticket, 0 if not set
	Branch         string // git branch for dispatch windows (e.g. "agent/go-engineer/issue-42")
	WorkDir        string // working directory of the agent pane
}
