package tui

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/closedlog"
)

// Recent projects in the new-session prompt: up to fleetRecentMax directories
// listed below the input, from the closed-window ledger (newest first, last
// fleetRecentWindow) and the hub's origin dir. Dirs that already have a live
// group, worktree paths and dirs that no longer exist are left out. The
// ledger is read once, in the background, when the prompt opens.

const (
	fleetRecentMax    = 8
	fleetRecentWindow = 30 * 24 * time.Hour
	// fleetRecentScan bounds how many ledger dirs are examined (stat'ed).
	fleetRecentScan = 60
	// fleetRecentMargin is the margin of a list entry.
	fleetRecentMargin = "    "
)

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
	since := fleetNow().Add(-fleetRecentWindow)
	return func() tea.Msg {
		return fleetRecentDirsMsg{dirs: loadRecentDirs(origin, fleetRecentLedger(since))}
	}
}

// resolvedKey is p with symlinks resolved, or the cleaned p when it cannot be.
func resolvedKey(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// isWorktreePath reports a path inside a repo's .worktrees directory.
func isWorktreePath(p string) bool {
	return strings.Contains(filepath.ToSlash(filepath.Clean(p))+"/", "/.worktrees/")
}

// loadRecentDirs dedups origin and the ledger's project dirs by resolved path
// and drops worktree paths and dirs that no longer exist.
func loadRecentDirs(origin string, recs []closedlog.Record) []string {
	cands := make([]string, 0, len(recs)+1)
	if origin != "" {
		cands = append(cands, origin)
	}
	for _, r := range recs {
		if r.ProjectDir != "" {
			cands = append(cands, r.ProjectDir)
		}
	}
	seen := map[string]bool{}
	var out []string
	for i, c := range cands {
		if i >= fleetRecentScan {
			break
		}
		if !filepath.IsAbs(c) || isWorktreePath(c) {
			continue
		}
		k := resolvedKey(c)
		if seen[k] || isWorktreePath(k) {
			continue
		}
		seen[k] = true
		if fleetRecentIsDir(k) {
			out = append(out, filepath.Clean(c))
		}
	}
	return out
}

// applyRecentDirs stores the candidates on the open prompt, minus the dirs
// that already have a live group.
func (m *FleetModel) applyRecentDirs(msg fleetRecentDirsMsg) {
	s := m.dirPrompt
	if s == nil {
		return
	}
	live := map[string]bool{}
	for _, g := range m.groups {
		if g.ProjectDir != "" {
			live[resolvedKey(g.ProjectDir)] = true
		}
	}
	s.recent = s.recent[:0]
	for _, d := range msg.dirs {
		if !live[resolvedKey(d)] {
			s.recent = append(s.recent, d)
		}
	}
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
	if s.focus != nsFieldDir || (key != "up" && key != "down") {
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

// recentLines renders the list lines for content width cw.
func (m FleetModel) recentLines(cw int) []string {
	s := m.dirPrompt
	home, _ := fleetUserHome()
	es := s.visibleRecent(home)
	lines := make([]string, 0, len(es))
	for i, d := range es {
		text := fleetRecentMargin + tildePath(d, home)
		st := fleetDimStyle
		if i == s.hl {
			st = fleetBlockedStyle.Reverse(true)
		}
		lines = append(lines, st.Render(truncateToWidth(text, cw)))
	}
	return lines
}

// newSessionRowAt maps a footer row (0 = the prompt's first line) to the
// recent entry drawn there, or -1.
func (m FleetModel) newSessionRowAt(row int) int {
	di := 0
	for i, f := range m.dirPrompt.fields() {
		if f == nsFieldDir {
			di = i
		}
	}
	i := row - di - 1
	if i < 0 || i >= len(m.recentEntries()) {
		return -1
	}
	return i
}

// clickRecent handles a left click on footer row row of the prompt; ok is
// false when it hit no entry.
func (m *FleetModel) clickRecent(row int) (tea.Cmd, bool) {
	i := m.newSessionRowAt(row)
	if i < 0 {
		return nil, false
	}
	return m.openRecent(m.recentEntries()[i]), true
}
