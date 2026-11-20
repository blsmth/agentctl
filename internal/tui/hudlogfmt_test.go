package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestParseRuntimeLine(t *testing.T) {
	home, _ := os.UserHomeDir()
	tests := []struct {
		name                       string
		line                       string
		parsed                     bool
		clock, level, source, comp string
		body                       string
	}{
		{"valid", "2026/11/19 09:16:06.314 INFO <hooks/session.go:170> hook-session-start: linked session_id=abc window=@1",
			true, "09:16:06", "INF", "hooks/session.go:170", "hook-session-start", "linked session_id=abc window=@1"},
		{"warn", "2026/11/19 09:16:06.314 WARN <a.go:1> signal: x", true, "09:16:06", "WRN", "a.go:1", "signal", "x"},
		{"error", "2026/11/19 09:16:06.314 ERROR <a.go:1> signal: x", true, "09:16:06", "ERR", "a.go:1", "signal", "x"},
		{"debug", "2026/11/19 09:16:06 DEBUG <a.go:1> chrome-bar: x", true, "09:16:06", "DBG", "a.go:1", "chrome-bar", "x"},
		{"error word at INFO", "2026/11/19 09:16:06.314 INFO <a.go:1> signal: no error found", true, "09:16:06", "INF", "a.go:1", "signal", "no error found"},
		{"no component", "2026/11/19 09:16:06.314 INFO <a.go:1> just a message here", true, "09:16:06", "INF", "a.go:1", "", "just a message here"},
		{"no source", "2026/11/19 09:16:06.314 INFO monitor: tick", true, "09:16:06", "INF", "", "monitor", "tick"},
		{"unparseable", "goroutine 1 [running]:", false, "", "", "", "", ""},
		{"empty", "", false, "", "", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := parseRuntimeLine(tt.line)
			if r.parsed != tt.parsed {
				t.Fatalf("parsed = %v, want %v", r.parsed, tt.parsed)
			}
			if !tt.parsed {
				if r.raw != tt.line {
					t.Errorf("raw = %q", r.raw)
				}
				return
			}
			if r.clock != tt.clock || r.level != tt.level || r.source != tt.source || r.comp != tt.comp || r.body != tt.body {
				t.Errorf("got %+v", r)
			}
		})
	}

	t.Run("long path shortening", func(t *testing.T) {
		if home == "" || home == "/" {
			t.Skip("no home dir")
		}
		long := home + "/.claude-personal/projects/-Users-someone-code-personal-agentctl--worktrees-agent-session-482256b5/00606b65-aaaa-bbbb-cccc-dddddddddddd.jsonl"
		r := parseRuntimeLine("2026/11/19 09:16:06.314 INFO <a.go:1> hook: linked log=" + long)
		lines := renderLogRec(r, 100, false)
		out := ansi.Strip(strings.Join(lines, "\n"))
		if strings.Contains(out, home) {
			t.Errorf("home not shortened: %s", out)
		}
		if !strings.Contains(out, "log=~/") || !strings.Contains(out, "…") {
			t.Errorf("want ~ prefix and middle ellipsis: %s", out)
		}
		if !strings.HasSuffix(strings.TrimSpace(out), "dddd.jsonl") {
			t.Errorf("tail of path should survive ellipsis: %s", out)
		}
	})
}

