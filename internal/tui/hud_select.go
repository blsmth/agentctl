package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/logging"
)

// In-app text selection for the HUD body.
//
// The HUD runs with mouse reporting on, so a plain drag reaches the HUD, not
// tmux, and the terminal's bypass modifier selects across every pane of the
// window. The HUD therefore selects itself: a left press then drag in the
// body selects a linear range (like a terminal), drawn reversed, and the
// release copies it. Points are kept in buffer coordinates (record or entry
// index, line within it, cell column), so new lines arriving, ring-buffer
// trims and scrolling never move the selected text.

// hudLineJoin says how a rendered line continues the line above it.
type hudLineJoin uint8

const (
	joinNone  hudLineJoin = iota // starts a logical line
	joinHard                     // a hard wrap: concatenate
	joinSpace                    // a word wrap: rejoin with one space
)

// hudLineMeta is one rendered line's wrap metadata. indent is how many
// leading cells are layout (margin plus hanging indent), dropped when the
// line is joined back onto the previous one.
type hudLineMeta struct {
	join   hudLineJoin
	indent int
}

// hudSelState is the drag state machine.
type hudSelState uint8

const (
	selNone     hudSelState = iota
	selPending              // left press in the body, no motion yet
	selDragging             // the pointer moved: a selection is being drawn
	selShown                // released: the selection stays drawn until cleared
)

// hudSelPoint is a cell in buffer coordinates: unit is the stream entry or
// log record index, intra the line inside it, col the cell column.
type hudSelPoint struct {
	unit, intra, col int
}

// hudSelection is the HUD's text selection.
type hudSelection struct {
	state        hudSelState
	tab          int
	anchor, head hudSelPoint
	// lastX is the pointer column of the last motion, used by auto-scroll.
	lastX int
	// edge is -1 while the pointer is above the body during a drag, +1
	// below it, 0 inside: auto-scroll moves that way on every tick.
	edge int
}

// visible reports whether there is a selection to draw.
func (s hudSelection) visible() bool { return s.state == selDragging || s.state == selShown }

// hudSelScrollInterval is the auto-scroll rate while dragging past an edge.
const hudSelScrollInterval = 60 * time.Millisecond

// hudSelScrollMsg is an auto-scroll tick.
type hudSelScrollMsg struct{}

// hudFlashTicks is how many poll ticks (500ms each) the footer flash shows.
const hudFlashTicks = 4

// hudClipboard copies text to the clipboard. A seam: tests replace it so
// nothing ever spawns tmux or writes OSC 52.
var hudClipboard = copyToClipboard

// copyToClipboard returns a command that copies text: through tmux when the
// HUD runs inside it (load-buffer -w also sets the system clipboard through
// set-clipboard / OSC 52, and tmux knows which client to send it to), and by
// emitting OSC 52 through Bubble Tea otherwise or when tmux fails.
func copyToClipboard(text string) tea.Cmd {
	if os.Getenv("TMUX") == "" {
		return tea.SetClipboard(text)
	}
	return func() tea.Msg {
		if err := tmuxLoadBuffer(text); err != nil {
			logging.Log("hud: tmux load-buffer: %v", err)
			return tea.SetClipboard(text)()
		}
		return nil
	}
}

