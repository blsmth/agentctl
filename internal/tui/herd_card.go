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
	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/render"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/theme"
)

// herdCardDwell is how long the pointer must rest on a row before its hover
// card appears.
const herdCardDwell = 400 * time.Millisecond

const (
	herdCardMaxW  = 44
	herdCardMinW  = 30
	herdCardMinH  = 6 // list rows needed before a card is worth drawing
	herdCardInset = 4
)

// herdCardTickMsg fires after the dwell. gen and target identify the hover it
// was scheduled for; a mismatch on arrival means the pointer moved on.
type herdCardTickMsg struct {
	gen    int
	target herdHover
}

// herdCard is the visible hover card: the row it is anchored to, identified
// by (session, window) so a reload that shifts line indexes cannot retarget it.
type herdCard struct {
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

// herdCardStatsMsg carries parsed stats for a log back to the update loop.
type herdCardStatsMsg struct {
	path  string
	entry cardCacheEntry
}

func herdCardTickCmd(gen int, target herdHover) tea.Cmd {
	return tea.Tick(herdCardDwell, func(time.Time) tea.Msg {
		return herdCardTickMsg{gen: gen, target: target}
	})
}

// hideCard drops the card and invalidates any pending dwell tick.
func (m *HerdModel) hideCard() {
	m.card = nil
	m.hoverGen++
}

// rowForCard finds the live row a card is anchored to and its line index.
func (m HerdModel) rowForCard(c *herdCard) (herd.Row, int, bool) {
	if c == nil {
		return herd.Row{}, 0, false
	}
	for i, l := range m.lines {
		if l.kind == herdLineRow && l.row.Session == c.session && l.row.WindowID == c.windowID {
			return l.row, i, true
		}
	}
	return herd.Row{}, 0, false
}

// handleCardTick shows the card when the dwell completed on the same target.
func (m HerdModel) handleCardTick(msg herdCardTickMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.hoverGen || msg.target != m.hover || m.hover.reg != herdHoverBody {
		return m, nil
	}
	if m.hover.line < 0 || m.hover.line >= len(m.lines) || m.lines[m.hover.line].kind != herdLineRow {
		return m, nil
	}
	r := m.lines[m.hover.line].row
	m.card = &herdCard{session: r.Session, windowID: r.WindowID}
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
func cardStatsCmd(r herd.Row, cached *cardCacheEntry) tea.Cmd {
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
		return herdCardStatsMsg{path: r.LogPath, entry: cardCacheEntry{
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

// cardContent builds the card's lines (each at most w columns). A line whose
// data is unknown is omitted. stats may be nil while the log is still parsing.
func cardContent(r herd.Row, st *cardStats, parentName string, now time.Time, w int, cs cardStyles) []string {
	var out []string
	add := func(label, value string) {
		if value == "" {
			return
		}
		out = append(out, truncateToWidth(cs.dim.Render(label+" ")+cs.text.Render(value), w))
	}

	glyph, gs := rowGlyph(r)
	stateTxt := r.State
	if stateTxt == "" || (r.Done && stateTxt != "working" && stateTxt != "blocked") {
		stateTxt = map[bool]string{true: "done", false: "idle"}[r.Done]
	}
	if !r.StateSince.IsZero() {
		stateTxt += " " + herd.CompactAge(now.Sub(r.StateSince))
	}
	title := cs.text.Bold(true).Render(r.Name) + cs.bg.Render("  ") + gs.Background(cs.bg.GetBackground()).Render(glyph) + cs.dim.Render(" "+stateTxt)
	out = append(out, truncateToWidth(title, w))

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
		add("model", render.AbbreviateModel(model))
	}
	if r.ContextPct > 0 {
		add("context", fmt.Sprintf("%d%%", r.ContextPct))
	}
	var tok []string
	if in > 0 {
		tok = append(tok, render.TokensK(in)+" in")
	}
	if outTok > 0 {
		tok = append(tok, render.TokensK(outTok)+" out")
	}
	if cacheRead > 0 {
		tok = append(tok, render.TokensK(cacheRead)+" cached")
	}
	add("tokens", strings.Join(tok, " · "))
	if st != nil && st.hasCost {
		c := fmt.Sprintf("$%.2f", st.cost.USD)
		if st.cost.Billing == sessions.BillingFree {
			c = "free"
		}
		if st.cost.Billing != sessions.BillingUnknown {
			c += " (" + st.cost.Billing.String() + ")"
		}
		add("cost", c)
	}
	if dur > 0 {
		v := render.Duration(dur)
		if turns > 0 {
			v += fmt.Sprintf(" · %d turns", turns)
		}
		add("session", v)
	} else if turns > 0 {
		add("turns", fmt.Sprint(turns))
	}

	add("branch", r.Branch)
	wt := firstNonEmpty(r.WorktreeDir, r.WorktreePath)
	if wt != "" {
		add("worktree", filepath.Base(wt))
	}
	if r.PRNumber > 0 {
		pr := fmt.Sprintf("#%d", r.PRNumber)
		if r.PRURL != "" {
			pr += " " + r.PRURL
		}
		add("pr", pr)
	}
	add("from", parentName)

	if act := herd.ActivityLine(r); act != "" && act != r.Recap.Text {
		add("last", act)
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
func (m HerdModel) parentName(r herd.Row) string {
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
func (m HerdModel) lineScreenY(i int) (int, bool) {
	cw := m.listColWidth()
	y := m.herdListTop()
	for j := m.scrollOffset; j < len(m.lines); j++ {
		if y >= m.herdListTop()+m.herdListRows() {
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
func (m HerdModel) withCard(bg string) string {
	row, idx, ok := m.rowForCard(m.card)
	if !ok {
		return bg
	}
	cw := m.listColWidth()
	w := min(herdCardMaxW, cw-herdCardInset)
	top, rows := m.herdListTop(), m.herdListRows()
	if w < herdCardMinW || rows < herdCardMinH {
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
	lines := cardContent(row, st, m.parentName(row), herdNow(), w-4, cs)
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
	x := min(herdCardInset, max(cw-herdSidebarRightGutter-w, 0))
	return compositeOverlayAt(bg, body, x, y, cw-herdSidebarRightGutter, m.termHeight)
}

// storeCardStats records parsed stats; the card picks them up on next render.
func (m *HerdModel) storeCardStats(msg herdCardStatsMsg) {
	if m.cardCache == nil {
		m.cardCache = map[string]cardCacheEntry{}
	}
	m.cardCache[msg.path] = msg.entry
}
