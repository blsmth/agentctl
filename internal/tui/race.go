package tui

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/race"
	"github.com/blsmth/agentctl/internal/theme"
)

// `agentctl race`: a replay of an agent transcript racing a simulated human
// doing the same keystrokes and reading on the same compressed clock. The
// model is driven only by raceTickMsg times, so tests feed it a fake clock.

const (
	raceFrame = 40 * time.Millisecond
	// raceBeat is how long (wall time) the human lane keeps going after the
	// agent finishes, before the end card.
	raceBeat = 3 * time.Second
	// Below these sizes the layout changes.
	raceWideCols = 100
	raceWideRows = 26
	raceTinyCols = 40
	raceTinyRows = 12
)

type raceTickMsg time.Time

func raceTick() tea.Cmd {
	return tea.Tick(raceFrame, func(t time.Time) tea.Msg { return raceTickMsg(t) })
}

type raceStyles struct {
	agent, human, accent, text, bright, muted, subtle, overlay, red, yellow, blue, sky, info color.Color
}

func newRaceStyles() raceStyles {
	t := theme.Current()
	c := func(s string) color.Color { return lipgloss.Color(s) }
	return raceStyles{
		agent: c(t.Green), human: c(t.Orange), accent: c(t.Accent), text: c(t.Text),
		bright: c(t.Bright), muted: c(t.Muted), subtle: c(t.Subtle), overlay: c(t.Overlay),
		red: c(t.Red), yellow: c(t.Yellow), blue: c(t.Blue), sky: c(t.Sky), info: c(t.Info),
	}
}

// RaceModel is the race TUI.
type RaceModel struct {
	r       *race.Race
	title   string
	pb      *race.Playback
	factor0 float64 // initial replay rate, restored by r

	width, height int
	now           time.Time // last tick
	agentDoneAt   time.Time // wall time the agent finished; zero before
	endCard       bool
	ticking       bool

	st    raceStyles
	runes map[int][]rune // typed text per step, decoded once
}

// NewRaceModel returns a race over r replayed at factor transcript seconds
// per wall second, starting at wall time now.
func NewRaceModel(r *race.Race, title string, factor float64, now time.Time) RaceModel {
	return RaceModel{
		r: r, title: title, pb: race.NewPlayback(factor, now), factor0: factor,
		width: 120, height: 35, now: now, ticking: true,
		st: newRaceStyles(), runes: map[int][]rune{},
	}
}

// Init starts the frame clock.
func (m RaceModel) Init() tea.Cmd { return raceTick() }

// Update handles frames, resizes and keys.
func (m RaceModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case raceTickMsg:
		m = m.advance(time.Time(msg))
		if m.endCard {
			m.ticking = false
			return m, nil
		}
		return m, raceTick()
	case tea.KeyPressMsg:
		return m.key(msg.String())
	}
	return m, nil
}

// advance moves the playback to wall time now and flips to the end card one
// beat after the agent finishes.
func (m RaceModel) advance(now time.Time) RaceModel {
	m.now = now
	pos := m.pb.Advance(now)
	if m.endCard || pos < m.r.AgentTotal {
		return m
	}
	if m.agentDoneAt.IsZero() {
		m.agentDoneAt = now
	}
	if !m.pb.Paused() && now.Sub(m.agentDoneAt) >= raceBeat {
		m.endCard = true
	}
	return m
}

func (m RaceModel) key(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "space", " ":
		if !m.endCard {
			m.pb.TogglePause(m.now)
		}
	case "+", "=", "right":
		m.pb.SetFactor(m.pb.Factor()*2, m.now)
	case "-", "_", "left":
		m.pb.SetFactor(m.pb.Factor()/2, m.now)
	case "e":
		m.endCard = true
	case "r":
		m.pb = race.NewPlayback(m.factor0, m.now)
		m.agentDoneAt = time.Time{}
		m.endCard = false
		if !m.ticking {
			m.ticking = true
			return m, raceTick()
		}
	}
	return m, nil
}

// View renders the race.
func (m RaceModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m RaceModel) render() string {
	w, h := m.width, m.height
	switch {
	case w < raceTinyCols || h < raceTinyRows:
		return m.renderTiny()
	case m.endCard:
		return m.renderEndCard()
	case w >= raceWideCols && h >= raceWideRows:
		return m.renderWide()
	}
	return m.renderNarrow()
}

// ── shared numbers ────────────────────────────────────────────────────────────

