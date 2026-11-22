package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/tmux"
)

// HubStatusModel is `agentctl hub-status`, the hub's status pane above the
// live viewer (see fleet_status.go): it follows the hub selection file and
// shows the selected row's title, recap wrapped to the pane width, and
// chips (never the last command: the viewer and the activity pane show it). It takes no input: focus, a key or a click hands focus to
// the viewer below, which is where typing belongs.
type HubStatusModel struct {
	read        func() (HubSelection, error)
	focusViewer func() error

	sel     HubSelection
	readErr bool

	width, height int
	reload        *reloader
}

// hubStatusTickInterval is how often the status pane re-reads the selection
// file: fast enough that it keeps up with the hub cursor, and the read is
// one small file.
const hubStatusTickInterval = 250 * time.Millisecond

type hubStatusTickMsg struct{}

// NewHubStatusModel creates the status pane model reading the published hub
// selection.
func NewHubStatusModel() HubStatusModel {
	return HubStatusModel{
		read:        readPublishedHubSelection,
		focusViewer: tmux.FocusViewerPane,
		width:       80,
		height:      fleetStatusRows,
		reload:      newReloader("hub-status", "hub-status"),
	}
}

// Reexec reports whether the pane quit because the binary changed; the
// caller execs the new image after the program returns.
func (m HubStatusModel) Reexec() bool { return m.reload.triggered() }

// WithoutReload disables hot reload after a failed exec.
func (m HubStatusModel) WithoutReload() HubStatusModel { m.reload.disable(); return m }

func hubStatusTick() tea.Cmd {
	return tea.Tick(hubStatusTickInterval, func(time.Time) tea.Msg { return hubStatusTickMsg{} })
}

// Init reads the selection at once and starts the poll.
func (m HubStatusModel) Init() tea.Cmd {
	return func() tea.Msg { return hubStatusTickMsg{} }
}

// Update handles size, poll, and input (handed to the viewer).
func (m HubStatusModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case hubStatusTickMsg:
		if m.reload.due() {
			return m, tea.Quit
		}
		refreshThemeIfChanged()
		m.poll()
		return m, hubStatusTick()
	case tea.KeyPressMsg, tea.MouseClickMsg, tea.FocusMsg:
		if m.focusViewer != nil {
			focus := m.focusViewer
			return m, func() tea.Msg { _ = focus(); return nil }
		}
	}
	return m, nil
}

// poll re-reads the selection; an unreadable file shows the placeholder.
func (m *HubStatusModel) poll() {
	sel, err := m.read()
	m.readErr = err != nil
	if err != nil || !sel.valid() {
		sel = HubSelection{}
	}
	m.sel = sel
}

// View renders the pane.
func (m HubStatusModel) View() tea.View {
	v := tea.NewView(renderHubStatus(m.sel, m.readErr, m.width, m.height))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true // focus arriving here (pane navigation) moves on to the viewer
	return v
}

// hubStatusIndent indents the lines under the title to align with it (past
// the glyph and its space).
const hubStatusIndent = 2

// renderHubStatus lays sel's status out in exactly h lines of at most w
// columns: the glyph, role and title (project right-aligned), then the
// recap wrapped over every free line (never the activity line), then the
// chips. With no agent selected
// it is one muted placeholder line.
func renderHubStatus(sel HubSelection, readErr bool, w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	var lines []string
	if st := sel.Status; st != nil && sel.valid() {
		lines = hubStatusLines(st, w, h)
	} else {
		lines = []string{fleetDimStyle.Render(" " + truncateToWidth(followPlaceholderText(readErr, false), w-1))}
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, l := range lines[:h] {
		lines[i] = ansi.Truncate(l, w, "")
	}
	return strings.Join(lines[:h], "\n")
}

// hubStatusLines renders a selected row's status in at most h lines.
func hubStatusLines(st *hubStatus, w, h int) []string {
	lines := []string{hubStatusTitle(st, w)}
	tw := w - hubStatusIndent - 1 // one column of right margin
	indent := strings.Repeat(" ", hubStatusIndent)
	var chips string
	if len(st.Chips) > 0 && tw > 0 {
		chips = renderChips(st.Chips, tw)
	}
	budget := h - 1
	if chips != "" && budget > 1 {
		budget--
	} else if chips != "" {
		chips = "" // one row left: the recap matters more
	}
	if st.Note != "" && budget > 0 {
		for _, l := range wrapLines(st.Note, tw, budget) {
			lines = append(lines, indent+fleetEmptyStyle.Render(l))
		}
	}
	// The recap only: never the activity line, even from a file written by
	// an older hub that still carries one.
	hasRecap := false
	if st.Detail.Recap != "" && !st.Detail.RecapIsActivity && budget-len(lines)+1 > 0 {
		for _, l := range wrapLines(st.Detail.Recap, tw, budget-(len(lines)-1)) {
			lines = append(lines, indent+fleetActivityStyle.Render(l))
			hasRecap = true
		}
	}
	if !hasRecap && st.Note == "" && chips == "" && tw > 0 {
		lines = append(lines, indent+fleetEmptyStyle.Render(truncateToWidth("nothing to summarise yet", tw)))
	}
	if chips != "" {
		lines = append(lines, indent+chips)
	}
	return lines
}

// hubStatusTitle renders the title line: glyph, role, title, and the project
// right-aligned when it fits.
func hubStatusTitle(st *hubStatus, w int) string {
	left := hubStatusGlyphStyle(st.Glyph).Render(st.Glyph) + " "
	if st.Role != "" {
		left += fleetDimStyle.Render(st.Role) + " "
	}
	project := ""
	if st.Project != "" {
		project = fleetDimStyle.Render(st.Project)
	}
	room := w - lipgloss.Width(left) - 1
	pw := lipgloss.Width(project)
	if pw > 0 && room-pw-2 >= fleetMinRecapWidth {
		room -= pw + 2
	} else {
		project = ""
	}
	title := renderRecapTitle(truncateToWidth(st.Title, max(room, 0)), fleetRecapWorkingStyle.Bold(true))
	line := left + title
	if project != "" {
		pad := w - 1 - lipgloss.Width(line) - lipgloss.Width(project)
		line += strings.Repeat(" ", max(pad, 1)) + project
	}
	return line
}

// hubStatusGlyphStyle is the style rowGlyph gives glyph.
func hubStatusGlyphStyle(glyph string) lipgloss.Style {
	switch glyph {
	case "⏸":
		return fleetBlockedStyle
	case "●":
		return fleetWorkingStyle
	case "✓":
		return fleetDoneStyle
	case "○":
		return fleetIdleStyle
	}
	return fleetDimStyle
}
