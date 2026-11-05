package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/theme"
)

// SplitOrientation selects how SplitPane arranges its list and detail
// panes. The zero value (OrientVertical) is the original list-on-top,
// detail-on-bottom layout used by the Memory, Tickets, Sessions, and Agents
// browsers; OrientHorizontal places them side by side (list = a narrow
// sidebar on the left, detail = the main panel on the right), used by the
// herd hub's sidebar + detail layout.
type SplitOrientation int

const (
	OrientVertical SplitOrientation = iota
	OrientHorizontal
)

// SplitPane is a reusable Bubble Tea helper for either the list-on-top +
// detail-on-bottom layout used by the table-based viewers (OrientVertical,
// the default), or a list-sidebar + detail-main-panel layout (OrientHorizontal).
//
// It is NOT a standalone tea.Model. The parent model owns a SplitPane
// value, calls Update and View, and delegates relevant messages to it.
// Content is supplied by the parent via SetListContent / SetDetailContent
// before each View call.
type SplitPane struct {
	// Configuration — set once before use.

	// Orientation selects the layout. Zero value is OrientVertical, so
	// existing callers are unaffected.
	Orientation SplitOrientation

	// ListRatio is the fraction of the terminal allocated to the list pane:
	// of height in OrientVertical (e.g. 0.4 for 40%), of width in
	// OrientHorizontal (e.g. 0.32 for a narrow sidebar).
	ListRatio float64

	// MinListRows is the minimum size of the list pane: rows in
	// OrientVertical, columns in OrientHorizontal.
	MinListRows int

	// MinDetailRows is the minimum size of the detail pane: rows in
	// OrientVertical, columns in OrientHorizontal.
	MinDetailRows int

	// State — managed internally.

	termWidth  int
	termHeight int

	listScroll   int // scroll offset within the list pane
	detailScroll int // scroll offset within the detail pane
	cursor       int // selected row index (0-based)
	totalRows    int // total rows in the list data set

	focused int // 0 = list pane, 1 = detail pane

	// Content — set by the parent before View is called.
	listContent   string // pre-rendered list rows (may be empty string)
	detailContent string // pre-rendered detail body (may be empty string)
}

const (
	focusList   = 0
	focusDetail = 1
)

// SetSize updates the terminal dimensions.
func (p *SplitPane) SetSize(w, h int) {
	p.termWidth = w
	p.termHeight = h
}

// SetListContent updates the pre-rendered list content and the total number
// of rows in the data set (used for scroll-clamping).
func (p *SplitPane) SetListContent(content string, totalRows int) {
	p.listContent = content
	p.totalRows = totalRows
	p.clampCursor()
	p.clampListScroll()
}

// SetDetailContent updates the pre-rendered detail pane content.
func (p *SplitPane) SetDetailContent(content string) {
	p.detailContent = content
	p.clampDetailScroll()
}

// ListHeight returns the computed number of visible rows for the list pane.
// In OrientHorizontal mode the list (sidebar) and detail (main) panes sit
// side by side rather than stacked, so both simply get the full terminal
// height (see ListWidth for the sidebar's horizontal sizing).
func (p *SplitPane) ListHeight() int {
	if p.Orientation == OrientHorizontal {
		return p.paneHeight()
	}
	ratio := p.ListRatio
	if ratio <= 0 {
		ratio = 0.4
	}
	h := int(float64(p.termHeight) * ratio)
	min := p.MinListRows
	if min <= 0 {
		min = 4
	}
	if h < min {
		h = min
	}
	return h
}

// DetailHeight returns the computed number of visible rows for the detail
// pane. In OrientHorizontal mode this is the detail box's interior row
// count (see detailContentHeight), not the full pane height — the box's
// border and padding both consume rows of the shared pane height.
func (p *SplitPane) DetailHeight() int {
	if p.Orientation == OrientHorizontal {
		return p.detailContentHeight()
	}
	// Total height minus list pane minus separator line (1).
	h := p.termHeight - p.ListHeight() - 1
	min := p.MinDetailRows
	if min <= 0 {
		min = 4
	}
	if h < min {
		h = min
	}
	return h
}

