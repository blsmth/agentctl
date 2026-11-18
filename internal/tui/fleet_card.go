package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/render"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/theme"
)

// fleetCardDwell is how long the pointer must rest on a row before its hover
// card appears.
const fleetCardDwell = 400 * time.Millisecond

const (
	fleetCardMaxW  = 44
	fleetCardMinW  = 30
	fleetCardMinH  = 6 // list rows needed before a card is worth drawing
	fleetCardInset = 4
)

// fleetCardTickMsg fires after the dwell. gen and target identify the hover it
// was scheduled for; a mismatch on arrival means the pointer moved on.
type fleetCardTickMsg struct {
	gen    int
	target fleetHover
}

// fleetCard is the visible hover card: the row it is anchored to, identified
// by (session, window) so a reload that shifts line indexes cannot retarget it.
type fleetCard struct {
	session  string
	windowID string
}

// cardStats is what needs the session log to compute.
type cardStats struct {
	model     string
	in, out   int
	cacheRead int
	turns     int
	cost      sessions.SessionCost
	hasCost   bool
	duration  time.Duration
}

// cardCacheEntry is cardStats keyed by the log's identity.
type cardCacheEntry struct {
	mtime time.Time
	size  int64
	stats cardStats
}

// fleetCardStatsMsg carries parsed stats for a log back to the update loop.
type fleetCardStatsMsg struct {
	path  string
	entry cardCacheEntry
}

func fleetCardTickCmd(gen int, target fleetHover) tea.Cmd {
	return tea.Tick(fleetCardDwell, func(time.Time) tea.Msg {
		return fleetCardTickMsg{gen: gen, target: target}
	})
}

// hideCard drops the card and invalidates any pending dwell tick.
func (m *FleetModel) hideCard() {
	m.card = nil
	m.hoverGen++
}

// rowForCard finds the live row a card is anchored to and its line index.
func (m FleetModel) rowForCard(c *fleetCard) (fleet.Row, int, bool) {
	if c == nil {
		return fleet.Row{}, 0, false
	}
	for i, l := range m.lines {
		if l.kind == fleetLineRow && l.row.Session == c.session && l.row.WindowID == c.windowID {
			return l.row, i, true
		}
	}
	return fleet.Row{}, 0, false
}

// handleCardTick shows the card when the dwell completed on the same target.
func (m FleetModel) handleCardTick(msg fleetCardTickMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.hoverGen || msg.target != m.hover || m.hover.reg != fleetHoverBody {
		return m, nil
	}
	if m.hover.line < 0 || m.hover.line >= len(m.lines) || m.lines[m.hover.line].kind != fleetLineRow {
		return m, nil
	}
	r := m.lines[m.hover.line].row
	if r.Shell {
		return m, nil // nothing agent-specific to show
	}
	m.card = &fleetCard{session: r.Session, windowID: r.WindowID}
	if r.LogPath == "" {
		return m, nil
	}
	var cached *cardCacheEntry
	if e, ok := m.cardCache[r.LogPath]; ok {
		cached = &e
	}
	return m, cardStatsCmd(r, cached)
}

// cardStatsCmd stats the log and, unless the cached entry still matches,
// parses it. Runs off the update loop.
func cardStatsCmd(r fleet.Row, cached *cardCacheEntry) tea.Cmd {
	return func() tea.Msg {
		fi, err := os.Stat(r.LogPath)
		if err != nil {
			return nil
		}
		if cached != nil && cached.mtime.Equal(fi.ModTime()) && cached.size == fi.Size() {
			return nil
		}
		sum, err := sessions.ParseSummary(sessions.Session{ID: r.SessionID, Agent: r.AgentName, LogFile: r.LogPath})
		if err != nil || sum == nil {
			return nil
		}
		return fleetCardStatsMsg{path: r.LogPath, entry: cardCacheEntry{
			mtime: fi.ModTime(), size: fi.Size(), stats: statsFromSummary(sum, sessions.RecordedBackend(r.SessionID)),
		}}
	}
}

func statsFromSummary(sum *sessions.Summary, backend string) cardStats {
	st := cardStats{
		model:     sum.ModelName,
		in:        sum.TotalInputTokens,
		out:       sum.TotalOutputTokens,
		cacheRead: sum.CacheReadTokens,
		turns:     sum.TurnCount,
	}
	billing := sessions.ClassifyBilling(sum.ModelName, backend, config.Load().Backends)
	if c := sessions.CostOfSummary(sum, billing); c.Known {
		st.cost, st.hasCost = c, true
	}
	var first, last time.Time
	for _, tm := range sum.TurnMetrics {
		if tm.Timestamp.IsZero() {
			continue
		}
		if first.IsZero() || tm.Timestamp.Before(first) {
			first = tm.Timestamp
		}
		if tm.Timestamp.After(last) {
			last = tm.Timestamp
		}
	}
	if !first.IsZero() {
		st.duration = last.Sub(first)
	}
	return st
}

