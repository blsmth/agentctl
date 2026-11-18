package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/herd"
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
		if l.kind == herdLineRow && i != sel {
			other = i
			break
		}
	}
	tests := []struct {
		name     string
		line     int
		hover    herdHoverRegion
		wantShow bool
	}{
		{"selected", sel, herdHoverNone, true},
		{"plain row", other, herdHoverNone, false},
		{"hovered body", other, herdHoverBody, true},
		{"hovered close", other, herdHoverClose, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := m.lines[tt.line]
			out := m.renderTreeRowHover(l.row, l.treePrefix, l.nameCol, tt.line == sel, tt.hover, cw)
			first := firstLine(out)
			if got := strings.Contains(first, herdCloseLabel); got != tt.wantShow {
				t.Errorf("✕ shown = %v, want %v: %q", got, tt.wantShow, first)
			}
			// The slot is always reserved: the row is as wide as ever.
			if w := lipgloss.Width(first); w < cw-herdSidebarRightGutter {
				t.Errorf("row width %d, want at least %d", w, cw-herdSidebarRightGutter)
			}
			if tt.hover == herdHoverClose && !strings.Contains(out, herdCloseHoverStyle.Render(herdCloseLabel)) {
				t.Errorf("close hover must render the ✕ red")
			}
		})
	}
}

func TestRowRightColumnsAlign(t *testing.T) {
	InitStyles()
	fixedHerdNow(t)
	ago := func(d time.Duration) time.Time { return sidebarNow.Add(-d) }
	rows := []struct {
		name string
		pr   int
		age  time.Duration
	}{
		{"no pr", 0, 4 * time.Minute},
		{"two digit", 60, 25 * time.Minute},
		{"four digit", 1025, 3 * time.Hour},
		{"pr no age", 7, 0},
		{"short age", 0, 40 * time.Second},
	}
	m := newHerdModel()
	m.termWidth, m.liveLeft = 56, 56
	cw := m.contentWidth()
	prEnd, ageEnd := -1, -1
	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			r := testRow("worker", "s", "idle", "Reading a fairly long file name.go")
			r.PRNumber = tt.pr
			if tt.age > 0 {
				r.StateSince = ago(tt.age)
			}
			line := []rune(firstLine(m.renderTreeRowHover(r, "", 8, false, herdHoverNone, cw)))
			// Right edge: gutter, ✕ slot, space, then the age slot.
			ageCol := cw - herdSidebarRightGutter - 1 - 1 - 1
			prCol := ageCol - herdAgeSlotW - 1
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

func TestFitRightSlotsDegrades(t *testing.T) {
	full := herdRightFull.width() + 1 + herdMinRecapWidth
	pr := herdRightPR.width() + 1 + herdMinRecapWidth
	tests := []struct {
		available int
		want      herdRightSlots
	}{
		{full + 10, herdRightFull},
		{full, herdRightFull},
		{full - 1, herdRightPR},
		{pr, herdRightPR},
		{pr - 1, herdRightNone},
		{0, herdRightNone},
	}
	for _, tt := range tests {
		if got := fitRightSlots(tt.available); got != tt.want {
			t.Errorf("fitRightSlots(%d) = %v, want %v", tt.available, got, tt.want)
		}
	}
}

func TestNameStyleByState(t *testing.T) {
	InitStyles()
	tests := []struct {
		name  string
		state string
		done  bool
		want  herdWeight
		style lipgloss.Style
		bold  bool
	}{
		{"working", "working", false, herdWeightWorking, herdNameWorkingStyle, true},
		{"done", "idle", true, herdWeightDone, herdNameDoneStyle, false},
		{"idle", "idle", false, herdWeightIdle, herdNameIdleStyle, false},
		{"blocked", "blocked", false, herdWeightBlocked, herdBlockedStyle.Bold(true), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testRow("worker", "s", tt.state, "")
			r.Done = tt.done
			if got := rowWeight(r); got != tt.want {
				t.Fatalf("rowWeight = %v, want %v", got, tt.want)
			}
			st := herdNameStyle(tt.want)
			if st.GetBold() != tt.bold {
				t.Errorf("bold = %v, want %v", st.GetBold(), tt.bold)
			}
			if got, want := renderAgentName("worker", tt.want), tt.style.Render("worker"); got != want {
				t.Errorf("name = %q, want %q", got, want)
			}
			// Names never wear a project palette colour.
			for _, c := range herdPalette() {
				if st.GetForeground() == lipgloss.Color(c) && tt.want != herdWeightBlocked {
					t.Errorf("name uses palette colour %s", c)
				}
			}
		})
	}
	// Recap weight follows: working text, done subtle, idle muted.
	if recapStyle(herdWeightWorking, herd.RecapSummary).GetForeground() != herdRecapWorkingStyle.GetForeground() {
		t.Error("working recap should be normal text")
	}
	if recapStyle(herdWeightDone, herd.RecapSummary).GetForeground() != herdRecapStyle.GetForeground() {
		t.Error("done recap should be subtle")
	}
	if recapStyle(herdWeightIdle, herd.RecapSummary).GetForeground() != herdRecapIdleStyle.GetForeground() {
		t.Error("idle recap should be muted")
	}
}

