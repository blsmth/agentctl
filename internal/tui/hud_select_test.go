package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// stubClipboard replaces the clipboard seam and returns what it received.
func stubClipboard(t *testing.T) *[]string {
	t.Helper()
	var got []string
	prev := hudClipboard
	hudClipboard = func(text string) tea.Cmd {
		got = append(got, text)
		return nil
	}
	t.Cleanup(func() { hudClipboard = prev })
	return &got
}

func hudMouse(t *testing.T, m HudModel, msg tea.Msg) (HudModel, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(HudModel), cmd
}

func selPressAt(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}
func selDragTo(x, y int) tea.MouseMotionMsg {
	return tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft}
}
func selReleaseAt(x, y int) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// textStream fills the Stream tab with one text entry per line.
func textStream(m *HudModel, lines ...string) {
	for _, l := range lines {
		m.logEntries = append(m.logEntries, LogEntry{Kind: "text", FullText: l, Summary: l, Style: lipgloss.NewStyle()})
	}
	m.terminalMode = true
	m.rebuildStreamLineBuffer()
}

func TestExtractSelection(t *testing.T) {
	styled := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff0000"))
	lines := []string{
		" " + styled.Render("hello") + " world   ",
		" second line",
		" third",
	}
	tests := []struct {
		name           string
		lines          []string
		meta           []hudLineMeta
		l0, c0, l1, c1 int
		want           string
		wantN          int
	}{
		{"single line part", lines, nil, 0, 1, 0, 6, "hello", 1},
		{"single line, ANSI stripped, trailing spaces trimmed", lines, nil, 0, 0, 0, 99, " hello world", 1},
		{"multi-line", lines, nil, 0, 7, 2, 4, "world\n second line\n thi", 3},
		{
			"hard wrap rejoins without indent",
			[]string{" $ go test", "   abcdef", "   ghij", " next"},
			[]hudLineMeta{{}, {}, {join: joinHard, indent: 3}, {}},
			1, 0, 3, 99,
			"   abcdefghij\n next", 2,
		},
		{
			"word wrap rejoins with one space",
			[]string{" 09:00:00 INF hook: one two ", "              three"},
			[]hudLineMeta{{}, {join: joinSpace, indent: 14}},
			0, 0, 1, 99,
			" 09:00:00 INF hook: one two three", 1,
		},
		{
			"selection starting inside a continuation skips its indent",
			[]string{" aaaa", "   bbbb"},
			[]hudLineMeta{{}, {join: joinHard, indent: 3}},
			1, 0, 1, 99,
			"bbbb", 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n := extractSelection(tt.lines, tt.meta, tt.l0, tt.c0, tt.l1, tt.c1)
			if got != tt.want || n != tt.wantN {
				t.Errorf("got %q (%d lines), want %q (%d lines)", got, n, tt.want, tt.wantN)
			}
		})
	}
}

func TestSelectionRejoinsWrappedBashOutput(t *testing.T) {
	m := newTestHud()
	m.width = 30
	m.terminalMode = true
	long := strings.Repeat("abcdefghij", 6) // wraps at 30-2-2 = 26 cells
	m.logEntries = []LogEntry{bashEntry("echo hi", long+"\nshort", true, false)}
	m.rebuildStreamLineBuffer()
	if len(m.streamLineBuffer) < 4 {
		t.Fatalf("expected the output to wrap, got %d lines", len(m.streamLineBuffer))
	}
	got, n := extractSelection(m.streamLineBuffer, m.streamLineMeta, 0, 0, len(m.streamLineBuffer)-1, 1<<30)
	// The pane margin and the output indent are layout, not copied.
	if want := "$ echo hi\n" + long + "\nshort"; got != want || n != 3 {
		t.Errorf("got %q (%d lines), want %q", got, n, want)
	}
}

func TestSelectionRejoinsWrappedLogRecord(t *testing.T) {
	line := "2026/11/19 09:16:06.314 INFO hooks: " + strings.Repeat("word ", 30) + "end"
	recs, _ := appendLogRecs(nil, []string{line})
	lines, _, meta := renderLogRecsMeta(recs, 60, false)
	if len(lines) < 4 { // divider + wrapped record
		t.Fatalf("expected a wrapped record, got %d lines", len(lines))
	}
	got, n := extractSelection(lines, meta, 1, 0, len(lines)-1, 1<<30)
	want := "09:16:06 INF hooks: " + strings.Repeat("word ", 30) + "end"
	if got != want || n != 1 {
		t.Errorf("got %q (%d lines), want %q", got, n, want)
	}
}

