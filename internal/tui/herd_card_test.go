package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/herd"
)

// cardModel is mouseTestModel with the pointer resting on row "a" (line 2).
func cardModel(t *testing.T, w, h int) (HerdModel, herdHover) {
	t.Helper()
	m := mouseTestModel(t, w, h)
	y := m.herdListTop() + 2
	var cmd tea.Cmd
	var mm tea.Model = m
	mm, cmd = mm.Update(tea.MouseMotionMsg{X: 10, Y: y})
	m = mm.(HerdModel)
	if m.hover.reg != herdHoverBody {
		t.Fatalf("hover = %+v, want body", m.hover)
	}
	if cmd == nil {
		t.Fatal("expected a dwell tick cmd")
	}
	return m, m.hover
}

func update(m HerdModel, msg tea.Msg) HerdModel {
	mm, _ := m.Update(msg)
	return mm.(HerdModel)
}

func TestCardDwellShowsAndStaleTickIgnored(t *testing.T) {
	m, target := cardModel(t, 120, 40)
	if got := update(m, herdCardTickMsg{gen: m.hoverGen, target: target}); got.card == nil {
		t.Fatal("current tick should show the card")
	}
	if got := update(m, herdCardTickMsg{gen: m.hoverGen - 1, target: target}); got.card != nil {
		t.Fatal("stale generation must not show the card")
	}
	if got := update(m, herdCardTickMsg{gen: m.hoverGen, target: herdHover{line: 3, reg: herdHoverBody}}); got.card != nil {
		t.Fatal("different target must not show the card")
	}
}

func TestCardHides(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.Msg
	}{
		{"move off", tea.MouseMotionMsg{X: 10, Y: 0}},
		{"move to other row", tea.MouseMotionMsg{X: 10, Y: 3}},
		{"key", tea.KeyPressMsg{Code: 'z'}},
		{"click", tea.MouseClickMsg{X: 10, Y: 3, Button: tea.MouseLeft}},
		{"wheel", tea.MouseWheelMsg{X: 10, Y: 3, Button: tea.MouseWheelDown}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, target := cardModel(t, 120, 40)
			m = update(m, herdCardTickMsg{gen: m.hoverGen, target: target})
			if m.card == nil {
				t.Fatal("card not shown")
			}
			if tc.name == "move off" || tc.name == "move to other row" {
				if tc.name == "move to other row" {
					m.hasPointer = true
				}
			}
			if got := update(m, tc.msg); got.card != nil {
				t.Fatal("card still shown")
			}
		})
	}
}

func TestCardHidesWhenRowDisappears(t *testing.T) {
	m, target := cardModel(t, 120, 40)
	m = update(m, herdCardTickMsg{gen: m.hoverGen, target: target})
	m.groups[0].Rows = m.groups[0].Rows[1:] // drop row a
	m.rebuildLines()
	m.refreshHover()
	if m.card != nil {
		t.Fatal("card should hide when its row is gone")
	}
}

func TestCardPlacement(t *testing.T) {
	tests := []struct {
		name                  string
		rowY, rowH, top, rows int
		h                     int
		wantY                 int
		wantOK                bool
	}{
		{"below", 5, 1, 4, 30, 7, 6, true},
		{"above near bottom", 31, 1, 4, 30, 6, 25, true},
		{"no room", 5, 1, 4, 5, 8, 0, false},
		{"below selected row detail", 5, 3, 4, 30, 6, 8, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			y, ok := cardRect(tc.rowY, tc.rowH, tc.top, tc.rows, tc.h)
			if ok != tc.wantOK || (ok && y != tc.wantY) {
				t.Fatalf("got (%d,%v), want (%d,%v)", y, ok, tc.wantY, tc.wantOK)
			}
		})
	}
}