// cardLine is one styled line of card content.
type cardStyles struct {
	text, dim, bg lipgloss.Style
}

func newCardStyles() cardStyles {
	t := theme.Current()
	bg := lipgloss.NewStyle().Background(lipgloss.Color(t.Surface))
	return cardStyles{
		text: bg.Foreground(lipgloss.Color(t.Text)),
		dim:  bg.Foreground(lipgloss.Color(t.Muted)),
		bg:   bg,
	}
}

// cardBarCells is the width of the card's context bar.
const cardBarCells = 10

// contextBar renders a small bar plus percent ("██░░░░░░░░ 13%"), coloured
// green below 50, yellow below 80 and red otherwise.
func contextBar(pct int, cs cardStyles) string {
	pct = max(0, min(pct, 100))
	t := theme.Current()
	color := t.Red
	switch {
	case pct < 50:
		color = t.Green
	case pct < 80:
		color = t.Yellow
	}
	filled := (pct*cardBarCells + 99) / 100
	fill := cs.bg.Foreground(lipgloss.Color(color))
	return fill.Render(strings.Repeat("█", filled)) +
		cs.bg.Foreground(lipgloss.Color(t.Overlay)).Render(strings.Repeat("░", cardBarCells-filled)) +
		cs.text.Render(fmt.Sprintf(" %d%%", pct))
}

// cardRow is one label/value pair; value is already styled when styled is set.
type cardRow struct {
	label, value string
	styled       bool
}

