package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/tmux"
)

// The hub's Closed section (fleetClosedKey): agent windows closed in the last
// fleetClosedWindow, read from the closed ledger (internal/closedlog) only
// while the toggle is on, through a Cache that re-reads on a file change.
// Closed rows never drive the live viewer; Enter (or the row menu's resume)
// relaunches the agent resuming its recorded session through the hidden
// `agentctl hub-resume` helper, run like hub-new-session.

// fleetClosedKey shows or hides the Closed section.
const fleetClosedKey = "C"

// fleetClosedWindow is how far back the section looks.
const fleetClosedWindow = 7 * 24 * time.Hour

// fleetClosedGlyph marks a closed row.
const fleetClosedGlyph = "✗"

// Seams so tests never read the real ledger or spawn agentctl.
var (
	fleetClosedLoad = func(c *closedlog.Cache, since time.Time) ([]closedlog.Record, error) {
		return c.Load(since)
	}
	fleetResumeRun = runHubResumeHelper
	fleetStat      = os.Stat
)

// fleetClosedLoadedMsg carries a ledger read.
type fleetClosedLoadedMsg struct {
	recs []closedlog.Record
	err  error
}

// closedLoadCmd reads the ledger in the background; nil while the section is
// hidden, so a hub that never shows it never touches the ledger.
func (m FleetModel) closedLoadCmd() tea.Cmd {
	if !m.showClosed || m.closedCache == nil {
		return nil
	}
	c := m.closedCache
	since := fleetNow().Add(-fleetClosedWindow)
	return func() tea.Msg {
		recs, err := fleetClosedLoad(c, since)
		return fleetClosedLoadedMsg{recs: recs, err: err}
	}
}

// applyClosedLoaded stores a ledger read and re-lays out the list.
func (m *FleetModel) applyClosedLoaded(msg fleetClosedLoadedMsg) {
	if !m.showClosed {
		return // hidden meanwhile
	}
	if msg.err != nil {
		m.jumpErr = "closed agents: " + msg.err.Error()
		return
	}
	m.closed = msg.recs
	m.relayout()
}

// toggleClosed is fleetClosedKey: show or hide the Closed section.
func (m *FleetModel) toggleClosed() tea.Cmd {
	m.showClosed = !m.showClosed
	m.jumpErr = ""
	if m.showClosed {
		m.setSuccess("closed agents shown (last 7 days)")
	} else {
		m.setSuccess("closed agents hidden")
		m.closed = nil
	}
	m.relayout()
	return tea.Batch(m.saveViewCmd(), m.closedLoadCmd())
}

// closedLines is the Closed section for rebuildLines: a section label, then
// the records grouped by project (projects ordered by their newest close),
// newest first within each.
func (m FleetModel) closedLines() []fleetLine {
	if !m.showClosed {
		return nil
	}
	label := fmt.Sprintf("Closed (%d) · last 7 days", len(m.closed))
	if len(m.closed) == 0 {
		label = "Closed · none in the last 7 days"
	}
	lines := []fleetLine{{kind: fleetLineClosedLabel, label: label}}
	var order []string
	byProject := map[string][]closedlog.Record{}
	for _, r := range m.closed {
		p := r.Project()
		if _, ok := byProject[p]; !ok {
			order = append(order, p)
		}
		byProject[p] = append(byProject[p], r)
	}
	for _, p := range order {
		lines = append(lines, fleetLine{kind: fleetLineClosedLabel, label: p, group: fleet.Group{Project: p}})
		for _, r := range byProject[p] {
			lines = append(lines, fleetLine{kind: fleetLineClosed, closed: r, group: fleet.Group{Project: p}})
		}
	}
	return lines
}

// selectedClosed is the selected closed row's record, or nil.
func (m *FleetModel) selectedClosed() *closedlog.Record {
	idx := m.splitPane.Cursor()
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != fleetLineClosed {
		return nil
	}
	r := m.lines[idx].closed
	return &r
}

// renderClosedLabel renders the section label (no project) or a project
// label inside it.
func renderClosedLabel(l fleetLine, cw int) string {
	if l.group.Project == "" {
		return fleetFitLine("  "+fleetDimStyle.Render(fleetClosedGlyph+" "+l.label), false, cw)
	}
	return fleetFitLine(fleetProjectBar(l.group.Project)+" "+fleetDimStyle.Render(l.label), false, cw)
}

