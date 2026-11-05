package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// newTestPane returns a SplitPane with sensible defaults for testing.
func newTestPane() *SplitPane {
	p := &SplitPane{
		ListRatio:     0.4,
		MinListRows:   4,
		MinDetailRows: 4,
	}
	p.SetSize(100, 40)
	return p
}

// pressKey is a test helper that sends a key press string to the SplitPane.
// We set Text to key so that msg.String() returns the key string.
func pressKey(t *testing.T, p *SplitPane, key string) bool {
	t.Helper()
	var code rune
	if len([]rune(key)) == 1 {
		code = []rune(key)[0]
	}
	msg := tea.KeyPressMsg{Code: code, Text: key}
	return p.Update(msg)
}

// ── SetSize / height computation ─────────────────────────────────────────────

func TestSplitPane_ListHeight(t *testing.T) {
	tests := []struct {
		name       string
		termHeight int
		ratio      float64
		minRows    int
		want       int
	}{
		{"40% of 40 rows", 40, 0.4, 4, 16},
		{"40% of 100 rows", 100, 0.4, 4, 40},
		{"minimum enforced", 5, 0.4, 8, 8},
		{"zero ratio uses default 0.4", 40, 0, 4, 16},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &SplitPane{ListRatio: tc.ratio, MinListRows: tc.minRows}
			p.SetSize(80, tc.termHeight)
			got := p.ListHeight()
			if got != tc.want {
				t.Errorf("ListHeight() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSplitPane_DetailHeight(t *testing.T) {
	tests := []struct {
		name       string
		termHeight int
		ratio      float64
		minDetail  int
		want       int
	}{
		// termHeight=40, list=16, sep=1 → detail = 40-16-1 = 23
		{"standard split", 40, 0.4, 4, 23},
		// termHeight=20, list=8, sep=1 → detail = 11
		{"smaller terminal", 20, 0.4, 4, 11},
		// minimum enforced when result too small
		{"minimum enforced", 10, 0.9, 6, 6},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &SplitPane{ListRatio: tc.ratio, MinListRows: 4, MinDetailRows: tc.minDetail}
			p.SetSize(80, tc.termHeight)
			got := p.DetailHeight()
			if got != tc.want {
				t.Errorf("DetailHeight() = %d, want %d", got, tc.want)
			}
		})
	}
}

// ── Cursor / SetCursor clamping ──────────────────────────────────────────────

func TestSplitPane_SetCursor_Clamps(t *testing.T) {
	p := newTestPane()
	p.SetListContent("a\nb\nc", 3)

	t.Run("negative clamped to zero", func(t *testing.T) {
		p.SetCursor(-5)
		if p.Cursor() != 0 {
			t.Errorf("cursor = %d, want 0", p.Cursor())
		}
	})

	t.Run("beyond totalRows clamped", func(t *testing.T) {
		p.SetCursor(100)
		if p.Cursor() != 2 {
			t.Errorf("cursor = %d, want 2", p.Cursor())
		}
	})

	t.Run("valid value accepted", func(t *testing.T) {
		p.SetCursor(1)
		if p.Cursor() != 1 {
			t.Errorf("cursor = %d, want 1", p.Cursor())
		}
	})
}

func TestSplitPane_SetCursor_NoRows(t *testing.T) {
	p := newTestPane()
	p.SetListContent("", 0)
	p.SetCursor(5)
	if p.Cursor() != 0 {
		t.Errorf("cursor with 0 rows = %d, want 0", p.Cursor())
	}
}

// ── Cursor navigation ────────────────────────────────────────────────────────

func TestSplitPane_CursorNavigation(t *testing.T) {
	p := newTestPane()
	p.SetListContent("r1\nr2\nr3\nr4\nr5", 5)

	t.Run("j moves cursor down", func(t *testing.T) {
		p.SetCursor(0)
		handled := pressKey(t, p, "j")
		if !handled {
			t.Fatal("expected j to be handled")
		}
		if p.Cursor() != 1 {
			t.Errorf("cursor = %d, want 1", p.Cursor())
		}
	})

	t.Run("k moves cursor up", func(t *testing.T) {
		p.SetCursor(2)
		pressKey(t, p, "k")
		if p.Cursor() != 1 {
			t.Errorf("cursor = %d, want 1", p.Cursor())
		}
	})

	t.Run("j does not go past last row", func(t *testing.T) {
		p.SetCursor(4) // last row (0-based, totalRows=5)
		pressKey(t, p, "j")
		if p.Cursor() != 4 {
			t.Errorf("cursor = %d, want 4", p.Cursor())
		}
	})

	t.Run("k does not go below zero", func(t *testing.T) {
		p.SetCursor(0)
		pressKey(t, p, "k")
		if p.Cursor() != 0 {
			t.Errorf("cursor = %d, want 0", p.Cursor())
		}
	})
}

// ── List scroll follows cursor ───────────────────────────────────────────────

func TestSplitPane_ScrollFollowsCursor(t *testing.T) {
	// Use a small list pane to force scrolling.
	p := &SplitPane{ListRatio: 0.1, MinListRows: 3, MinDetailRows: 3}
	p.SetSize(80, 30)
	// ListHeight = max(int(30*0.1)=3, minListRows=3) = 3

	// Build 10 rows of content.
	rows := make([]string, 10)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}
	p.SetListContent(strings.Join(rows, "\n"), 10)
	p.SetCursor(0)

	// Move cursor to row 5 — scroll must catch up.
	for i := 0; i < 5; i++ {
		pressKey(t, p, "j")
	}
	if p.Cursor() != 5 {
		t.Errorf("cursor = %d, want 5", p.Cursor())
	}
	// With list height 3, scroll should be cursor-height+1 = 5-3+1 = 3.
	if p.listScroll != 3 {
		t.Errorf("listScroll = %d, want 3", p.listScroll)
	}

	// Move cursor back to 0 — scroll should reset to 0.
	for i := 0; i < 5; i++ {
		pressKey(t, p, "k")
	}
	if p.Cursor() != 0 {
		t.Errorf("cursor = %d, want 0", p.Cursor())
	}
	if p.listScroll != 0 {
		t.Errorf("listScroll = %d, want 0", p.listScroll)
	}
}