func TestAppendLogRecsCollapse(t *testing.T) {
	l := func(clock, msg string) string { return "2026/11/19 " + clock + " INFO <a.go:1> chrome-bar: " + msg }
	tests := []struct {
		name   string
		in     []string
		counts []int
	}{
		{"run collapses", []string{l("09:00:01", "binary changed"), l("09:00:02", "binary changed"), l("09:00:03", "binary changed")}, []int{3}},
		{"different message splits", []string{l("09:00:01", "a"), l("09:00:02", "b"), l("09:00:03", "b")}, []int{1, 2}},
		{"non-adjacent not merged", []string{l("09:00:01", "a"), l("09:00:02", "b"), l("09:00:03", "a")}, []int{1, 1, 1}},
		{"unparsed never collapse", []string{"trace", "trace"}, []int{1, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs, _ := appendLogRecs(nil, tt.in)
			if len(recs) != len(tt.counts) {
				t.Fatalf("got %d recs, want %d", len(recs), len(tt.counts))
			}
			for i, c := range tt.counts {
				if recs[i].count != c {
					t.Errorf("rec %d count = %d, want %d", i, recs[i].count, c)
				}
			}
		})
	}

	t.Run("collapse across batches and suffix", func(t *testing.T) {
		recs, _ := appendLogRecs(nil, []string{l("09:00:01", "x")})
		recs, _ = appendLogRecs(recs, []string{l("09:00:09", "x")})
		if len(recs) != 1 || recs[0].count != 2 || recs[0].clock != "09:00:09" {
			t.Fatalf("got %+v", recs)
		}
		if out := ansi.Strip(strings.Join(renderLogRec(recs[0], 80, false), "")); !strings.Contains(out, "×2") {
			t.Errorf("missing ×2: %q", out)
		}
	})

	t.Run("cap", func(t *testing.T) {
		var in []string
		for i := 0; i < maxLogLines+50; i++ {
			in = append(in, l("09:00:01", "m"+string(rune('a'+i%7))+strings.Repeat("x", i%5)+string(rune('A'+i%26))+string(rune('a'+i/26%26))))
		}
		recs, trimmed := appendLogRecs(nil, in)
		if len(recs) != maxLogLines || trimmed != 50 {
			t.Errorf("len=%d trimmed=%d", len(recs), trimmed)
		}
	})
}

func TestRenderLogRecWrapWidth(t *testing.T) {
	line := "2026/11/19 09:16:06.314 INFO <hooks/session.go:170> hook-session-start: linked " +
		strings.Repeat("some long message words ", 12) + "session_id=00606b65-1111-2222-3333-444455556666 window=@1175"
	for _, width := range []int{40, 100} {
		for _, src := range []bool{false, true} {
			rec := parseRuntimeLine(line)
			lines := renderLogRec(rec, width, src)
			if len(lines) < 2 && width == 40 {
				t.Errorf("w=%d expected wrapping, got %d lines", width, len(lines))
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > width {
					t.Errorf("w=%d src=%v line %d width %d > %d: %q", width, src, i, w, width, ansi.Strip(l))
				}
			}
			if len(lines) > 1 {
				// Hanging indent: continuation starts under the message column.
				first := ansi.Strip(lines[0])
				cont := ansi.Strip(lines[1])
				indent := len(cont) - len(strings.TrimLeft(cont, " "))
				col := strings.Index(first, "hook-session-start")
				if src {
					col = strings.Index(first, "hook-session-start")
				}
				if col >= 0 && indent != col {
					t.Errorf("w=%d src=%v indent %d, message column %d", width, src, indent, col)
				}
			}
		}
	}

	t.Run("unparsed wraps", func(t *testing.T) {
		for _, l := range renderLogRec(parseRuntimeLine(strings.Repeat("x", 250)), 40, false) {
			if ansi.StringWidth(l) > 40 {
				t.Errorf("too wide: %d", ansi.StringWidth(l))
			}
		}
	})
}

func TestRenderLogRecsDateDivider(t *testing.T) {
	recs, _ := appendLogRecs(nil, []string{
		"2026/11/19 23:59:59.000 INFO a: x",
		"2026/11/19 23:59:59.500 INFO a: y",
		"2026/11/20 00:00:01.000 INFO a: z",
	})
	lines, starts := renderLogRecs(recs, 80, false)
	var dividers int
	for _, l := range lines {
		if strings.Contains(ansi.Strip(l), "──") {
			dividers++
		}
	}
	if dividers != 2 {
		t.Errorf("dividers = %d, want 2", dividers)
	}
	if len(starts) != 3 || starts[0] != 0 || starts[1] != 2 || starts[2] != 3 {
		t.Errorf("starts = %v", starts)
	}
}

func TestLogScrollStableAcrossResize(t *testing.T) {
	m := newTestHud()
	var raw []string
	for i := 0; i < 60; i++ {
		raw = append(raw, "2026/11/19 09:00:00 INFO a.go: msg "+strings.Repeat("word ", 20)+string(rune('A'+i%26))+string(rune('a'+i/26)))
	}
	m.appendLog(m.agentPane(), raw)
	if !m.agentLogAtBottom || m.agentLogScroll != len(m.agentLogLines)-m.visibleLines() {
		t.Fatalf("not pinned to bottom: scroll=%d lines=%d", m.agentLogScroll, len(m.agentLogLines))
	}
	m.agentLogAtBottom = false
	m.agentLogScroll = (m.agentStarts[30]) // top of record 30
	anchor := m.agentPane().anchor()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = next.(HudModel)
	if got := m.agentPane().anchor(); got != anchor {
		t.Errorf("anchor %d after resize, want %d", got, anchor)
	}
	// Pinned stays pinned after resize.
	m.agentLogAtBottom = true
	next, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = next.(HudModel)
	if m.agentLogScroll != max(len(m.agentLogLines)-m.visibleLines(), 0) {
		t.Errorf("not pinned after resize")
	}
}