type raceNow struct {
	pos, agentEl, humanEl time.Duration
	agent                 race.AgentPos
	human                 race.HumanPos
	speedup               float64
	byHand                time.Duration // human time for the agent's work so far
}

func (m RaceModel) numbers() raceNow {
	pos := m.pb.Pos()
	n := raceNow{
		pos:     pos,
		agentEl: min(pos, m.r.AgentTotal),
		humanEl: min(pos, m.r.HumanTotal),
		agent:   m.r.AgentAt(pos),
		human:   m.r.HumanAt(pos),
	}
	n.byHand = m.r.HumanWorkFor(n.agentEl)
	if n.agentEl > 0 {
		n.speedup = float64(n.byHand) / float64(n.agentEl)
	}
	return n
}

func (n raceNow) multiplier() string {
	if n.agentEl <= 0 {
		return "0x"
	}
	return race.Multiplier(n.speedup)
}

func pct(f float64) string {
	p := f * 100
	if p > 0 && p < 10 {
		return fmt.Sprintf("%.1f%%", p)
	}
	return fmt.Sprintf("%.0f%%", math.Floor(p))
}

func (m RaceModel) agentFrac(n raceNow) float64 {
	if m.r.AgentTotal <= 0 {
		return 1
	}
	return math.Min(1, float64(n.agentEl)/float64(m.r.AgentTotal))
}

func (m RaceModel) humanFrac(n raceNow) float64 {
	if m.r.HumanTotal <= 0 {
		return 1
	}
	return math.Min(1, float64(n.humanEl)/float64(m.r.HumanTotal))
}

func (m RaceModel) agentStatus(n raceNow) string {
	if n.agent.Done {
		return fmt.Sprintf("✓ finished · %d steps", len(m.r.Steps))
	}
	return fmt.Sprintf("step %d/%d · %s", n.agent.Index+1, len(m.r.Steps), pct(m.agentFrac(n)))
}

func (m RaceModel) humanStatus(n raceNow) string {
	if n.human.Done {
		return fmt.Sprintf("✓ finished · %d steps", len(m.r.Steps))
	}
	return fmt.Sprintf("step %d/%d · %s · %s", n.human.Index+1, len(m.r.Steps), pct(m.humanFrac(n)), n.human.Phase)
}

// ── layouts ───────────────────────────────────────────────────────────────────

func (m RaceModel) renderWide() string {
	w, h := m.width, m.height
	n := m.numbers()
	mid := 30
	if w < 110 {
		mid = 26
	}
	left := (w - mid) / 2
	right := w - mid - left
	header := lipgloss.JoinHorizontal(lipgloss.Top,
		m.clockBox("AGENT", race.Clock(n.agentEl), m.agentStatus(n), m.st.agent, left),
		m.clockBox("SPEEDUP", n.multiplier(), "by hand: "+race.Friendly(n.byHand), m.st.accent, mid),
		m.clockBox(fmt.Sprintf("HUMAN @ %g WPM", m.r.Params.WPM), race.Clock(n.humanEl), m.humanStatus(n), m.st.human, right),
	)
	laneH := h - 1 - 7 - 2 - 2
	lw := w / 2
	lanes := lipgloss.JoinHorizontal(lipgloss.Top,
		m.box("AGENT", m.agentLane(n, lw-4, laneH-2), lw, laneH, m.st.agent),
		m.box(fmt.Sprintf("HUMAN · %g WPM typing, %g WPM reading", m.r.Params.WPM, m.r.Params.ReadWPM), m.humanLane(n, w-lw-4, laneH-2), w-lw, laneH, m.st.human),
	)
	return strings.Join([]string{m.titleLine(w, true), header, lanes, m.bars(n, w), m.footer(w, false)}, "\n")
}

func (m RaceModel) renderNarrow() string {
	w, h := m.width, m.height
	n := m.numbers()
	agent := m.style(m.st.agent, true).Render("AGENT "+race.Clock(n.agentEl)) + "  " + m.style(m.st.subtle, false).Render(m.agentStatus(n))
	human := m.style(m.st.human, true).Render("HUMAN "+race.Clock(n.humanEl)) + "  " + m.style(m.st.subtle, false).Render(m.humanStatus(n))
	mult := m.style(m.st.accent, true).Render(n.multiplier()+" faster") + "  " + m.style(m.st.subtle, false).Render("by hand: "+race.Friendly(n.byHand))
	header := m.box("RACE", []string{agent, human, mult}, w, 5, m.st.accent)

	avail := h - 1 - 5 - 2 - 3
	agentH := avail / 2
	humanH := avail - agentH
	parts := []string{m.titleLine(w, false), header}
	if agentH >= 3 {
		parts = append(parts, m.box("AGENT", m.agentLane(n, w-4, agentH-2), w, agentH, m.st.agent))
	}
	if humanH >= 3 {
		parts = append(parts, m.box(fmt.Sprintf("HUMAN · %g WPM", m.r.Params.WPM), m.humanLane(n, w-4, humanH-2), w, humanH, m.st.human))
	}
	parts = append(parts, m.bars(n, w), m.footer(w, true))
	return strings.Join(parts, "\n")
}