// renderClosedRow renders a closed row: dimmed glyph, agent, title, and
// "closed 2h ago" right-aligned.
func renderClosedRow(r closedlog.Record, selected bool, cw int) string {
	cursor := "  "
	if selected {
		cursor = cursorActiveStyle.Render("▸") + " "
	}
	left := fleetProjectBar(r.Project()) + cursor + fleetDimStyle.Render(fleetClosedGlyph+" "+r.Instance)
	right := "closed " + closedlog.Ago(fleetNow(), r.ClosedAt)
	inner := cw - fleetSidebarRightGutter
	room := inner - lipgloss.Width(left) - lipgloss.Width(right) - 4
	title := ""
	if t := r.Title(); t != "" && t != r.Instance && room > 3 {
		title = "  " + truncateToWidth(t, room)
	}
	body := left + fleetDimStyle.Render(title)
	gap := inner - lipgloss.Width(body) - lipgloss.Width(right)
	if gap < 1 {
		return fleetFitLine(body, selected, cw)
	}
	return fleetFitLine(body+strings.Repeat(" ", gap)+fleetDimStyle.Render(right), selected, cw)
}

// closedMenuItems is the row menu for a closed row.
func closedMenuItems(r closedlog.Record) []fleetMenuItem {
	items := []fleetMenuItem{{"resume", "enter"}}
	if r.PRURL != "" {
		items = append(items, fleetMenuItem{"open PR", "p"})
	}
	return items
}

// resumeCheck reports why r cannot be resumed, or nil.
func resumeCheck(r closedlog.Record) error {
	if r.SessionID == "" {
		return errors.New("no session id was recorded for " + r.Title())
	}
	if r.ProjectDir == "" {
		return errors.New("project directory unknown for " + r.Title())
	}
	if r.LogPath != "" {
		if _, err := fleetStat(r.LogPath); err != nil {
			return fmt.Errorf("transcript %s is missing", r.LogPath)
		}
	}
	if r.Agent == "" {
		return errors.New("no agent recorded for " + r.Title())
	}
	work := r.WorkDir
	if work == "" {
		work = r.ProjectDir
	}
	if fi, err := fleetStat(work); err != nil || !fi.IsDir() {
		if rel, err := filepath.Rel(filepath.Join(r.ProjectDir, ".worktrees"), work); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			return fmt.Errorf("worktree %s was reclaimed", rel)
		}
		return fmt.Errorf("directory %s is gone", work)
	}
	return nil
}

// resumeClosed is Enter on a closed row: relaunch its agent resuming the
// recorded session in the project's tmux session, or say why it cannot.
func (m *FleetModel) resumeClosed(r closedlog.Record) tea.Cmd {
	if err := resumeCheck(r); err != nil {
		m.jumpErr = "cannot resume: " + err.Error()
		return nil
	}
	m.jumpErr = ""
	m.setProgress("resuming " + r.Title() + " in " + r.Project() + "…")
	session := r.Session
	if session == "" {
		session = tmux.SessionNameForProjectRoot(r.ProjectDir)
	}
	return func() tea.Msg {
		out, err := fleetResumeRun(r, fleetNewSessionEnv(r.ProjectDir, os.Environ()))
		if err != nil {
			return fleetLaunchedMsg{session: session, err: fmt.Errorf("resume %s: %w", r.Title(), err)}
		}
		return fleetLaunchedMsg{session: session, windowID: parseLaunchWindowID(out)}
	}
}

// runHubResumeHelper runs `agentctl hub-resume <project> ...` with cwd = the
// project, like the new-session helper.
func runHubResumeHelper(r closedlog.Record, env []string) (string, error) {
	work := r.WorkDir
	if work == "" {
		work = r.ProjectDir
	}
	args := []string{"hub-resume", r.ProjectDir, "--work-dir", filepath.Clean(work), "--agent", r.Agent, "--session-id", r.SessionID}
	return runAgentctlHelper(r.ProjectDir, env, args...)
}
