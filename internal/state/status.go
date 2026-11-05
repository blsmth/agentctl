package state

// AgentStatus represents the current status of a monitored agent window.
type AgentStatus int

const (
	StatusWorking AgentStatus = iota // log is growing, agent is busy
	StatusIdle                       // waiting for user input (last message was text)
	StatusBlocked                    // waiting for approval (last message was tool_use)
	StatusGone                       // window disappeared (session ended)
)

// String returns the lowercase string representation of the status.
func (s AgentStatus) String() string {
	switch s {
	case StatusWorking:
		return "working"
	case StatusIdle:
		return "idle"
	case StatusBlocked:
		return "blocked"
	case StatusGone:
		return "gone"
	}
	return "unknown"
}
