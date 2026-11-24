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
	"github.com/blsmth/agentctl/internal/state"
)

// firstLine is the plain first screen row of a rendered (possibly expanded) line.
func firstLine(s string) string {
	l, _, _ := strings.Cut(ansi.Strip(s), "\n")
	return l
}

func TestCloseGlyphOnlySelectedOrHovered(t *testing.T) {
	m := sampleSidebar(t, 56)
	cw := m.contentWidth()
	sel := m.splitPane.Cursor()
	var other int
	for i, l := range m.lines {
		if l.kind == fleetLineRow && i != sel {
			other = i
			break
		}
	}
	tests := []struct {
		name     string
		line     int
		hover    fleetHoverRegion
		wantShow bool
	}{
		{"selected", sel, fleetHoverNone, true},
		{"plain row", other, fleetHoverNone, false},
		{"hovered body", other, fleetHoverBody, true},
		{"hovered close", other, fleetHoverClose, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := m.lines[tt.line]
			out := m.renderTreeRowHover(l.row, l.treePrefix, tt.line == sel, tt.hover, cw)
			first := firstLine(out)
			if got := strings.Contains(first, fleetCloseLabel); got != tt.wantShow {
				t.Errorf("✕ shown = %v, want %v: %q", got, tt.wantShow, first)
			}
			// The slot is always reserved: the row is as wide as ever.
			if w := lipgloss.Width(first); w < cw-fleetSidebarRightGutter {
				t.Errorf("row width %d, want at least %d", w, cw-fleetSidebarRightGutter)
			}
			if tt.hover == fleetHoverClose && !strings.Contains(out, fleetCloseHoverStyle.Render(fleetCloseLabel)) {
				t.Errorf("close hover must render the ✕ red")
			}
		})
	}
}

func TestRowRightColumnsAlign(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	ago := func(d time.Duration) time.Time { return sidebarNow.Add(-d) }
	rows := []struct {
		name  string
		pr    int
		age   time.Duration
		model string
		ctx   int
	}{
		{"no pr", 0, 4 * time.Minute, "", 0},
		{"two digit", 60, 25 * time.Minute, "claude-opus-4-7", 42},
		{"four digit", 1025, 3 * time.Hour, "claude-sonnet-4-5-20250929", 7},
		{"pr no age", 7, 0, "claude-haiku-4-5", 0},
		{"short age", 0, 40 * time.Second, "claude-opus-4-7[1m]", 100},
	}
	m := newFleetModel()
	m.termWidth, m.liveLeft = 56, 56
	// A list where some row shows a context warning, so every column is
	// drawn.
	m.except = fleetExceptions{ctx: true}
	cw := m.contentWidth()
	prEnd, ageEnd := -1, -1
	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			r := testRow("worker", "s", "idle", "Reading a fairly long file name.go")
			r.PRNumber = tt.pr
			r.ModelName, r.ContextPct, r.TurnCount = tt.model, tt.ctx, 1
			if tt.age > 0 {
				r.StateSince = ago(tt.age)
			}
			line := []rune(firstLine(m.renderTreeRowHover(r, "", false, fleetHoverNone, cw)))
			// Right edge: gutter, ✕ slot, space, then the age slot.
			ageCol := cw - fleetSidebarRightGutter - 1 - 1 - 1
			prCol := ageCol - fleetAgeSlotW - 1
			ctxCol := prCol - fleetPRSlotW - 1
			warned := rowCtxWarn(r, fleetDisplay{})
			if warned && string(line[ctxCol]) != "%" {
				t.Errorf("ctx must end at col %d: %q", ctxCol, string(line))
			}
			if !warned && strings.Contains(string(line), "%") {
				t.Errorf("a calm context must not show: %q", string(line))
			}
			for _, name := range []string{"opus", "sonnet", "haiku"} {
				if strings.Contains(string(line), name) {
					t.Errorf("the model must not show on the row: %q", string(line))
				}
			}
			if tt.age > 0 && line[ageCol] == ' ' {
				t.Errorf("age must end at col %d: %q", ageCol, string(line))
			}
			if tt.pr > 0 && line[prCol] == ' ' {
				t.Errorf("PR must end at col %d: %q", prCol, string(line))
			}
			if tt.age > 0 {
				if ageEnd >= 0 && ageEnd != ageCol {
					t.Errorf("age column moved")
				}
				ageEnd = ageCol
			}
			if tt.pr > 0 {
				if prEnd >= 0 && prEnd != prCol {
					t.Errorf("PR column moved")
				}
				prEnd = prCol
			}
		})
	}
}