// ── Detail scroll ────────────────────────────────────────────────────────────

func TestSplitPane_DetailScrollFromDetailFocus(t *testing.T) {
	p := newTestPane()
	p.SetListContent("row", 1)
	// Use enough lines to exceed detailH (23) so scrolling is possible.
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = "detail line"
	}
	p.SetDetailContent(strings.Join(lines, "\n"))

	// Switch to detail focus.
	pressKey(t, p, "tab")
	if p.Focused() != focusDetail {
		t.Fatalf("focused = %d, want %d", p.Focused(), focusDetail)
	}

	// j scrolls detail down.
	pressKey(t, p, "j")
	if p.detailScroll != 1 {
		t.Errorf("detailScroll = %d, want 1", p.detailScroll)
	}

	// k scrolls detail back up.
	pressKey(t, p, "k")
	if p.detailScroll != 0 {
		t.Errorf("detailScroll = %d, want 0", p.detailScroll)
	}

	// k at top does not go negative.
	pressKey(t, p, "k")
	if p.detailScroll != 0 {
		t.Errorf("detailScroll = %d, want 0 (no underflow)", p.detailScroll)
	}
}

func TestSplitPane_DetailScrollFromListFocus(t *testing.T) {
	p := newTestPane()
	p.SetListContent("row", 1)
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = "detail line"
	}
	p.SetDetailContent(strings.Join(lines, "\n"))

	// Shift+J and Shift+K scroll detail while list has focus.
	pressKey(t, p, "J")
	if p.detailScroll != 1 {
		t.Errorf("detailScroll = %d, want 1", p.detailScroll)
	}
	pressKey(t, p, "K")
	if p.detailScroll != 0 {
		t.Errorf("detailScroll = %d, want 0", p.detailScroll)
	}
}

// ── Tab switches focus ───────────────────────────────────────────────────────

func TestSplitPane_TabSwitchesFocus(t *testing.T) {
	p := newTestPane()

	if p.Focused() != focusList {
		t.Fatalf("initial focus = %d, want %d (list)", p.Focused(), focusList)
	}

	// tab → detail
	handled := pressKey(t, p, "tab")
	if !handled {
		t.Fatal("expected tab to be handled")
	}
	if p.Focused() != focusDetail {
		t.Errorf("focused = %d, want %d (detail)", p.Focused(), focusDetail)
	}

	// tab → list
	pressKey(t, p, "tab")
	if p.Focused() != focusList {
		t.Errorf("focused = %d, want %d (list)", p.Focused(), focusList)
	}
}

// ── Unhandled key returns false ──────────────────────────────────────────────

func TestSplitPane_UnhandledKeyReturnsFalse(t *testing.T) {
	p := newTestPane()
	handled := pressKey(t, p, "q")
	if handled {
		t.Error("expected 'q' to be unhandled")
	}
}

// ── OrientHorizontal (sidebar + main panel) ─────────────────────────────────

func TestSplitPane_OrientVertical_IsZeroValue(t *testing.T) {
	var p SplitPane
	if p.Orientation != OrientVertical {
		t.Errorf("zero-value Orientation = %v, want OrientVertical (backward compat for existing callers)", p.Orientation)
	}
}