func (m RaceModel) renderTiny() string {
	n := m.numbers()
	lines := []string{
		m.style(m.st.agent, true).Render("AGENT " + race.Clock(n.agentEl)),
		m.style(m.st.human, true).Render("HUMAN " + race.Clock(n.humanEl)),
		m.style(m.st.accent, true).Render(n.multiplier() + " faster"),
	}
	if m.endCard {
		lines = append(lines, fmt.Sprintf("Agent %s / Human %s", race.Friendly(m.r.AgentTotal), race.Friendly(m.r.HumanTotal)))
	}
	lines = append(lines, m.style(m.st.muted, false).Render("(enlarge the terminal)"))
	return fitBlock(lines, m.width, m.height)
}

func (m RaceModel) renderEndCard() string {
	w, h := m.width, m.height
	r := m.r
	cw := min(w-2, 86)
	iw := cw - 4
	center := func(s string) string { return centerIn(s, iw) }
	agentS := m.style(m.st.agent, true)
	humanS := m.style(m.st.human, true)
	muted := m.style(m.st.muted, false)
	subtle := m.style(m.st.subtle, false)

	var lines []string
	add := func(s ...string) { lines = append(lines, s...) }
	add(center(m.style(m.st.bright, true).Render("⚑  FINISH  ⚑")), "")
	add(center(agentS.Render("Agent: "+race.Friendly(r.AgentTotal)) + subtle.Render("  /  ") + humanS.Render("Human: "+race.Friendly(r.HumanTotal))))
	if r.HumanTotal >= time.Hour {
		add(center(muted.Render(fmt.Sprintf("(%s of nonstop typing and reading)", race.HM(r.HumanTotal)))))
	}
	add("")
	mult := race.Multiplier(r.Speedup())
	if big := bigText(mult); lipgloss.Width(big[0]) <= iw && h >= 24 {
		for _, row := range big {
			add(center(m.style(m.st.accent, true).Render(row)))
		}
		add(center(subtle.Render("faster than by hand")))
	} else {
		add(center(m.style(m.st.accent, true).Render(mult + " faster than by hand")))
	}
	add("")
	calls := 0
	var names []string
	for i, tc := range r.SortedTools() {
		calls += tc.Count
		if i < 5 {
			names = append(names, fmt.Sprintf("%s %d", tc.Tool, tc.Count))
		}
	}
	if len(r.ToolCalls) > 5 {
		names = append(names, "…")
	}
	add(center(fmt.Sprintf("%d steps · %d tool calls · %d messages", len(r.Steps), calls, r.TextBlocks)))
	if len(names) > 0 {
		add(center(subtle.Render(strings.Join(names, " · "))))
	}
	add(center(fmt.Sprintf("%s chars typed · %s chars read", commas(r.TypedRunes), commas(r.ReadRunes))))
	add("")
	for _, a := range []string{r.HumanAssumptions(), r.AgentAssumptions()} {
		for _, l := range wrapPlain(a, iw) {
			add(center(muted.Render(l)))
		}
	}
	add("", center(renderRaceKeys(m.st, "r", "replay", "q", "quit")))

	card := m.box("agentctl race · "+m.title, lines, cw, len(lines)+2, m.st.accent)
	if len(lines)+2 > h {
		return fitBlock(strings.Split(card, "\n"), w, h)
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, card)
}

// ── pieces ────────────────────────────────────────────────────────────────────

func (m RaceModel) style(c color.Color, bold bool) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(c).Bold(bold)
}

