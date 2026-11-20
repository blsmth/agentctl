package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/claudehome"
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
	fleetCardMaxW  = 72
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
	model       string
	in, out     int
	cacheRead   int
	cacheCreate int
	turns       int
	cost        sessions.SessionCost
	hasCost     bool
	duration    time.Duration
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
		model:       sum.ModelName,
		in:          sum.TotalInputTokens,
		out:         sum.TotalOutputTokens,
		cacheRead:   sum.CacheReadTokens,
		cacheCreate: sum.CacheCreateTokens,
		turns:       sum.TurnCount,
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
	pr           bool // the PR row: its screen line is the click target
}

// cardOpts is what cardContent needs beyond the row and its stats.
type cardOpts struct {
	parent  string // dispatcher's name, "" when none
	account string // the row's account when it is not the default one
	compact bool   // one line each for title and last: the fallback when the full card has no room
}

// Wrap limits for the card's free-text lines.
const (
	cardTitleLines = 2
	cardLastLines  = 3
)

// cardLines is a rendered card body: its lines (each at most w columns) and
// the index of the PR line in it (-1 when there is none).
type cardLines struct {
	lines  []string
	prLine int
}

// cardContent builds the card's lines (each at most w columns). A line whose
// data is unknown is omitted. stats may be nil while the log is still parsing.
// Layout: name/state header, the row's title, divider, usage rows, then
// (after a blank line) provenance rows and the last activity.
func cardContent(r fleet.Row, st *cardStats, o cardOpts, now time.Time, w int, cs cardStyles) cardLines {
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
	header := fleetNameStyle(rowWeight(r)).Background(cs.bg.GetBackground()).Render(r.Name) + cs.bg.Render("  ") + gs.Background(cs.bg.GetBackground()).Render(glyph) + cs.dim.Render(" "+stateTxt)
	out := cardLines{lines: []string{truncateToWidth(header, w)}, prLine: -1}

	titleLines := cardTitleLines
	if o.compact {
		titleLines = 1
	}
	for _, l := range wrapFill(cardTitle(r), w, titleLines) {
		out.lines = append(out.lines, cs.text.Bold(true).Render(l))
	}

	model := r.ModelName
	in, outTok, cacheRead, cacheCreate, turns := r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheCreateTokens, r.TurnCount
	var dur time.Duration
	if st != nil {
		model = firstNonEmpty(st.model, model)
		in, outTok, cacheRead = firstPos(st.in, in), firstPos(st.out, outTok), firstPos(st.cacheRead, cacheRead)
		cacheCreate = firstPos(st.cacheCreate, cacheCreate)
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
	add(&usage, "tokens", tokenSummary(in+cacheCreate, outTok, cacheRead))
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

	if base := fleet.BaseName(r); base != "" && base != fleet.StripInstanceSuffix(r.Name) {
		add(&prov, "agent", base)
	}
	add(&prov, "account", o.account)
	add(&prov, "branch", r.Branch)
	if wt := firstNonEmpty(r.WorktreeDir, r.WorktreePath); wt != "" {
		add(&prov, "worktree", filepath.Base(wt))
	}
	prText, prURL := prLabel(r.PRNumber, r.PRURL)
	if prText != "" {
		prov = append(prov, cardRow{label: "pr", value: prText, pr: true})
	}
	add(&prov, "from", o.parent)
	if r.ScheduledBy != "" {
		v := r.ScheduledBy
		if !r.ScheduledAt.IsZero() {
			v += " · fired " + fleet.CompactAge(now.Sub(r.ScheduledAt)) + " ago"
		}
		add(&prov, "scheduled", v)
	}

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
	labelCell := func(label string) string {
		return cs.dim.Render(label + strings.Repeat(" ", labelW-lipgloss.Width(label)+1))
	}
	valueW := max(w-labelW-1, 0)
	line := func(cr cardRow) string {
		v := cr.value
		switch {
		case cr.pr:
			// Cut the text before styling so the OSC 8 link is never split.
			v = cs.text.Underline(prURL != "").Hyperlink(prURL).Render(truncateToWidth(v, valueW))
		case !cr.styled:
			v = cs.text.Render(truncateToWidth(v, valueW))
		}
		return truncateToWidth(labelCell(cr.label)+v, w)
	}

	if len(usage)+len(prov) == 0 && last == "" {
		return out
	}
	out.lines = append(out.lines, cs.dim.Render(strings.Repeat("─", max(w, 0))))
	for _, cr := range usage {
		out.lines = append(out.lines, line(cr))
	}
	if len(prov) > 0 && len(usage) > 0 {
		out.lines = append(out.lines, cs.bg.Render(""))
	}
	for _, cr := range prov {
		if cr.pr {
			out.prLine = len(out.lines)
		}
		out.lines = append(out.lines, line(cr))
	}
	if last != "" {
		out.lines = append(out.lines, lastLines(last, labelCell("last"), labelW+1, w, o.compact, cs)...)
	}
	return out
}

// lastLines renders the last activity after its label and marker, wrapped
// to cardLastLines (1 when compact) with continuation lines indented under
// the text, so the label column stays clear. Words longer than a line
// (paths, flags) are hard-wrapped; only the final line gets an ellipsis.
func lastLines(last, label string, labelW, w int, compact bool, cs cardStyles) []string {
	n := cardLastLines
	if compact {
		n = 1
	}
	indent := labelW + lipgloss.Width(fleetActivityMarker)
	var out []string
	for i, l := range wrapFill(last, w-indent, n) {
		if i == 0 {
			out = append(out, label+cs.dim.Render(fleetActivityMarker)+cs.text.Render(l))
			continue
		}
		out = append(out, cs.bg.Render(strings.Repeat(" ", indent))+cs.text.Render(l))
	}
	return out
}

// wrapFill is wrapLines for plain text whose final kept line, when the
// text does not fit, carries on with the rest of the text cut at w with
// "…", rather than stopping where the wrap broke (a hyphen in a long path
// would leave a near-empty last line).
func wrapFill(s string, w, n int) []string {
	lines := wrapLines(s, w, n)
	if len(lines) < n || !strings.HasSuffix(lines[n-1], "…") {
		return lines
	}
	pos := 0
	for _, l := range lines[:n-1] {
		i := strings.Index(s[pos:], l)
		if i < 0 {
			return lines
		}
		pos += i + len(l)
	}
	lines[n-1] = truncateToWidth(strings.TrimLeft(s[pos:], " "), w)
	return lines
}

// cardTitle is the title the row's list line shows (manual titles
// included), or "" when the row only has the empty-recap placeholder.
func cardTitle(r fleet.Row) string {
	rc := rowRecap(r)
	if rc.Source == fleet.RecapNone {
		return ""
	}
	return rc.Short()
}

// tokenSummary reads "1.2k new in · 11k out · 2.3M cached". newIn is every
// input token not served from cache (uncached input plus cache writes); a
// bare "in" for the uncached part alone reads like a bug next to "cached".
func tokenSummary(newIn, out, cacheRead int) string {
	var tok []string
	if newIn > 0 {
		tok = append(tok, render.TokensCompact(newIn)+" new in")
	}
	if out > 0 {
		tok = append(tok, render.TokensCompact(out)+" out")
	}
	if cacheRead > 0 {
		tok = append(tok, render.TokensCompact(cacheRead)+" cached")
	}
	return strings.Join(tok, " · ")
}

var prNumberRe = regexp.MustCompile(`/pull/(\d+)`)

// prLabel is the PR row's text ("#N owner/repo") and the URL to link it to.
// A URL with no recorded number takes its number from the /pull/N path; with
// neither the text is the shortened URL. Both empty means no PR row.
func prLabel(num int, url string) (text, link string) {
	if num <= 0 && url != "" {
		if m := prNumberRe.FindStringSubmatch(url); m != nil {
			num, _ = strconv.Atoi(m[1])
		}
	}
	short := shortPRURL(url)
	switch {
	case num > 0 && short != "":
		return fmt.Sprintf("#%d %s", num, short), url
	case num > 0:
		return fmt.Sprintf("#%d", num), ""
	}
	return short, url
}

// shortPRURL drops the scheme, a github.com host and the /pull/N tail
// ("https://github.com/o/r/pull/7" → "o/r"): the number is shown already.
func shortPRURL(url string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	s = strings.TrimPrefix(s, "github.com/")
	if loc := prNumberRe.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	return strings.TrimSuffix(s, "/")
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

// cardPlacement is a rendered card and where it goes on screen.
type cardPlacement struct {
	body   string
	x, y   int // top-left cell
	w      int
	prY    int    // screen row of the PR line, -1 when none
	prLink string // URL the PR line opens, "" when none
}

// cardLayout renders the visible card and places it, or reports false when
// there is no card, no room, or the anchor row is out of view. A card too
// tall for the space above and below its row is retried in compact form
// (one-line title and last activity) before giving up.
func (m FleetModel) cardLayout() (cardPlacement, bool) {
	row, idx, ok := m.rowForCard(m.card)
	if !ok {
		return cardPlacement{}, false
	}
	cw := m.listColWidth()
	w := min(fleetCardMaxW, cw-fleetCardInset)
	top, rows := m.fleetListTop(), m.fleetListRows()
	if w < fleetCardMinW || rows < fleetCardMinH {
		return cardPlacement{}, false
	}
	rowY, vis := m.lineScreenY(idx)
	if !vis {
		return cardPlacement{}, false
	}
	var st *cardStats
	if e, ok := m.cardCache[row.LogPath]; ok && row.LogPath != "" {
		st = &e.stats
	}
	opts := cardOpts{parent: m.parentName(row), account: cardAccount(row.Account)}
	cs := newCardStyles()
	t := theme.Current()
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Accent)).
		BorderBackground(lipgloss.Color(t.Surface)).
		Background(lipgloss.Color(t.Surface)).
		Padding(0, 1).
		Width(w)
	for _, compact := range []bool{false, true} {
		opts.compact = compact
		c := cardContent(row, st, opts, fleetNow(), w-4, cs)
		body := box.Render(strings.Join(c.lines, "\n"))
		h := strings.Count(body, "\n") + 1
		y, ok := cardRect(rowY, m.lineHeight(idx, cw), top, rows, h)
		if !ok {
			continue
		}
		p := cardPlacement{body: body, x: min(fleetCardInset, max(cw-fleetSidebarRightGutter-w, 0)), y: y, w: w, prY: -1}
		if c.prLine >= 0 {
			p.prY = y + 1 + c.prLine // +1: the top border
			_, p.prLink = prLabel(row.PRNumber, row.PRURL)
		}
		return p, true
	}
	return cardPlacement{}, false
}