func TestFitRightColsDegradesWithoutExceptions(t *testing.T) {
	// No row shows a context warning: that column takes no
	// width, so the PR and age need only their own.
	full := joinedWidth(fleetPRSlotW, fleetAgeSlotW) + 1 + fleetMinRecapWidth
	pr := fleetPRSlotW + 1 + fleetMinRecapWidth
	if full != 18 || pr != 14 {
		t.Fatalf("thresholds = %d/%d, want 18/14", full, pr)
	}
	tests := []struct {
		available int
		pr, age   bool
	}{
		{full + 10, true, true},
		{full, true, true},
		// The age drops first, the PR last.
		{full - 1, true, false},
		{pr, true, false},
		{pr - 1, false, false},
		{0, false, false},
	}
	for _, tt := range tests {
		c := fitRightCols(tt.available, fleetExceptions{}, false)
		if c.pr != tt.pr || c.age != tt.age || c.ctx {
			t.Errorf("fitRightCols(%d) = %+v, want pr=%v age=%v", tt.available, c, tt.pr, tt.age)
		}
	}
}

func TestNameStyleByState(t *testing.T) {
	InitStyles()
	tests := []struct {
		name  string
		state string
		done  bool
		want  fleetWeight
		style lipgloss.Style
		bold  bool
	}{
		{"working", "working", false, fleetWeightWorking, fleetNameWorkingStyle, true},
		{"done", "idle", true, fleetWeightDone, fleetNameDoneStyle, false},
		{"idle", "idle", false, fleetWeightIdle, fleetNameIdleStyle, false},
		{"blocked", "blocked", false, fleetWeightBlocked, fleetBlockedStyle.Bold(true), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testRow("worker", "s", tt.state, "")
			r.Done = tt.done
			if got := rowWeight(r); got != tt.want {
				t.Fatalf("rowWeight = %v, want %v", got, tt.want)
			}
			st := fleetNameStyle(tt.want)
			if st.GetBold() != tt.bold {
				t.Errorf("bold = %v, want %v", st.GetBold(), tt.bold)
			}
			if got, want := renderAgentName("worker", tt.want), tt.style.Render("worker"); got != want {
				t.Errorf("name = %q, want %q", got, want)
			}
			// Names never wear a project palette colour.
			for _, c := range fleetPalette() {
				if st.GetForeground() == lipgloss.Color(c) && tt.want != fleetWeightBlocked {
					t.Errorf("name uses palette colour %s", c)
				}
			}
		})
	}
	// Recap weight follows: working text, done subtle, idle muted.
	if recapStyle(fleetWeightWorking, fleet.RecapSummary).GetForeground() != fleetRecapWorkingStyle.GetForeground() {
		t.Error("working recap should be normal text")
	}
	if recapStyle(fleetWeightDone, fleet.RecapSummary).GetForeground() != fleetRecapStyle.GetForeground() {
		t.Error("done recap should be subtle")
	}
	if recapStyle(fleetWeightIdle, fleet.RecapSummary).GetForeground() != fleetRecapIdleStyle.GetForeground() {
		t.Error("idle recap should be muted")
	}
}

func TestCardTitleUsesNameWeight(t *testing.T) {
	InitStyles()
	cs := newCardStyles()
	r := testRow("worker:1", "s", "idle", "")
	r.Done = true
	title := cardContent(r, nil, "", time.Unix(0, 0), 50, cs)[0]
	want := fleetNameStyle(fleetWeightDone).Background(cs.bg.GetBackground()).Render("worker:1")
	if !strings.HasPrefix(title, want) {
		t.Errorf("card title should use the done name style: %q", title)
	}
}

