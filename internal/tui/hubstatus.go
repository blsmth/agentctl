package tui

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tmux"
)

// HubStatusModel is `agentctl hub-status`, the hub's status pane above the
// live viewer (see fleet_status.go): it follows the hub selection file and
// shows, behind a one-column gutter bar in the state colour: the title line
// (the title in bold, the project right-aligned, and left of it a 30-minute
// tool-call sparkline when the whole title still fits; see
// hubstatus_activity.go), the main line (a "needs you ▸ <tool>  <detail>" line
// for a blocked row with a pending tool or a specific attention reason, then
// the recap wrapped over the free rows, or "Waiting on you since ..." when an
// idle row has none), and a facts line (ticket, PR, parent, cron job,
// sub-agent dots, agent name and backend/account when they add information;
// the PR carries a CI chip (hubstatus_ci.go) and its latest review verdict).
// The PR and ticket facts are click targets that open in the browser. The
// state, as a reverse-video pill with its age, sits at the right end of the
// pane's last row (in a one-row pane, beside the title): when the content
// reaches that row it shares it and gives way (facts drop whole segments, the
// needs-you line and recap are truncated; the pill never is). The whole pane is drawn on the theme's surface colour when it has one.
// Rows are kept in that priority when the pane is short, and the content packs
// upward. Model, context, cost, branch and account usage are in the viewer's
// statusline and never shown here, nor is the last command. It takes no input:
// focus, a key or a click elsewhere hands focus to the viewer below, which is where
// typing belongs. A status block from an older hub (no Version) is laid out
// the old way, with its chips.
type HubStatusModel struct {
	read        func() (HubSelection, error)
	focusViewer func() error
	// lookupVerdict is the PR's latest review verdict ("" for none).
	lookupVerdict func(pr int, repo string) string

	sel     HubSelection
	readErr bool

	width, height int
	reload        *reloader

	activity *hubActivityCache // tool-call times per session (sparkline)
	ci       *hubCICache       // CI result per PR
	now      func() time.Time
}

// hubLinkSpan is a clickable stretch of the pane: columns [x0, x1) of line y
// open url.
type hubLinkSpan struct {
	y, x0, x1 int
	url       string
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

		lookupVerdict: latestVerdict,
		width:         80,
		height:        fleetStatusRows,
		reload:        newReloader("hub-status", "hub-status"),
		activity:      newHubActivityCache(),
		ci:            newHubCICache(),
		now:           time.Now,
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
		return m, tea.Batch(hubStatusTick(), m.liveRefresh())
	case hubActivityMsg:
		m.activity.store(msg)
	case hubCIMsg:
		m.ci.store(msg, m.clock())
	case tea.MouseClickMsg:
		if url := m.linkAt(msg.X, msg.Y, msg.Button); url != "" {
			return m, func() tea.Msg { _ = openURL(url); return nil }
		}
		return m.handToViewer()
	case tea.KeyPressMsg, tea.FocusMsg:
		return m.handToViewer()
	}
	return m, nil
}

// handToViewer moves focus to the viewer pane below.
func (m HubStatusModel) handToViewer() (tea.Model, tea.Cmd) {
	if m.focusViewer == nil {
		return m, nil
	}
	focus := m.focusViewer
	return m, func() tea.Msg { _ = focus(); return nil }
}

// linkAt is the URL of the link a left click at (x, y) lands on, or "".
func (m HubStatusModel) linkAt(x, y int, b tea.MouseButton) string {
	if b != tea.MouseLeft {
		return ""
	}
	_, spans := renderHubStatusLayout(m.withLive(), m.readErr, m.width, m.height, time.Now())
	for _, sp := range spans {
		if y == sp.y && x >= sp.x0 && x < sp.x1 {
			return sp.url
		}
	}
	return ""
}

// poll re-reads the selection; an unreadable file shows the placeholder.
func (m *HubStatusModel) poll() {
	sel, err := m.read()
	m.readErr = err != nil
	if err != nil || !sel.valid() {
		sel = HubSelection{}
	}
	// Without a PR URL the repo is unknown, and a PR number alone could
	// belong to another repo's review: show no verdict then.
	if st := sel.Status; st != nil {
		st.Verdict = ""
	}
	if st := sel.Status; st != nil && st.PRNumber > 0 && m.lookupVerdict != nil {
		if repo := prURLRepoSlug(st.PRURL); repo != "" {
			st.Verdict = m.lookupVerdict(st.PRNumber, repo)
		}
	}
	m.sel = sel
}