// paneHeight is the shared full-height viewport used by both panes in
// OrientHorizontal mode.
func (p *SplitPane) paneHeight() int {
	if p.termHeight < 1 {
		return 1
	}
	return p.termHeight
}

// ListWidth returns the computed sidebar width in columns (OrientHorizontal
// only). Ratio and minimum come from ListRatio / MinListRows, reused here as
// a column count rather than a row count.
func (p *SplitPane) ListWidth() int {
	ratio := p.ListRatio
	if ratio <= 0 {
		ratio = 0.3
	}
	w := int(float64(p.termWidth) * ratio)
	min := p.MinListRows
	if min <= 0 {
		min = 20
	}
	if w < min {
		w = min
	}
	// Leave room for the detail pane.
	maxW := p.termWidth - 10
	if w > maxW {
		w = maxW
	}
	if w < 1 {
		w = 1
	}
	return w
}

// DetailWidth returns the main panel's width in columns (OrientHorizontal
// only): total width minus the sidebar. There is no separate separator
// column to account for — the detail box's own left border (see
// viewHorizontal) is the sole divider between the two panes.
func (p *SplitPane) DetailWidth() int {
	w := p.termWidth - p.ListWidth()
	if w < 1 {
		w = 1
	}
	return w
}

// detailBoxVerticalOverhead / detailBoxHorizontalOverhead are the row/column
// cost added by wrapping detail content in tableStyles.DetailBox: a rounded
// border (1 row/column on each side) plus Padding(1, 2) (1 row top/bottom, 2
// columns left/right). Vertical: 2 (border) + 2 (padding) = 4. Horizontal: 2
// (border) + 4 (padding) = 6.
const (
	detailBoxVerticalOverhead   = 4
	detailBoxHorizontalOverhead = 6
)

// detailContentHeight returns the number of interior rows available inside
// the detail box in OrientHorizontal mode: the shared pane height minus the
// box's fixed border+padding overhead. viewHorizontal uses this to decide
// how many lines of detail content to window into view; the box's own
// Style.Height is pinned separately to the full pane height (see
// viewHorizontal) so its rendered footprint — and therefore its left
// border, the sole divider between the sidebar and the main panel — always
// spans that full height, regardless of how much sidebar or detail content
// exists.
func (p *SplitPane) detailContentHeight() int {
	h := p.paneHeight() - detailBoxVerticalOverhead
	if h < 1 {
		h = 1
	}
	return h
}

// detailContentWidth returns the number of interior columns available
// inside the detail box in OrientHorizontal mode: DetailWidth minus the
// box's fixed border+padding overhead.
func (p *SplitPane) detailContentWidth() int {
	w := p.DetailWidth() - detailBoxHorizontalOverhead
	if w < 1 {
		w = 1
	}
	return w
}

// Cursor returns the current cursor (selected row) index.
func (p *SplitPane) Cursor() int { return p.cursor }

// SetCursor sets the cursor to n, clamped to the valid range.
func (p *SplitPane) SetCursor(n int) {
	p.cursor = n
	p.clampCursor()
	p.clampListScroll()
}

// Focused returns 0 if the list pane has focus, 1 if the detail pane does.
func (p *SplitPane) Focused() int { return p.focused }