// titleLine is the top row; with keys, the key hints sit at its right.
func (m RaceModel) titleLine(w int, keys bool) string {
	left := m.style(m.st.accent, true).Render(" ⚑ agentctl race") +
		m.style(m.st.subtle, false).Render(fmt.Sprintf("  %s · %d steps · replay %s", m.title, len(m.r.Steps), race.Multiplier(m.pb.Factor())))
	right := ""
	if m.pb.Paused() {
		right = m.style(m.st.yellow, true).Render("⏸ paused") + "   "
	}
	if keys {
		right += raceKeyHints(m.st) + " "
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m RaceModel) clockBox(title, value, status string, c color.Color, w int) string {
	iw := w - 4
	big := bigText(value)
	var lines []string
	if lipgloss.Width(big[0]) <= iw {
		lines = append(lines, "")
		for _, row := range big {
			lines = append(lines, centerIn(m.style(c, true).Render(row), iw))
		}
	} else {
		lines = append(lines, "", "", centerIn(m.style(c, true).Render(value), iw), "")
	}
	lines = append(lines, centerIn(m.style(m.st.subtle, false).Render(status), iw))
	return m.box(title, lines, w, 7, c)
}

// box draws a rounded border w x h with title in the top edge and lines
// fitted inside (one space of padding each side).
func (m RaceModel) box(title string, lines []string, w, h int, c color.Color) string {
	if w < 6 || h < 2 {
		return ""
	}
	border := m.style(c, false)
	iw := w - 4
	title = ansi.Truncate(title, max(0, w-6), "…")
	top := border.Render("╭─ ") + m.style(c, true).Render(title) + " "
	top += border.Render(strings.Repeat("─", max(0, w-lipgloss.Width(top)-1)) + "╮")
	out := []string{top}
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, border.Render("│")+" "+fit(l, iw)+" "+border.Render("│"))
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", w-2)+"╯"))
	return strings.Join(out, "\n")
}

func (m RaceModel) bars(n raceNow, w int) string {
	bw := max(10, w-44)
	af, hf := m.agentFrac(n), m.humanFrac(n)
	agentTail := "done in " + race.Friendly(m.r.AgentTotal)
	if !n.agent.Done {
		agentTail = "of " + race.Friendly(m.r.AgentTotal)
	}
	humanTail := "done"
	if !n.human.Done {
		left := m.r.HumanTotal - n.humanEl
		humanTail = "ETA " + race.Friendly(left)
		if left >= 8*time.Hour {
			humanTail += " (" + race.HM(left) + ")"
		}
	}
	line := func(label string, f float64, c color.Color, tail string) string {
		return fit(m.style(c, true).Render(label)+" "+m.bar(f, bw, c)+" "+
			m.style(m.st.text, false).Render(fmt.Sprintf("%5s", pct(f)))+"  "+m.style(m.st.subtle, false).Render(tail), w)
	}
	return line("AGENT", af, m.st.agent, agentTail) + "\n" + line("HUMAN", hf, m.st.human, humanTail)
}

func (m RaceModel) bar(f float64, w int, c color.Color) string {
	full := int(math.Round(f * float64(w)))
	if f > 0 && full == 0 {
		full = 1
	}
	full = min(full, w)
	return m.style(c, false).Render(strings.Repeat("█", full)) + m.style(m.st.overlay, false).Render(strings.Repeat("░", w-full))
}

// footer states the assumptions, one line per racer, then (with keys) the
// key hints.
func (m RaceModel) footer(w int, keys bool) string {
	muted := m.style(m.st.muted, false)
	lines := []string{
		fit(muted.Render(" "+m.r.HumanAssumptions()), w),
		fit(muted.Render(" "+m.r.AgentAssumptions()), w),
	}
	if keys {
		lines = append(lines, fit(" "+raceKeyHints(m.st), w))
	}
	return strings.Join(lines, "\n")
}

func raceKeyHints(st raceStyles) string {
	return renderRaceKeys(st, "space", "pause", "+/-", "speed", "r", "restart", "e", "end", "q", "quit")
}

func renderRaceKeys(st raceStyles, pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, lipgloss.NewStyle().Foreground(st.accent).Bold(true).Render(pairs[i])+" "+
			lipgloss.NewStyle().Foreground(st.muted).Render(pairs[i+1]))
	}
	return strings.Join(parts, lipgloss.NewStyle().Foreground(st.overlay).Render("  ·  "))
}

// ── lanes ─────────────────────────────────────────────────────────────────────

func (m RaceModel) glyph(s race.Step) (string, color.Color) {
	if s.Kind == race.KindText {
		return "✦", m.st.subtle
	}
	switch s.Tool {
	case "Bash":
		return "❯", m.st.yellow
	case "Read":
		return "▤", m.st.blue
	case "Write":
		return "✚", m.st.agent
	case "Edit", "MultiEdit", "NotebookEdit":
		return "✎", m.st.agent
	case "Grep", "Glob":
		return "⌕", m.st.sky
	case "Task", "Agent":
		return "⧉", m.st.info
	case "WebFetch", "WebSearch":
		return "◍", m.st.sky
	case "TodoWrite":
		return "☰", m.st.subtle
	}
	return "•", m.st.subtle
}