func TestHighlightCellsKeepsText(t *testing.T) {
	initHudStyles()
	line := " " + lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")).Render("hello world")
	got := highlightCells(line, 3, 8, hudSelectionStyle)
	if ansi.Strip(got) != ansi.Strip(line) {
		t.Errorf("text changed: %q -> %q", ansi.Strip(line), ansi.Strip(got))
	}
	if got == line {
		t.Error("expected a highlight")
	}
	if highlightCells(line, 50, 60, hudSelectionStyle) != line {
		t.Error("highlight past the end should leave the line alone")
	}
}

func TestDragSelectCopiesOnRelease(t *testing.T) {
	got := stubClipboard(t)
	m := newTestHud()
	textStream(&m, "alpha one", "beta two", "gamma three")
	top := m.bodyTop()

	m, _ = hudMouse(t, m, selPressAt(1, top))
	if m.sel.state != selPending {
		t.Fatalf("press: state = %d, want pending", m.sel.state)
	}
	m, _ = hudMouse(t, m, selDragTo(4, top+1))
	if m.sel.state != selDragging {
		t.Fatalf("motion: state = %d, want dragging", m.sel.state)
	}
	if !strings.Contains(m.View().Content, "\x1b[") {
		t.Fatal("view should render")
	}
	m, _ = hudMouse(t, m, selReleaseAt(4, top+1))
	if m.sel.state != selShown {
		t.Fatalf("release: state = %d, want shown", m.sel.state)
	}
	if len(*got) != 1 || (*got)[0] != "alpha one\nbeta" {
		t.Fatalf("clipboard got %q, want [\"alpha one\\nbeta\"]", *got)
	}
	if !strings.Contains(ansi.Strip(m.renderFooter(80)), "copied 2 lines") {
		t.Errorf("footer should flash the copy: %q", ansi.Strip(m.renderFooter(80)))
	}
	// The flash expires after a few ticks.
	for range hudFlashTicks {
		m, _ = hudMouse(t, m, hudTickMsg{})
	}
	if m.flash != "" {
		t.Errorf("flash should expire, still %q", m.flash)
	}
}

func TestClickWithoutMotionFallsThrough(t *testing.T) {
	got := stubClipboard(t)
	m := newTestHud()
	textStream(&m, "alpha", "beta")

	t.Run("body click selects nothing", func(t *testing.T) {
		m, _ := hudMouse(t, m, selPressAt(2, m.bodyTop()))
		m, _ = hudMouse(t, m, selDragTo(2, m.bodyTop())) // same cell: still a click
		m, _ = hudMouse(t, m, selReleaseAt(2, m.bodyTop()))
		if m.sel.state != selNone || len(*got) != 0 {
			t.Errorf("state %d, copies %q: a click must not select or copy", m.sel.state, *got)
		}
	})
	t.Run("tab click still switches tabs", func(t *testing.T) {
		m, _ := hudMouse(t, m, selPressAt(m.hudTabSpans(80)[1].x0, m.tabBarRow(80)))
		if m.activeTab != tabAgentLogs {
			t.Errorf("activeTab = %d, want %d", m.activeTab, tabAgentLogs)
		}
		if m.sel.state != selNone {
			t.Errorf("a tab click must not arm a selection")
		}
	})
	t.Run("click elsewhere clears a selection", func(t *testing.T) {
		m, _ := hudMouse(t, m, selPressAt(1, m.bodyTop()))
		m, _ = hudMouse(t, m, selDragTo(4, m.bodyTop()+1))
		m, _ = hudMouse(t, m, selReleaseAt(4, m.bodyTop()+1))
		if m.sel.state != selShown {
			t.Fatal("expected a selection")
		}
		m, _ = hudMouse(t, m, selPressAt(0, 0))
		if m.sel.state != selNone {
			t.Errorf("a click in the header should clear the selection")
		}
	})
}

func TestEscClearsSelectionWithoutQuitting(t *testing.T) {
	stubClipboard(t)
	m := newTestHud()
	textStream(&m, "alpha", "beta")
	m, _ = hudMouse(t, m, selPressAt(1, m.bodyTop()))
	m, _ = hudMouse(t, m, selDragTo(3, m.bodyTop()))
	m, _ = hudMouse(t, m, selReleaseAt(3, m.bodyTop()))
	m, cmd := hudMouse(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("Esc with a selection must not quit")
		}
	}
	if m.sel.state != selNone {
		t.Error("Esc should clear the selection")
	}
}