// tmuxLoadBuffer pipes text into `tmux load-buffer -w -`.
func tmuxLoadBuffer(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", "load-buffer", "-w", "-")
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// hudSelBuffer is one tab's rendered lines as the selection sees them.
type hudSelBuffer struct {
	lines  []string
	starts []int // first line of each unit (entry or record)
	meta   []hudLineMeta
	scroll int
}

// selBuffer returns the given tab's buffer.
func (m *HudModel) selBuffer(tab int) hudSelBuffer {
	switch tab {
	case tabAgentLogs:
		return hudSelBuffer{m.agentLogLines, m.agentStarts, m.agentLineMeta, m.agentLogScroll}
	case tabSysLogs:
		return hudSelBuffer{m.sysLogLines, m.sysStarts, m.sysLineMeta, m.sysLogScroll}
	}
	if len(m.logEntries) == 0 {
		return hudSelBuffer{}
	}
	return hudSelBuffer{m.streamLineBuffer, m.streamEntryStart, m.streamLineMeta, m.scrollTop}
}

// line converts a point to an absolute line index, clamped to its unit.
func (b hudSelBuffer) line(p hudSelPoint) int {
	if len(b.starts) == 0 || len(b.lines) == 0 {
		return 0
	}
	u := min(max(p.unit, 0), len(b.starts)-1)
	end := len(b.lines)
	if u+1 < len(b.starts) {
		end = b.starts[u+1]
	}
	l := b.starts[u] + max(p.intra, 0)
	return min(max(l, b.starts[u]), max(end-1, b.starts[u]), len(b.lines)-1)
}

// point converts an absolute line index and column to a point.
func (b hudSelBuffer) point(line, col int) hudSelPoint {
	u := sort.Search(len(b.starts), func(i int) bool { return b.starts[i] > line }) - 1
	u = max(u, 0)
	intra := 0
	if u < len(b.starts) {
		intra = line - b.starts[u]
	}
	return hudSelPoint{unit: u, intra: intra, col: max(col, 0)}
}

// ordered returns the selection's start and end as (line, col) pairs, start
// first; endCol is exclusive.
func (m *HudModel) selRange() (l0, c0, l1, c1 int) {
	b := m.selBuffer(m.sel.tab)
	la, ca := b.line(m.sel.anchor), m.sel.anchor.col
	lh, ch := b.line(m.sel.head), m.sel.head.col
	if lh < la || (lh == la && ch < ca) {
		la, ca, lh, ch = lh, ch, la, ca
	}
	return la, ca, lh, ch + 1
}

// bodyTop is the screen row of the first body line: below the header and
// the tab bar.
func (m *HudModel) bodyTop() int { return hudTabBarRow + 1 }

// inBody reports whether screen row y is a body row.
func (m *HudModel) inBody(y int) bool {
	return m.height > 5 && y >= m.bodyTop() && y < m.bodyTop()+m.visibleLines()
}

// screenPoint maps a screen cell to a point in the active tab's buffer,
// clamping rows outside the body (or past the last line) to the nearest
// body line. ok is false when the tab has nothing to select.
func (m *HudModel) screenPoint(x, y int) (hudSelPoint, bool) {
	b := m.selBuffer(m.activeTab)
	if len(b.lines) == 0 || len(b.starts) == 0 {
		return hudSelPoint{}, false
	}
	row := min(max(y-m.bodyTop(), 0), m.visibleLines()-1)
	line := min(b.scroll+row, len(b.lines)-1)
	return b.point(line, x), true
}

// clearSelection drops the selection and any auto-scroll in progress.
func (m *HudModel) clearSelection() {
	m.sel = hudSelection{}
}

// selPress handles a left press: it clears any selection and, in the body,
// arms a possible drag. The press keeps its normal click meaning.
func (m *HudModel) selPress(msg tea.MouseClickMsg) {
	m.clearSelection()
	if msg.Button != tea.MouseLeft || !m.inBody(msg.Y) || m.followIdle() {
		return
	}
	p, ok := m.screenPoint(msg.X, msg.Y)
	if !ok {
		return
	}
	m.sel = hudSelection{state: selPending, tab: m.activeTab, anchor: p, head: p, lastX: msg.X}
}

// selMotion handles pointer motion while a press is pending or a drag is in
// progress. handled is false when there is nothing to do.
func (m *HudModel) selMotion(msg tea.MouseMotionMsg) (cmd tea.Cmd, handled bool) {
	if m.sel.state != selPending && m.sel.state != selDragging {
		return nil, false
	}
	if msg.Button != tea.MouseLeft || m.sel.tab != m.activeTab {
		m.clearSelection() // the release was missed
		return nil, true
	}
	m.sel.lastX = msg.X
	switch {
	case msg.Y < m.bodyTop():
		m.sel.edge = -1
	case msg.Y >= m.bodyTop()+m.visibleLines():
		m.sel.edge = 1
	default:
		m.sel.edge = 0
	}
	p, ok := m.screenPoint(msg.X, msg.Y)
	if !ok {
		return nil, true
	}
	if m.sel.state == selPending {
		if p == m.sel.anchor && m.sel.edge == 0 {
			return nil, true // same cell: still a click
		}
		m.sel.state = selDragging
		// Stop following new output while selecting, so the viewport (and
		// the text under the pointer) holds still.
		m.setAtBottom(m.activeTab, false)
	}
	m.sel.head = p
	if m.sel.edge != 0 {
		m.selAutoScroll()
		if !m.selTicking {
			m.selTicking = true
			cmd = selScrollTick()
		}
	}
	return cmd, true
}

func selScrollTick() tea.Cmd {
	return tea.Tick(hudSelScrollInterval, func(time.Time) tea.Msg { return hudSelScrollMsg{} })
}

// selScrollStep handles an auto-scroll tick: scroll one line toward the edge
// the pointer is past and extend the selection to it.
func (m *HudModel) selScrollStep() tea.Cmd {
	if m.sel.state != selDragging || m.sel.edge == 0 {
		m.selTicking = false
		return nil
	}
	m.selAutoScroll()
	return selScrollTick()
}

// selAutoScroll scrolls the active tab one line toward the edge and moves
// the selection head to the body's edge row.
func (m *HudModel) selAutoScroll() {
	m.scrollBy(m.sel.edge)
	m.setAtBottom(m.activeTab, false)
	y := m.bodyTop()
	if m.sel.edge > 0 {
		y += m.visibleLines() - 1
	}
	if p, ok := m.screenPoint(m.sel.lastX, y); ok {
		m.sel.head = p
	}
}

// selRelease handles the button release: a drag copies its text and stays
// drawn; a press without motion clears (it was a plain click).
func (m *HudModel) selRelease(msg tea.MouseReleaseMsg) tea.Cmd {
	switch m.sel.state {
	case selPending:
		m.clearSelection()
		return nil
	case selDragging:
	default:
		return nil
	}
	if m.inBody(msg.Y) {
		if p, ok := m.screenPoint(msg.X, msg.Y); ok {
			m.sel.head = p
		}
	}
	m.sel.state = selShown
	m.sel.edge = 0
	text, n := m.selectionText()
	return m.copyCmd(text, n)
}

// selectionText is the selected text as plain text.
func (m *HudModel) selectionText() (string, int) {
	b := m.selBuffer(m.sel.tab)
	if len(b.lines) == 0 {
		return "", 0
	}
	l0, c0, l1, c1 := m.selRange()
	return extractSelection(b.lines, b.meta, l0, c0, l1, c1)
}

// copyCmd copies text through the clipboard seam and flashes the result.
func (m *HudModel) copyCmd(text string, n int) tea.Cmd {
	if text == "" {
		m.setFlash("nothing to copy")
		return nil
	}
	noun := "lines"
	if n == 1 {
		noun = "line"
	}
	m.setFlash(fmt.Sprintf("copied %d %s", n, noun))
	return hudClipboard(text)
}

// setFlash shows msg in the footer for hudFlashTicks poll ticks.
func (m *HudModel) setFlash(msg string) {
	m.flash, m.flashTicks = msg, hudFlashTicks
}

// extractSelection returns the plain text between (l0, c0) and (l1, c1) of
// lines (c1 exclusive; lines in between are taken whole) and how many
// logical lines it has. ANSI is stripped and trailing spaces trimmed. A line
// whose metadata marks it as a wrap continuation is joined back onto the one
// above it, without its indent.
func extractSelection(lines []string, meta []hudLineMeta, l0, c0, l1, c1 int) (string, int) {
	if len(lines) == 0 {
		return "", 0
	}
	l0 = min(max(l0, 0), len(lines)-1)
	l1 = min(max(l1, 0), len(lines)-1)
	var out []string
	var cur string
	for l := l0; l <= l1; l++ {
		plain := ansi.Strip(lines[l])
		lm := hudLineMeta{}
		if l < len(meta) {
			lm = meta[l]
		}
		from, to := 0, ansi.StringWidth(plain)
		if l == l0 {
			from = c0
			if lm.join != joinNone {
				from = max(from, lm.indent) // never copy the hanging indent
			}
		}
		if l == l1 {
			to = min(to, c1)
		}
		seg := ""
		if from < to {
			seg = ansi.Cut(plain, from, to)
		}
		if l > l0 && lm.join != joinNone {
			seg = ansi.TruncateLeft(seg, lm.indent, "")
			if lm.join == joinSpace {
				if c := strings.TrimRight(cur, " "); c != "" {
					cur = c + " " + strings.TrimLeft(seg, " ")
				} else {
					cur = strings.TrimLeft(seg, " ")
				}
			} else {
				cur += seg
			}
			continue
		}
		if l > l0 {
			out = append(out, cur)
		}
		cur = seg
	}
	out = append(out, cur)
	for i, s := range out {
		out[i] = strings.TrimRight(s, " ")
	}
	return strings.Join(out, "\n"), len(out)
}

// highlightCells redraws cells [from, to) of a rendered line in style st
// (to past the line's end stops at its end). The rest keeps its styling.
func highlightCells(line string, from, to int, st lipgloss.Style) string {
	w := ansi.StringWidth(line)
	to = min(to, w)
	from = max(from, 0)
	if from >= to {
		return line
	}
	sel := ansi.Strip(ansi.Cut(line, from, to))
	return ansi.Cut(line, 0, from) + "\x1b[m" + st.Render(sel) + ansi.TruncateLeft(line, to, "")
}

// renderSelected applies the selection highlight to visible body line
// number i (an absolute buffer line of the active tab).
func (m *HudModel) renderSelected(i int, line string) string {
	if !m.sel.visible() || m.sel.tab != m.activeTab {
		return line
	}
	l0, c0, l1, c1 := m.selRange()
	if i < l0 || i > l1 {
		return line
	}
	from, to := 0, ansi.StringWidth(line)
	if i == l0 {
		from = c0
	}
	if i == l1 {
		to = min(to, c1)
	}
	return highlightCells(line, from, to, hudSelectionStyle)
}

// shiftSelection moves selection points after trim units were dropped from
// the front of tab's buffer. A selection wholly inside the dropped part is
// cleared; one partly inside starts at the new first unit.
func (m *HudModel) shiftSelection(tab, trim int) {
	if trim <= 0 || m.sel.state == selNone || m.sel.tab != tab {
		return
	}
	shift := func(p hudSelPoint) (hudSelPoint, bool) {
		p.unit -= trim
		if p.unit < 0 {
			return hudSelPoint{}, false
		}
		return p, true
	}
	a, aok := shift(m.sel.anchor)
	h, hok := shift(m.sel.head)
	if !aok && !hok {
		m.clearSelection()
		return
	}
	m.sel.anchor, m.sel.head = a, h
}

// copyKey is `y`: copy the selection when there is one, else the selected
// Stream entry in full, else the visible content of the active tab.
func (m *HudModel) copyKey() tea.Cmd {
	if m.sel.visible() && m.sel.tab == m.activeTab {
		text, n := m.selectionText()
		return m.copyCmd(text, n)
	}
	if m.activeTab == tabStream && m.streamCursor >= 0 && m.streamCursor < len(m.logEntries) {
		text := entryCopyText(m.logEntries[m.streamCursor])
		return m.copyCmd(text, strings.Count(text, "\n")+1)
	}
	b := m.selBuffer(m.activeTab)
	if len(b.lines) == 0 {
		return m.copyCmd("", 0)
	}
	end := min(b.scroll+m.visibleLines(), len(b.lines)) - 1
	text, n := extractSelection(b.lines, b.meta, b.scroll, 0, end, 1<<30)
	return m.copyCmd(strings.Trim(text, "\n"), n)
}

// entryCopyText is a Stream entry in full: the command and its full output
// for a Bash call, the input and output for other tools, the full text
// otherwise.
func entryCopyText(e LogEntry) string {
	var sb strings.Builder
	switch {
	case e.Kind == "tool_call" && e.ToolName == "Bash":
		cmd, _ := e.FullInput["command"].(string)
		if cmd == "" {
			cmd, _ = e.FullInput["description"].(string)
		}
		sb.WriteString("$ " + strings.TrimRight(cmd, "\n "))
		if out := strings.TrimRight(e.Output, "\n"); e.HasResult && out != "" {
			sb.WriteString("\n" + out)
		}
	case e.Kind == "tool_call":
		sb.WriteString(e.ToolName)
		keys := toolKeyPriority(e.ToolName)
		seen := map[string]bool{}
		for _, k := range keys {
			seen[k] = true
		}
		var rest []string
		for k := range e.FullInput {
			if !seen[k] {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		for _, k := range append(keys, rest...) {
			if v, ok := e.FullInput[k]; ok {
				fmt.Fprintf(&sb, "\n%s: %v", k, v)
			}
		}
		if out := strings.TrimRight(e.Output, "\n"); e.HasResult && out != "" {
			sb.WriteString("\n" + out)
		}
	default:
		text := e.FullText
		if text == "" {
			text = e.Summary
		}
		sb.WriteString(text)
	}
	lines := strings.Split(sb.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(ansi.Strip(l), " \t\r")
	}
	return strings.Join(lines, "\n")
}