// stepLine is a step's one-line heading: glyph, tool name, summary.
func (m RaceModel) stepLine(s race.Step, labelColor color.Color) string {
	g, gc := m.glyph(s)
	out := m.style(gc, true).Render(g) + " "
	if s.Kind == race.KindTool {
		out += m.style(gc, true).Render(s.Tool) + " "
	}
	return out + m.style(labelColor, false).Render(cleanLine(s.Label))
}

func (m RaceModel) agentLane(n raceNow, iw, ih int) []string {
	if ih <= 0 {
		return nil
	}
	steps := m.r.Steps
	cur := n.agent.Index
	var rev []string // newest first
	if n.agent.Done {
		rev = append(rev, m.style(m.st.agent, true).Render("✓ done in "+race.Friendly(m.r.AgentTotal)))
	}
	for i := min(cur, len(steps)-1); i >= 0 && len(rev) < ih; i-- {
		s := steps[i]
		if i == cur && !n.agent.Done {
			rev = append(rev, m.style(m.st.accent, true).Render("▸ ")+m.stepLine(s, m.st.bright))
			continue
		}
		if s.Kind == race.KindTool && s.ResultTail != "" {
			c := m.st.muted
			if s.IsError {
				c = m.st.red
			}
			rev = append(rev, m.style(c, false).Render("    ⎿ "+cleanLine(s.ResultTail)))
		}
		rev = append(rev, "  "+m.stepLine(s, m.st.text))
	}
	if len(rev) > ih {
		rev = rev[:ih]
	}
	out := make([]string, len(rev))
	for i, l := range rev {
		out[len(rev)-1-i] = l
	}
	return out
}

func (m RaceModel) humanLane(n raceNow, iw, ih int) []string {
	if ih <= 0 {
		return nil
	}
	steps := m.r.Steps
	hp := n.human
	muted := m.style(m.st.muted, false)
	if hp.Done {
		return []string{m.style(m.st.human, true).Render("✓ done in " + race.Friendly(m.r.HumanTotal))}
	}

	var top []string
	for i := max(0, hp.Index-2); i < hp.Index; i++ {
		top = append(top, muted.Render("✓ ")+m.stepLine(steps[i], m.st.muted))
	}
	s := steps[hp.Index]
	top = append(top, m.style(m.st.human, true).Render("▸ ")+m.stepLine(s, m.st.bright))

	queued := len(steps) - hp.Index - 1
	var bottom []string
	if queued > 0 {
		bottom = append(bottom, muted.Render(fmt.Sprintf("⋯ %d more steps waiting · the agent did all of them already", queued)))
		if n.agent.Index < len(steps) && !n.agent.Done {
			bottom[0] = muted.Render(fmt.Sprintf("⋯ %d more steps waiting · agent is on step %d", queued, n.agent.Index+1))
		}
	}

	room := ih - len(top) - len(bottom)
	var body []string
	switch hp.Phase {
	case race.PhaseTyping:
		body = append(body, m.style(m.st.human, false).Render(fmt.Sprintf("⌨ typing %s / %s chars", commas(hp.Typed), commas(s.TypedRunes))))
		body = append(body, m.typedTail(hp.Index, hp.Typed, iw-2, room-1)...)
	case race.PhaseReading:
		bw := max(8, min(30, iw-36))
		body = append(body, m.style(m.st.human, false).Render("◔ reading ")+m.bar(hp.ReadFrac, bw, m.st.human)+
			m.style(m.st.subtle, false).Render(fmt.Sprintf(" %s of %s chars", pct(hp.ReadFrac), commas(s.ReadRunes))))
		body = append(body, m.readingLines(s, hp.ReadFrac, iw-2, room-1)...)
	case race.PhaseThinking:
		body = append(body, m.style(m.st.human, false).Render("… thinking"))
	}
	if len(body) > room {
		body = body[:max(0, room)]
	}
	lines := append(top, body...)
	for len(lines) < ih-len(bottom) {
		lines = append(lines, "")
	}
	lines = append(lines, bottom...)
	if len(lines) > ih {
		// Keep the current step and its progress; drop finished ones first.
		drop := len(lines) - ih
		lines = lines[min(drop, len(top)-1):]
		if len(lines) > ih {
			lines = lines[:ih]
		}
	}
	return lines
}

