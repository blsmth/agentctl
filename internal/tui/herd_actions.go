package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/tmux"
)

// Labels for the clickable affordances drawn on hub lines.
const (
	herdNewLabel   = "+ new"
	herdCloseLabel = "✕"
)

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

// groupCanLaunch reports whether the hub may offer "new" for g: groups with no
// known project dir/runtime dir cannot be targeted reliably.
func groupCanLaunch(g herd.Group) bool {
	return g.ProjectDir != "" && g.RuntimeDir != ""
}

// rowCanClose reports whether the hub may close r's window. The hub session
// is never a valid target.
func rowCanClose(r herd.Row) bool {
	return r.Session != "" && r.Session != tmux.HubSessionName && strings.HasPrefix(r.WindowID, "@")
}

// herdLaunchedMsg reports the outcome of a background `agentctl launch`.
type herdLaunchedMsg struct {
	session  string
	windowID string // "" when the launch did not report a usable window ID
	err      error
}

// herdFocusRetryMsg re-triggers a load while a launched window has not yet
// shown up in state.json.
type herdFocusRetryMsg struct{ session, windowID string }

// Bounds for waiting on the monitor to publish a newly launched window.
const (
	herdFocusRetryInterval = 500 * time.Millisecond
	herdFocusMaxRetries    = 10
)

// herdPendingFocus is a launched window the hub should select once it appears.
type herdPendingFocus struct {
	session  string
	windowID string
	tries    int
}

// parseLaunchWindowID extracts the window ID from `agentctl launch
// --background` stdout: the last non-empty line, which must look like "@123".
// Returns "" if absent or malformed.
func parseLaunchWindowID(stdout string) string {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if len(last) < 2 || last[0] != '@' {
		return ""
	}
	for _, r := range last[1:] {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return last
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
		var stdout, stderr bytes.Buffer
		c.Stdout, c.Stderr = &stdout, &stderr
		if err := c.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = strings.TrimSpace(stdout.String())
			}
			if i := strings.LastIndex(msg, "\n"); i >= 0 {
				msg = msg[i+1:]
			}
			return herdLaunchedMsg{session: g.Session, err: fmt.Errorf("launch in %s: %s: %w", g.Session, msg, err)}
		}
		return herdLaunchedMsg{session: g.Session, windowID: parseLaunchWindowID(stdout.String())}
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
		m.jumpErr = "cannot launch in " + g.Session + ": unknown project directory"
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
			m.recordClose(l.row)
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
			herdMenuItem{"focus", "enter"},
			herdMenuItem{"close", "x"},
			herdMenuItem{"home", "J"},
		)
		if m.lines[idx].row.PRURL != "" {
			items = append(items, herdMenuItem{"open PR", "p"})
		}
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
		_, cmd := m.focusLive()
		return cmd
	case "x":
		m.startClose(line)
	case "J":
		_, cmd := m.jumpToSelectedHome()
		return cmd
	case "n":
		return m.launchForLine(line)
	case "p":
		return m.openSelectedPR()
	}
	return nil
}

// herdOpenURL opens a PR URL in the browser; tests replace it.
var herdOpenURL = tkOpenPRInBrowserCmd

// openSelectedPR opens the selected row's PR (from its session log's pr-link
// record), or does nothing when the row has none.
func (m *HerdModel) openSelectedPR() tea.Cmd {
	r := m.selectedRow()
	if r == nil || r.PRURL == "" {
		return nil
	}
	return herdOpenURL(r.PRURL)
}

// applyPendingFocus selects the pending launched row if it is now present and
// clears the target. Reports whether a row was selected.
func (m *HerdModel) applyPendingFocus() bool {
	p := m.pendingFocus
	if p == nil {
		return false
	}
	for i, l := range m.lines {
		if l.kind == herdLineRow && l.row.Session == p.session && l.row.WindowID == p.windowID {
			m.selectLine(i)
			m.pendingFocus = nil
			return true
		}
	}
	return false
}

// focusRetry handles a retry tick: it gives up after herdFocusMaxRetries,
// otherwise reloads and schedules the next tick. Stale ticks (target replaced
// or cleared) are ignored.
func (m *HerdModel) focusRetry(msg herdFocusRetryMsg) tea.Cmd {
	p := m.pendingFocus
	if p == nil || p.session != msg.session || p.windowID != msg.windowID {
		return nil
	}
	if p.tries++; p.tries > herdFocusMaxRetries {
		m.pendingFocus = nil
		return nil
	}
	return tea.Batch(herdLoadCmd(), herdFocusRetryCmd(p.session, p.windowID))
}

func herdFocusRetryCmd(session, windowID string) tea.Cmd {
	return tea.Tick(herdFocusRetryInterval, func(time.Time) tea.Msg {
		return herdFocusRetryMsg{session: session, windowID: windowID}
	})
}
