package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/tmux"
)

// Labels for the clickable affordances drawn on hub lines.
const (
	herdNewLabel   = "+ new"
	herdCloseLabel = "✕"
)

// herdWheelStep is how many preview lines one wheel notch scrolls.
const herdWheelStep = 3

// herdLaunchStripEnv lists env vars inherited from the hub that would point a
// launched agent at the hub's session/window instead of the target project.
var herdLaunchStripEnv = []string{
	"AGENTCTL_INSTANCE",
	"AGENTCTL_SESSION_ID",
	"AGENTCTL_WINDOW_ID",
	"TMUX_PANE",
}

// herdLaunchEnv builds the environment for the `agentctl launch` subprocess
// that creates a window in group g. The hub's own identity vars are dropped
// and AGENTCTL_RUNTIME_DIR is pinned to the group's runtime dir; agentctl
// derives the target tmux session from that dir (see paths.DirName and
// tmux.SetSessionName), so the new window lands in g.Session, not the hub.
// Pure: parent is passed in rather than read from os.Environ.
func herdLaunchEnv(g herd.Group, parent []string) []string {
	out := make([]string, 0, len(parent)+1)
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if name == "AGENTCTL_RUNTIME_DIR" || containsString(herdLaunchStripEnv, name) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "AGENTCTL_RUNTIME_DIR="+g.RuntimeDir)
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// groupCanLaunch reports whether the hub may offer "new" for g: stale groups
// (no live monitor snapshot) and groups with no known project dir/runtime dir
// cannot be targeted reliably.
func groupCanLaunch(g herd.Group) bool {
	return !g.Stale && g.ProjectDir != "" && g.RuntimeDir != ""
}

// rowCanClose reports whether the hub may close r's window. The hub session
// is never a valid target.
func rowCanClose(r herd.Row) bool {
	return r.Session != "" && r.Session != tmux.HubSessionName && strings.HasPrefix(r.WindowID, "@")
}

// herdLaunchedMsg reports the outcome of a background `agentctl launch`.
type herdLaunchedMsg struct {
	session string
	err     error
}

// herdClosedMsg reports the outcome of a window close.
type herdClosedMsg struct {
	name string
	err  error
}

// herdLaunchCmd runs `agentctl launch --background` for g as a subprocess so
// the TUI never blocks and the hub's own process state cannot leak in.
func herdLaunchCmd(g herd.Group) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return herdLaunchedMsg{session: g.Session, err: fmt.Errorf("locating agentctl: %w", err)}
		}
		c := exec.Command(exe, "launch", "--background")
		c.Dir = g.ProjectDir
		c.Env = herdLaunchEnv(g, os.Environ())
		if out, err := c.CombinedOutput(); err != nil {
			msg := strings.TrimSpace(string(out))
			if i := strings.LastIndex(msg, "\n"); i >= 0 {
				msg = msg[i+1:]
			}
			return herdLaunchedMsg{session: g.Session, err: fmt.Errorf("launch in %s: %s: %w", g.Session, msg, err)}
		}
		return herdLaunchedMsg{session: g.Session}
	}
}

func herdCloseCmd(r herd.Row) tea.Cmd {
	return func() tea.Msg {
		return herdClosedMsg{name: r.Name, err: tmux.KillSessionWindow(r.Session, r.WindowID)}
	}
}

// ── Actions on the model ─────────────────────────────────────────────────────

// groupForSession finds the loaded group for a tmux session name.
func (m *HerdModel) groupForSession(session string) *herd.Group {
	for i := range m.groups {
		if m.groups[i].Session == session {
			return &m.groups[i]
		}
	}
	return nil
}

// groupOfLine returns the group that owns line idx (header, placeholder, or
// row), or nil.
func (m *HerdModel) groupOfLine(idx int) *herd.Group {
	if idx < 0 || idx >= len(m.lines) {
		return nil
	}
	l := m.lines[idx]
	switch l.kind {
	case herdLineHeader, herdLinePlaceholder:
		return m.groupForSession(l.group.Session)
	case herdLineRow:
		return m.groupForSession(l.row.Session)
	}
	return nil
}