func TestDragAutoScrollsAtEdges(t *testing.T) {
	stubClipboard(t)
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}

	t.Run("past the top", func(t *testing.T) {
		m := newTestHud()
		textStream(&m, lines...)
		m.scrollTop = 30
		m.streamAtBottom = false
		m, _ = hudMouse(t, m, selPressAt(1, m.bodyTop()+2))
		m, cmd := hudMouse(t, m, selDragTo(1, 0)) // into the header
		if m.scrollTop != 29 {
			t.Errorf("scrollTop = %d, want 29", m.scrollTop)
		}
		if cmd == nil || !m.selTicking {
			t.Fatal("expected an auto-scroll tick")
		}
		m, cmd = hudMouse(t, m, hudSelScrollMsg{})
		if m.scrollTop != 28 || cmd == nil {
			t.Errorf("tick: scrollTop = %d (cmd %v), want 28 and another tick", m.scrollTop, cmd != nil)
		}
		if l, _, _, _ := m.selRange(); l != 28 {
			t.Errorf("selection start line = %d, want 28 (the top body row)", l)
		}
		// Back inside the body: auto-scroll stops.
		m, _ = hudMouse(t, m, selDragTo(1, m.bodyTop()+1))
		m, cmd = hudMouse(t, m, hudSelScrollMsg{})
		if cmd != nil || m.selTicking || m.scrollTop != 28 {
			t.Errorf("auto-scroll should stop inside the body (scrollTop %d)", m.scrollTop)
		}
	})

	t.Run("past the bottom", func(t *testing.T) {
		m := newTestHud()
		textStream(&m, lines...)
		m.scrollTop = 0
		m.streamAtBottom = false
		m, _ = hudMouse(t, m, selPressAt(1, m.bodyTop()))
		m, _ = hudMouse(t, m, selDragTo(3, m.height-1)) // the footer
		if m.scrollTop != 1 {
			t.Errorf("scrollTop = %d, want 1", m.scrollTop)
		}
		_, _, l1, _ := m.selRange()
		if want := 1 + m.visibleLines() - 1; l1 != want {
			t.Errorf("selection end line = %d, want %d (the bottom body row)", l1, want)
		}
		m, _ = hudMouse(t, m, selReleaseAt(3, m.height-1))
		if m.sel.state != selShown || m.sel.edge != 0 {
			t.Error("release past the edge should finish the selection")
		}
	})
}

func TestSelectionAnchoredToBuffer(t *testing.T) {
	stubClipboard(t)

	t.Run("new stream entries do not move it", func(t *testing.T) {
		m := newTestHud()
		textStream(&m, "alpha", "beta", "gamma")
		m, _ = hudMouse(t, m, selPressAt(1, m.bodyTop()))
		m, _ = hudMouse(t, m, selDragTo(4, m.bodyTop()+1))
		before, _ := m.selectionText()
		m.logEntries = append(m.logEntries, LogEntry{Kind: "text", FullText: "delta", Style: lipgloss.NewStyle()})
		m.rebuildStreamLineBuffer()
		if after, _ := m.selectionText(); after != before || m.sel.state != selDragging {
			t.Errorf("selection moved: %q -> %q", before, after)
		}
	})

	t.Run("a trim shifts it with the buffer", func(t *testing.T) {
		m := newTestHud()
		textStream(&m, "alpha", "beta", "gamma")
		m, _ = hudMouse(t, m, selPressAt(1, m.bodyTop()+1))
		m, _ = hudMouse(t, m, selDragTo(4, m.bodyTop()+2))
		before, _ := m.selectionText()
		m.logEntries = m.logEntries[1:]
		m.shiftSelection(tabStream, 1)
		m.rebuildStreamLineBuffer()
		if after, _ := m.selectionText(); after != before {
			t.Errorf("selection moved after trim: %q -> %q", before, after)
		}
		m.logEntries = m.logEntries[2:]
		m.shiftSelection(tabStream, 2)
		if m.sel.state != selNone {
			t.Error("a selection wholly trimmed away should clear")
		}
	})

	t.Run("new log lines do not move it", func(t *testing.T) {
		m := newTestHud()
		m.activeTab = tabSysLogs
		m.appendLog(m.sysPane(), []string{"first line", "second line"})
		m, _ = hudMouse(t, m, selPressAt(1, m.bodyTop()))
		m, _ = hudMouse(t, m, selDragTo(6, m.bodyTop()+1))
		before, _ := m.selectionText()
		if before != "first line\nsecond" {
			t.Fatalf("selection = %q", before)
		}
		m.appendLog(m.sysPane(), []string{"third line"})
		if after, _ := m.selectionText(); after != before {
			t.Errorf("selection moved: %q -> %q", before, after)
		}
	})
}