func TestGroupHeaderCountsAlign(t *testing.T) {
	InitStyles()
	mk := func(st string, n int) []fleet.Row {
		var out []fleet.Row
		for range n {
			out = append(out, testRow("a", "s", st, ""))
		}
		return out
	}
	groups := []fleet.Group{
		{Session: "agentctl-a", Project: "a", Rows: mk("working", 1), ProjectDir: "/a", RuntimeDir: "/a/.agentctl"},
		{Session: "agentctl-much-longer-name", Project: "much-longer-name", Rows: append(mk("blocked", 1), mk("idle", 3)...)},
		{Session: "agentctl-mid", Project: "mid", Rows: mk("idle", 12), ProjectDir: "/mid", RuntimeDir: "/mid/.agentctl"},
	}
	for _, cw := range []int{44, 56, 80} {
		m := FleetModel{groups: groups}
		cols := map[string]int{}
		for _, g := range groups {
			line := ansi.Strip(m.renderGroupHeader(g, false, cw))
			rs := []rune(line)
			if len(rs) != cw-fleetSidebarRightGutter {
				t.Errorf("cw %d: header width %d: %q", cw, len(rs), line)
			}
			// Each state glyph sits at the same column on every header that has it.
			for _, gl := range []string{"⏸", "●", "✓", "○"} {
				idx := strings.LastIndex(line, gl)
				if idx < 0 {
					continue
				}
				c := len([]rune(line[:idx]))
				if want, ok := cols[gl]; ok && c != want {
					t.Errorf("cw %d: %s at col %d, want %d: %q", cw, gl, c, want, line)
				}
				cols[gl] = c
			}
		}
		if len(cols) != 3 {
			t.Errorf("cw %d: expected ●, ⏸ and ○ counts, got %v", cw, cols)
		}
	}
}

// TestGroupHeaderDottedProjectSession is the regression test for a dotted
// project: its session name is sanitized ("." becomes "-"), which used to be
// shown as a custom session and squeeze the counts.
func TestGroupHeaderDottedProjectSession(t *testing.T) {
	InitStyles()
	g := fleet.Group{
		Session: "agentctl-energy-solutions-ops", Project: "energy-solutions.ops",
		ProjectDir: "/src/energy-solutions.ops", RuntimeDir: "/src/energy-solutions.ops/.agentctl",
		Rows: []fleet.Row{testRow("a", "s", "idle", ""), testRow("b", "s", "working", "")},
	}
	if groupCustomSession(g) {
		t.Fatal("sanitized default session reported as custom")
	}
	m := FleetModel{groups: []fleet.Group{g}}
	out := ansi.Strip(m.renderGroupHeader(g, false, 44))
	if strings.Contains(out, "agentctl-") {
		t.Errorf("default session shown: %q", out)
	}
	if !strings.Contains(out, "● 1") || !strings.Contains(out, "○ 1") {
		t.Errorf("counts truncated: %q", out)
	}

	// A really custom session shows, but loses to the counts when tight.
	g.Session = "my-very-own-session-name"
	if !groupCustomSession(g) {
		t.Fatal("custom session not detected")
	}
	m.groups = []fleet.Group{g}
	if out := ansi.Strip(m.renderGroupHeader(g, false, 80)); !strings.Contains(out, "my-very-own") {
		t.Errorf("custom session hidden at 80 cols: %q", out)
	}
	if out := ansi.Strip(m.renderGroupHeader(g, false, 44)); !strings.Contains(out, "● 1") || !strings.Contains(out, "○ 1") || strings.Contains(out, "my-very") {
		t.Errorf("custom session must not truncate the counts: %q", out)
	}
}

// withoutChipLine drops the trailing chip line (rows never draw the agent
// name, so a selected row always has one) after checking it is there.
func withoutChipLine(t *testing.T, d []fleetDetailLine) []fleetDetailLine {
	t.Helper()
	if len(d) == 0 || !strings.HasPrefix(d[len(d)-1].text, "[") {
		t.Fatalf("detail has no trailing chip line: %+v", d)
	}
	return d[:len(d)-1]
}

func TestRowDetailActivityIsOneLine(t *testing.T) {
	long := strings.Repeat("arg ", 40)
	tests := []struct {
		name       string
		tool, text string
		wantMarker string
		wantPrefix string
	}{
		{"bash command", "Bash: go test " + long, "", fleetCommandMarker, "go test arg"},
		{"other tool", "Editing " + long, "", fleetActivityMarker, "Editing arg"},
		{"last text", "", "I looked at " + long, fleetActivityMarker, "I looked at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testRow("w", "s", "working", tt.tool)
			r.LastText = tt.text
			r.Recap = fleet.Recap(r, fleet.LogInfo{})
			if r.Recap.Source != fleet.RecapActivity {
				t.Fatalf("fixture: source = %v", r.Recap.Source)
			}
			d := withoutChipLine(t, rowDetail(r, 0, 56, fleetDisplay{}, false))
			if len(d) != 1 {
				t.Fatalf("activity expansion = %d lines, want 1: %+v", len(d), d)
			}
			if d[0].marker != tt.wantMarker || !strings.HasPrefix(d[0].text, tt.wantPrefix) || !strings.HasSuffix(d[0].text, "…") {
				t.Errorf("detail = %+v", d[0])
			}
		})
	}
	// Summary text still wraps.
	r := testRow("w", "s", "working", "")
	r.Recap = fleet.RecapLine{Text: long, Source: fleet.RecapSummary}
	if n := len(withoutChipLine(t, rowDetail(r, 0, 56, fleetDisplay{}, false))); n != fleetDetailMaxLines {
		t.Errorf("summary expansion = %d lines, want %d", n, fleetDetailMaxLines)
	}
}

