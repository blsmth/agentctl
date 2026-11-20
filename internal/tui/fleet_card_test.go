package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
)

// cardModel is mouseTestModel with the pointer resting on row "a" (line 2).
func cardModel(t *testing.T, w, h int) (FleetModel, fleetHover) {
	t.Helper()
	m := mouseTestModel(t, w, h)
	y := m.fleetListTop() + 2
	var cmd tea.Cmd
	var mm tea.Model = m
	mm, cmd = mm.Update(tea.MouseMotionMsg{X: 10, Y: y})
	m = mm.(FleetModel)
	if m.hover.reg != fleetHoverBody {
		t.Fatalf("hover = %+v, want body", m.hover)
	}
	if cmd == nil {
		t.Fatal("expected a dwell tick cmd")
	}
	return m, m.hover
}

func update(m FleetModel, msg tea.Msg) FleetModel {
	mm, _ := m.Update(msg)
	return mm.(FleetModel)
}

func TestCardDwellShowsAndStaleTickIgnored(t *testing.T) {
	m, target := cardModel(t, 120, 40)
	if got := update(m, fleetCardTickMsg{gen: m.hoverGen, target: target}); got.card == nil {
		t.Fatal("current tick should show the card")
	}
	if got := update(m, fleetCardTickMsg{gen: m.hoverGen - 1, target: target}); got.card != nil {
		t.Fatal("stale generation must not show the card")
	}
	if got := update(m, fleetCardTickMsg{gen: m.hoverGen, target: fleetHover{line: 3, reg: fleetHoverBody}}); got.card != nil {
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
			m = update(m, fleetCardTickMsg{gen: m.hoverGen, target: target})
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
	m = update(m, fleetCardTickMsg{gen: m.hoverGen, target: target})
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
	m = update(m, fleetCardTickMsg{gen: m.hoverGen, target: target})
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
	m = update(m, fleetCardTickMsg{gen: m.hoverGen, target: target})
	m.termHeight = 12 // list area under fleetCardMinH rows
	if m.fleetListRows() >= fleetCardMinH {
		t.Skipf("list rows = %d; pane not short enough", m.fleetListRows())
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
	lines := cardContent(r, nil, cardOpts{}, now, 40, cs).lines
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
	r.Recap = fleet.RecapLine{Text: "go test"} // same as activity: omitted
	full := ansi.Strip(strings.Join(cardContent(r, nil, cardOpts{parent: "boss"}, now, 60, cs).lines, "\n"))
	for _, want := range []string{"worker", "model", "42%", "12k new in", "agent/x", "#7", "boss"} {
		if !strings.Contains(full, want) {
			t.Errorf("missing %q in %q", want, full)
		}
	}
	if strings.Contains(full, "last") {
		t.Errorf("activity equal to recap should be omitted: %q", full)
	}
}

func TestCardTitle(t *testing.T) {
	InitStyles()
	cs := newCardStyles()
	now := time.Unix(1_000_000, 0)
	long := "Reducing signal log noise across the monitor, the hooks and every chrome bar pane in the session"
	tests := []struct {
		name   string
		recap  fleet.RecapLine
		manual bool
		want   []string // the lines right under the header
	}{
		{"short title", fleet.RecapLine{Title: "Reducing Signal Log Noise", Source: fleet.RecapTitle}, false,
			[]string{"Reducing Signal Log Noise"}},
		{"manual title", fleet.RecapLine{Title: "My own name", Source: fleet.RecapTitle}, true,
			[]string{"My own name"}},
		{"wraps to two lines", fleet.RecapLine{Title: long, Source: fleet.RecapTitle}, false,
			[]string{"Reducing signal log noise across the", "monitor, the hooks and every chrome bar…"}},
		{"empty", fleet.RecapLine{}, false, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := testRow("worker", "s1", "idle", "")
			r.Recap = tc.recap
			r.ShortManual = tc.manual
			lines := cardContent(r, nil, cardOpts{}, now, 40, cs).lines
			var got []string
			for _, l := range lines[1:] {
				got = append(got, ansi.Strip(l))
			}
			if len(got) != len(tc.want) {
				t.Fatalf("lines after header = %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("line %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
			for _, l := range lines {
				if w := lipgloss.Width(l); w > 40 {
					t.Errorf("line %q is %d wide, over 40", ansi.Strip(l), w)
				}
			}
		})
	}
}

func TestCardLastWraps(t *testing.T) {
	InitStyles()
	cs := newCardStyles()
	now := time.Unix(1_000_000, 0)
	tests := []struct {
		name    string
		last    string
		compact bool
		want    []string
	}{
		{"fits", "go test ./...", false, []string{"last   › go test ./..."}},
		{"wraps under the value", "agentctl status peek @1405 --lines 200 --since 10m --follow and then some more words here to fill", false, []string{
			"last   › agentctl status peek @1405 --lines",
			"         200 --since 10m --follow and then",
			"         some more words here to fill",
		}},
		{"ellipsis on the third line", strings.Repeat("word ", 40), false, []string{
			"last   › word word word word word word word",
			"         word word word word word word word",
			"         word word word word word word wor…",
		}},
		{"long path hard-wraps", "/Users/someone/code/personal/agentctl/.worktrees/agent-go-engineer-opus-adhoc/internal/tui/fleet_card.go", false, []string{
			"last   › /Users/someone/code/personal/agent",
			"         ctl/.worktrees/agent-go-engineer-",
			"         opus-adhoc/internal/tui/fleet_car…",
		}},
		{"compact keeps one line", strings.Repeat("word ", 40), true, []string{
			"last   › word word word word word word wor…",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := testRow("worker", "s1", "idle", tc.last)
			r.Branch = "b" // a row so labelW includes "branch"
			lines := cardContent(r, nil, cardOpts{compact: tc.compact}, now, 43, cs).lines
			got := lines[len(lines)-len(tc.want):]
			for i, want := range tc.want {
				if g := ansi.Strip(got[i]); g != want {
					t.Errorf("line %d = %q, want %q", i, g, want)
				}
				if w := lipgloss.Width(got[i]); w > 43 {
					t.Errorf("line %d is %d wide, over 43", i, w)
				}
			}
			if ansi.Strip(lines[len(lines)-len(tc.want)-1]) != "branch b" {
				t.Errorf("unexpected extra last lines: %q", lines)
			}
		})
	}
}

func TestPRLabel(t *testing.T) {
	tests := []struct {
		num            int
		url            string
		wantText, link string
	}{
		{7, "https://github.com/blsmth/agentctl/pull/7", "#7 blsmth/agentctl", "https://github.com/blsmth/agentctl/pull/7"},
		{0, "https://github.com/blsmth/agentctl/pull/12", "#12 blsmth/agentctl", "https://github.com/blsmth/agentctl/pull/12"},
		{0, "https://git.example.com/o/r/merge_requests/3", "git.example.com/o/r/merge_requests/3", "https://git.example.com/o/r/merge_requests/3"},
		{9, "", "#9", ""},
		{0, "", "", ""},
	}
	for _, tc := range tests {
		text, link := prLabel(tc.num, tc.url)
		if text != tc.wantText || link != tc.link {
			t.Errorf("prLabel(%d, %q) = %q, %q; want %q, %q", tc.num, tc.url, text, link, tc.wantText, tc.link)
		}
	}
}

func TestCardPRHyperlink(t *testing.T) {
	InitStyles()
	cs := newCardStyles()
	now := time.Unix(1_000_000, 0)
	const url = "https://github.com/blsmth/agentctl/pull/211"
	r := testRow("worker", "s1", "idle", "")
	r.PRNumber, r.PRURL = 211, url
	c := cardContent(r, nil, cardOpts{}, now, 40, cs)
	if c.prLine < 0 {
		t.Fatal("no PR line")
	}
	pr := c.lines[c.prLine]
	if !strings.Contains(pr, ansi.SetHyperlink(url)) || !strings.Contains(pr, ansi.ResetHyperlink()) {
		t.Fatalf("PR line has no OSC 8 link: %q", pr)
	}
	plain := ansi.Strip(pr)
	if plain != "pr #211 blsmth/agentctl" {
		t.Errorf("PR line = %q", plain)
	}
	if w := lipgloss.Width(pr); w != lipgloss.Width(plain) {
		t.Errorf("OSC 8 counted as width: %d vs %d", w, lipgloss.Width(plain))
	}

	t.Run("truncate keeps the link closed and width-neutral", func(t *testing.T) {
		got := truncateToWidth(pr, 10)
		if w := lipgloss.Width(got); w != 10 {
			t.Errorf("width %d, want 10: %q", w, got)
		}
		if strings.Count(got, "\x1b]8;") != 2 {
			t.Errorf("link open/close not both kept: %q", got)
		}
	})
	t.Run("composite does not count OSC 8 bytes", func(t *testing.T) {
		bg := strings.Repeat("x", 60) + "\nsecond"
		out := strings.Split(compositeOverlayAt(bg, pr, 5, 0, 80, 2), "\n")
		if w := lipgloss.Width(out[0]); w != 60 {
			t.Errorf("composited width %d, want 60", w)
		}
		plain := ansi.Strip(out[0])
		if want := "xxxxx" + "pr #211 blsmth/agentctl" + strings.Repeat("x", 60-5-len("pr #211 blsmth/agentctl")); plain != want {
			t.Errorf("composited = %q, want %q", plain, want)
		}
		if !strings.Contains(out[0], ansi.SetHyperlink(url)) {
			t.Error("link lost in composite")
		}
	})
	t.Run("no URL, no link", func(t *testing.T) {
		r.PRURL = ""
		c := cardContent(r, nil, cardOpts{}, now, 40, cs)
		if strings.Contains(c.lines[c.prLine], "\x1b]8;") {
			t.Errorf("link without URL: %q", c.lines[c.prLine])
		}
	})
}

func TestCardTokensFoldCacheWrites(t *testing.T) {
	InitStyles()
	r := testRow("worker", "s1", "idle", "")
	st := &cardStats{in: 68, cacheCreate: 40_000, out: 11_000, cacheRead: 2_300_000}
	full := ansi.Strip(strings.Join(cardContent(r, st, cardOpts{}, time.Unix(0, 0), 60, newCardStyles()).lines, "\n"))
	if !strings.Contains(full, "40k new in · 11k out · 2.3M cached") {
		t.Errorf("tokens row: %q", full)
	}
}

func TestCardAgentAndAccount(t *testing.T) {
	InitStyles()
	r := testRow("brandon:112", "s1", "idle", "")
	r.AgentName = "go-engineer"
	full := ansi.Strip(strings.Join(cardContent(r, nil, cardOpts{account: "work"}, time.Unix(0, 0), 60, newCardStyles()).lines, "\n"))
	for _, want := range []string{"agent   go-engineer", "account work"} {
		if !strings.Contains(full, want) {
			t.Errorf("missing %q in %q", want, full)
		}
	}
	r.AgentName = "brandon" // the name already says it
	full = ansi.Strip(strings.Join(cardContent(r, nil, cardOpts{}, time.Unix(0, 0), 60, newCardStyles()).lines, "\n"))
	if strings.Contains(full, "agent") || strings.Contains(full, "account") {
		t.Errorf("redundant agent/account rows: %q", full)
	}
}

// cardShownModel is cardModel with the card visible and row "a" carrying a
// title, a PR and a long last activity.
func cardShownModel(t *testing.T, w, h int) FleetModel {
	t.Helper()
	m, target := cardModel(t, w, h)
	r := &m.groups[0].Rows[0]
	r.Recap = fleet.RecapLine{Title: "Reducing Signal Log Noise", Source: fleet.RecapTitle}
	r.PRNumber, r.PRURL = 211, "https://github.com/blsmth/agentctl/pull/211"
	r.Branch = "agent/go-engineer-opus/adhoc-f7f8052e"
	r.ModelName = "claude-opus-4-5"
	r.LastToolDetail = "agentctl status peek @1405 --lines 200 --since 10m and a few more words to wrap it"
	m.rebuildLines()
	m = update(m, fleetCardTickMsg{gen: m.hoverGen, target: target})
	if m.card == nil {
		t.Fatal("card not shown")
	}
	return m
}

func TestCardClickOpensPR(t *testing.T) {
	m := cardShownModel(t, 120, 40)
	p, ok := m.cardLayout()
	if !ok || p.prY < 0 {
		t.Fatalf("layout %+v ok=%v", p, ok)
	}
	if row := ansi.Strip(strings.Split(m.render(), "\n")[p.prY]); !strings.Contains(row, "#211 blsmth/agentctl") {
		t.Fatalf("screen row %d is not the PR line: %q", p.prY, row)
	}
	var opened []string
	old := fleetOpenURL
	fleetOpenURL = func(url string) tea.Cmd { opened = append(opened, url); return nil }
	t.Cleanup(func() { fleetOpenURL = old })

	got := update(m, tea.MouseClickMsg{X: p.x + 3, Y: p.prY, Button: tea.MouseLeft})
	if len(opened) != 1 || opened[0] != "https://github.com/blsmth/agentctl/pull/211" {
		t.Fatalf("opened = %q", opened)
	}
	if got.card != nil {
		t.Error("card should hide after opening")
	}

	opened = nil
	for _, c := range []tea.MouseClickMsg{
		{X: p.x + 3, Y: p.prY - 1, Button: tea.MouseLeft},   // another card line
		{X: p.x + 3, Y: p.prY, Button: tea.MouseRight},      // right click
		{X: p.x + p.w + 2, Y: p.prY, Button: tea.MouseLeft}, // beside the card
	} {
		update(m, c)
	}
	if len(opened) != 0 {
		t.Errorf("only a left click on the PR line opens: %q", opened)
	}
}

func TestCardRendersCleanly(t *testing.T) {
	for _, width := range []int{fleetCardMinW + fleetCardInset, 60, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := cardShownModel(t, width, 40)
			p, ok := m.cardLayout()
			if !ok {
				t.Fatal("card not placed")
			}
			if p.w > fleetCardMaxW {
				t.Errorf("card %d wide, over max", p.w)
			}
			for i, l := range strings.Split(p.body, "\n") {
				if w := lipgloss.Width(l); w != p.w {
					t.Errorf("line %d is %d wide, want %d (gaps in the Surface fill): %q", i, w, p.w, ansi.Strip(l))
				}
			}
			if !strings.Contains(ansi.Strip(p.body), "Reducing Signal Log") {
				t.Error("title missing")
			}
		})
	}
}

func TestCardCompactFallback(t *testing.T) {
	m := cardShownModel(t, 120, 40)
	full, ok := m.cardLayout()
	if !ok {
		t.Fatal("card not placed")
	}
	fullH := strings.Count(full.body, "\n") + 1
	// Shrink the list until the full card fits neither below nor above row a.
	for h := 40; h > 10; h-- {
		m.termHeight = h
		p, ok := m.cardLayout()
		if !ok {
			break
		}
		if strings.Count(p.body, "\n")+1 < fullH {
			return // compact card drawn where the full one does not fit
		}
	}
	t.Fatal("never fell back to the compact card")
}