func TestCopyKey(t *testing.T) {
	t.Run("selected stream entry in full", func(t *testing.T) {
		got := stubClipboard(t)
		m := newTestHud()
		m.logEntries = []LogEntry{bashEntry("go test ./...", "ok  pkg\nFAIL other  \n", true, false)}
		m.rebuildStreamLineBuffer()
		m = sendKey(m, "j")
		m = sendKey(m, "y")
		if len(*got) != 1 || (*got)[0] != "$ go test ./...\nok  pkg\nFAIL other" {
			t.Errorf("clipboard got %q", *got)
		}
		if !strings.Contains(m.flash, "copied 3 lines") {
			t.Errorf("flash = %q", m.flash)
		}
	})
	t.Run("visible log tab content", func(t *testing.T) {
		got := stubClipboard(t)
		m := newTestHud()
		m.activeTab = tabSysLogs
		m.appendLog(m.sysPane(), []string{"plain one", "plain two"})
		m = sendKey(m, "y")
		if len(*got) != 1 || (*got)[0] != "plain one\nplain two" {
			t.Errorf("clipboard got %q", *got)
		}
	})
	t.Run("an active selection wins", func(t *testing.T) {
		got := stubClipboard(t)
		m := newTestHud()
		textStream(&m, "alpha", "beta")
		m, _ = hudMouse(t, m, selPressAt(1, m.bodyTop()))
		m, _ = hudMouse(t, m, selDragTo(3, m.bodyTop()))
		m, _ = hudMouse(t, m, selReleaseAt(3, m.bodyTop()))
		m = sendKey(m, "y")
		if len(*got) != 2 || (*got)[1] != "alp" {
			t.Errorf("clipboard got %q", *got)
		}
	})
	t.Run("nothing to copy", func(t *testing.T) {
		got := stubClipboard(t)
		m := newTestHud()
		m = sendKey(m, "y")
		if len(*got) != 0 || m.flash != "nothing to copy" {
			t.Errorf("clipboard got %q, flash %q", *got, m.flash)
		}
	})
}

