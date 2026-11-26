package tui

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/closedlog"
)

// Recent projects in the new-session dropdown: up to fleetRecentMax directories
// listed below the input, from the closed-window ledger (newest first, last
// closedlog.RecentWindow) and the hub's origin dir. Dirs that already have a live
// group, worktree paths and dirs that no longer exist are left out. The
// ledger is read once, in the background, when the prompt opens.

// fleetRecentMax is how many entries the dropdown lists at once.
const fleetRecentMax = 8

// Seams so tests never read the real ledger or touch the real filesystem.
var (
	fleetRecentLedger = func(since time.Time) []closedlog.Record {
		recs, _ := closedlog.Load(since)
		return recs
	}
	fleetRecentIsDir = func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.IsDir()
	}
)

// fleetRecentDirsMsg carries the candidate directories for an open prompt.
type fleetRecentDirsMsg struct{ dirs []string }

// recentDirsCmd lists the candidates off the render path: the hub origin dir
// first, then the ledger's project dirs newest first.
func recentDirsCmd(origin string) tea.Cmd {
	since := fleetNow().Add(-closedlog.RecentWindow)
	return func() tea.Msg {
		return fleetRecentDirsMsg{dirs: loadRecentDirs(origin, fleetRecentLedger(since))}
	}
}

// loadRecentDirs is closedlog.RecentDirs over the fleetRecentIsDir seam.
func loadRecentDirs(origin string, recs []closedlog.Record) []string {
	return closedlog.RecentDirs(origin, recs, fleetRecentIsDir)
}

// applyRecentDirs stores the candidates on the open prompt, minus the dirs
// that already have a live group.
func (m *FleetModel) applyRecentDirs(msg fleetRecentDirsMsg) {
	s := m.dirPrompt
	if s == nil {
		return
	}
	var live []string
	for _, g := range m.groups {
		live = append(live, g.ProjectDir)
	}
	s.recent = closedlog.WithoutDirs(msg.dirs, live)
	s.hl = -1
}

// tildePath shortens a leading home dir to "~".
func tildePath(p, home string) string {
	if home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// visibleRecent is the listed entries: the candidates matching what the user
// typed (a case-insensitive substring of the path), at most fleetRecentMax.
// The prefill is not a filter until it is edited.
func (s *fleetNewSessionState) visibleRecent(home string) []string {
	if s == nil || len(s.recent) == 0 {
		return nil
	}
	q := ""
	if s.edited {
		q = strings.ToLower(strings.TrimSpace(s.input.Value()))
	}
	var out []string
	for _, d := range s.recent {
		if q != "" && !strings.Contains(strings.ToLower(d), q) && !strings.Contains(strings.ToLower(tildePath(d, home)), q) {
			continue
		}
		out = append(out, d)
		if len(out) == fleetRecentMax {
			break
		}
	}
	return out
}

// recentEntries is visibleRecent for the model's home dir.
func (m FleetModel) recentEntries() []string {
	home, _ := fleetUserHome()
	return m.dirPrompt.visibleRecent(home)
}

// recentKey handles up/down on the list; handled is false when there is no
// list to move in or the dir field is not focused.
func (m *FleetModel) recentKey(key string) bool {
	s := m.dirPrompt
	if key != "up" && key != "down" {
		return false
	}
	n := len(m.recentEntries())
	if n == 0 {
		return false
	}
	if s.hl >= n {
		s.hl = n - 1
	}
	if key == "down" {
		s.hl = min(s.hl+1, n-1)
	} else {
		s.hl = max(s.hl-1, -1)
	}
	return true
}

// highlightedRecent is the highlighted entry, if any.
func (m FleetModel) highlightedRecent() (string, bool) {
	es := m.recentEntries()
	if m.dirPrompt.hl < 0 || m.dirPrompt.hl >= len(es) {
		return "", false
	}
	return es[m.dirPrompt.hl], true
}

// openRecent fills the input with dir and submits like Enter.
func (m *FleetModel) openRecent(dir string) tea.Cmd {
	m.dirPrompt.input.SetValue(dir)
	m.dirPrompt.input.CursorEnd()
	m.dirPrompt.edited = false
	return m.submitNewSession()
}

// recentLines renders the list lines, each padded to width w (the panel's
// inner width) so the highlight spans the row.
func (m FleetModel) recentLines(w int) []string {
	s := m.dirPrompt
	home, _ := fleetUserHome()
	es := s.visibleRecent(home)
	lines := make([]string, 0, len(es))
	for i, d := range es {
		text := truncateToWidth(tildePath(d, home), w)
		st := fleetDimStyle
		if i == s.hl {
			st = fleetBlockedStyle.Reverse(true)
		}
		lines = append(lines, st.Render(padToWidth(text, w)))
	}
	return lines
}
