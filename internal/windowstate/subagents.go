package windowstate

import "time"

// SubagentStaleAfter bounds how long a recorded subagent counts as running. A
// lost SubagentStop hook must not pin a window as working forever.
const SubagentStaleAfter = 2 * time.Hour

// ActiveSubagentCount returns how many subagents are still running at now.
// Entries older than SubagentStaleAfter are ignored. When no IDs are tracked
// it falls back to the legacy SubagentCount, which is only driven by hooks
// that arrive without an agent_id.
func (ws WindowState) ActiveSubagentCount(now time.Time) int {
	if len(ws.ActiveSubagents) == 0 {
		if ws.SubagentCount < 0 {
			return 0
		}
		return ws.SubagentCount
	}
	n := 0
	for _, started := range ws.ActiveSubagents {
		if now.Sub(started) <= SubagentStaleAfter {
			n++
		}
	}
	return n
}

// StartSubagent records a running subagent. It is idempotent per id. An empty
// id falls back to the bare counter.
func (ws *WindowState) StartSubagent(id string, now time.Time) {
	if id == "" {
		ws.SubagentCount++
		return
	}
	if ws.ActiveSubagents == nil {
		ws.ActiveSubagents = map[string]time.Time{}
	}
	if _, ok := ws.ActiveSubagents[id]; !ok {
		ws.ActiveSubagents[id] = now
	}
	ws.pruneSubagents(now)
}

// StopSubagent removes a subagent. An empty id decrements the bare counter
// (floored at 0).
func (ws *WindowState) StopSubagent(id string, now time.Time) {
	if id == "" {
		if ws.SubagentCount > 0 {
			ws.SubagentCount--
		}
		return
	}
	delete(ws.ActiveSubagents, id)
	ws.pruneSubagents(now)
}

// ClearSubagents forgets every tracked subagent.
func (ws *WindowState) ClearSubagents() {
	ws.ActiveSubagents = nil
	ws.SubagentCount = 0
}

// pruneSubagents drops stale entries and re-syncs SubagentCount.
func (ws *WindowState) pruneSubagents(now time.Time) {
	for id, started := range ws.ActiveSubagents {
		if now.Sub(started) > SubagentStaleAfter {
			delete(ws.ActiveSubagents, id)
		}
	}
	if len(ws.ActiveSubagents) == 0 {
		ws.ActiveSubagents = nil
	}
	ws.SubagentCount = len(ws.ActiveSubagents)
}