func TestEmptyRowFallback(t *testing.T) {
	now := sidebarNow
	tests := []struct {
		name string
		row  fleet.Row
		want string
	}{
		{"no turns, started", fleet.Row{AgentState: state.AgentState{StartTime: now.Add(-4 * time.Minute).Unix()}}, "no prompt yet · 4m"},
		{"no turns, unknown start", fleet.Row{}, "no prompt yet"},
		{"no turns, state since", fleet.Row{StateSince: now.Add(-90 * time.Second)}, "no prompt yet · 1m"},
		{"turns", fleet.Row{AgentState: state.AgentState{TurnCount: 3, StartTime: now.Add(-50 * time.Hour).Unix()}}, "no activity yet · started 2d ago"},
		{"turns, unknown start", fleet.Row{AgentState: state.AgentState{TurnCount: 3}}, "no activity yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := emptyRecapText(tt.row, now); got != tt.want {
				t.Errorf("emptyRecapText = %q, want %q", got, tt.want)
			}
		})
	}

	// Through rowRecap and the row render, in the muted italic fallback style.
	InitStyles()
	fixedFleetNow(t)
	r := testRow("w", "s", "idle", "")
	r.StartTime = sidebarNow.Add(-4 * time.Minute).Unix()
	rc := rowRecap(r)
	if rc.Source != fleet.RecapNone || rc.Text != "no prompt yet · 4m" {
		t.Errorf("rowRecap = %+v", rc)
	}
	if fleet.Recap(r, fleet.LogInfo{}).Text != "" {
		t.Error("fleet.Recap must stay pure")
	}
	m := newFleetModel()
	out := m.renderTreeRowHover(r, "", false, fleetHoverNone, 80)
	if !strings.Contains(out, fleetEmptyStyle.Render("no prompt yet · 4m")) {
		t.Errorf("fallback not rendered muted italic: %q", ansi.Strip(out))
	}
	if d := withoutChipLine(t, rowDetail(r, 0, 80, fleetDisplay{}, false)); len(d) != 0 {
		t.Errorf("fallback must not expand: %+v", d)
	}
}

func TestFleetFooterSingleLine(t *testing.T) {
	InitStyles()
	tests := []struct {
		cw       int
		want     []string
		dropped  []string
		lastHint string
	}{
		{77, []string{"j/k move", "⏎ focus", "b blocked", "n new", "w sweep", "x close", "q leave", "? more"}, nil, "? more"},
		{67, []string{"j/k move", "⏎ focus", "b blocked", "n new", "w sweep", "? more"}, []string{"q leave"}, "? more"},
		{44, []string{"j/k move", "⏎ focus", "? more"}, []string{"q leave"}, "? more"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("cw=%d", tt.cw), func(t *testing.T) {
			m := newFleetModel()
			m.termWidth, m.liveLeft = tt.cw, tt.cw
			if m.contentWidth() != tt.cw {
				t.Fatalf("contentWidth = %d", m.contentWidth())
			}
			footer := ansi.Strip(m.renderFooter(tt.cw))
			lines := strings.Split(footer, "\n")
			hints := lines[len(lines)-1]
			if m.liveHintRows() != 0 {
				t.Errorf("hint bar takes %d extra rows", m.liveHintRows())
			}
			if w := ansi.StringWidth(hints); w > tt.cw {
				t.Errorf("hint bar %d wide > %d: %q", w, tt.cw, hints)
			}
			for _, w := range tt.want {
				if !strings.Contains(hints, w) {
					t.Errorf("missing %q in %q", w, hints)
				}
			}
			for _, d := range tt.dropped {
				if strings.Contains(hints, d) {
					t.Errorf("%q should be dropped at %d: %q", d, tt.cw, hints)
				}
			}
			if !strings.HasSuffix(strings.TrimSpace(hints), tt.lastHint) {
				t.Errorf("%q must stay last: %q", tt.lastHint, hints)
			}
		})
	}
}

