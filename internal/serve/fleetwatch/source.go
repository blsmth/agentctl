// Package fleetwatch is the fleet event bus behind `agentctl serve`: it
// watches every live session's windowstate directory, rebuilds the fleet
// through herd (tmux + windowstate, never the monitor), diffs the rendered
// windows and fans typed events out to subscribers (SSE handlers, keep-awake,
// later push).
package fleetwatch

import (
	"github.com/blsmth/agentctl/internal/herd"
)

// Group is one session's worth of windows as served by /v1/fleet.
type Group struct {
	Session string        `json:"session"`
	Project string        `json:"project"`
	Dir     string        `json:"dir"`
	Rollup  string        `json:"rollup"`
	Windows []herd.Window `json:"windows"`

	// RuntimeDir is the session's .agentctl dir, watched by the bus.
	RuntimeDir string `json:"-"`
	// Rows and Meta carry the detail the window endpoint renders.
	Rows []herd.Row           `json:"-"`
	Meta map[string]herd.Meta `json:"-"`
}

// Source produces the current fleet.
type Source interface {
	Fleet() ([]Group, error)
}

// HerdSource is the production Source: herd.Load over tmux and windowstate.
type HerdSource struct{}

// Fleet implements Source.
func (HerdSource) Fleet() ([]Group, error) {
	groups, err := herd.Load()
	if err != nil {
		return nil, err
	}
	return FromHerd(groups), nil
}

// FromHerd converts herd groups, loading each group's windowstate metadata.
func FromHerd(groups []herd.Group) []Group {
	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		meta := herd.LoadMeta(g)
		out = append(out, Group{
			Session:    g.Session,
			Project:    g.Project,
			Dir:        g.ProjectDir,
			Rollup:     g.Rollup.String(),
			Windows:    g.Windows(meta),
			RuntimeDir: g.RuntimeDir,
			Rows:       g.Rows,
			Meta:       meta,
		})
	}
	return out
}

// Find returns the group, row-aligned window and meta for a window ID.
func Find(groups []Group, session, windowID string) (Group, int, bool) {
	for _, g := range groups {
		if g.Session != session {
			continue
		}
		for i, w := range g.Windows {
			if w.WindowID == windowID {
				return g, i, true
			}
		}
	}
	return Group{}, 0, false
}

// Status names the attention state of a window: blocked, working, done or
// idle (the same precedence as herd's rollup).
func Status(w herd.Window) string {
	switch {
	case w.State == "blocked":
		return "blocked"
	case w.State == "working":
		return "working"
	case w.Done:
		return "done"
	default:
		return "idle"
	}
}

// Active reports whether any window is working or blocked.
func Active(groups []Group) bool {
	for _, g := range groups {
		for _, w := range g.Windows {
			if s := Status(w); s == "blocked" || s == "working" {
				return true
			}
		}
	}
	return false
}
