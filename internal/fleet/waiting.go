package fleet

import (
	"fmt"
	"strings"
)

// WaitingGlyph marks an orchestrating row that is idle while at least one of
// its child windows is working or blocked.
const WaitingGlyph = "◐"

// WaitChild is a busy child a waiting row is waiting on.
type WaitChild struct {
	// Label is the child's short display name (agent:instance).
	Label string
	// Blocked is true for a blocked child, false for a working one.
	Blocked bool
}

type waitKey struct{ windowID, sessionID string }

// TagWaiting sets Row.WaitingOn on every row of groups that has a busy child
// (working or blocked) anywhere in groups, using the same child rule as
// windowstate.HasBusyChild: SpawnedBy is the parent's window ID,
// SpawnedBySession its current SessionID, and the child is not cron-scheduled.
// Children are looked up across all groups (they may live in another tmux
// session). Display only. It indexes parents once, so the cost is linear.
func TagWaiting(groups []Group) {
	busy := map[waitKey][]WaitChild{}
	for _, g := range groups {
		for _, r := range g.Rows {
			if r.Shell || r.ScheduledBy != "" || r.SpawnedBy == "" || r.SpawnedBySession == "" || r.SpawnedBy == r.WindowID {
				continue
			}
			if r.State != "working" && r.State != "blocked" {
				continue
			}
			k := waitKey{r.SpawnedBy, r.SpawnedBySession}
			busy[k] = append(busy[k], WaitChild{Label: waitLabel(r), Blocked: r.State == "blocked"})
		}
	}
	for gi := range groups {
		rows := groups[gi].Rows
		for i := range rows {
			rows[i].WaitingOn = nil
			r := &rows[i]
			if r.Shell || r.WindowID == "" || r.SessionID == "" {
				continue
			}
			r.WaitingOn = busy[waitKey{r.WindowID, r.SessionID}]
		}
	}
}

// waitLabel is a child's short name: agent and instance number, e.g.
// "go-engineer:3" for the window named "go-engineer~3".
func waitLabel(r Row) string {
	name := RowDisplayName(r)
	if base, n, ok := strings.Cut(name, "~"); ok && n != "" {
		return base + ":" + n
	}
	return name
}

// WaitingLine is the detail line of a waiting row, e.g.
// "waiting on go-engineer:3 ●, code-reviewer:2 ⏸": at most three names, then
// "+N". "" when the row waits on nothing.
func WaitingLine(r Row) string {
	if len(r.WaitingOn) == 0 {
		return ""
	}
	const maxNames = 3
	var parts []string
	for i, c := range r.WaitingOn {
		if i == maxNames {
			parts = append(parts, fmt.Sprintf("+%d", len(r.WaitingOn)-maxNames))
			break
		}
		g := "●"
		if c.Blocked {
			g = "⏸"
		}
		parts = append(parts, c.Label+" "+g)
	}
	return "waiting on " + strings.Join(parts, ", ")
}
