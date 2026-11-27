package tui

import (
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/tmux"
)

// HubStatusModel is `agentctl hub-status`, the hub's status pane above the
// live viewer (see fleet_status.go): it follows the hub selection file and
// shows, behind a one-column gutter bar in the state colour: the title line
// (the state as a reverse-video pill with its age, the title in bold, the
// project right-aligned), the main line (a "needs you ▸ <tool>  <detail>" line
// for a blocked row with a pending tool or a specific attention reason, then
// the recap wrapped over the free rows, or "Waiting on you since ..." when an
// idle row has none), and a facts line (ticket, PR, parent, cron job,
// sub-agents, agent name and backend/account when they add information).
// Rows are kept in that priority when the pane is short, and the content packs
// upward. Model, context, cost, branch and account usage are in the viewer's
// statusline and never shown here, nor is the last command. It takes no input:
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

// hubStatusLines renders a selected row's status in at most h lines, each
// behind a gutter bar in the state colour. Rows are kept in priority order
// when the pane is short: the state/title line, the "needs you" line (a
// pending tool or a specific attention reason), the recap, then the facts
// line. The content packs upward: the facts follow the recap directly and the
// rest of the pane is blank. With a single row left after the needs-you line
// the recap wins over the facts.
func hubStatusLines(st *hubStatus, w, h int, now time.Time) []string {
	if st.Version < hubStatusVersion {
		return hubStatusLinesLegacy(st, w, h)
	}
	tw := w - hubStatusIndent - 1 // one column of right margin
	gutter := hubStatusGutter(st)
	titleLine, titleCut := hubStatusTitleAt(st, w, now)
	lines := []string{titleLine}
	free := h - 1
	if tw <= 0 || free <= 0 {
		return lines
	}
	if needs := hubStatusNeeds(st, tw); needs != "" {
		lines = append(lines, gutter+needs)
		free--
	}
	text, style := hubStatusBody(st, titleCut, now)
	facts := ""
	if free >= 1 {
		if f := hubStatusFooter(st, tw, now); f != "" && (free >= 2 || text == "") {
			facts = f
			free--
		}
	}
	for _, l := range wrapLines(text, min(tw, hubStatusRecapMeasure), free) {
		lines = append(lines, gutter+style.Render(l))
	}
	if facts != "" {
		lines = append(lines, gutter+facts)
	}
	return lines
}

// hubStatusGutter is the one-column bar in the row's state colour, and the
// space after it, that starts every line of the pane.
func hubStatusGutter(st *hubStatus) string {
	return hubStatusGlyphStyle(st.Glyph).Render(hubStatusBar) + " "
}

// hubStatusBar is the gutter glyph (a plain block element, no icon font).
const hubStatusBar = "▌"

// hubStatusBody is the main-line text: the recap (less its first sentence when
// that is the title) in the normal text colour; with no recap, "Waiting on you
// since ..." for a row waiting on the human, else the Note, if any. Never
// invented filler.
func hubStatusBody(st *hubStatus, titleCut bool, now time.Time) (string, lipgloss.Style) {
	text := st.Detail.Recap
	if st.Detail.RecapIsActivity {
		text = "" // never the activity line, even from an older hub's file
	}
	if st.TitleFromRecap && !titleCut && text != "" {
		// The title is the recap's first sentence: don't print it twice.
		if text = afterFirstSentence(text); text == "" {
			return "", fleetRecapWorkingStyle
		}
	}
	if text != "" {
		return text, fleetRecapWorkingStyle
	}
	if (st.State == "your turn" || st.State == "idle") && !st.StateSince.IsZero() {
		return "Waiting on you since " + hubStatusSinceText(st.StateSince, now), fleetRecapWorkingStyle
	}
	return st.Note, fleetActivityStyle
}

// hubStatusSinceText is when a state began, as the human reads it: the clock
// time today ("15:04"), "yesterday 04:10", or the date for anything older. It
// is read in now's location.
func hubStatusSinceText(since, now time.Time) string {
	since = since.In(now.Location())
	day := func(t time.Time) time.Time {
		y, m, d := t.Date()
		return time.Date(y, m, d, 12, 0, 0, 0, t.Location())
	}
	switch days := int(day(now).Sub(day(since)).Round(24 * time.Hour).Hours() / 24); {
	case days <= 0:
		return since.Format("15:04")
	case days == 1:
		return "yesterday " + since.Format("15:04")
	case since.Year() == now.Year():
		return since.Format("Jan 2")
	}
	return since.Format("Jan 2, 2006")
}

// afterFirstSentence is s without its leading sentence (the one
// fleet.FirstSentence returns, which becomes an untitled row's title), with
// whitespace collapsed; "" when that sentence is all there is.
func afterFirstSentence(s string) string {
	flat := strings.Join(strings.Fields(s), " ")
	first := fleet.FirstSentence(flat)
	if first == "" || !strings.HasPrefix(flat, first) {
		return flat
	}
	return strings.TrimLeft(flat[len(first):], ".!? ")
}

