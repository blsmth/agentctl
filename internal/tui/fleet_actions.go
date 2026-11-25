package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/reconcile"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/tmux"
)

// fleetCronKey opens a new cron job for the selected group's project, also
// while the "+ new" selector is armed.
const fleetCronKey = "t"

// Labels for the clickable affordances drawn on hub lines.
const (
	fleetNewLabel   = "+ new"
	fleetShellLabel = "shell"
	fleetAgentLabel = "agent"
	fleetCronLabel  = "cron"
	fleetSelSep     = " | "
	fleetCloseLabel = "✕"
)

// fleetLaunchStripEnv lists env vars inherited from the hub that would point a
// launched agent at the hub's session/window instead of the target project.
var fleetLaunchStripEnv = []string{
	"AGENTCTL_INSTANCE",
	"AGENTCTL_SESSION_ID",
	"AGENTCTL_WINDOW_ID",
	"TMUX_PANE",
}

// fleetLaunchEnv builds the environment for the `agentctl launch` subprocess
// that creates a window in group g. The hub's own identity vars are dropped
// and AGENTCTL_RUNTIME_DIR is pinned to the group's runtime dir; agentctl
// derives the target tmux session from that dir (see paths.DirName and
// tmux.SetSessionName), so the new window lands in g.Session, not the hub.
// Pure: parent is passed in rather than read from os.Environ.
func fleetLaunchEnv(g fleet.Group, parent []string) []string {
	out := make([]string, 0, len(parent)+1)
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if name == "AGENTCTL_RUNTIME_DIR" || containsString(fleetLaunchStripEnv, name) {
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
func groupCanLaunch(g fleet.Group) bool {
	return g.ProjectDir != "" && g.RuntimeDir != ""
}

// rowCanClose reports whether the hub may close r's window. The hub session
// is never a valid target.
func rowCanClose(r fleet.Row) bool {
	return r.Session != "" && r.Session != tmux.HubSessionName && strings.HasPrefix(r.WindowID, "@")
}

// fleetLaunchedMsg reports the outcome of a background `agentctl launch`.
type fleetLaunchedMsg struct {
	session  string
	windowID string // "" when the launch did not report a usable window ID
	err      error
}

// fleetFocusRetryMsg re-triggers a load while a launched window has not yet
// shown up in state.json.
type fleetFocusRetryMsg struct{ session, windowID string }

// Bounds for waiting on the monitor to publish a newly launched window.
const (
	fleetFocusRetryInterval = 500 * time.Millisecond
	fleetFocusMaxRetries    = 10
)

// fleetPendingFocus is a launched window the hub should select once it appears.
type fleetPendingFocus struct {
	session  string
	windowID string
	tries    int
	// focus moves tmux focus to the live pane once the row is shown. Set only
	// for launches, never for restore/reselect uses of pendingFocus.
	focus bool
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

// fleetClosedMsg reports the outcome of a window close.
type fleetClosedMsg struct {
	name string
	err  error
}

// fleetLaunchArgs is the argv (after the executable) of a hub "new" launch.
// Hub-launched agents start in auto permission mode.
func fleetLaunchArgs() []string {
	return []string{"launch", "--background", "--permission-mode", "auto"}
}

// fleetLaunch is the per-group launch; a seam so tests never spawn agentctl.
var fleetLaunch = fleetLaunchCmd

// fleetLaunchCmd runs `agentctl launch --background` for g as a subprocess so
// the TUI never blocks and the hub's own process state cannot leak in.
func fleetLaunchCmd(g fleet.Group) tea.Cmd {
	return func() tea.Msg {
		exe, err := selfpath.Spawnable()
		if err != nil {
			return fleetLaunchedMsg{session: g.Session, err: fmt.Errorf("locating agentctl: %w", err)}
		}
		c := exec.Command(exe, fleetLaunchArgs()...)
		c.Dir = g.ProjectDir
		c.Env = fleetLaunchEnv(g, os.Environ())
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
			return fleetLaunchedMsg{session: g.Session, err: fmt.Errorf("launch in %s: %s: %w", g.Session, msg, err)}
		}
		return fleetLaunchedMsg{session: g.Session, windowID: parseLaunchWindowID(stdout.String())}
	}
}

// fleetShellOpenedMsg reports the outcome of opening a shell window.
type fleetShellOpenedMsg struct {
	session  string
	windowID string
	err      error
}

// tmux seams for row actions, overridden in tests.
var (
	newShellWindow    = tmux.NewShellWindow
	killSessionWindow = tmux.KillSessionWindow
	// fleetChildCloser closes a window's dispatched sub-sessions ahead of it.
	fleetChildCloser = reconcile.DefaultChildCloser()
)

// fleetShellCmd opens a plain shell window in g's project. The window's env
// comes from the target tmux session, not the hub process, so no hub identity
// (AGENTCTL_WINDOW_ID, TMUX_PANE, ...) leaks in. Shell windows are not agents:
// fleet.LoadAgents skips them, so the hub never lists them.
func fleetShellCmd(g fleet.Group) tea.Cmd {
	return func() tea.Msg {
		id, err := newShellWindow(g.Session, g.ProjectDir)
		return fleetShellOpenedMsg{session: g.Session, windowID: id, err: err}
	}
}

func fleetCloseCmd(r fleet.Row) tea.Cmd {
	return func() tea.Msg {
		// Sub-sessions first, so none is left orphaned: the reconcile cascade
		// does not run reliably without the monitor. Failures are logged and
		// skipped inside Close; they never block the parent close.
		fleetChildCloser.Close(r.WindowID, r.SessionID)
		return fleetClosedMsg{name: r.Name, err: killSessionWindow(r.Session, r.WindowID)}
	}
}

// ── Actions on the model ─────────────────────────────────────────────────────

// groupForSession finds the loaded group for a tmux session name.
func (m *FleetModel) groupForSession(session string) *fleet.Group {
	for i := range m.groups {
		if m.groups[i].Session == session {
			return &m.groups[i]
		}
	}
	return nil
}

// groupOfLine returns the group that owns line idx (header, placeholder, or
// row), or nil.
func (m *FleetModel) groupOfLine(idx int) *fleet.Group {
	if idx < 0 || idx >= len(m.lines) {
		return nil
	}
	l := m.lines[idx]
	switch l.kind {
	case fleetLineHeader, fleetLinePlaceholder:
		return m.groupForSession(l.group.Session)
	case fleetLineRow:
		return m.groupForSession(l.row.Session)
	}
	return nil
}

// launchForGroup starts a background agent launch in g's project.
func (m *FleetModel) launchForGroup(g *fleet.Group) tea.Cmd {
	if g == nil {
		return nil
	}
	if !groupCanLaunch(*g) {
		m.jumpErr = "cannot launch in " + g.Session + ": unknown project directory"
		return nil
	}
	m.jumpErr = ""
	m.setProgress("launching agent in " + m.projectLabel(g.Session) + "…")
	return fleetLaunch(*g)
}

// shellForGroup opens a shell window in g's project.
func (m *FleetModel) shellForGroup(g *fleet.Group) tea.Cmd {
	if g == nil {
		return nil
	}
	if !groupCanLaunch(*g) {
		m.jumpErr = "cannot open shell in " + g.Session + ": unknown project directory"
		return nil
	}
	m.jumpErr = ""
	m.setProgress("opening shell in " + m.projectLabel(g.Session) + "…")
	return fleetShellCmd(*g)
}

// cronForGroup opens the schedule form in create mode for g's project
// (openSchedForm disarms the selector and loads the Schedules section).
func (m *FleetModel) cronForGroup(g *fleet.Group) tea.Cmd {
	if g == nil {
		return nil
	}
	if !groupCanLaunch(*g) {
		m.jumpErr = "cannot create a cron job for " + g.Session + ": unknown project directory"
		return nil
	}
	m.jumpErr = ""
	return m.openSchedForm(schedFormNew, nil, g.ProjectDir)
}

// armNew turns the header's "+ new" button of the group owning line idx into
// the shell | cron | agent selector. Groups that cannot launch never arm.
func (m *FleetModel) armNew(idx int) {
	g := m.groupOfLine(idx)
	if g == nil || !groupCanLaunch(*g) {
		m.cancelNew()
		return
	}
	m.jumpErr = ""
	m.newSession, m.newLine = g.Session, idx
}

func (m *FleetModel) cancelNew() { m.newSession, m.newLine = "", 0 }

// validateNew cancels the selector when its group is gone, can no longer
// launch, or no longer owns the armed line (after a reload).
func (m *FleetModel) validateNew() {
	if !m.newArmed() {
		return
	}
	g := m.groupForSession(m.newSession)
	if g == nil || !groupCanLaunch(*g) {
		m.cancelNew()
		return
	}
	if o := m.groupOfLine(m.newLine); o == nil || o.Session != m.newSession {
		m.cancelNew()
	}
}

func (m FleetModel) newArmed() bool { return m.newSession != "" }

// newArmedFor reports whether the selector is showing on group g's header.
func (m FleetModel) newArmedFor(g fleet.Group) bool {
	return m.newArmed() && g.Session == m.newSession
}

// handleNewKey handles a key while the selector is armed. handled is false for
// keys that should fall through to normal handling (after cancelling).
func (m *FleetModel) handleNewKey(key string) (cmd tea.Cmd, handled bool) {
	// Target by session, not line index: a reload can move other groups
	// under the armed index.
	g := m.groupForSession(m.newSession)
	if g == nil || !groupCanLaunch(*g) {
		m.cancelNew()
		return nil, key == "s" || key == "a" || key == fleetCronKey || key == "c" || key == "enter" || key == "esc" || key == "n"
	}
	switch key {
	case "s":
		m.cancelNew()
		return m.shellForGroup(g), true
	case fleetCronKey, "c":
		m.cancelNew()
		return m.cronForGroup(g), true
	case "a", "enter":
		m.cancelNew()
		return m.launchForGroup(g), true
	case "esc", "n":
		m.cancelNew()
		return nil, true
	}
	m.cancelNew()
	return nil, false
}

// startClose arms the inline close confirmation for the row at line idx.
func (m *FleetModel) startClose(idx int) {
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != fleetLineRow {
		return
	}
	r := m.lines[idx].row
	if !rowCanClose(r) {
		m.jumpErr = "cannot close this window"
		return
	}
	m.jumpErr = ""
	m.cancelArtDelete()
	m.confirmSession, m.confirmWindow = r.Session, r.WindowID
	m.confirmKids = 0
	if r.SessionID != "" && config.Load().CloseChildrenEnabled() {
		m.confirmKids = m.dispatchedUnder(r)
	}
}

// dispatchedUnder counts the rows dispatched by r, directly or transitively
// (same session, linked by Parent): what closing r also closes.
func (m *FleetModel) dispatchedUnder(r fleet.Row) int {
	seen := map[string]bool{r.WindowID: true}
	queue := []string{r.WindowID}
	n := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, l := range m.lines {
			c := l.row
			if l.kind != fleetLineRow || c.Session != r.Session || c.Parent != id || seen[c.WindowID] {
				continue
			}
			seen[c.WindowID] = true
			n++
			queue = append(queue, c.WindowID)
		}
	}
	return n
}

func (m *FleetModel) cancelClose() {
	m.confirmSession, m.confirmWindow, m.confirmKids = "", "", 0
}

func (m FleetModel) closeArmed() bool { return m.confirmWindow != "" }

// confirmingRow reports whether r is the row awaiting close confirmation.
func (m FleetModel) confirmingRow(r fleet.Row) bool {
	return m.closeArmed() && r.Session == m.confirmSession && r.WindowID == m.confirmWindow
}

// confirmClose runs the armed close. The worktree (if any) is left for
// sweep/cleanup.
func (m *FleetModel) confirmClose() tea.Cmd {
	for _, l := range m.lines {
		if l.kind == fleetLineRow && m.confirmingRow(l.row) {
			m.cancelClose()
			m.recordClose(l.row)
			m.setProgress("closing " + l.row.Name + "…")
			return fleetCloseCmd(l.row)
		}
	}
	m.cancelClose()
	return nil
}

// ── Right-click menu ─────────────────────────────────────────────────────────

type fleetMenuItem struct {
	label string
	key   string // the keyboard equivalent, also the action id
}

// fleetMenuItems lists the context-menu entries for line idx.
func (m *FleetModel) fleetMenuItems(idx int) []fleetMenuItem {
	if idx < 0 || idx >= len(m.lines) {
		return nil
	}
	if m.lines[idx].kind == fleetLineClosed {
		return closedMenuItems(m.lines[idx].closed)
	}
	if m.lines[idx].kind == fleetLineSchedule {
		return schedMenuItems(m.lines[idx])
	}
	var items []fleetMenuItem
	g := m.groupOfLine(idx)
	if m.lines[idx].kind == fleetLineRow {
		items = append(items,
			fleetMenuItem{"focus", "enter"},
			fleetMenuItem{"close", "x"},
			fleetMenuItem{"home", "J"},
		)
		if m.rowCanRename(m.lines[idx].row) {
			items = append(items, fleetMenuItem{"rename", fleetRenameKey})
		}
		if m.lines[idx].row.PRURL != "" {
			items = append(items, fleetMenuItem{"open PR", "p"})
		}
		if m.rowArtifacts(m.lines[idx].row).n > 0 {
			items = append(items, fleetMenuItem{"artifact", fleetArtifactKey})
		}
	}
	if g != nil && groupCanLaunch(*g) {
		items = append(items, fleetMenuItem{"new", "n"}, fleetMenuItem{"new cron", fleetCronKey})
	}
	if g != nil && m.lines[idx].kind == fleetLineHeader && groupCanSweep(*g) {
		items = append(items, fleetMenuItem{"sweep", fleetSweepKey})
	}
	return items
}

// menuItemSpans lays the menu out on one footer line and returns each item's
// [start,end) column range (2-column indent, " · " separators).
func menuItemSpans(items []fleetMenuItem) (text string, spans [][2]int) {
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
func (m FleetModel) footerMessageY() int {
	return m.fleetListTop() + m.fleetListRows() + 1
}

// openMenu opens the context menu for line idx (selecting it first).
func (m *FleetModel) openMenu(idx int) {
	m.menuHelp = false
	m.cancelRename()
	m.cancelClose()
	m.cancelNew()
	m.cancelNewSession()
	m.cancelSweep()
	m.selectLine(idx)
	if len(m.fleetMenuItems(idx)) == 0 {
		m.closeMenu()
		return
	}
	m.menuOpen, m.menuLine = true, idx
}

// runMenuKey executes a menu action by its key id.
func (m *FleetModel) runMenuKey(key string) tea.Cmd {
	line := m.menuLine
	m.closeMenu()
	if line >= 0 && line < len(m.lines) && m.lines[line].kind == fleetLineSchedule {
		m.selectLine(line)
		cmd, _ := m.scheduleKey(key)
		return cmd
	}
	switch key {
	case "enter":
		if line >= 0 && line < len(m.lines) && m.lines[line].kind == fleetLineClosed {
			return m.resumeClosed(m.lines[line].closed)
		}
		_, cmd := m.focusLive()
		return cmd
	case "x":
		m.startClose(line)
	case "J":
		_, cmd := m.jumpToSelectedHome()
		return cmd
	case "n":
		m.armNew(line)
	case fleetCronKey:
		return m.cronForGroup(m.groupOfLine(line))
	case fleetRenameKey:
		return m.openRename(line)
	case fleetSweepKey:
		return m.startSweep(line)
	case "p":
		return m.openSelectedPR()
	case fleetArtifactKey:
		return m.openSelectedArtifact()
	}
	return nil
}

// fleetOpenURL opens a PR URL in the browser; tests replace it.
var fleetOpenURL = tkOpenPRInBrowserCmd

// openSelectedPR opens the selected row's PR (from its session log's pr-link
// record), or does nothing when the row has none.
func (m *FleetModel) openSelectedPR() tea.Cmd {
	if c := m.selectedClosed(); c != nil && c.PRURL != "" {
		return fleetOpenURL(c.PRURL)
	}
	r := m.selectedRow()
	if r == nil || r.PRURL == "" {
		return nil
	}
	return fleetOpenURL(r.PRURL)
}

// applyPendingFocus selects the pending launched row if it is now present and
// clears the target. Reports whether a row was selected.
func (m *FleetModel) applyPendingFocus() bool {
	p := m.pendingFocus
	if p == nil {
		return false
	}
	m.revealRow(p.session, p.windowID) // unfold (or unfilter) the target first
	for i, l := range m.lines {
		if l.kind == fleetLineRow && l.row.Session == p.session && l.row.WindowID == p.windowID {
			m.selectLine(i)
			m.pendingFocus = nil
			return true
		}
	}
	return false
}

// focusRetry handles a retry tick: it gives up after fleetFocusMaxRetries,
// otherwise reloads and schedules the next tick. Stale ticks (target replaced
// or cleared) are ignored.
func (m *FleetModel) focusRetry(msg fleetFocusRetryMsg) tea.Cmd {
	p := m.pendingFocus
	if p == nil || p.session != msg.session || p.windowID != msg.windowID {
		return nil
	}
	if p.tries++; p.tries > fleetFocusMaxRetries {
		m.pendingFocus = nil
		return nil
	}
	return tea.Batch(fleetLoadCmd(), fleetFocusRetryCmd(p.session, p.windowID))
}

func fleetFocusRetryCmd(session, windowID string) tea.Cmd {
	return tea.Tick(fleetFocusRetryInterval, func(time.Time) tea.Msg {
		return fleetFocusRetryMsg{session: session, windowID: windowID}
	})
}