func TestSplitPane_ListWidth(t *testing.T) {
	tests := []struct {
		name       string
		termWidth  int
		ratio      float64
		minCols    int
		wantAtLast int // exact expected width
	}{
		{"30% of 100", 100, 0.3, 20, 30},
		{"minimum enforced", 40, 0.1, 20, 20},
		{"zero ratio uses default 0.3", 100, 0, 20, 30},
		{"capped so detail pane keeps room", 100, 0.95, 20, 90}, // termWidth-10
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &SplitPane{Orientation: OrientHorizontal, ListRatio: tc.ratio, MinListRows: tc.minCols}
			p.SetSize(tc.termWidth, 40)
			got := p.ListWidth()
			if got != tc.wantAtLast {
				t.Errorf("ListWidth() = %d, want %d", got, tc.wantAtLast)
			}
		})
	}
}

func TestSplitPane_DetailWidth(t *testing.T) {
	p := &SplitPane{Orientation: OrientHorizontal, ListRatio: 0.3, MinListRows: 20}
	p.SetSize(100, 40)
	// ListWidth = 30, so DetailWidth = 100 - 30 = 70. There is no separate
	// separator column: the detail box's own left border is the divider.
	if got := p.DetailWidth(); got != 70 {
		t.Errorf("DetailWidth() = %d, want 70", got)
	}
}

func TestSplitPane_OrientHorizontal_PaneHeightIsFullHeight(t *testing.T) {
	p := &SplitPane{Orientation: OrientHorizontal, ListRatio: 0.3, MinListRows: 20}
	p.SetSize(100, 40)
	if got := p.ListHeight(); got != 40 {
		t.Errorf("ListHeight() = %d, want 40 (full height, panes are side by side not stacked)", got)
	}
	// DetailHeight is the detail box's *interior* row count, not the full
	// pane height: the box's border (2 rows) and vertical padding (2 rows)
	// both consume rows of the shared 40-row pane, so 40-4=36. Pinning the
	// box's Style.Height to this value is what keeps its rendered height —
	// interior + border + padding — equal to the full pane height (see
	// TestSplitPane_ViewHorizontal_DividerSpansFullHeight).
	if got := p.DetailHeight(); got != 36 {
		t.Errorf("DetailHeight() = %d, want 36", got)
	}
}

func TestSplitPane_ViewHorizontal_RendersSideBySide(t *testing.T) {
	InitStyles()

	p := &SplitPane{Orientation: OrientHorizontal, ListRatio: 0.3, MinListRows: 20, MinDetailRows: 20}
	p.SetSize(80, 10)
	p.SetListContent("sidebar row one\nsidebar row two", 2)
	p.SetDetailContent("main panel line one\nmain panel line two")

	view := p.View()

	if !strings.Contains(view, "sidebar row one") {
		t.Error("view missing sidebar content 'sidebar row one'")
	}
	if !strings.Contains(view, "main panel line one") {
		t.Error("view missing main panel content 'main panel line one'")
	}
	if !strings.Contains(view, "│") {
		t.Error("view missing vertical separator column")
	}

	// Side-by-side (not stacked) means the sidebar's first row and the
	// detail box's top border land on the very same rendered line — the
	// detail box's own top/left padding pushes its actual text content
	// down a couple of rows within the box, so this checks placement, not
	// line-for-line text alignment.
	lines := strings.Split(view, "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "sidebar row one") || !strings.Contains(lines[0], "╭") {
		t.Errorf("expected sidebar content and the detail box's top border on the same first row (side by side); view:\n%s", view)
	}

	// Every line containing the main panel's text must also carry the
	// vertical separator column — proving it renders to the right of the
	// sidebar, not appended below it the way OrientVertical would.
	for _, l := range lines {
		if strings.Contains(l, "main panel line one") && !strings.Contains(l, "│") {
			t.Errorf("main panel line missing the vertical separator column: %q", l)
		}
	}
}