// Update handles keyboard navigation for the SplitPane. It returns true when
// the message was consumed so the caller can skip further processing.
//
// Key bindings:
//   - List focus:  j/down = cursor down, k/up = cursor up
//     J/K = detail scroll, tab = switch to detail
//   - Detail focus: j/down/J/pgdn = scroll down, k/up/K/pgup = scroll up
//     tab = switch back to list
func (p *SplitPane) Update(msg tea.KeyPressMsg) bool {
	switch p.focused {
	case focusList:
		switch msg.String() {
		case "j", "down":
			if p.cursor < p.totalRows-1 {
				p.cursor++
				p.clampListScroll()
			}
			return true
		case "k", "up":
			if p.cursor > 0 {
				p.cursor--
				p.clampListScroll()
			}
			return true
		case "J":
			// Scroll detail pane down while in list focus.
			p.detailScroll++
			p.clampDetailScroll()
			return true
		case "K":
			// Scroll detail pane up while in list focus.
			if p.detailScroll > 0 {
				p.detailScroll--
			}
			return true
		case "tab":
			p.focused = focusDetail
			return true
		}

	case focusDetail:
		switch msg.String() {
		case "j", "down", "J", "pgdown":
			p.detailScroll++
			p.clampDetailScroll()
			return true
		case "k", "up", "K", "pgup":
			if p.detailScroll > 0 {
				p.detailScroll--
			}
			return true
		case "tab":
			p.focused = focusList
			return true
		}
	}

	return false
}

// View renders the split pane in whichever orientation is configured. The
// detail content is wrapped in tableStyles.DetailBox in both orientations.
func (p *SplitPane) View() string {
	if p.Orientation == OrientHorizontal {
		return p.viewHorizontal()
	}
	return p.viewVertical()
}

// viewHorizontal renders the sidebar (list) and main panel (detail) side by
// side. There is a single divider between them: the detail box's own left
// border (added by tableStyles.DetailBox). No separate separator column is
// drawn — that would produce the doubled "│╭" artifact this design avoids.
// Both panes share the full terminal height (see ListHeight/DetailHeight).
//
// lipgloss.Style's Width/Height set a box's *outer* dimensions — border and
// padding included, not just the interior content area — so the box style
// is pinned directly to (detailW, h), the exact footprint JoinHorizontal
// allocates it. That guarantees the box's rendered size, and therefore its
// left border (the divider), always matches that footprint exactly,
// regardless of how much sidebar or detail content exists.
//
// Style.Height only pads *short* content up to the target — it does not
// truncate content that's already taller, and a single source line wider
// than the interior would otherwise get wrapped into extra lines, inflating
// the box past h. So every line is explicitly truncated to the interior
// width (never left for lipgloss to reflow) before joining, and windowed to
// at most the interior row count — keeping the box's total rendered height
// exactly h in every case.
func (p *SplitPane) viewHorizontal() string {
	h := p.paneHeight()
	listW := p.ListWidth()
	detailW := p.DetailWidth()

	listBlock := windowLines(p.listContent, p.listScroll, h, listW)

	innerW := p.detailContentWidth()
	innerH := p.detailContentHeight()

	detailLines := strings.Split(p.detailContent, "\n")
	dStart := p.detailScroll
	if dStart >= len(detailLines) {
		dStart = 0
	}
	dEnd := dStart + innerH
	if dEnd > len(detailLines) {
		dEnd = len(detailLines)
	}
	dVisible := make([]string, 0, dEnd-dStart)
	for _, l := range detailLines[dStart:dEnd] {
		dVisible = append(dVisible, truncateToWidth(l, innerW))
	}
	detailRendered := strings.Join(dVisible, "\n")

	boxStyle := tableStyles.DetailBox.Width(detailW).Height(h)
	if p.focused == focusDetail {
		t := theme.Current()
		boxStyle = boxStyle.BorderForeground(lipgloss.Color(t.Accent))
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, listBlock, boxStyle.Render(detailRendered))
}