// hubStatusNeeds is the "needs you ▸ <tool>  <detail>" line, in bold: for a
// blocked row with a pending tool, else for a specific attention reason ("" for
// neither: a blocked row with nothing pending just says blocked). The detail
// is untrusted: flattened to one line, escape sequences and control characters
// removed, then end-truncated by display width.
func hubStatusNeeds(st *hubStatus, tw int) string {
	const label = "needs you ▸ "
	if st.State == "blocked" && st.PendingTool != "" {
		head := label + fleet.PendingLabel(st.PendingTool)
		out := fleetBlockedStyle.Bold(true).Render(truncateToWidth(head, tw))
		if rest := tw - lipgloss.Width(head) - 2; rest > 0 {
			if detail := flattenInline(st.PendingToolDetail); detail != "" {
				out += "  " + fleetRecapWorkingStyle.Bold(true).Render(truncateToWidth(detail, rest))
			}
		}
		return out
	}
	if att := flattenInline(st.Attention); att != "" {
		return fleetAttentionStyle.Render(truncateToWidth(label+att, tw))
	}
	return ""
}

// flattenInline makes untrusted text one plain line: ANSI sequences and
// control characters gone, runs of whitespace collapsed.
func flattenInline(s string) string {
	s = ansi.Strip(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// hubStatusTitleAt renders the title line: the gutter bar, the state pill
// (glyph and state word in reverse video, in the state colour) with its age
// after it, then the role and the title in bold, and the project right-aligned
// and dim. A narrow pane drops the project first, then the state word (the
// pill shrinks to the glyph), then the age. The bool reports that the title
// had to be cut to fit.
func hubStatusTitleAt(st *hubStatus, w int, now time.Time) (string, bool) {
	gs := hubStatusGlyphStyle(st.Glyph)
	age := ""
	if !st.StateSince.IsZero() && st.State != "" {
		age = fleet.CompactAge(now.Sub(st.StateSince))
	}
	ageStyle := hubStatusAgeStyle(st, now)
	type variant struct{ word, project, age bool }
	build := func(v variant) (string, int) {
		var sb strings.Builder
		sb.WriteString(hubStatusGutter(st))
		if st.State != "" {
			pill := " " + st.Glyph + " "
			if v.word {
				pill = " " + st.Glyph + " " + strings.ToUpper(st.State) + " "
			}
			sb.WriteString(gs.Bold(true).Reverse(true).Render(pill))
			if v.age && age != "" {
				sb.WriteString(ageStyle.Render(age))
			}
			sb.WriteString("  ")
		} else {
			sb.WriteString(gs.Render(st.Glyph) + " ")
		}
		if st.Role != "" {
			sb.WriteString(fleetDimStyle.Render(st.Role) + " ")
		}
		s := sb.String()
		return s, lipgloss.Width(s)
	}
	variants := []variant{{true, true, true}, {true, false, true}, {false, false, true}, {false, false, false}}
	var left string
	var leftW int
	project, projectW := "", 0
	room := 0
	for i, v := range variants {
		left, leftW = build(v)
		project, projectW = "", 0
		room = w - leftW - 1
		if v.project && st.Project != "" {
			project, projectW = fleetDimStyle.Render(st.Project), lipgloss.Width(st.Project)
			room -= projectW + 2
		}
		if i == len(variants)-1 || room >= fleetMinRecapWidth {
			break
		}
	}
	cut := lipgloss.Width(st.Title) > max(room, 0)
	title := renderRecapTitle(truncateToWidth(st.Title, max(room, 0)), fleetRecapWorkingStyle.Bold(true))
	line := left + title
	if projectW > 0 && room >= 0 {
		pad := w - 1 - lipgloss.Width(line) - projectW
		line += strings.Repeat(" ", max(pad, 2)) + project
	}
	return line, cut
}

// hubStatusAgeStyle is the age's style: dim, except that a row that has sat
// in a waiting state (anything but working) for an hour or more escalates to
// the attention colour.
func hubStatusAgeStyle(st *hubStatus, now time.Time) lipgloss.Style {
	if st.State != "working" && !st.StateSince.IsZero() && now.Sub(st.StateSince) >= time.Hour {
		return fleetAttentionStyle
	}
	return fleetDimStyle
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
	if st.Detail.Recap != "" && !st.Detail.RecapIsActivity && budget-len(lines)+1 > 0 {
		for _, l := range wrapLines(st.Detail.Recap, tw, budget-(len(lines)-1)) {
			lines = append(lines, indent+fleetActivityStyle.Render(l))
		}
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
	case fleet.WaitingGlyph:
		return fleetWaitingStyle()
	case fleet.AttentionGlyph:
		return fleetAttentionStyle
	case "○":
		return fleetIdleStyle
	}
	return fleetDimStyle
}