// TestSplitPane_ViewHorizontal_DividerIsSingleAndFullHeight is the
// regression test for the two herd hub defects fixed here: a doubled
// vertical rule where the sidebar met the detail panel (defect 1 — "│╭" /
// "││" on every row), and a divider that stopped short of the pane's full
// height once the sidebar ran out of content while the detail box's border
// kept going (defect 2). It's table-driven over sidebar/detail content
// lengths — empty, short, and taller than the pane — because both defects
// were sensitive to how much content each pane had relative to the pane
// height.
func TestSplitPane_ViewHorizontal_DividerIsSingleAndFullHeight(t *testing.T) {
	InitStyles()

	const paneHeight = 24

	tests := []struct {
		name        string
		listRows    int
		detailLines int
	}{
		{"empty sidebar, empty detail", 0, 0},
		{"short sidebar, short detail", 1, 1},
		{"short sidebar, detail taller than pane", 1, 60},
		{"sidebar taller than pane, short detail", 60, 1},
		{"both taller than pane", 60, 60},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &SplitPane{Orientation: OrientHorizontal, ListRatio: 0.3, MinListRows: 20, MinDetailRows: 20}
			p.SetSize(100, paneHeight)

			var listLines, detailLines []string
			for i := 0; i < tc.listRows; i++ {
				listLines = append(listLines, fmt.Sprintf("sidebar row %d", i))
			}
			for i := 0; i < tc.detailLines; i++ {
				detailLines = append(detailLines, fmt.Sprintf("detail row %d", i))
			}
			p.SetListContent(strings.Join(listLines, "\n"), tc.listRows)
			p.SetDetailContent(strings.Join(detailLines, "\n"))

			view := p.View()
			lines := strings.Split(view, "\n")

			// The divider must run the full pane height, independent of how
			// many lines of sidebar (or detail) content exist (defect 2).
			if len(lines) != paneHeight {
				t.Fatalf("rendered %d lines, want %d (full pane height)", len(lines), paneHeight)
			}

			listW := p.ListWidth()
			for i, raw := range lines {
				plain := ansi.Strip(raw)
				runes := []rune(plain)
				if listW >= len(runes) {
					t.Fatalf("line %d has only %d columns, expected a divider at column %d: %q", i, len(runes), listW, plain)
				}
				// Exactly one border rune at the seam — never two adjacent
				// ones, which was the doubled-rule defect (defect 1).
				seam := runes[listW]
				if !strings.ContainsRune("│╭╮╰╯", seam) {
					t.Errorf("line %d: expected a single box-border rune at the seam (column %d), got %q: %q", i, listW, string(seam), plain)
				}
				if listW+1 < len(runes) && strings.ContainsRune("│╭╮╰╯", runes[listW+1]) {
					t.Errorf("line %d: doubled border runes at the seam (columns %d,%d): %q — the sidebar/detail divider must be a single column", i, listW, listW+1, plain)
				}
			}
		})
	}
}

// TestSplitPane_ViewHorizontal_WideDetailLineDoesNotOverflowHeight guards
// against a subtler way defect 2 could resurface: a single detail-pane line
// (e.g. a raw tmux pane capture, which is not pre-truncated) wider than the
// box's interior would otherwise get wrapped into extra lines by lipgloss,
// pushing the box's rendered height past the pane height and reintroducing
// a ragged, less-than-full-height divider. Every line must be truncated to
// the interior width before rendering so the box's footprint stays fixed
// regardless of content width.
func TestSplitPane_ViewHorizontal_WideDetailLineDoesNotOverflowHeight(t *testing.T) {
	InitStyles()

	const paneHeight = 20

	p := &SplitPane{Orientation: OrientHorizontal, ListRatio: 0.3, MinListRows: 20, MinDetailRows: 20}
	p.SetSize(80, paneHeight)

	// A single line far wider than the detail pane's interior width.
	wide := strings.Repeat("x", 500)
	p.SetListContent("sidebar", 1)
	p.SetDetailContent(wide)

	view := p.View()
	lines := strings.Split(view, "\n")
	if len(lines) != paneHeight {
		t.Fatalf("rendered %d lines, want %d (full pane height) — a wide detail line must not wrap into extra rows", len(lines), paneHeight)
	}
}

func TestSplitPane_ViewHorizontal_EmptyContentDoesNotPanic(t *testing.T) {
	InitStyles()
	p := &SplitPane{Orientation: OrientHorizontal}
	p.SetSize(80, 10)
	p.SetListContent("", 0)
	p.SetDetailContent("")
	_ = p.View() // must not panic
}

// ── View renders both panes with separator ───────────────────────────────────

func TestSplitPane_View(t *testing.T) {
	// InitStyles must be called so tableStyles is populated.
	InitStyles()

	p := newTestPane()
	p.SetSize(80, 30)
	p.SetListContent("row one\nrow two\nrow three", 3)
	p.SetDetailContent("detail line one\ndetail line two")

	view := p.View()

	// List content must appear.
	if !strings.Contains(view, "row one") {
		t.Error("view missing list content 'row one'")
	}
	if !strings.Contains(view, "row two") {
		t.Error("view missing list content 'row two'")
	}

	// Detail content must appear.
	if !strings.Contains(view, "detail line one") {
		t.Error("view missing detail content 'detail line one'")
	}

	// Separator must appear (─ repeated, rendered by tableStyles.Rule).
	if !strings.Contains(view, "─") {
		t.Error("view missing horizontal separator")
	}
}
