package windowstate

import "time"

// SubagentStaleAfter bounds how long a recorded subagent counts as running. A
// lost SubagentStop hook must not pin a window as working forever.
const SubagentStaleAfter = 2 * time.Hour

// ActiveSubagentCount returns how many tracked subagents are still running at
// now. Entries older than SubagentStaleAfter are ignored. SubagentCount is a
// display mirror and is never consulted here.
func (ws WindowState) ActiveSubagentCount(now time.Time) int {
	n := 0
	for _, started := range ws.ActiveSubagents {
		if now.Sub(started) <= SubagentStaleAfter {
			n++
		}
	}
	return n
}

// StartSubagent records a running subagent. It is idempotent per id. An empty
// id only bumps the display counter and never affects ActiveSubagentCount.
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
