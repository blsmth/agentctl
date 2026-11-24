package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/tmux"
)

// HubStatusModel is `agentctl hub-status`, the hub's status pane above the
// live viewer (see fleet_status.go): it follows the hub selection file and
// shows, in the pane's rows: the title line (glyph, role, title, with the
// state word, its age and the project right-aligned), a "needs you" line for
// a blocked row with a pending tool, the recap wrapped to a readable measure,
// and, pinned to the last row, a facts footer (ticket, agent, branch, PR,
// parent, sub-agents; model, context, cost, session length). Never the last
// command: the viewer and the activity pane show it. It takes no input:
// focus, a key or a click hands focus to the viewer below, which is where
// typing belongs. A status block from an older hub (no Version) is laid out
// the old way, with its chips.
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
	v := tea.NewView(renderHubStatus(m.sel, m.readErr, m.width, m.height, time.Now()))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true // focus arriving here (pane navigation) moves on to the viewer
	return v
}

// hubStatusIndent indents the lines under the title to align with it (past
// the glyph and its space).
const hubStatusIndent = 2

// hubStatusRecapMeasure caps the recap's line length: past it, wrapped text
// on a wide pane is harder to read than a shorter column.
const hubStatusRecapMeasure = 120

// renderHubStatus lays sel's status out in exactly h lines of at most w
// columns (see hubStatusLines); now ages the state and session length. With
// no agent selected it is one muted placeholder line.
func renderHubStatus(sel HubSelection, readErr bool, w, h int, now time.Time) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	var lines []string
	if st := sel.Status; st != nil && sel.valid() {
		lines = hubStatusLines(st, w, h, now)
	} else {
		lines = []string{fleetDimStyle.Render(" " + truncateToWidth(followPlaceholderText(readErr, false), w-1))}
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "")
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// hubStatusLines renders a selected row's status in exactly h lines (fewer
// only for a legacy block): the title line, a "needs you" line (blocked with
// a pending tool, when a recap line still fits), the recap top-anchored over
// the free rows, blank rows, and the facts footer on row h. With only one row
// after the title the recap wins and the footer goes.
func hubStatusLines(st *hubStatus, w, h int, now time.Time) []string {
	if st.Version < hubStatusVersion {
		return hubStatusLinesLegacy(st, w, h)
	}
	tw := w - hubStatusIndent - 1 // one column of right margin
	indent := strings.Repeat(" ", hubStatusIndent)
	lines := []string{hubStatusTitleAt(st, w, now)}
	free := h - 1
	var footer string
	if free >= 2 && tw > 0 {
		footer = hubStatusFooter(st, tw, now)
		if footer != "" {
			free--
		}
	}
	if st.State == "blocked" && st.PendingTool != "" && free >= 2 && tw > 0 {
		lines = append(lines, indent+hubStatusNeeds(st, tw))
		free--
	} else if st.Attention != "" && free >= 2 && tw > 0 {
		lines = append(lines, indent+fleetAttentionStyle.Render(truncateToWidth("needs you: "+st.Attention, tw)))
		free--
	}
	rw := min(tw, hubStatusRecapMeasure)
	hasText := false
	if rw > 0 && free > 0 {
		text, style := st.Detail.Recap, fleetActivityStyle
		if st.Detail.RecapIsActivity {
			text = "" // never the activity line, even from an older hub's file
		}
		if text == "" {
			text, style = st.Note, fleetEmptyStyle
		}
		for _, l := range wrapLines(text, rw, free) {
			lines = append(lines, indent+style.Render(l))
			hasText = true
		}
		if !hasText {
			lines = append(lines, indent+fleetEmptyStyle.Render(truncateToWidth("nothing to summarise yet", rw)))
		}
	}
	if footer != "" {
		for len(lines) < h-1 {
			lines = append(lines, "")
		}
		lines = append(lines, indent+footer)
	}
	return lines
}

// hubStatusNeeds is the blocked row's "needs you: <tool>  <detail>" line, one
// line, the detail end-truncated.
func hubStatusNeeds(st *hubStatus, tw int) string {
	label := "needs you: "
	tool := fleet.PendingLabel(st.PendingTool)
	out := fleetBlockedStyle.Render(truncateToWidth(label+tool, tw))
	if rest := tw - lipgloss.Width(label+tool) - 2; rest > 0 && st.PendingToolDetail != "" {
		detail := strings.Join(strings.Fields(st.PendingToolDetail), " ")
		out += "  " + fleetActivityStyle.Render(truncateToWidth(detail, rest))
	}
	return out
}

// hubStatusTitleAt renders the title line: glyph, role and title on the left;
// "<state word> <age> · <project>" right-aligned, the state word in the glyph
// colour, the rest dim. A narrow pane drops the project first, then the age,
// and keeps the state word.
func hubStatusTitleAt(st *hubStatus, w int, now time.Time) string {
	gs := hubStatusGlyphStyle(st.Glyph)
	left := gs.Render(st.Glyph) + " "
	if st.Role != "" {
		left += fleetDimStyle.Render(st.Role) + " "
	}
	age := ""
	if !st.StateSince.IsZero() && st.State != "" {
		age = fleet.CompactAge(now.Sub(st.StateSince))
	}
	type part struct {
		text string
		w    int
	}
	build := func(withAge, withProject bool) part {
		var sb strings.Builder
		pw := 0
		if st.State != "" {
			sb.WriteString(gs.Render(st.State))
			pw += lipgloss.Width(st.State)
		}
		if withAge && age != "" {
			sb.WriteString(fleetDimStyle.Render(" " + age))
			pw += 1 + lipgloss.Width(age)
		}
		if withProject && st.Project != "" {
			sep := " · "
			if sb.Len() == 0 {
				sep = ""
			}
			sb.WriteString(fleetDimStyle.Render(sep + st.Project))
			pw += lipgloss.Width(sep + st.Project)
		}
		return part{sb.String(), pw}
	}
	variants := []part{build(true, true), build(true, false), build(false, false)}
	room := w - lipgloss.Width(left) - 1
	right := part{}
	for i, v := range variants {
		if v.w == 0 {
			continue
		}
		if i == len(variants)-1 || room-v.w-2 >= fleetMinRecapWidth {
			right = v
			break
		}
	}
	if right.w > 0 {
		room -= right.w + 2
	}
	title := renderRecapTitle(truncateToWidth(st.Title, max(room, 0)), fleetRecapWorkingStyle.Bold(true))
	line := left + title
	if right.w > 0 && room >= 0 {
		pad := w - 1 - lipgloss.Width(line) - right.w
		line += strings.Repeat(" ", max(pad, 2)) + right.text
	}
	return line
}

// hubStatusLinesLegacy lays out a status block from an older hub (no facts,
// just chips): title, recap, then the chip line.
func hubStatusLinesLegacy(st *hubStatus, w, h int) []string {
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

// hubStatusTitle renders the legacy title line: glyph, role, title, and the
// project right-aligned when it fits.
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
	case fleet.AttentionGlyph:
		return fleetAttentionStyle
	case "○":
		return fleetIdleStyle
	}
	return fleetDimStyle
}