func TestHelpMenuListsEveryBinding(t *testing.T) {
	m := mouseTestModel(t, 120, 40)
	got, _ := m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	m = got.(FleetModel)
	if !m.menuOpen || !m.menuHelp {
		t.Fatal("? must open the key reference")
	}
	out := ansi.Strip(m.render())
	for _, want := range []string{
		"j/k move", "⏎/tab focus live", "^b o back", "b next blocked", "n new", "x close",
		"J home", "p open PR", "r refresh", "c chrome", "q leave", "esc close help",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("key reference missing %q:\n%s", want, out)
		}
	}
	// The footer grew by the reference's extra lines, the list shrank to match.
	lines, _ := fleetHelpLayout(fleetHelpItems(), m.contentWidth())
	if got := m.liveHintRows(); got != len(lines)-1 {
		t.Errorf("liveHintRows = %d, want %d", got, len(lines)-1)
	}
	if got := strings.Count(m.render(), "\n") + 1; got != m.termHeight-1 {
		t.Errorf("render is %d rows, want %d", got, m.termHeight-1)
	}

	// Any key closes it; a listed binding then runs.
	got, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m2 := got.(FleetModel)
	if m2.menuOpen || m2.splitPane.Cursor() != m.splitPane.Cursor()+1 {
		t.Errorf("j should close the reference and move: open=%v cursor=%d", m2.menuOpen, m2.splitPane.Cursor())
	}
	got, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m3 := got.(FleetModel); m3.menuOpen || m3.splitPane.Cursor() != m.splitPane.Cursor() {
		t.Error("esc should only close the reference")
	}

	// Clicking an entry runs it.
	_, spans := fleetHelpLayout(fleetHelpItems(), m.contentWidth())
	got, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: spans[0].start, Y: m.footerMessageY() + spans[0].line})
	if m4 := got.(FleetModel); m4.menuOpen || m4.splitPane.Cursor() != m.splitPane.Cursor()+1 {
		t.Errorf("clicking j/k move should move: open=%v cursor=%d", m4.menuOpen, m4.splitPane.Cursor())
	}
}

func TestInfoMessageExpires(t *testing.T) {
	InitStyles()
	now := sidebarNow
	old := fleetNow
	fleetNow = func() time.Time { return now }
	t.Cleanup(func() { fleetNow = old })

	m := newFleetModel()
	m.termWidth, m.liveLeft = 80, 80
	m.groups = []fleet.Group{{Session: "agentctl-energy-solutions-ops", Project: "energy-solutions.ops"}}
	got, _ := m.Update(fleetLaunchedMsg{session: "agentctl-energy-solutions-ops"})
	m = got.(FleetModel)
	if m.infoMsg != "launched new agent in energy-solutions.ops" || !m.infoOK {
		t.Fatalf("info = %q ok=%v", m.infoMsg, m.infoOK)
	}
	footer := m.renderFooter(m.contentWidth())
	if !strings.Contains(footer, fleetSuccessStyle.Render("  "+fleetInfoGlyph+m.infoMsg)) {
		t.Errorf("success notice not in success style with glyph: %q", ansi.Strip(footer))
	}

	steps := []struct {
		after time.Duration
		want  bool
	}{{time.Second, true}, {3 * time.Second, true}, {fleetInfoTTL, false}}
	for _, s := range steps {
		now = sidebarNow.Add(s.after)
		got, _ = m.Update(fleetTickMsg{})
		if has := got.(FleetModel).infoMsg != ""; has != s.want {
			t.Errorf("after %v: shown = %v, want %v", s.after, has, s.want)
		}
	}

	// Progress notices do not expire; errors stay until the next key.
	m.setProgress("launching agent in x…")
	now = sidebarNow.Add(time.Hour)
	got, _ = m.Update(fleetTickMsg{})
	if got.(FleetModel).infoMsg == "" {
		t.Error("progress notice expired")
	}
	m.jumpErr = "boom"
	got, _ = m.Update(fleetTickMsg{})
	if got.(FleetModel).jumpErr != "boom" {
		t.Error("error cleared without a key")
	}
	got, _ = got.(FleetModel).Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if got.(FleetModel).jumpErr != "" {
		t.Error("error should clear on the next key")
	}
}

