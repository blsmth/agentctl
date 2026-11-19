package reconcile

import (
	"fmt"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/registry"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// LiveWindow is a live window in some agentctl session, joined with its
// windowstate record. Windows without a record are not listed.
type LiveWindow struct {
	Session    string // home tmux session, e.g. "agentctl-myproject"
	RuntimeDir string // that session's .agentctl directory
	WindowID   string
	Record     windowstate.WindowState
}

// Name is the window's display name: its instance, else its session name,
// else its window ID.
func (w LiveWindow) Name() string {
	if inst, _ := identity(w.Record); inst != "" {
		return inst
	}
	if w.Record.SessionName != "" {
		return w.Record.SessionName
	}
	return w.WindowID
}

// CascadeDeps are the side effects of closing a gone window's dispatched
// children. A zero value (nil LiveWindows or Kill) disables the cascade.
type CascadeDeps struct {
	// Enabled reports the global close_children switch; nil means enabled.
	Enabled func() bool
	// HasSession reports whether the gone window's own tmux session still
	// exists. False (or nil) means the session is going away: no cascade.
	HasSession func() bool
	// Snapshotted reports whether the window (ID and session UUID) is in the
	// project's restore.json, i.e. it is coming back after a restore.
	Snapshotted func(windowID, sessionID string) bool
	// LiveWindows lists live windows across every agentctl session. An error
	// is not zero windows: the cascade is skipped.
	LiveWindows func() ([]LiveWindow, error)
	// Kill closes one child window (and spawns its own reconcile, which is
	// what cascades to grandchildren).
	Kill func(w LiveWindow) error
}

// DefaultCascadeDeps wires the cascade to config, tmux and the restore file
// for the project at runtimeDir whose tmux session is session.
func DefaultCascadeDeps(runtimeDir, session string) CascadeDeps {
	return CascadeDeps{
		Enabled:     func() bool { return config.Load().CloseChildrenEnabled() },
		HasSession:  func() bool { return tmux.HasSessionNamed(session) },
		Snapshotted: func(id, sid string) bool { return Snapshotted(runtimeDir, id, sid) },
		LiveWindows: ListLiveWindows,
		Kill: func(w LiveWindow) error {
			return tmux.KillWindowIn(w.Session, w.RuntimeDir, w.WindowID)
		},
	}
}

// IsChild reports whether w was dispatched by the window parentID running
// session parentSessionID. Both must match: spawned_by alone is untrustworthy
// (bare launches inherit a session-global AGENTCTL_WINDOW_ID, see
// fleet.Meta.ParentTrusted) and tmux recycles @N IDs. Cron-scheduled windows
// are never children.
func IsChild(w LiveWindow, parentID, parentSessionID string) bool {
	r := w.Record
	return parentID != "" && parentSessionID != "" &&
		w.WindowID != parentID &&
		r.ScheduledBy == "" &&
		r.SpawnedBy == parentID &&
		r.SpawnedBySession == parentSessionID
}

// Descendants returns the live windows dispatched by parentID/parentSessionID
// and, recursively, by them, in breadth-first order without duplicates.
// It is what a close of the parent will take down.
func Descendants(parentID, parentSessionID string, wins []LiveWindow) []LiveWindow {
	type parent struct{ id, sid string }
	queue := []parent{{parentID, parentSessionID}}
	seen := map[string]bool{parentID: true}
	var out []LiveWindow
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, w := range wins {
			if seen[w.WindowID] || !IsChild(w, p.id, p.sid) {
				continue
			}
			seen[w.WindowID] = true
			out = append(out, w)
			queue = append(queue, parent{w.WindowID, w.Record.SessionID})
		}
	}
	return out
}

// closeChildren closes the live windows the gone window c dispatched. It
// closes direct children only: each child's own reconcile cascades further.
// It does nothing when disabled, when the session is going away, when the
// window was snapshotted for a restore, or when any live window still carries
// the gone window's session UUID (it was restored elsewhere, not closed).
func closeChildren(c Claimed, d CascadeDeps) {
	sid := c.Record.SessionID
	if d.LiveWindows == nil || d.Kill == nil || sid == "" {
		return
	}
	if d.Enabled != nil && !d.Enabled() {
		return
	}
	if d.HasSession == nil || !d.HasSession() {
		logging.Log("reconcile: cascade skipped for %s: session is going away", c.WindowID)
		return
	}
	if d.Snapshotted != nil && d.Snapshotted(c.WindowID, sid) {
		logging.Log("reconcile: cascade skipped for %s: window is saved for restore", c.WindowID)
		return
	}
	wins, err := d.LiveWindows()
	if err != nil {
		logging.Log("reconcile: cascade skipped for %s: listing windows: %v", c.WindowID, err)
		return
	}
	var children []LiveWindow
	seen := make(map[string]bool)
	for _, w := range wins {
		if w.Record.SessionID == sid {
			logging.Log("reconcile: cascade skipped for %s: session %s is live in %s", c.WindowID, sid, w.WindowID)
			return
		}
		if IsChild(w, c.WindowID, sid) && !seen[w.WindowID] {
			seen[w.WindowID] = true
			children = append(children, w)
		}
	}
	for _, w := range children {
		if err := d.Kill(w); err != nil {
			logging.Log("reconcile: cascade close of %s (%s, child of %s) failed: %v", w.WindowID, w.Name(), c.WindowID, err)
			continue
		}
		logging.Log("reconcile: cascade closed %s (%s) in %s, dispatched by gone window %s", w.WindowID, w.Name(), w.Session, c.WindowID)
	}
}

// Snapshotted reports whether runtimeDir's restore.json holds windowID with
// session UUID sessionID (an entry with no session UUID matches on the
// window ID alone, erring towards not cascading).
func Snapshotted(runtimeDir, windowID, sessionID string) bool {
	if runtimeDir == "" {
		return false
	}
	s, err := state.LoadFrom(filepath.Join(runtimeDir, state.RestoreFileName))
	if err != nil {
		logging.Log("reconcile: reading restore file: %v", err)
		return false
	}
	if s == nil {
		return false
	}
	for _, a := range s.Agents {
		if a.WindowID == windowID && (a.SessionID == "" || a.SessionID == sessionID) {
			return true
		}
	}
	return false
}

// ListLiveWindows lists the windows of every local agentctl session (the
// discovery fleet.Load uses: tmux sessions enriched by the registry) joined
// with each session's windowstate records. Any failed listing is an error,
// since a missing session would hide a restored parent or a child.
func ListLiveWindows() ([]LiveWindow, error) {
	sessions, err := tmux.ListSessionsWithPathsErr()
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	entries, err := registry.List()
	if err != nil {
		logging.Debug("reconcile: registry list: %v", err)
		entries = nil
	}
	var out []LiveWindow
	for _, e := range fleet.MergeSessions(sessions, entries) {
		if e.Host != "" && e.Host != "local" {
			continue
		}
		wins, err := tmux.ListWindowsInErr(e.SessionName)
		if err != nil {
			return nil, fmt.Errorf("listing windows of %s: %w", e.SessionName, err)
		}
		records := windowstate.AllFrom(windowstate.WindowsDirFor(e.RuntimeDir))
		for _, w := range wins {
			if rec, ok := records[w.ID]; ok {
				out = append(out, LiveWindow{Session: e.SessionName, RuntimeDir: e.RuntimeDir, WindowID: w.ID, Record: rec})
			}
		}
	}
	return out, nil
}