// View renders the pane.
func (m HubStatusModel) View() tea.View {
	v := tea.NewView(renderHubStatus(m.withLive(), m.readErr, m.width, m.height, time.Now()))
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
// no agent selected it is one muted placeholder line. Every line is padded to
// w columns and, when the theme has a surface colour, drawn on it.
func renderHubStatus(sel HubSelection, readErr bool, w, h int, now time.Time) string {
	out, _ := renderHubStatusLayout(sel, readErr, w, h, now)
	return out
}

// renderHubStatusLayout is renderHubStatus plus the clickable spans, in the
// columns they were drawn at.
func renderHubStatusLayout(sel HubSelection, readErr bool, w, h int, now time.Time) (string, []hubLinkSpan) {
	if w <= 0 || h <= 0 {
		return "", nil
	}
	var lines []string
	var spans []hubLinkSpan
	if st := sel.Status; st != nil && sel.valid() {
		lines, spans = hubStatusLayout(st, w, h, now)
	} else {
		lines = []string{hubStatusDim.Render(" " + truncateToWidth(followPlaceholderText(readErr, false), w-1))}
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	bg := hubStatusBackground()
	return renderHubStatusLines(lines, spans, w, h, bg)
}

// renderHubStatusLines clips lines to h rows of w columns, tints and pads
// them, and drops spans on clipped rows.
func renderHubStatusLines(lines []string, spans []hubLinkSpan, w, h int, bg string) (string, []hubLinkSpan) {
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		lines[i] = hubStatusTint(ansi.Truncate(l, w, ""), w, bg)
	}
	for len(lines) < h {
		lines = append(lines, hubStatusTint("", w, bg))
	}
	var kept []hubLinkSpan
	for _, sp := range spans {
		if sp.y < h {
			kept = append(kept, sp)
		}
	}
	return strings.Join(lines, "\n"), kept
}

// hubStatusBackground is the SGR sequence that sets the pane's background: the
// theme's surface colour, or "" when the theme has none (or the terminal has no
// colour), in which case the pane is left untinted.
func hubStatusBackground() string { return hubStatusBackgroundFor(theme.Current()) }

// hubStatusBackgroundFor is hubStatusBackground for theme t.
func hubStatusBackgroundFor(t theme.Theme) string {
	tint := hubStatusTintColor(t)
	if tint == "" {
		return ""
	}
	const probe = "x"
	r := lipgloss.NewStyle().Background(lipgloss.Color(tint)).Render(probe)
	if i := strings.Index(r, probe); i > 0 {
		return r[:i]
	}
	return ""
}

// hubStatusTintColor is the pane's background colour: halfway between the
// theme's base and surface colours, so it sits below the hub's selected-row
// highlight; "" when the theme has no distinct surface colour.
func hubStatusTintColor(t theme.Theme) string {
	if t.Surface == "" || t.Surface == t.Base {
		return ""
	}
	return midColor(t.Base, t.Surface)
}

// hubStatusMinContrast is the WCAG contrast ratio secondary text must reach
// against the tint.
const hubStatusMinContrast = 4.5

// hubStatusDimColor is the secondary text colour for theme t: the midpoint of
// subtle and text when it reads on the pane's tint, else the full text colour.
func hubStatusDimColor(t theme.Theme) string {
	bg := hubStatusTintColor(t)
	if bg == "" {
		bg = t.Base
	}
	mid := midColor(t.Subtle, t.Text)
	if c, ok := contrastRatio(mid, bg); !ok || c >= hubStatusMinContrast {
		return mid
	}
	return t.Text
}

// contrastRatio is the WCAG contrast ratio of two #rrggbb colours.
func contrastRatio(a, b string) (float64, bool) {
	la, ok1 := relLuminance(a)
	lb, ok2 := relLuminance(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05), true
}

func relLuminance(c string) (float64, bool) {
	if len(c) != 7 || c[0] != '#' {
		return 0, false
	}
	v, err := strconv.ParseInt(c[1:], 16, 32)
	if err != nil {
		return 0, false
	}
	lin := func(x int64) float64 {
		f := float64(x) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(v>>16&0xff) + 0.7152*lin(v>>8&0xff) + 0.0722*lin(v&0xff), true
}

// hubStatusTint pads line to w display columns and draws it on bg, restoring
// bg after every style reset inside the line so the tint is one solid block.
// With no bg it only pads.
func hubStatusTint(line string, w int, bg string) string {
	if pad := w - ansi.StringWidth(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	if bg == "" {
		return line
	}
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+bg)
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+bg)
	return bg + line + "\x1b[m"
}

// hubStatusLines renders a selected row's status in at most h lines, each
// behind a gutter bar in the state colour. Rows are kept in priority order
// when the pane is short: the state/title line, the "needs you" line (a
// pending tool or a specific attention reason), the recap, then the facts
// line. The content packs upward: the facts follow the recap directly and the
// rest of the pane is blank. With a single row left after the needs-you line
// the recap wins over the facts.
func hubStatusLines(st *hubStatus, w, h int, now time.Time) []string {
	lines, _ := hubStatusLayout(st, w, h, now)
	return lines
}

// hubStatusRow is one body row (under the title): its text without the gutter
// and a renderer that re-fits it to a narrower width, for when it shares the
// pane's bottom row with the state pill.
type hubStatusRow struct {
	text  string
	spans []footSpan
	fit   func(w int) (string, []footSpan)
}

// hubStatusPillGap is the least gap between the bottom row's text and the pill.
const hubStatusPillGap = 2

// hubStatusLayout is hubStatusLines plus the clickable spans of the facts line.
// The state pill sits at the right end of the pane's last row; when the content
// reaches that row it shares it, and the row's text is cut to leave the pill
// and a gap (facts drop whole segments, the needs-you line is truncated).
func hubStatusLayout(st *hubStatus, w, h int, now time.Time) ([]string, []hubLinkSpan) {
	if st.Version < hubStatusVersion {
		return hubStatusLinesLegacy(st, w, h), nil
	}
	tw := w - hubStatusIndent - 1 // one column of right margin
	gutter := hubStatusGutter(st)
	pill, pillW := hubStatusPill(st, tw, now)
	if h == 1 {
		// Title and pill share the row; the title gives way first.
		if tw <= 0 {
			line, _ := hubStatusTitleAt(st, w, now)
			return []string{line}, nil
		}
		line, _ := hubStatusTitleAt(st, w-pillW-hubStatusPillGap, now)
		return []string{hubStatusPillRow(line, pill, pillW, w)}, nil
	}
	titleLine, titleCut := hubStatusTitleAt(st, w, now)
	lines := []string{titleLine}
	if tw <= 0 {
		return lines, nil
	}
	var rows []hubStatusRow
	free := h - 1
	if needs := hubStatusNeeds(st, tw); needs != "" {
		rows = append(rows, hubStatusRow{text: needs, fit: func(w int) (string, []footSpan) { return hubStatusNeeds(st, w), nil }})
		free--
	}
	text, style := hubStatusBody(st, titleCut, now)
	var facts hubStatusRow
	if free >= 1 {
		if f, sp := hubStatusFooterSpans(st, tw, now); f != "" && (free >= 2 || text == "") {
			facts = hubStatusRow{text: f, spans: sp, fit: func(w int) (string, []footSpan) { return hubStatusFooterSpans(st, w, now) }}
			free--
		}
	}
	for _, l := range wrapLines(text, min(tw, hubStatusRecapMeasure), free) {
		rows = append(rows, hubStatusRow{text: style.Render(l), fit: func(w int) (string, []footSpan) {
			return style.Render(truncateToWidth(l, w)), nil
		}})
	}
	if facts.fit != nil {
		rows = append(rows, facts)
	}
	narrow := max(tw-pillW-hubStatusPillGap, 1)
	if len(rows) == h-1 {
		// The content reaches the bottom row: refit its last row beside the pill.
		last := &rows[len(rows)-1]
		last.text, last.spans = last.fit(narrow)
	}
	var spans []hubLinkSpan
	for i, r := range rows {
		for _, sp := range r.spans {
			spans = append(spans, hubLinkSpan{y: i + 1, x0: hubStatusIndent + sp.x0, x1: hubStatusIndent + sp.x1, url: sp.url})
		}
		lines = append(lines, gutter+r.text)
	}
	if len(rows) == h-1 {
		lines[h-1] = hubStatusPillRow(lines[h-1], pill, pillW, w)
		return lines, spans
	}
	for len(lines) < h-1 {
		lines = append(lines, "")
	}
	return append(lines, hubStatusPillRow(gutter, pill, pillW, w)), spans
}

// hubStatusPillRow right-aligns pill (pillW columns) on line, one column from
// the pane's right edge.
func hubStatusPillRow(line, pill string, pillW, w int) string {
	if pillW == 0 {
		return line
	}
	room := w - 1 - pillW
	line = ansi.Truncate(line, max(room-hubStatusPillGap, 0), "")
	return line + strings.Repeat(" ", max(room-lipgloss.Width(line), 0)) + pill
}

// hubStatusPill is the state as a reverse-video pill (glyph and state word, in
// the state colour) with its age after it, and its width. It starts as wide as
// possible and shrinks (drops the state word, then the age) until the row's text
// keeps some room beside it in tw columns; the pill itself is never dropped. A
// row with no state gets its glyph alone.
func hubStatusPill(st *hubStatus, tw int, now time.Time) (string, int) {
	gs := hubStatusGlyphStyle(st.Glyph)
	if st.State == "" {
		return gs.Render(st.Glyph), lipgloss.Width(st.Glyph)
	}
	age := ""
	if !st.StateSince.IsZero() {
		age = fleet.CompactAge(now.Sub(st.StateSince))
	}
	ageStyle := hubStatusAgeStyle(st, now)
	type variant struct{ word, age bool }
	var out string
	var outW int
	for _, v := range []variant{{true, true}, {false, true}, {false, false}} {
		body := " " + st.Glyph + " "
		if v.word {
			body = " " + st.Glyph + " " + strings.ToUpper(st.State) + " "
		}
		out = gs.Bold(true).Reverse(true).Render(body)
		outW = lipgloss.Width(body)
		if v.age && age != "" {
			out += ageStyle.Render(age)
			outW += lipgloss.Width(age)
		}
		if tw-outW-hubStatusPillGap >= fleetMinRecapWidth {
			break
		}
	}
	return out, outW
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
	switch days := int(day(now).Sub(day(since)).Round(24*time.Hour).Hours() / 24); {
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

// hubStatusTitleAt renders the title line: the gutter bar, the role and the
// title in bold, and the project right-aligned and dim, with the activity
// sparkline left of it. The state pill is not on this line (it is at the
// pane's bottom right, hubStatusPill). A narrow pane drops the sparkline, then
// the project. The bool reports that the title had to be cut to fit.
func hubStatusTitleAt(st *hubStatus, w int, now time.Time) (string, bool) {
	var sb strings.Builder
	sb.WriteString(hubStatusGutter(st))
	if st.Role != "" {
		sb.WriteString(hubStatusDim.Render(st.Role) + " ")
	}
	left := sb.String()
	leftW := lipgloss.Width(left)
	spark, sparkW := hubStatusSpark(st, now)
	project, projectW := "", 0
	room := w - leftW - 1
	if st.Project != "" {
		// The title wins: the project is shown only when the whole title
		// still fits beside it.
		need := max(fleetMinRecapWidth, lipgloss.Width(st.Title))
		if r := room - lipgloss.Width(st.Project) - 2; r >= need {
			project, projectW = hubStatusDim.Render(st.Project), lipgloss.Width(st.Project)
			room = r
		}
	}
	// The sparkline only takes room the title can spare, never cutting it; it
	// is dropped before any other part of the line gives way
	// (hubstatus_activity.go).
	if sparkW == 0 || projectW == 0 && st.Project != "" || room-sparkW-2 < lipgloss.Width(st.Title) {
		spark, sparkW = "", 0
	} else {
		room -= sparkW + 2
	}
	cut := lipgloss.Width(st.Title) > max(room, 0)
	title := renderRecapTitle(truncateToWidth(st.Title, max(room, 0)), fleetRecapWorkingStyle.Bold(true))
	line := left + title
	if right, rightW := joinRight(spark, sparkW, project, projectW); rightW > 0 && room >= 0 {
		pad := w - 1 - lipgloss.Width(line) - rightW
		line += strings.Repeat(" ", max(pad, 2)) + right
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
	return hubStatusDim
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
		left += hubStatusDim.Render(st.Role) + " "
	}
	project := ""
	if st.Project != "" {
		project = hubStatusDim.Render(st.Project)
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
	return hubStatusDim
}