// cardAccount is the account to show on the card: "" for none or the default.
func cardAccount(account string) string {
	if account == "" || claudehome.IsDefaultAccount(account) {
		return ""
	}
	return account
}

// withCard overlays the hover card on the rendered view, or returns bg as is
// when there is no card, no room, or the anchor row is out of view.
func (m FleetModel) withCard(bg string) string {
	p, ok := m.cardLayout()
	if !ok {
		return bg
	}
	cw := m.listColWidth()
	return compositeOverlayAt(bg, p.body, p.x, p.y, cw-fleetSidebarRightGutter, m.termHeight)
}

// cardLinkAt is the URL a left click at (x, y) opens: the visible card's PR
// line, when it has a link. Everything else on or off the card is "".
func (m FleetModel) cardLinkAt(x, y int) string {
	if m.card == nil {
		return ""
	}
	p, ok := m.cardLayout()
	if !ok || p.prLink == "" || y != p.prY || x < p.x || x >= p.x+p.w {
		return ""
	}
	return p.prLink
}

// storeCardStats records parsed stats; the card picks them up on next render.
func (m *FleetModel) storeCardStats(msg fleetCardStatsMsg) {
	if m.cardCache == nil {
		m.cardCache = map[string]cardCacheEntry{}
	}
	m.cardCache[msg.path] = msg.entry
}