// launchForLine starts a background launch in the group owning line idx.
func (m *HerdModel) launchForLine(idx int) tea.Cmd {
	g := m.groupOfLine(idx)
	if g == nil {
		return nil
	}
	if !groupCanLaunch(*g) {
		m.jumpErr = "cannot launch in " + g.Session + ": session is stale"
		return nil
	}
	m.jumpErr = ""
	m.infoMsg = "launching agent in " + g.Session + "…"
	return herdLaunchCmd(*g)
}

// startClose arms the inline close confirmation for the row at line idx.
func (m *HerdModel) startClose(idx int) {
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != herdLineRow {
		return
	}
	r := m.lines[idx].row
	if !rowCanClose(r) {
		m.jumpErr = "cannot close this window"
		return
	}
	m.jumpErr = ""
	m.confirmSession, m.confirmWindow = r.Session, r.WindowID
}

func (m *HerdModel) cancelClose() { m.confirmSession, m.confirmWindow = "", "" }

func (m HerdModel) closeArmed() bool { return m.confirmWindow != "" }

// confirmingRow reports whether r is the row awaiting close confirmation.
func (m HerdModel) confirmingRow(r herd.Row) bool {
	return m.closeArmed() && r.Session == m.confirmSession && r.WindowID == m.confirmWindow
}

// confirmClose runs the armed close. The worktree (if any) is left for
// sweep/cleanup.
func (m *HerdModel) confirmClose() tea.Cmd {
	for _, l := range m.lines {
		if l.kind == herdLineRow && m.confirmingRow(l.row) {
			m.cancelClose()
			m.infoMsg = "closing " + l.row.Name + "…"
			return herdCloseCmd(l.row)
		}
	}
	m.cancelClose()
	return nil
}

// ── Right-click menu ─────────────────────────────────────────────────────────

type herdMenuItem struct {
	label string
	key   string // the keyboard equivalent, also the action id
}

// herdMenuItems lists the context-menu entries for line idx.
func (m *HerdModel) herdMenuItems(idx int) []herdMenuItem {
	if idx < 0 || idx >= len(m.lines) {
		return nil
	}
	var items []herdMenuItem
	g := m.groupOfLine(idx)
	if m.lines[idx].kind == herdLineRow {
		items = append(items,
			herdMenuItem{"open", "enter"},
			herdMenuItem{"close", "x"},
			herdMenuItem{"home", "J"},
		)
	}
	if g != nil && groupCanLaunch(*g) {
		items = append(items, herdMenuItem{"new", "n"})
	}
	return items
}

// menuItemSpans lays the menu out on one footer line and returns each item's
// [start,end) column range (2-column indent, " · " separators).
func menuItemSpans(items []herdMenuItem) (text string, spans [][2]int) {
	var b strings.Builder
	b.WriteString("  ")
	col := 2
	for i, it := range items {
		if i > 0 {
			b.WriteString(" · ")
			col += 3
		}
		s := "[" + it.label + "]"
		spans = append(spans, [2]int{col, col + len(s)})
		b.WriteString(s)
		col += len(s)
	}
	return b.String(), spans
}

// footerMessageY is the screen row of the footer's message line.
func (m HerdModel) footerMessageY() int {
	return m.herdListTop() + m.herdListRows() + 1
}

// openMenu opens the context menu for line idx (selecting it first).
func (m *HerdModel) openMenu(idx int) {
	m.cancelClose()
	m.selectLine(idx)
	if len(m.herdMenuItems(idx)) == 0 {
		m.menuOpen = false
		return
	}
	m.menuOpen, m.menuLine = true, idx
}

// runMenuKey executes a menu action by its key id.
func (m *HerdModel) runMenuKey(key string) tea.Cmd {
	line := m.menuLine
	m.menuOpen = false
	switch key {
	case "enter":
		_, cmd := m.linkSelectedIntoHub()
		return cmd
	case "x":
		m.startClose(line)
	case "J":
		_, cmd := m.jumpToSelectedHome()
		return cmd
	case "n":
		return m.launchForLine(line)
	}
	return nil
}