func TestOverflowIndicators(t *testing.T) {
	m := captureSidebar(t, 56, 30)
	top := m.fleetListTop()
	if top != 2 {
		t.Fatalf("list top = %d, want 2 (title, one blank/indicator row)", top)
	}
	// With slack: no indicators.
	out := strings.Split(ansi.Strip(m.render()), "\n")
	if strings.TrimSpace(out[top-1]) != "" || strings.Contains(strings.Join(out, "\n"), "more") && !strings.Contains(out[len(out)-1], "more") {
		t.Errorf("unexpected indicator with slack:\n%s", strings.Join(out, "\n"))
	}

	// Short: rows hidden below.
	m.termHeight = fleetChromeRows + 6
	m.configureSplitPane()
	m.selectLine(m.splitPane.Cursor())
	rows, _ := m.hiddenRows(m.shownEnd(m.contentWidth()), len(m.lines))
	if rows == 0 {
		t.Fatal("fixture should hide rows below")
	}
	out = strings.Split(ansi.Strip(m.render()), "\n")
	below := out[top+m.visibleLines()]
	if want := fmt.Sprintf("↓ %d more", rows); strings.TrimSpace(below) != want {
		t.Errorf("below indicator = %q, want %q", below, want)
	}
	if strings.TrimSpace(out[top-1]) != "" {
		t.Errorf("no rows above yet, got %q", out[top-1])
	}
	footerY := m.footerMessageY()
	if footerY != top+m.visibleLines()+1 {
		t.Errorf("footer moved: %d", footerY)
	}

	// Scroll to the last row: rows hidden above.
	m.selectLine(len(m.lines) - 1)
	above, _ := m.hiddenRows(0, m.scrollOffset)
	out = strings.Split(ansi.Strip(m.render()), "\n")
	if want := fmt.Sprintf("↑ %d more", above); strings.TrimSpace(out[top-1]) != want {
		t.Errorf("above indicator = %q, want %q", out[top-1], want)
	}
	if len(out) != m.termHeight-1 {
		t.Errorf("render is %d rows, want %d", len(out), m.termHeight-1)
	}
}

func TestTitleFleetCounts(t *testing.T) {
	m := captureSidebar(t, 56, 30)
	title := m.renderTitle(56)
	plain := ansi.Strip(title)
	if !strings.HasPrefix(plain, "  agentctl hub") {
		t.Errorf("title = %q", plain)
	}
	for _, want := range []string{"⏸ 1", "● 2", "✓ 2", "○ 2"} {
		if !strings.Contains(plain, want) {
			t.Errorf("title missing %q: %q", want, plain)
		}
	}
	if !strings.Contains(title, fleetBlockedStyle.Bold(true).Reverse(true).Render(" ⏸ 1 ")) {
		t.Errorf("blocked count must be the yellow inverse cell: %q", title)
	}
	if strings.Index(plain, "⏸") > strings.Index(plain, "●") {
		t.Errorf("counts must be in glyph order ⏸ ● ✓ ○: %q", plain)
	}
	wide := ansi.Strip(m.renderTitle(120))
	if !strings.Contains(wide, "· 7 agents, 3 sessions") {
		t.Errorf("wide title missing totals: %q", wide)
	}
	if w := ansi.StringWidth(ansi.Strip(m.renderTitle(44))); w > 44 {
		t.Errorf("title overflows at 44: %d", w)
	}
	// Nothing blocked: no filled cell at all.
	for gi := range m.groups {
		for ri := range m.groups[gi].Rows {
			if m.groups[gi].Rows[ri].State == "blocked" {
				m.groups[gi].Rows[ri].State = "idle"
			}
		}
	}
	if out := ansi.Strip(m.renderTitle(56)); strings.Contains(out, "⏸") {
		t.Errorf("⏸ shown with nothing blocked: %q", out)
	}
	// The subtitle row is gone: the header is title, blank, indicator row.
	if hdr := ansi.Strip(m.renderHeader(56)); strings.Contains(hdr, "sessions ·") {
		t.Errorf("subtitle should be gone: %q", hdr)
	}
}

func TestSubheaderCarriesProjectBar(t *testing.T) {
	m := mouseTestModel(t, 120, 40)
	for i, l := range m.lines {
		if l.kind != fleetLineSubheader {
			continue
		}
		out := m.renderLine(i, m.contentWidth())
		if !strings.HasPrefix(out, fleetProjectBar(l.group.Project)) {
			t.Errorf("subheader %d lacks the project bar: %q", i, out)
		}
		if got := ansi.Strip(out); !strings.HasPrefix(got, "▎    dispatched") {
			t.Errorf("subheader alignment changed: %q", got)
		}
	}
}