func TestEntryCopyText(t *testing.T) {
	tests := []struct {
		name string
		e    LogEntry
		want string
	}{
		{"bash with output", bashEntry("ls", "a\nb\n", true, false), "$ ls\na\nb"},
		{"bash running", bashEntry("sleep 1", "", false, false), "$ sleep 1"},
		{"text", LogEntry{Kind: "text", FullText: "full answer\nsecond  "}, "full answer\nsecond"},
		{
			"other tool",
			LogEntry{Kind: "tool_call", ToolName: "Grep", FullInput: map[string]any{"pattern": "foo", "path": "x"}},
			"Grep\npattern: foo\npath: x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := entryCopyText(tt.e); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCopyToClipboardOutsideTmuxUsesOSC52(t *testing.T) {
	t.Setenv("TMUX", "")
	cmd := copyToClipboard("hello")
	if cmd == nil {
		t.Fatal("expected a command")
	}
	if got, want := cmd(), tea.SetClipboard("hello")(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want the tea.SetClipboard message %#v", got, want)
	}
}

func TestHudFootersFit80(t *testing.T) {
	stubClipboard(t)
	m := newTestHud()
	textStream(&m, "alpha", "beta")
	states := map[string]HudModel{}
	states["stream"] = m
	cur := sendKey(m, "j")
	states["stream cursor"] = cur
	logs := m
	logs.activeTab = tabSysLogs
	states["logs"] = logs
	sel, _ := hudMouse(t, m, selPressAt(1, m.bodyTop()))
	sel, _ = hudMouse(t, sel, selDragTo(3, m.bodyTop()))
	sel, _ = hudMouse(t, sel, selReleaseAt(3, m.bodyTop()))
	states["selection"] = sel
	for name, s := range states {
		footer := ansi.Strip(s.renderFooter(80))
		if strings.Contains(footer, "…") {
			t.Errorf("%s: footer hint truncated at 80 cols: %q", name, footer)
		}
		if name != "selection" && !strings.Contains(footer, "y copy") {
			t.Errorf("%s: footer should mention y copy: %q", name, footer)
		}
		if w := lipgloss.Width(footer); w != 80 {
			t.Errorf("%s: footer width %d, want 80", name, w)
		}
	}
	mini := m
	mini.height = 4
	if l := strings.Split(ansi.Strip(mini.renderMiniFooter(80)), "\n")[1]; strings.Contains(l, "…") || lipgloss.Width(l) > 80 || !strings.Contains(l, "y copy") {
		t.Errorf("mini footer hint: %q", l)
	}
}

func TestLostReleaseStopsAutoScroll(t *testing.T) {
	stubClipboard(t)
	var lines []string
	for i := range 40 {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}

	t.Run("tick chain ends at the top", func(t *testing.T) {
		m := newTestHud()
		textStream(&m, lines...)
		m.scrollTop, m.streamAtBottom = 2, false
		m, _ = hudMouse(t, m, selPressAt(1, m.bodyTop()+3))
		m, cmd := hudMouse(t, m, selDragTo(1, 0)) // out past the header, never released
		ticks := 0
		for cmd != nil && ticks < 100 {
			m, cmd = hudMouse(t, m, hudSelScrollMsg{})
			ticks++
		}
		if cmd != nil || m.selTicking {
			t.Fatalf("auto-scroll still ticking after %d ticks", ticks)
		}
		if m.scrollTop != 0 || ticks > 3 {
			t.Errorf("scrollTop = %d after %d ticks, want 0 within 3", m.scrollTop, ticks)
		}
		// A new motion past the edge re-arms it once there is room again.
		m.scrollTop = 1
		if _, cmd := hudMouse(t, m, selDragTo(1, 0)); cmd == nil {
			t.Error("motion past the edge should re-arm the auto-scroll")
		}
	})

	t.Run("a scroll key ends the drag", func(t *testing.T) {
		m := newTestHud()
		textStream(&m, lines...)
		m.scrollTop, m.streamAtBottom = 10, false
		m, _ = hudMouse(t, m, selPressAt(1, m.bodyTop()+3))
		m, _ = hudMouse(t, m, selDragTo(1, 0))
		m = sendKey(m, "k")
		if m.sel.state != selShown || m.sel.edge != 0 {
			t.Fatalf("state %d edge %d: a key should end a drag whose release was lost", m.sel.state, m.sel.edge)
		}
		if _, cmd := hudMouse(t, m, hudSelScrollMsg{}); cmd != nil {
			t.Error("the tick chain should stop once the drag ended")
		}
	})
}

func TestOSC52CopyIsCapped(t *testing.T) {
	got := stubClipboard(t)
	prev := hudClipboardLimit
	hudClipboardLimit = func() int { return 1 << 10 }
	t.Cleanup(func() { hudClipboardLimit = prev })

	m := newTestHud()
	out := strings.Repeat(strings.Repeat("x", 99)+"\n", 30) // 3000 bytes
	m.logEntries = []LogEntry{bashEntry("cat big", out, true, false)}
	m.rebuildStreamLineBuffer()
	m = sendKey(m, "j")
	m = sendKey(m, "y")
	if len(*got) != 1 || len((*got)[0]) > 1<<10 || !strings.HasPrefix((*got)[0], "$ cat big\nxxx") {
		t.Fatalf("clipboard got %d copies, first %d bytes", len(*got), len((*got)[0]))
	}
	if strings.HasSuffix((*got)[0], "x\n") || !strings.HasSuffix((*got)[0], "x") {
		t.Error("the cut should fall on a line break")
	}
	if !strings.Contains(m.flash, "cut to 1KB") {
		t.Errorf("flash should say the copy was cut: %q", m.flash)
	}

	hudClipboardLimit = func() int { return 0 } // inside tmux: no cap
	m = sendKey(m, "y")
	if len((*got)[1]) < len(out) {
		t.Errorf("tmux copy should be whole, got %d bytes", len((*got)[1]))
	}
}

func TestCapText(t *testing.T) {
	tests := []struct {
		name, in string
		limit    int
		want     string
	}{
		{"under the cap", "abc", 10, "abc"},
		{"at a line break", "ab\ncd\nef", 6, "ab\ncd"},
		{"no line break, rune boundary", "aé", 2, "a"},
		{"no limit", "abc", 0, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := capText(tt.in, tt.limit); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClearingSelectionResumesFollow(t *testing.T) {
	stubClipboard(t)
	var lines []string
	for i := range 40 {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}
	selectThenAppend := func(t *testing.T, scrollKey string) HudModel {
		t.Helper()
		m := newTestHud()
		textStream(&m, lines...)
		m.scrollToBottom()
		m.streamAtBottom = true
		m, _ = hudMouse(t, m, selPressAt(1, m.bodyTop()))
		m, _ = hudMouse(t, m, selDragTo(4, m.bodyTop()+1))
		m, _ = hudMouse(t, m, selReleaseAt(4, m.bodyTop()+1))
		if m.streamAtBottom {
			t.Fatal("a drag should stop following while the selection is up")
		}
		if scrollKey != "" {
			m = sendKey(m, scrollKey)
		}
		m.logEntries = append(m.logEntries, LogEntry{Kind: "text", FullText: "new", Style: lipgloss.NewStyle()})
		m.rebuildStreamLineBuffer()
		m, _ = hudMouse(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
		return m
	}

	t.Run("esc resumes following", func(t *testing.T) {
		m := selectThenAppend(t, "")
		if !m.streamAtBottom || !m.isStreamAtBottom() {
			t.Errorf("following should resume: pinned %v, scrollTop %d", m.streamAtBottom, m.scrollTop)
		}
	})
	t.Run("a hand scroll keeps the viewport", func(t *testing.T) {
		m := selectThenAppend(t, "k")
		if m.streamAtBottom {
			t.Error("after scrolling by hand, clearing should not jump back to the bottom")
		}
	})
}

func TestReselectPressDoesNotScroll(t *testing.T) {
	stubClipboard(t)
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}
	m := newTestHud()
	textStream(&m, lines...)
	m.scrollToBottom()
	m.streamAtBottom = true
	row := m.bodyTop() + 2
	m, _ = hudMouse(t, m, selPressAt(1, row))
	m, _ = hudMouse(t, m, selDragTo(4, row+1))
	m, _ = hudMouse(t, m, selReleaseAt(4, row+1))
	for i := range 10 {
		m.logEntries = append(m.logEntries, LogEntry{Kind: "text", FullText: fmt.Sprintf("new %d", i), Style: lipgloss.NewStyle()})
	}
	m.rebuildStreamLineBuffer()
	scroll := m.scrollTop
	want, _ := m.screenPoint(1, row)

	m, _ = hudMouse(t, m, selPressAt(1, row))
	if m.scrollTop != scroll {
		t.Errorf("press scrolled: scrollTop %d -> %d", scroll, m.scrollTop)
	}
	if m.sel.state != selPending || m.sel.anchor != want {
		t.Errorf("anchor = %+v (state %d), want %+v", m.sel.anchor, m.sel.state, want)
	}
	if !m.sel.repin {
		t.Error("the pending re-pin should carry over to the new selection")
	}

	t.Run("a second drag keeps the re-pin", func(t *testing.T) {
		m, _ := hudMouse(t, m, selDragTo(4, row+1))
		m, _ = hudMouse(t, m, selReleaseAt(4, row+1))
		if got, _ := m.selectionText(); got != "line 43\nline" {
			t.Errorf("selection = %q", got)
		}
		m, _ = hudMouse(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
		if !m.streamAtBottom || !m.isStreamAtBottom() {
			t.Error("clearing the second selection should resume following")
		}
	})
	t.Run("a plain click re-pins", func(t *testing.T) {
		m, _ := hudMouse(t, m, selReleaseAt(1, row))
		if m.sel.state != selNone || !m.streamAtBottom || !m.isStreamAtBottom() {
			t.Errorf("a body click with no drag should clear and resume following (state %d, scrollTop %d)", m.sel.state, m.scrollTop)
		}
	})
	t.Run("a header click re-pins", func(t *testing.T) {
		m, _ := hudMouse(t, m, selReleaseAt(1, row))
		m2 := m
		m2.sel = hudSelection{state: selShown, tab: tabStream, repin: true}
		m2.streamAtBottom = false
		m2.scrollTop = 0
		m2, _ = hudMouse(t, m2, selPressAt(0, 0))
		if !m2.streamAtBottom || !m2.isStreamAtBottom() {
			t.Error("a header click should clear and resume following")
		}
	})
}