func TestCardCompositeKeepsWidthAndOtherRows(t *testing.T) {
	m, target := cardModel(t, 120, 40)
	m = update(m, herdCardTickMsg{gen: m.hoverGen, target: target})
	m.groups[0].Rows[0].Branch = "feat/x"
	m.rebuildLines()
	base := m.renderList()
	out := m.render()
	if out == base {
		t.Fatal("card not drawn")
	}
	bl, ol := strings.Split(base, "\n"), strings.Split(out, "\n")
	if len(bl) != len(ol) {
		t.Fatalf("line count %d != %d", len(ol), len(bl))
	}
	cw := m.listColWidth()
	changed := 0
	for i := range ol {
		if w := lipgloss.Width(ol[i]); w > max(lipgloss.Width(bl[i]), cw) {
			t.Errorf("line %d width %d exceeds pane", i, w)
		}
		if ol[i] != bl[i] {
			changed++
		}
	}
	if changed < 4 || changed > 14 {
		t.Errorf("changed rows = %d, expected only the card rectangle", changed)
	}
	if !strings.Contains(ansi.Strip(out), "feat/x") {
		t.Error("branch missing from card")
	}
}

func TestCardNotShownWhenShort(t *testing.T) {
	m, target := cardModel(t, 120, 40)
	m = update(m, herdCardTickMsg{gen: m.hoverGen, target: target})
	m.termHeight = 12 // list area under herdCardMinH rows
	if m.herdListRows() >= herdCardMinH {
		t.Skipf("list rows = %d; pane not short enough", m.herdListRows())
	}
	base := m.renderList()
	if got := m.withCard(base); got != base {
		t.Fatal("card drawn in a pane too small for it")
	}
}

func TestCompositeOverlayAt(t *testing.T) {
	red := "\x1b[41m"
	tests := []struct {
		name string
		bg   string
		x    int
		fg   string
	}{
		{"plain", "abcdefghij", 3, "XY"},
		{"styled bg", red + "abcdefghij" + "\x1b[m", 3, "XY"},
		{"wide at left edge", "ab世界cdef", 3, "XY"},
		{"wide at right edge", "ab世界cdef", 3, "XYZ"},
		{"short bg", "ab", 5, "XY"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bgW := lipgloss.Width(tc.bg)
			out := compositeOverlayAt(tc.bg+"\nsecond", tc.fg, tc.x, 0, 40, 2)
			lines := strings.Split(out, "\n")
			if lines[1] != "second" {
				t.Errorf("untouched row changed: %q", lines[1])
			}
			plain := ansi.Strip(lines[0])
			if got, want := lipgloss.Width(plain), max(bgW, tc.x+lipgloss.Width(tc.fg)); got != want {
				t.Errorf("width %d, want %d (%q)", got, want, plain)
			}
			if !strings.Contains(plain, tc.fg) {
				t.Errorf("fg %q missing in %q", tc.fg, plain)
			}
		})
	}
}

func TestCardContentOmitsUnknown(t *testing.T) {
	InitStyles()
	cs := newCardStyles()
	now := time.Unix(1_000_000, 0)
	r := testRow("worker", "s1", "idle", "")
	lines := cardContent(r, nil, "", now, 40, cs)
	if len(lines) != 1 {
		t.Fatalf("bare row should only have a title, got %d lines: %q", len(lines), lines)
	}
	joined := ansi.Strip(strings.Join(lines, "\n"))
	for _, bad := range []string{"model", "context", "tokens", "cost", "branch", "?", " 0"} {
		if strings.Contains(joined, bad) {
			t.Errorf("unexpected %q in %q", bad, joined)
		}
	}

	r.ModelName = "claude-sonnet-4-20250514"
	r.ContextPct = 42
	r.InputTokens = 12000
	r.Branch = "agent/x"
	r.PRNumber = 7
	r.LastToolDetail = "go test"
	r.Recap = herd.RecapLine{Text: "go test"} // same as activity: omitted
	full := ansi.Strip(strings.Join(cardContent(r, nil, "boss", now, 60, cs), "\n"))
	for _, want := range []string{"worker", "model", "42%", "12k in", "agent/x", "#7", "boss"} {
		if !strings.Contains(full, want) {
			t.Errorf("missing %q in %q", want, full)
		}
	}
	if strings.Contains(full, "last") {
		t.Errorf("activity equal to recap should be omitted: %q", full)
	}
}