func TestHudTabHitTest(t *testing.T) {
	// Layout at w=80, Stream active: " [Stream] " then " Agent Logs " then " System Logs ".
	// x: 0 gap, 1-8 [Stream], 9 gap, 10-21 Agent Logs, 22 gap, 23-35 System Logs.
	m := newTestHud()
	tests := []struct {
		name  string
		width int
		x, y  int
		want  int
	}{
		{"stream first cell", 80, 1, 1, tabStream},
		{"stream last cell", 80, 8, 1, tabStream},
		{"leading gap", 80, 0, 1, -1},
		{"gap stream/agent", 80, 9, 1, -1},
		{"agent first", 80, 10, 1, tabAgentLogs},
		{"agent last", 80, 21, 1, tabAgentLogs},
		{"gap agent/sys", 80, 22, 1, -1},
		{"sys first", 80, 23, 1, tabSysLogs},
		{"sys last", 80, 35, 1, tabSysLogs},
		{"past labels", 80, 36, 1, -1},
		{"header row", 80, 12, 0, -1},
		{"content row", 80, 12, 2, -1},
		{"narrow: sys partly visible", 25, 24, 1, tabSysLogs},
		{"narrow: past width", 25, 25, 1, -1},
		{"narrow: agent visible part", 25, 21, 1, tabAgentLogs},
		{"narrow: agent clipped at width", 20, 19, 1, tabAgentLogs},
		{"narrow: sys dropped", 20, 20, 1, -1},
		{"narrow: sys partially visible", 30, 29, 1, tabSysLogs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.width = tt.width
			if got := m.tabAt(tt.x, tt.y); got != tt.want {
				t.Errorf("tabAt(%d,%d) w=%d = %d, want %d", tt.x, tt.y, tt.width, got, tt.want)
			}
		})
	}
}

func TestHudTabClickSwitchesAndMatchesRender(t *testing.T) {
	m := newTestHud()
	for _, c := range [][2]int{{tabAgentLogs, 12}, {tabSysLogs, 25}, {tabStream, 3}} {
		want, x := c[0], c[1]
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: 1})
		m = next.(HudModel)
		if m.activeTab != want {
			t.Errorf("click x=%d: activeTab = %d, want %d", x, m.activeTab, want)
		}
	}
	// Right click and clicks off the bar do nothing.
	m.activeTab = tabStream
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: 12, Y: 1})
	if next.(HudModel).activeTab != tabStream {
		t.Error("right click switched tab")
	}
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 12, Y: 3})
	if next.(HudModel).activeTab != tabStream {
		t.Error("click off the tab bar switched tab")
	}

	// Render and hit-test agree: each span starts where its label is drawn.
	for _, active := range []int{tabStream, tabAgentLogs, tabSysLogs} {
		m.activeTab = active
		bar := ansi.Strip(m.renderTabBar(80))
		for i, l := range m.tabLabels() {
			sp := m.hudTabSpans(80)[i]
			if got := strings.Index(bar, l); got != sp.x0 {
				t.Errorf("active=%d tab %d drawn at %d, span starts %d", active, i, got, sp.x0)
			}
		}
	}
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Error("per-window HUD should enable mouse mode")
	}
}

func TestSourceToggleKeyOnlyOnLogTabs(t *testing.T) {
	m := newTestHud()
	m = sendKey(m, "s")
	if m.showSource {
		t.Error("s on Stream tab should not toggle source")
	}
	m.activeTab = tabAgentLogs
	m = sendKey(m, "s")
	if !m.showSource {
		t.Error("s on a log tab should toggle source")
	}
	if !strings.Contains(m.renderFooter(80), "s source") {
		t.Error("footer on log tab should mention s")
	}
	m.activeTab = tabStream
	if strings.Contains(ansi.Strip(m.renderFooter(120)), "s source") {
		t.Error("footer on stream tab should not mention s")
	}
}