func TestCardTitleUsesNameWeight(t *testing.T) {
	InitStyles()
	cs := newCardStyles()
	r := testRow("worker:1", "s", "idle", "")
	r.Done = true
	title := cardContent(r, nil, "", time.Unix(0, 0), 50, cs)[0]
	want := herdNameStyle(herdWeightDone).Background(cs.bg.GetBackground()).Render("worker:1")
	if !strings.HasPrefix(title, want) {
		t.Errorf("card title should use the done name style: %q", title)
	}
}

func TestGroupHeaderCountsAlign(t *testing.T) {
	InitStyles()
	mk := func(st string, n int) []herd.Row {
		var out []herd.Row
		for range n {
			out = append(out, testRow("a", "s", st, ""))
		}
		return out
	}
	groups := []herd.Group{
		{Session: "agentctl-a", Project: "a", Rows: mk("working", 1), ProjectDir: "/a", RuntimeDir: "/a/.agentctl"},
		{Session: "agentctl-much-longer-name", Project: "much-longer-name", Rows: append(mk("blocked", 1), mk("idle", 3)...)},
		{Session: "agentctl-mid", Project: "mid", Rows: mk("idle", 12), ProjectDir: "/mid", RuntimeDir: "/mid/.agentctl"},
	}
	for _, cw := range []int{44, 56, 80} {
		m := HerdModel{groups: groups}
		cols := map[string]int{}
		for _, g := range groups {
			line := ansi.Strip(m.renderGroupHeader(g, false, cw))
			rs := []rune(line)
			if len(rs) != cw-herdSidebarRightGutter {
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
	g := herd.Group{
		Session: "agentctl-energy-solutions-ops", Project: "energy-solutions.ops",
		ProjectDir: "/src/energy-solutions.ops", RuntimeDir: "/src/energy-solutions.ops/.agentctl",
		Rows: []herd.Row{testRow("a", "s", "idle", ""), testRow("b", "s", "working", "")},
	}
	if groupCustomSession(g) {
		t.Fatal("sanitized default session reported as custom")
	}
	m := HerdModel{groups: []herd.Group{g}}
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
	m.groups = []herd.Group{g}
	if out := ansi.Strip(m.renderGroupHeader(g, false, 80)); !strings.Contains(out, "my-very-own") {
		t.Errorf("custom session hidden at 80 cols: %q", out)
	}
	if out := ansi.Strip(m.renderGroupHeader(g, false, 44)); !strings.Contains(out, "● 1") || !strings.Contains(out, "○ 1") || strings.Contains(out, "my-very") {
		t.Errorf("custom session must not truncate the counts: %q", out)
	}
}

func TestRowDetailActivityIsOneLine(t *testing.T) {
	long := strings.Repeat("arg ", 40)
	tests := []struct {
		name       string
		tool, text string
		wantMarker string
		wantPrefix string
	}{
		{"bash command", "Bash: go test " + long, "", herdCommandMarker, "go test arg"},
		{"other tool", "Editing " + long, "", herdActivityMarker, "Editing arg"},
		{"last text", "", "I looked at " + long, herdActivityMarker, "I looked at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testRow("w", "s", "working", tt.tool)
			r.LastText = tt.text
			r.Recap = herd.Recap(r, herd.LogInfo{})
			if r.Recap.Source != herd.RecapActivity {
				t.Fatalf("fixture: source = %v", r.Recap.Source)
			}
			d := rowDetail(r, 0, 56)
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
	r.Recap = herd.RecapLine{Text: long, Source: herd.RecapSummary}
	if n := len(rowDetail(r, 0, 56)); n != herdDetailMaxLines {
		t.Errorf("summary expansion = %d lines, want %d", n, herdDetailMaxLines)
	}
}

func TestEmptyRowFallback(t *testing.T) {
	now := sidebarNow
	tests := []struct {
		name string
		row  herd.Row
		want string
	}{
		{"no turns, started", herd.Row{AgentState: state.AgentState{StartTime: now.Add(-4 * time.Minute).Unix()}}, "waiting for first prompt · 4m"},
		{"no turns, unknown start", herd.Row{}, "waiting for first prompt"},
		{"no turns, state since", herd.Row{StateSince: now.Add(-90 * time.Second)}, "waiting for first prompt · 1m"},
		{"turns", herd.Row{AgentState: state.AgentState{TurnCount: 3, StartTime: now.Add(-50 * time.Hour).Unix()}}, "no activity yet · started 2d ago"},
		{"turns, unknown start", herd.Row{AgentState: state.AgentState{TurnCount: 3}}, "no activity yet"},
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
	fixedHerdNow(t)
	r := testRow("w", "s", "idle", "")
	r.StartTime = sidebarNow.Add(-4 * time.Minute).Unix()
	rc := rowRecap(r)
	if rc.Source != herd.RecapNone || rc.Text != "waiting for first prompt · 4m" {
		t.Errorf("rowRecap = %+v", rc)
	}
	if herd.Recap(r, herd.LogInfo{}).Text != "" {
		t.Error("herd.Recap must stay pure")
	}
	m := newHerdModel()
	out := m.renderTreeRowHover(r, "", 1, false, herdHoverNone, 80)
	if !strings.Contains(out, herdEmptyStyle.Render("waiting for first prompt · 4m")) {
		t.Errorf("fallback not rendered muted italic: %q", ansi.Strip(out))
	}
	if d := rowDetail(r, 0, 80); len(d) != 0 {
		t.Errorf("fallback must not expand: %+v", d)
	}
}

func TestHerdFooterSingleLine(t *testing.T) {
	InitStyles()
	tests := []struct {
		cw       int
		want     []string
		dropped  []string
		lastHint string
	}{
		{67, []string{"j/k move", "⏎ focus", "b blocked", "n new", "x close", "q leave", "? more"}, nil, "? more"},
		{44, []string{"j/k move", "⏎ focus", "? more"}, []string{"q leave"}, "? more"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("cw=%d", tt.cw), func(t *testing.T) {
			m := newHerdModel()
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
	m = got.(HerdModel)
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
	lines, _ := herdHelpLayout(herdHelpItems(), m.contentWidth())
	if got := m.liveHintRows(); got != len(lines)-1 {
		t.Errorf("liveHintRows = %d, want %d", got, len(lines)-1)
	}
	if got := strings.Count(m.render(), "\n") + 1; got != m.termHeight-1 {
		t.Errorf("render is %d rows, want %d", got, m.termHeight-1)
	}

	// Any key closes it; a listed binding then runs.
	got, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m2 := got.(HerdModel)
	if m2.menuOpen || m2.splitPane.Cursor() != m.splitPane.Cursor()+1 {
		t.Errorf("j should close the reference and move: open=%v cursor=%d", m2.menuOpen, m2.splitPane.Cursor())
	}
	got, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m3 := got.(HerdModel); m3.menuOpen || m3.splitPane.Cursor() != m.splitPane.Cursor() {
		t.Error("esc should only close the reference")
	}

	// Clicking an entry runs it.
	_, spans := herdHelpLayout(herdHelpItems(), m.contentWidth())
	got, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: spans[0].start, Y: m.footerMessageY() + spans[0].line})
	if m4 := got.(HerdModel); m4.menuOpen || m4.splitPane.Cursor() != m.splitPane.Cursor()+1 {
		t.Errorf("clicking j/k move should move: open=%v cursor=%d", m4.menuOpen, m4.splitPane.Cursor())
	}
}

func TestInfoMessageExpires(t *testing.T) {
	InitStyles()
	now := sidebarNow
	old := herdNow
	herdNow = func() time.Time { return now }
	t.Cleanup(func() { herdNow = old })

	m := newHerdModel()
	m.termWidth, m.liveLeft = 80, 80
	m.groups = []herd.Group{{Session: "agentctl-energy-solutions-ops", Project: "energy-solutions.ops"}}
	got, _ := m.Update(herdLaunchedMsg{session: "agentctl-energy-solutions-ops"})
	m = got.(HerdModel)
	if m.infoMsg != "launched new agent in energy-solutions.ops" || !m.infoOK {
		t.Fatalf("info = %q ok=%v", m.infoMsg, m.infoOK)
	}
	footer := m.renderFooter(m.contentWidth())
	if !strings.Contains(footer, herdSuccessStyle.Render("  "+herdInfoGlyph+m.infoMsg)) {
		t.Errorf("success notice not in success style with glyph: %q", ansi.Strip(footer))
	}

	steps := []struct {
		after time.Duration
		want  bool
	}{{time.Second, true}, {3 * time.Second, true}, {herdInfoTTL, false}}
	for _, s := range steps {
		now = sidebarNow.Add(s.after)
		got, _ = m.Update(herdTickMsg{})
		if has := got.(HerdModel).infoMsg != ""; has != s.want {
			t.Errorf("after %v: shown = %v, want %v", s.after, has, s.want)
		}
	}

	// Progress notices do not expire; errors stay until the next key.
	m.setProgress("launching agent in x…")
	now = sidebarNow.Add(time.Hour)
	got, _ = m.Update(herdTickMsg{})
	if got.(HerdModel).infoMsg == "" {
		t.Error("progress notice expired")
	}
	m.jumpErr = "boom"
	got, _ = m.Update(herdTickMsg{})
	if got.(HerdModel).jumpErr != "boom" {
		t.Error("error cleared without a key")
	}
	got, _ = got.(HerdModel).Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if got.(HerdModel).jumpErr != "" {
		t.Error("error should clear on the next key")
	}
}

func TestOverflowIndicators(t *testing.T) {
	m := captureSidebar(t, 56, 30)
	top := m.herdListTop()
	if top != 3 {
		t.Fatalf("list top = %d, want 3", top)
	}
	// With slack: no indicators.
	out := strings.Split(ansi.Strip(m.render()), "\n")
	if strings.TrimSpace(out[top-1]) != "" || strings.Contains(strings.Join(out, "\n"), "more") && !strings.Contains(out[len(out)-1], "more") {
		t.Errorf("unexpected indicator with slack:\n%s", strings.Join(out, "\n"))
	}

	// Short: rows hidden below.
	m.termHeight = herdChromeRows + 6
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
	if !strings.Contains(title, herdBlockedStyle.Bold(true).Reverse(true).Render(" ⏸ 1 ")) {
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
		if l.kind != herdLineSubheader {
			continue
		}
		out := m.renderLine(i, m.contentWidth())
		if !strings.HasPrefix(out, herdProjectBar(l.group.Project)) {
			t.Errorf("subheader %d lacks the project bar: %q", i, out)
		}
		if got := ansi.Strip(out); !strings.HasPrefix(got, "▎    dispatched") {
			t.Errorf("subheader alignment changed: %q", got)
		}
	}
}