// windowLines slices content to the visible [scroll, scroll+height) window,
// pads it to height with blank lines, and clamps every line to width via
// lipgloss (never len, to stay ANSI- and wide-rune-safe).
func windowLines(content string, scroll, height, width int) string {
	lines := strings.Split(content, "\n")
	start := scroll
	if start >= len(lines) {
		start = 0
	}
	end := start + height
	if end > len(lines) {
		end = len(lines)
	}
	visible := make([]string, 0, height)
	visible = append(visible, lines[start:end]...)
	for len(visible) < height {
		visible = append(visible, "")
	}
	style := lipgloss.NewStyle().Width(width).MaxWidth(width)
	for i, l := range visible {
		visible[i] = style.Render(l)
	}
	return strings.Join(visible, "\n")
}

// viewVertical renders the list pane on top, a horizontal separator, and the
// detail pane below. The detail content is wrapped in tableStyles.DetailBox.
func (p *SplitPane) viewVertical() string {
	var b strings.Builder

	// ── List pane ──────────────────────────────────────────────────────────
	listH := p.ListHeight()
	listLines := strings.Split(p.listContent, "\n")

	start := p.listScroll
	if start >= len(listLines) {
		start = 0
	}
	end := start + listH
	if end > len(listLines) {
		end = len(listLines)
	}
	visible := listLines[start:end]
	// Pad to listH so the separator is always at a consistent position.
	for len(visible) < listH {
		visible = append(visible, "")
	}
	b.WriteString(strings.Join(visible, "\n"))
	b.WriteString("\n")

	// ── Separator ──────────────────────────────────────────────────────────
	sepWidth := p.termWidth
	if sepWidth <= 0 {
		sepWidth = 80
	}
	sep := tableStyles.Rule.Render(strings.Repeat("─", sepWidth))
	b.WriteString(sep)
	b.WriteString("\n")

	// ── Detail pane ────────────────────────────────────────────────────────
	detailH := p.DetailHeight()
	detailLines := strings.Split(p.detailContent, "\n")

	dStart := p.detailScroll
	if dStart >= len(detailLines) {
		dStart = 0
	}
	dEnd := dStart + detailH
	if dEnd > len(detailLines) {
		dEnd = len(detailLines)
	}
	dVisible := detailLines[dStart:dEnd]
	for len(dVisible) < detailH {
		dVisible = append(dVisible, "")
	}

	detailRendered := strings.Join(dVisible, "\n")
	// Trim trailing newline — DetailBox owns vertical spacing.
	detailRendered = strings.TrimRight(detailRendered, "\n")

	// Highlight detail box border when detail pane has focus.
	boxStyle := tableStyles.DetailBox
	if p.focused == focusDetail {
		t := theme.Current()
		boxStyle = boxStyle.BorderForeground(lipgloss.Color(t.Accent))
	}

	b.WriteString(boxStyle.Render(detailRendered))

	return b.String()
}

// ── internal helpers ──────────────────────────────────────────────────────────

// clampCursor ensures cursor is within [0, totalRows-1].
func (p *SplitPane) clampCursor() {
	if p.totalRows <= 0 {
		p.cursor = 0
		return
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= p.totalRows {
		p.cursor = p.totalRows - 1
	}
}

// clampListScroll adjusts listScroll so the cursor is always visible.
func (p *SplitPane) clampListScroll() {
	h := p.ListHeight()
	if h <= 0 {
		h = 1
	}
	// Scroll down: cursor moved below the visible window.
	if p.cursor >= p.listScroll+h {
		p.listScroll = p.cursor - h + 1
	}
	// Scroll up: cursor moved above the visible window.
	if p.cursor < p.listScroll {
		p.listScroll = p.cursor
	}
	if p.listScroll < 0 {
		p.listScroll = 0
	}
}

// clampDetailScroll ensures detailScroll does not exceed the content length.
func (p *SplitPane) clampDetailScroll() {
	lines := strings.Count(p.detailContent, "\n")
	h := p.DetailHeight()
	maxScroll := lines - h
	if maxScroll < 0 {
		maxScroll = 0
	}
	if p.detailScroll > maxScroll {
		p.detailScroll = maxScroll
	}
	if p.detailScroll < 0 {
		p.detailScroll = 0
	}
}