// typedTail returns the last rows of what the human has typed so far,
// wrapped to w, with a cursor.
func (m RaceModel) typedTail(idx, typed, w, rows int) []string {
	if rows <= 0 || w <= 0 {
		return nil
	}
	rs, ok := m.runes[idx]
	if !ok {
		rs = []rune(m.r.Steps[idx].Typed)
		m.runes[idx] = rs
	}
	typed = min(typed, len(rs))
	start := max(0, typed-rows*w*2)
	text := cleanBlock(string(rs[start:typed]))
	wrapped := wrapBlock(text, w)
	if len(wrapped) > rows {
		wrapped = wrapped[len(wrapped)-rows:]
	}
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	cursor := m.style(m.st.human, true).Render("▌")
	out := make([]string, len(wrapped))
	for i, l := range wrapped {
		out[i] = "  " + m.style(m.st.text, false).Render(l)
	}
	last := len(out) - 1
	if ansi.StringWidth(wrapped[last]) >= w {
		out = append(out, "  "+cursor)
		if len(out) > rows {
			out = out[1:]
		}
	} else {
		out[last] += cursor
	}
	return out
}

// readingLines shows the start of the result, the read share lit.
func (m RaceModel) readingLines(s race.Step, f float64, w, rows int) []string {
	if rows <= 0 || w <= 0 {
		return nil
	}
	wrapped := wrapBlock(cleanBlock(s.ResultHead), w)
	if len(wrapped) == 0 {
		return nil
	}
	lit := int(math.Ceil(f * float64(len(wrapped))))
	// Scroll so the reading position stays in view.
	start := 0
	if lit > rows {
		start = lit - rows
	}
	var out []string
	for i := start; i < len(wrapped) && len(out) < rows; i++ {
		c := m.st.muted
		if i < lit {
			c = m.st.text
		}
		out = append(out, "  "+m.style(c, false).Render(wrapped[i]))
	}
	return out
}

// ── text helpers ──────────────────────────────────────────────────────────────

// cleanBlock makes transcript text safe to draw: no escape sequences, no
// control characters but newlines, tabs as spaces.
func cleanBlock(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\t", "    ")
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

func cleanLine(s string) string {
	return strings.ReplaceAll(cleanBlock(s), "\n", " ")
}

func wrapBlock(s string, w int) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(ansi.Wrap(l, w, ""), "\n")...)
	}
	return out
}

func wrapPlain(s string, w int) []string {
	return strings.Split(ansi.Wordwrap(s, w, " ·,"), "\n")
}

// fit truncates or pads a styled line to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func centerIn(s string, w int) string {
	sw := ansi.StringWidth(s)
	if sw >= w {
		return ansi.Truncate(s, w, "…")
	}
	return strings.Repeat(" ", (w-sw)/2) + s
}

func fitBlock(lines []string, w, h int) string {
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	return strings.Join(lines, "\n")
}

func commas(n int) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// ── big digits ────────────────────────────────────────────────────────────────

// bigGlyphs is a three-row block font for clocks and multipliers.
var bigGlyphs = map[rune][3]string{
	'0': {"█▀█", "█ █", "▀▀▀"},
	'1': {"▀█ ", " █ ", "▀▀▀"},
	'2': {"▀▀█", "█▀▀", "▀▀▀"},
	'3': {"▀▀█", " ▀█", "▀▀▀"},
	'4': {"█ █", "▀▀█", "  ▀"},
	'5': {"█▀▀", "▀▀█", "▀▀▀"},
	'6': {"█▀▀", "█▀█", "▀▀▀"},
	'7': {"▀▀█", "  █", "  ▀"},
	'8': {"█▀█", "█▀█", "▀▀▀"},
	'9': {"█▀█", "▀▀█", "▀▀▀"},
	':': {"▄", "▄", " "},
	'.': {" ", " ", "▀"},
	'x': {"   ", "▀▄▀", "▀ ▀"},
}

// bigText renders s in the block font; runes without a glyph are skipped.
func bigText(s string) [3]string {
	var rows [3]strings.Builder
	first := true
	for _, r := range s {
		g, ok := bigGlyphs[r]
		if !ok {
			continue
		}
		for i := range rows {
			if !first {
				rows[i].WriteByte(' ')
			}
			rows[i].WriteString(g[i])
		}
		first = false
	}
	return [3]string{rows[0].String(), rows[1].String(), rows[2].String()}
}