// cardContent builds the card's lines (each at most w columns). A line whose
// data is unknown is omitted. stats may be nil while the log is still parsing.
// Layout: title, divider, usage rows, then (after a blank line) provenance rows.
func cardContent(r fleet.Row, st *cardStats, parentName string, now time.Time, w int, cs cardStyles) []string {
	var usage, prov []cardRow
	add := func(dst *[]cardRow, label, value string) {
		if value != "" {
			*dst = append(*dst, cardRow{label: label, value: value})
		}
	}

	glyph, gs := rowGlyph(r)
	stateTxt := r.State
	if stateTxt == "" || (r.Done && stateTxt != "working" && stateTxt != "blocked") {
		stateTxt = map[bool]string{true: "done", false: "idle"}[r.Done]
	}
	if !r.StateSince.IsZero() {
		stateTxt += " " + fleet.CompactAge(now.Sub(r.StateSince))
	}
	title := fleetNameStyle(rowWeight(r)).Background(cs.bg.GetBackground()).Render(r.Name) + cs.bg.Render("  ") + gs.Background(cs.bg.GetBackground()).Render(glyph) + cs.dim.Render(" "+stateTxt)
	out := []string{truncateToWidth(title, w)}

	model := r.ModelName
	in, outTok, cacheRead, turns := r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.TurnCount
	var dur time.Duration
	if st != nil {
		model = firstNonEmpty(st.model, model)
		in, outTok, cacheRead = firstPos(st.in, in), firstPos(st.out, outTok), firstPos(st.cacheRead, cacheRead)
		turns = firstPos(st.turns, turns)
		dur = st.duration
	}
	if dur <= 0 && r.StartTime > 0 {
		dur = now.Sub(time.Unix(r.StartTime, 0))
	}

	if model != "" {
		add(&usage, "model", render.AbbreviateModel(model))
	}
	if r.ContextPct > 0 {
		usage = append(usage, cardRow{label: "context", value: contextBar(r.ContextPct, cs), styled: true})
	}
	var tok []string
	if in > 0 {
		tok = append(tok, render.TokensCompact(in)+" in")
	}
	if outTok > 0 {
		tok = append(tok, render.TokensCompact(outTok)+" out")
	}
	if cacheRead > 0 {
		tok = append(tok, render.TokensCompact(cacheRead)+" cached")
	}
	add(&usage, "tokens", strings.Join(tok, " · "))
	if st != nil && st.hasCost {
		c := fmt.Sprintf("$%.2f", st.cost.USD)
		if st.cost.Billing == sessions.BillingFree {
			c = "free"
		}
		if st.cost.Billing != sessions.BillingUnknown {
			c += " (" + st.cost.Billing.String() + ")"
		}
		add(&usage, "cost", c)
	}
	if dur > 0 {
		v := render.Duration(dur)
		if turns > 0 {
			v += fmt.Sprintf(" · %d turns", turns)
		}
		add(&usage, "session", v)
	} else if turns > 0 {
		add(&usage, "turns", fmt.Sprint(turns))
	}

	add(&prov, "branch", r.Branch)
	if wt := firstNonEmpty(r.WorktreeDir, r.WorktreePath); wt != "" {
		add(&prov, "worktree", filepath.Base(wt))
	}
	if r.PRNumber > 0 {
		pr := fmt.Sprintf("#%d", r.PRNumber)
		if r.PRURL != "" {
			pr += " " + r.PRURL
		}
		add(&prov, "pr", pr)
	}
	add(&prov, "from", parentName)

	var last string
	if act := fleet.ActivityLine(r); act != "" && act != r.Recap.Text {
		last = act
	}

	labelW := 0
	for _, rows := range [][]cardRow{usage, prov} {
		for _, cr := range rows {
			labelW = max(labelW, lipgloss.Width(cr.label))
		}
	}
	if last != "" {
		labelW = max(labelW, len("last"))
	}
	line := func(cr cardRow) string {
		label := cs.dim.Render(cr.label + strings.Repeat(" ", labelW-lipgloss.Width(cr.label)+1))
		v := cr.value
		if !cr.styled {
			v = cs.text.Render(v)
		}
		return truncateToWidth(label+v, w)
	}

	if len(usage)+len(prov) == 0 && last == "" {
		return out
	}
	out = append(out, cs.dim.Render(strings.Repeat("─", max(w, 0))))
	for _, cr := range usage {
		out = append(out, line(cr))
	}
	if len(prov) > 0 && len(usage) > 0 {
		out = append(out, cs.bg.Render(""))
	}
	for _, cr := range prov {
		out = append(out, line(cr))
	}
	if last != "" {
		// Marker then text, matching the sidebar's last-action line.
		label := cs.dim.Render("last" + strings.Repeat(" ", labelW-len("last")+1))
		out = append(out, truncateToWidth(label+cs.dim.Render(fleetActivityMarker)+cs.text.Render(last), w))
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func firstPos(a, b int) int {
	if a > 0 {
		return a
	}
	return b
}

// parentName resolves a row's dispatcher window to its name within the same session.
func (m FleetModel) parentName(r fleet.Row) string {
	if r.Parent == "" {
		return ""
	}
	for _, g := range m.groups {
		for _, o := range g.Rows {
			if o.Session == r.Session && o.WindowID == r.Parent {
				return o.Name
			}
		}
	}
	return ""
}

// lineScreenY is the screen row of line i's own row, and whether it is in view.
func (m FleetModel) lineScreenY(i int) (int, bool) {
	cw := m.listColWidth()
	y := m.fleetListTop()
	for j := m.scrollOffset; j < len(m.lines); j++ {
		if y >= m.fleetListTop()+m.fleetListRows() {
			return 0, false
		}
		if j == i {
			return y, true
		}
		y += m.lineHeight(j, cw)
	}
	return 0, false
}

// cardRect places a card h rows tall for a row at rowY (occupying rowH screen rows)
// inside the list area [top, top+rows): below the row, else above it.
func cardRect(rowY, rowH, top, rows, h int) (y int, ok bool) {
	if y = rowY + rowH; y+h <= top+rows {
		return y, true
	}
	if y = rowY - h; y >= top {
		return y, true
	}
	return 0, false
}

// withCard overlays the hover card on the rendered view, or returns bg as is
// when there is no card, no room, or the anchor row is out of view.
func (m FleetModel) withCard(bg string) string {
	row, idx, ok := m.rowForCard(m.card)
	if !ok {
		return bg
	}
	cw := m.listColWidth()
	w := min(fleetCardMaxW, cw-fleetCardInset)
	top, rows := m.fleetListTop(), m.fleetListRows()
	if w < fleetCardMinW || rows < fleetCardMinH {
		return bg
	}
	rowY, vis := m.lineScreenY(idx)
	if !vis {
		return bg
	}
	var st *cardStats
	if e, ok := m.cardCache[row.LogPath]; ok && row.LogPath != "" {
		st = &e.stats
	}
	cs := newCardStyles()
	lines := cardContent(row, st, m.parentName(row), fleetNow(), w-4, cs)
	t := theme.Current()
	body := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Accent)).
		BorderBackground(lipgloss.Color(t.Surface)).
		Background(lipgloss.Color(t.Surface)).
		Padding(0, 1).
		Width(w).
		Render(strings.Join(lines, "\n"))
	h := strings.Count(body, "\n") + 1
	y, ok := cardRect(rowY, m.lineHeight(idx, cw), top, rows, h)
	if !ok {
		return bg
	}
	x := min(fleetCardInset, max(cw-fleetSidebarRightGutter-w, 0))
	return compositeOverlayAt(bg, body, x, y, cw-fleetSidebarRightGutter, m.termHeight)
}

// storeCardStats records parsed stats; the card picks them up on next render.
func (m *FleetModel) storeCardStats(msg fleetCardStatsMsg) {
	if m.cardCache == nil {
		m.cardCache = map[string]cardCacheEntry{}
	}
	m.cardCache[msg.path] = msg.entry
}
