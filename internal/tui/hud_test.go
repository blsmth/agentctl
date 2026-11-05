package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// newTestHud returns a HudModel with no windowID (so tmux calls are skipped)
// and a fixed terminal size for deterministic rendering.
func newTestHud() HudModel {
	m := NewHudModel("")
	m.width = 80
	m.height = 24
	m.agentName = "test-agent"
	return m
}

// sendKey simulates a key press and returns the updated model.
func sendKey(m HudModel, key string) HudModel {
	r := []rune(key)
	updated, _ := m.Update(tea.KeyPressMsg{Code: r[0], Text: key})
	return updated.(HudModel)
}

// ── Tab switching ────────────────────────────────────────────────────────────

func TestTabKeySwitchDirect(t *testing.T) {
	tests := []struct {
		key     string
		wantTab int
	}{
		{"1", tabStream},
		{"2", tabAgentLogs},
		{"3", tabSysLogs},
	}

	for _, tt := range tests {
		t.Run("key_"+tt.key, func(t *testing.T) {
			m := newTestHud()
			m = sendKey(m, tt.key)
			if m.activeTab != tt.wantTab {
				t.Errorf("after pressing %q: activeTab = %d, want %d", tt.key, m.activeTab, tt.wantTab)
			}
		})
	}
}

func sendTabKey(m HudModel) HudModel {
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	return updated.(HudModel)
}

func sendShiftTabKey(m HudModel) HudModel {
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	return updated.(HudModel)
}

func TestTabKeyCycling(t *testing.T) {
	m := newTestHud()
	// starts at tabStream (0)
	if m.activeTab != tabStream {
		t.Fatalf("initial activeTab = %d, want %d (tabStream)", m.activeTab, tabStream)
	}

	// Tab: 0 → 1
	m = sendTabKey(m)
	if m.activeTab != tabAgentLogs {
		t.Errorf("after 1st Tab: activeTab = %d, want %d (tabAgentLogs)", m.activeTab, tabAgentLogs)
	}

	// Tab: 1 → 2
	m = sendTabKey(m)
	if m.activeTab != tabSysLogs {
		t.Errorf("after 2nd Tab: activeTab = %d, want %d (tabSysLogs)", m.activeTab, tabSysLogs)
	}

	// Tab: 2 → 0 (wraps back to stream)
	m = sendTabKey(m)
	if m.activeTab != tabStream {
		t.Errorf("after 3rd Tab: activeTab = %d, want %d (tabStream)", m.activeTab, tabStream)
	}
}

func TestShiftTabKeyCyclingBackward(t *testing.T) {
	m := newTestHud()
	// starts at tabStream (0) — shift+tab should wrap to last tab (tabSysLogs=2)
	m = sendShiftTabKey(m)
	if m.activeTab != tabSysLogs {
		t.Errorf("shift+tab from tab 0 should wrap to tabSysLogs (%d), got %d", tabSysLogs, m.activeTab)
	}

	// tabSysLogs → tabAgentLogs
	m = sendShiftTabKey(m)
	if m.activeTab != tabAgentLogs {
		t.Errorf("shift+tab from tabSysLogs should go to tabAgentLogs (%d), got %d", tabAgentLogs, m.activeTab)
	}

	// tabAgentLogs → tabStream
	m = sendShiftTabKey(m)
	if m.activeTab != tabStream {
		t.Errorf("shift+tab from tabAgentLogs should go to tabStream (%d), got %d", tabStream, m.activeTab)
	}
}

func TestShiftTabAndTabAreInverse(t *testing.T) {
	m := newTestHud()
	// Tab forward then shift+tab should return to start.
	m = sendTabKey(m)
	m = sendShiftTabKey(m)
	if m.activeTab != tabStream {
		t.Errorf("tab then shift+tab should return to tabStream, got %d", m.activeTab)
	}
}

// ── Scroll routing ───────────────────────────────────────────────────────────

func TestScrollRoutesPerTab(t *testing.T) {
	m := newTestHud()
	// Populate agent and sys log lines so scroll has room to move.
	for i := 0; i < 100; i++ {
		m.agentLogLines = append(m.agentLogLines, "agent line")
		m.sysLogLines = append(m.sysLogLines, "sys line")
	}

	// On stream tab: j should move scrollTop, not agentLogScroll/sysLogScroll.
	m.activeTab = tabStream
	m = sendKey(m, "j")
	if m.agentLogScroll != 0 {
		t.Errorf("stream tab j: agentLogScroll = %d, want 0", m.agentLogScroll)
	}
	if m.sysLogScroll != 0 {
		t.Errorf("stream tab j: sysLogScroll = %d, want 0", m.sysLogScroll)
	}

	// On agent logs tab: j should move agentLogScroll, not others.
	m.activeTab = tabAgentLogs
	prevScrollTop := m.scrollTop
	m = sendKey(m, "j")
	if m.agentLogScroll == 0 {
		t.Errorf("agent logs tab j: agentLogScroll should be > 0")
	}
	if m.sysLogScroll != 0 {
		t.Errorf("agent logs tab j: sysLogScroll = %d, want 0", m.sysLogScroll)
	}
	if m.scrollTop != prevScrollTop {
		t.Errorf("agent logs tab j: scrollTop changed unexpectedly")
	}

	// On sys logs tab: j should move sysLogScroll, not others.
	prevAgentScroll := m.agentLogScroll
	m.activeTab = tabSysLogs
	m = sendKey(m, "j")
	if m.sysLogScroll == 0 {
		t.Errorf("sys logs tab j: sysLogScroll should be > 0")
	}
	if m.agentLogScroll != prevAgentScroll {
		t.Errorf("sys logs tab j: agentLogScroll changed unexpectedly")
	}
}

// ── Scroll increment tests ────────────────────────────────────────────────────

func TestScrollIncrementNonStreamTabs(t *testing.T) {
	tests := []struct {
		name      string
		tab       int
		getScroll func(m HudModel) int
	}{
		{
			name:      "agent_logs_j_increments_by_3",
			tab:       tabAgentLogs,
			getScroll: func(m HudModel) int { return m.agentLogScroll },
		},
		{
			name:      "sys_logs_j_increments_by_3",
			tab:       tabSysLogs,
			getScroll: func(m HudModel) int { return m.sysLogScroll },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestHud()
			// Populate enough lines so scrolling has room.
			for i := 0; i < 100; i++ {
				m.agentLogLines = append(m.agentLogLines, "line")
				m.sysLogLines = append(m.sysLogLines, "line")
			}
			m.activeTab = tt.tab
			m = sendKey(m, "j")
			if got := tt.getScroll(m); got != 3 {
				t.Errorf("j on %s should scroll by 3, got %d", tt.name, got)
			}
		})
	}
}

func TestScrollDecrementNonStreamTabs(t *testing.T) {
	tests := []struct {
		name      string
		tab       int
		setScroll func(m *HudModel, v int)
		getScroll func(m HudModel) int
	}{
		{
			name:      "agent_logs_k_decrements_by_3",
			tab:       tabAgentLogs,
			setScroll: func(m *HudModel, v int) { m.agentLogScroll = v },
			getScroll: func(m HudModel) int { return m.agentLogScroll },
		},
		{
			name:      "sys_logs_k_decrements_by_3",
			tab:       tabSysLogs,
			setScroll: func(m *HudModel, v int) { m.sysLogScroll = v },
			getScroll: func(m HudModel) int { return m.sysLogScroll },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestHud()
			tt.setScroll(&m, 10)
			m.activeTab = tt.tab
			m = sendKey(m, "k")
			if got := tt.getScroll(m); got != 7 {
				t.Errorf("k on %s from scroll=10 should reach 7, got %d", tt.name, got)
			}
		})
	}
}

func TestScrollDecrementClampsToZero(t *testing.T) {
	m := newTestHud()
	for i := 0; i < 100; i++ {
		m.agentLogLines = append(m.agentLogLines, "line")
	}
	m.agentLogScroll = 1 // less than decrement of 3
	m.activeTab = tabAgentLogs
	m = sendKey(m, "k")
	if m.agentLogScroll != 0 {
		t.Errorf("k should clamp to 0 when scroll < 3, got %d", m.agentLogScroll)
	}
}

func TestStreamNoCursorKScrollBy3(t *testing.T) {
	m := newTestHud()
	populateStream(&m, 50)
	m.scrollToBottom()
	startTop := m.scrollTop
	// cursor is -1, so k should scroll scrollTop up by 3
	m = sendKey(m, "k")
	if m.scrollTop != startTop-3 {
		t.Errorf("stream k without cursor: scrollTop should decrease by 3, got %d (was %d)", m.scrollTop, startTop)
	}
}

// ── renderTabBar ─────────────────────────────────────────────────────────────

func TestRenderTabBarActiveStream(t *testing.T) {
	m := newTestHud()
	m.activeTab = tabStream
	bar := m.renderTabBar(80)

	if !strings.Contains(bar, "[Stream]") {
		t.Errorf("stream active: tab bar missing [Stream], got: %q", bar)
	}
	if strings.Contains(bar, "[Agent Logs]") {
		t.Errorf("stream active: tab bar should not contain [Agent Logs]")
	}
	if strings.Contains(bar, "[System Logs]") {
		t.Errorf("stream active: tab bar should not contain [System Logs]")
	}
}

func TestRenderTabBarActiveAgentLogs(t *testing.T) {
	m := newTestHud()
	m.activeTab = tabAgentLogs
	bar := m.renderTabBar(80)

	if !strings.Contains(bar, "[Agent Logs]") {
		t.Errorf("agent logs active: tab bar missing [Agent Logs], got: %q", bar)
	}
	if strings.Contains(bar, "[Stream]") {
		t.Errorf("agent logs active: tab bar should not contain [Stream]")
	}
	if strings.Contains(bar, "[System Logs]") {
		t.Errorf("agent logs active: tab bar should not contain [System Logs]")
	}
}

func TestRenderTabBarActiveSysLogs(t *testing.T) {
	m := newTestHud()
	m.activeTab = tabSysLogs
	bar := m.renderTabBar(80)

	if !strings.Contains(bar, "[System Logs]") {
		t.Errorf("sys logs active: tab bar missing [System Logs], got: %q", bar)
	}
	if strings.Contains(bar, "[Stream]") {
		t.Errorf("sys logs active: tab bar should not contain [Stream]")
	}
	if strings.Contains(bar, "[Agent Logs]") {
		t.Errorf("sys logs active: tab bar should not contain [Agent Logs]")
	}
}

// ── NewHudModel defaults ──────────────────────────────────────────────────────

func TestNewHudModelDefaults(t *testing.T) {
	m := NewHudModel("win-1")
	if !m.agentLogAtBottom {
		t.Error("agentLogAtBottom should default to true")
	}
	if !m.sysLogAtBottom {
		t.Error("sysLogAtBottom should default to true")
	}
	if m.activeTab != tabStream {
		t.Errorf("activeTab should default to tabStream (0), got %d", m.activeTab)
	}
}

// ── Scroll clamp helpers ──────────────────────────────────────────────────────

func TestClampAgentLogScroll(t *testing.T) {
	m := newTestHud()
	// visibleLines = height(24) - 5 = 19
	for i := 0; i < 10; i++ {
		m.agentLogLines = append(m.agentLogLines, "line")
	}
	// 10 lines, 19 visible → max scroll = 0
	m.agentLogScroll = 99
	m.clampAgentLogScroll()
	if m.agentLogScroll != 0 {
		t.Errorf("clampAgentLogScroll: got %d, want 0", m.agentLogScroll)
	}
}

func TestClampSysLogScroll(t *testing.T) {
	m := newTestHud()
	for i := 0; i < 10; i++ {
		m.sysLogLines = append(m.sysLogLines, "line")
	}
	m.sysLogScroll = 99
	m.clampSysLogScroll()
	if m.sysLogScroll != 0 {
		t.Errorf("clampSysLogScroll: got %d, want 0", m.sysLogScroll)
	}
}

func TestScrollAgentLogToBottom(t *testing.T) {
	m := newTestHud()
	// visibleLines = 19, add 30 lines → max = 11
	for i := 0; i < 30; i++ {
		m.agentLogLines = append(m.agentLogLines, "line")
	}
	m.scrollAgentLogToBottom()
	want := 30 - m.visibleLines()
	if m.agentLogScroll != want {
		t.Errorf("scrollAgentLogToBottom: got %d, want %d", m.agentLogScroll, want)
	}
}

func TestScrollSysLogToBottom(t *testing.T) {
	m := newTestHud()
	for i := 0; i < 30; i++ {
		m.sysLogLines = append(m.sysLogLines, "line")
	}
	m.scrollSysLogToBottom()
	want := 30 - m.visibleLines()
	if m.sysLogScroll != want {
		t.Errorf("scrollSysLogToBottom: got %d, want %d", m.sysLogScroll, want)
	}
}

// ── Empty-state hints ─────────────────────────────────────────────────────────

func TestEmptyAgentLogsHintNoSessionID(t *testing.T) {
	m := newTestHud()
	hint := m.emptyAgentLogsHint()
	if !strings.Contains(hint, "session start") {
		t.Errorf("expected hint to mention 'session start', got: %q", hint)
	}
}

func TestEmptyAgentLogsHintWithSessionID(t *testing.T) {
	m := newTestHud()
	m.agentctlSessionID = "abc123"
	m.agentctlLogIsGlobal = false
	hint := m.emptyAgentLogsHint()
	if !strings.Contains(hint, "abc123") {
		t.Errorf("expected hint to contain session ID, got: %q", hint)
	}
}

func TestEmptySysLogsHintNoPath(t *testing.T) {
	m := newTestHud()
	hint := m.emptySysLogsHint()
	if hint == "" {
		t.Error("emptySysLogsHint should return non-empty string")
	}
}

func TestEmptySysLogsHintWithPath(t *testing.T) {
	m := newTestHud()
	m.sysLogPath = "/tmp/agentctl.log"
	hint := m.emptySysLogsHint()
	if !strings.Contains(hint, "global log") {
		t.Errorf("expected hint to mention global log, got: %q", hint)
	}
}

// ── Footer hint text ──────────────────────────────────────────────────────────

func TestFooterHintContainsShiftTab(t *testing.T) {
	m := newTestHud()
	footer := m.renderFooter(120)
	if !strings.Contains(footer, "Shift+Tab") {
		t.Errorf("footer hint should contain 'Shift+Tab', got: %q", footer)
	}
}

func TestFooterHintContainsCtrlDU(t *testing.T) {
	m := newTestHud()
	footer := m.renderFooter(120)
	if !strings.Contains(footer, "Ctrl+d/u") {
		t.Errorf("footer hint should contain 'Ctrl+d/u', got: %q", footer)
	}
}

func TestFooterCursorHintContainsCollapse(t *testing.T) {
	m := newTestHud()
	populateStream(&m, 3)
	m = sendKey(m, "j") // activate cursor
	footer := m.renderFooter(120)
	if !strings.Contains(footer, "collapse") {
		t.Errorf("footer with cursor should contain 'collapse', got: %q", footer)
	}
	if !strings.Contains(footer, "g/G") {
		t.Errorf("footer with cursor should contain 'g/G', got: %q", footer)
	}
}

// ── Tab bar has no Tickets tab ───────────────────────────────────────────────

func TestRenderTabBarNoTicketsTab(t *testing.T) {
	m := newTestHud()
	for tab := 0; tab < hudNumTabs; tab++ {
		m.activeTab = tab
		bar := m.renderTabBar(80)
		if strings.Contains(bar, "Tickets") {
			t.Errorf("tab bar should not contain Tickets in any tab state, got: %q", bar)
		}
	}
}

// ── newTestEntry helpers ──────────────────────────────────────────────────────

func newToolEntry(toolName string, input map[string]any) LogEntry {
	summary := "▶ " + toolName
	return LogEntry{
		Timestamp: time.Now(),
		Kind:      "tool_call",
		Summary:   summary,
		FullInput: input,
		ToolName:  toolName,
		Style:     lipgloss.NewStyle(),
	}
}

func newTextEntry(text string) LogEntry {
	preview := text
	if len(preview) > 80 {
		preview = preview[:80] + "…"
	}
	return LogEntry{
		Timestamp: time.Now(),
		Kind:      "text",
		Summary:   "💬 \"" + preview + "\"",
		FullText:  text,
		Style:     lipgloss.NewStyle(),
	}
}

// populateStream adds n tool entries to m and rebuilds the line buffer.
func populateStream(m *HudModel, n int) {
	for i := 0; i < n; i++ {
		m.logEntries = append(m.logEntries, newToolEntry("Bash", map[string]any{
			"command": "go test ./...",
		}))
	}
	m.rebuildStreamLineBuffer()
}

// ── TestStreamCursorNavigation ────────────────────────────────────────────────

func TestStreamCursorNavigation(t *testing.T) {
	t.Run("starts_at_minus1", func(t *testing.T) {
		m := newTestHud()
		if m.streamCursor != -1 {
			t.Errorf("streamCursor should start at -1, got %d", m.streamCursor)
		}
	})

	t.Run("first_j_activates_cursor", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 5)
		m = sendKey(m, "j")
		if m.streamCursor == -1 {
			t.Error("first j press should activate cursor")
		}
	})

	t.Run("j_moves_cursor_down", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 5)
		// First j activates cursor (at last visible entry).
		m = sendKey(m, "j")
		first := m.streamCursor
		// Ensure cursor is not already at the last entry so j can move it.
		if first < len(m.logEntries)-1 {
			m = sendKey(m, "j")
			if m.streamCursor != first+1 {
				t.Errorf("j should move cursor down: got %d, want %d", m.streamCursor, first+1)
			}
		}
	})

	t.Run("k_moves_cursor_up", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 5)
		m = sendKey(m, "j") // activate cursor
		m.streamCursor = 3  // place cursor in middle
		m.rebuildStreamLineBuffer()
		m = sendKey(m, "k")
		if m.streamCursor != 2 {
			t.Errorf("k should move cursor up: got %d, want 2", m.streamCursor)
		}
	})

	t.Run("cursor_does_not_go_below_zero", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 3)
		m = sendKey(m, "j") // activate cursor
		m.streamCursor = 0
		m.rebuildStreamLineBuffer()
		m = sendKey(m, "k")
		if m.streamCursor != 0 {
			t.Errorf("k at index 0 should stay at 0, got %d", m.streamCursor)
		}
	})

	t.Run("cursor_does_not_exceed_last_entry", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 3)
		m = sendKey(m, "j") // activate
		m.streamCursor = 2  // last entry
		m.rebuildStreamLineBuffer()
		m = sendKey(m, "j")
		if m.streamCursor != 2 {
			t.Errorf("j at last entry should stay at 2, got %d", m.streamCursor)
		}
	})

	t.Run("esc_deactivates_cursor", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 3)
		m = sendKey(m, "j")
		if m.streamCursor == -1 {
			t.Fatal("cursor should be active after j")
		}
		updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		m = updated.(HudModel)
		if cmd != nil {
			t.Error("esc with active cursor should not quit")
		}
		if m.streamCursor != -1 {
			t.Errorf("esc should set cursor to -1, got %d", m.streamCursor)
		}
	})

	t.Run("esc_without_cursor_quits", func(t *testing.T) {
		m := newTestHud()
		_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if cmd == nil {
			t.Error("esc without cursor should return quit command")
		}
	})
}

// ── TestEntryExpansion ────────────────────────────────────────────────────────

func TestEntryExpansion(t *testing.T) {
	t.Run("enter_expands_entry", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 3)
		m = sendKey(m, "j") // activate cursor
		m.streamCursor = 1
		m.rebuildStreamLineBuffer()
		collapsedLines := len(m.streamLineBuffer)

		m = sendKey(m, "j") // press enter via "enter" key
		// Use sendSpecialKey for Enter
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated.(HudModel)

		if !m.expandedEntries[m.streamCursor] {
			t.Error("enter should expand the selected entry")
		}
		if len(m.streamLineBuffer) <= collapsedLines {
			t.Errorf("expanded buffer should be larger: got %d, was %d", len(m.streamLineBuffer), collapsedLines)
		}
	})

	t.Run("enter_collapses_expanded_entry", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 3)
		m = sendKey(m, "j")
		m.streamCursor = 1
		m.expandedEntries[1] = true
		m.rebuildStreamLineBuffer()
		expandedLines := len(m.streamLineBuffer)

		// Second enter collapses.
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated.(HudModel)

		if m.expandedEntries[1] {
			t.Error("second enter should collapse the entry")
		}
		if len(m.streamLineBuffer) >= expandedLines {
			t.Errorf("collapsed buffer should be smaller: got %d, was %d", len(m.streamLineBuffer), expandedLines)
		}
	})

	t.Run("l_expands_entry", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 3)
		m = sendKey(m, "j")
		m.streamCursor = 0
		m.rebuildStreamLineBuffer()
		m = sendKey(m, "l")
		if !m.expandedEntries[0] {
			t.Error("l should expand the selected entry")
		}
	})

	t.Run("h_collapses_entry", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 3)
		m = sendKey(m, "j")
		m.streamCursor = 0
		m.expandedEntries[0] = true
		m.rebuildStreamLineBuffer()
		m = sendKey(m, "h")
		if m.expandedEntries[0] {
			t.Error("h should collapse the expanded entry")
		}
	})

	t.Run("h_deactivates_cursor_when_collapsed", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 3)
		m = sendKey(m, "j")
		m.streamCursor = 1
		// Entry 1 is not expanded.
		m.rebuildStreamLineBuffer()
		m = sendKey(m, "h")
		if m.streamCursor != -1 {
			t.Errorf("h on collapsed entry should deactivate cursor, got %d", m.streamCursor)
		}
	})

	t.Run("no_expand_without_cursor", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 3)
		// cursor is -1
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated.(HudModel)
		if len(m.expandedEntries) != 0 {
			t.Error("enter without cursor should not expand anything")
		}
	})
}

// ── TestRenderExpandedDetail ──────────────────────────────────────────────────

func TestRenderExpandedDetail(t *testing.T) {
	tests := []struct {
		name     string
		entry    LogEntry
		wantKeys []string // substrings that must appear in rendered output
	}{
		{
			name: "bash_command",
			entry: newToolEntry("Bash", map[string]any{
				"command":     "go test -run TestFoo ./...",
				"description": "Run specific test",
			}),
			wantKeys: []string{"command", "go test"},
		},
		{
			name: "read_file_path",
			entry: newToolEntry("Read", map[string]any{
				"file_path": "/home/user/code/file.go",
				"offset":    float64(10),
				"limit":     float64(50),
			}),
			wantKeys: []string{"file_path", "offset", "limit"},
		},
		{
			name: "edit_strings",
			entry: newToolEntry("Edit", map[string]any{
				"file_path":  "/tmp/foo.go",
				"old_string": "old line 1\nold line 2",
				"new_string": "new line 1\nnew line 2",
			}),
			wantKeys: []string{"file_path", "old_string", "new_string"},
		},
		{
			name: "grep_pattern",
			entry: newToolEntry("Grep", map[string]any{
				"pattern": "func.*Test",
				"path":    ".",
			}),
			wantKeys: []string{"pattern", "func.*Test"},
		},
		{
			name: "glob_pattern",
			entry: newToolEntry("Glob", map[string]any{
				"pattern": "**/*.go",
			}),
			wantKeys: []string{"pattern", "**/*.go"},
		},
		{
			name: "agent_description",
			entry: newToolEntry("Agent", map[string]any{
				"description": "Run tests and report results",
			}),
			wantKeys: []string{"description", "Run tests"},
		},
		{
			name: "write_file",
			entry: newToolEntry("Write", map[string]any{
				"file_path": "/tmp/out.go",
				"content":   "package main\n",
			}),
			wantKeys: []string{"file_path", "content"},
		},
		{
			name:     "text_entry_full_text",
			entry:    newTextEntry("This is a long message that should be rendered in full."),
			wantKeys: []string{"This is a long message"},
		},
		{
			name:     "empty_input",
			entry:    newToolEntry("Bash", nil),
			wantKeys: []string{"no input data"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := renderExpandedDetail(tt.entry, 60)
			joined := strings.Join(lines, "\n")
			for _, want := range tt.wantKeys {
				if !strings.Contains(joined, want) {
					t.Errorf("expected %q in expanded detail, got:\n%s", want, joined)
				}
			}
			// Must have border characters.
			if !strings.Contains(joined, "┌") || !strings.Contains(joined, "└") {
				t.Errorf("detail block should have border chars, got:\n%s", joined)
			}
		})
	}
}

// ── TestExpandedViewportMath ──────────────────────────────────────────────────

func TestExpandedViewportMath(t *testing.T) {
	t.Run("expanded_entry_increases_line_count", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 5)
		collapsed := len(m.streamLineBuffer)

		m.expandedEntries[2] = true
		m.rebuildStreamLineBuffer()
		expanded := len(m.streamLineBuffer)

		if expanded <= collapsed {
			t.Errorf("expanding an entry should increase buffer size: collapsed=%d, expanded=%d", collapsed, expanded)
		}
	})

	t.Run("entry_start_lines_increase_monotonically", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 5)
		m.expandedEntries[1] = true
		m.rebuildStreamLineBuffer()

		for i := 1; i < len(m.streamEntryStart); i++ {
			if m.streamEntryStart[i] <= m.streamEntryStart[i-1] {
				t.Errorf("streamEntryStart[%d]=%d should be > streamEntryStart[%d]=%d",
					i, m.streamEntryStart[i], i-1, m.streamEntryStart[i-1])
			}
		}
	})

	t.Run("scroll_clamp_respects_line_count", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 3) // 3 entries, all fit in viewport (visibleLines=19)
		m.scrollTop = 9999
		m.clampScroll()
		if m.scrollTop != 0 {
			t.Errorf("clampScroll should set scrollTop=0 when all entries fit, got %d", m.scrollTop)
		}
	})

	t.Run("scroll_to_bottom_uses_line_buffer", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 30) // more than viewport
		m.expandedEntries[5] = true
		m.rebuildStreamLineBuffer()
		m.scrollToBottom()
		want := len(m.streamLineBuffer) - m.visibleLines()
		if m.scrollTop != want {
			t.Errorf("scrollToBottom: got %d, want %d", m.scrollTop, want)
		}
	})

	t.Run("ensure_cursor_visible_scrolls_down", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 50)
		m.scrollTop = 0
		m.streamCursor = 49 // last entry, not visible
		m.ensureCursorVisible()
		if m.scrollTop == 0 {
			t.Error("ensureCursorVisible should have scrolled down to show cursor")
		}
	})

	t.Run("ensure_cursor_visible_scrolls_up", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 50)
		m.scrollToBottom()
		m.streamCursor = 0 // first entry, not visible
		m.ensureCursorVisible()
		if m.scrollTop != 0 {
			t.Errorf("ensureCursorVisible should have scrolled to top, got %d", m.scrollTop)
		}
	})
}

// ── TestRingBufferTrimWithCursor ──────────────────────────────────────────────

func TestRingBufferTrimWithCursor(t *testing.T) {
	t.Run("cursor_adjusts_after_trim", func(t *testing.T) {
		m := newTestHud()
		// Fill well past maxLogEntries so we can simulate trim.
		// We'll manually trim a small batch.
		populateStream(&m, 10)
		m.streamCursor = 7

		// Simulate a trim of 3 entries.
		trim := 3
		m.logEntries = m.logEntries[trim:]
		if m.streamCursor >= 0 {
			m.streamCursor -= trim
			if m.streamCursor < 0 {
				m.streamCursor = 0
			}
		}

		if m.streamCursor != 4 {
			t.Errorf("cursor should be 4 after trim of 3 from cursor=7, got %d", m.streamCursor)
		}
	})

	t.Run("expanded_entries_adjust_after_trim", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 10)
		m.expandedEntries[5] = true
		m.expandedEntries[7] = true

		trim := 3
		m.logEntries = m.logEntries[trim:]
		newExpanded := make(map[int]bool)
		for k, v := range m.expandedEntries {
			if k >= trim {
				newExpanded[k-trim] = v
			}
		}
		m.expandedEntries = newExpanded

		if !m.expandedEntries[2] {
			t.Error("entry originally at index 5 should be at 2 after trim of 3")
		}
		if !m.expandedEntries[4] {
			t.Error("entry originally at index 7 should be at 4 after trim of 3")
		}
		if m.expandedEntries[5] || m.expandedEntries[7] {
			t.Error("old keys should be removed after trim adjustment")
		}
	})

	t.Run("trimmed_expanded_entries_removed", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 10)
		// Mark entries 0 and 1 as expanded (they will be trimmed).
		m.expandedEntries[0] = true
		m.expandedEntries[1] = true
		m.expandedEntries[5] = true

		trim := 3
		m.logEntries = m.logEntries[trim:]
		newExpanded := make(map[int]bool)
		for k, v := range m.expandedEntries {
			if k >= trim {
				newExpanded[k-trim] = v
			}
		}
		m.expandedEntries = newExpanded

		// 0 and 1 were trimmed — should not appear.
		if m.expandedEntries[0] || m.expandedEntries[1] {
			t.Error("trimmed entries should not remain in expandedEntries (as old keys)")
		}
		// 5 → 2.
		if !m.expandedEntries[2] {
			t.Error("entry 5 should have shifted to key 2")
		}
	})
}

// ── TestFullTextPreservation ──────────────────────────────────────────────────

func TestFullTextPreservation(t *testing.T) {
	t.Run("text_entry_full_text_stored", func(t *testing.T) {
		longText := strings.Repeat("word ", 50) // > 80 chars
		e := newTextEntry(longText)
		if e.FullText != longText {
			t.Errorf("FullText should store full untruncated text, got len=%d, want len=%d",
				len(e.FullText), len(longText))
		}
		// Summary should be truncated.
		if len(e.Summary) <= 100 && strings.Contains(e.Summary, longText) {
			t.Error("Summary should be truncated for long text")
		}
	})

	t.Run("tool_entry_full_input_stored", func(t *testing.T) {
		input := map[string]any{"command": "go build ./...", "description": "build all"}
		e := newToolEntry("Bash", input)
		if e.FullInput == nil {
			t.Error("FullInput should not be nil for tool entries")
		}
		if e.FullInput["command"] != "go build ./..." {
			t.Errorf("FullInput['command'] = %v, want 'go build ./...'", e.FullInput["command"])
		}
		if e.ToolName != "Bash" {
			t.Errorf("ToolName = %q, want 'Bash'", e.ToolName)
		}
	})
}

// ── TestFooterHintChangesWithCursor ──────────────────────────────────────────

func TestFooterHintChangesWithCursor(t *testing.T) {
	t.Run("no_cursor_shows_scroll_hint", func(t *testing.T) {
		m := newTestHud()
		footer := m.renderFooter(120)
		if !strings.Contains(footer, "j/k scroll") {
			t.Errorf("footer without cursor should show j/k scroll hint, got: %q", footer)
		}
	})

	t.Run("cursor_active_shows_navigate_hint", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 3)
		m = sendKey(m, "j") // activate cursor
		footer := m.renderFooter(120)
		if !strings.Contains(footer, "navigate") {
			t.Errorf("footer with cursor should show navigate hint, got: %q", footer)
		}
	})
}

// ── TestSelectedEntryRendering ────────────────────────────────────────────────

func TestSelectedEntryRendering(t *testing.T) {
	t.Run("selected_tool_entry_uses_hollow_arrow", func(t *testing.T) {
		m := newTestHud()
		e := newToolEntry("Bash", map[string]any{"command": "go test ./..."})
		line := m.renderEntry(e, 60, true)
		if !strings.Contains(line, "▷") {
			t.Errorf("selected tool entry should use hollow arrow ▷, got: %q", line)
		}
	})

	t.Run("unselected_tool_entry_uses_filled_arrow", func(t *testing.T) {
		m := newTestHud()
		e := newToolEntry("Bash", map[string]any{"command": "go test ./..."})
		line := m.renderEntry(e, 60, false)
		if !strings.Contains(line, "▶") {
			t.Errorf("unselected tool entry should use filled arrow ▶, got: %q", line)
		}
	})
}

// ── Footer metrics tests ──────────────────────────────────────────────────────
// Metrics have moved to ChromeBarModel. Footer now shows only keybind hints.

func TestFooterNoMetrics(t *testing.T) {
	m := newTestHud()
	footer := m.renderFooter(120)
	// Footer should not show any metrics — these are now in the chrome bar.
	if strings.Contains(footer, "ctx") {
		t.Errorf("footer should not show ctx metrics, got: %q", footer)
	}
	if strings.Contains(footer, "tokens") {
		t.Errorf("footer should not show tokens, got: %q", footer)
	}
	if strings.Contains(footer, "turns") {
		t.Errorf("footer should not show turns, got: %q", footer)
	}
}

func TestFooterSingleLine(t *testing.T) {
	m := newTestHud()
	footer := m.renderFooter(120)
	lines := strings.Split(footer, "\n")
	if len(lines) != 1 {
		t.Errorf("renderFooter should return exactly 1 line, got %d: %q", len(lines), footer)
	}
	// The single line must be the keybind hints line.
	if !strings.Contains(lines[0], "Tab") {
		t.Errorf("footer line should contain keybind hints (Tab), got: %q", lines[0])
	}
}

func TestFooterHintFillsWidth(t *testing.T) {
	m := newTestHud()
	footer := m.renderFooter(80)
	got := lipgloss.Width(footer)
	if got != 80 {
		t.Errorf("renderFooter visible width = %d, want 80", got)
	}
}

func TestMiniFooterHeight2ShowsNameState(t *testing.T) {
	m := newTestHud()
	m.height = 2
	m.width = 80
	m.agentName = "test-agent"
	out := m.renderMiniFooter(80)
	// At height=2, mini footer shows agent name + state only.
	if !strings.Contains(out, "test-agent") {
		t.Errorf("mini footer height=2 should show agent name, got: %q", out)
	}
	// No metrics in mini footer.
	if strings.Contains(out, "tokens") {
		t.Errorf("mini footer height=2 should not show tokens, got: %q", out)
	}
}

// ── TestRebuildStreamLineBuffer ───────────────────────────────────────────────

func TestRebuildStreamLineBuffer(t *testing.T) {
	t.Run("empty_entries_empty_buffer", func(t *testing.T) {
		m := newTestHud()
		m.rebuildStreamLineBuffer()
		if len(m.streamLineBuffer) != 0 {
			t.Errorf("empty entries should produce empty buffer, got %d lines", len(m.streamLineBuffer))
		}
	})

	t.Run("n_collapsed_entries_n_lines", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 5)
		if len(m.streamLineBuffer) != 5 {
			t.Errorf("5 collapsed entries should produce 5 lines, got %d", len(m.streamLineBuffer))
		}
	})

	t.Run("entry_start_lines_length_matches_entries", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 7)
		if len(m.streamEntryStart) != 7 {
			t.Errorf("streamEntryStart length should equal entry count: got %d, want 7", len(m.streamEntryStart))
		}
	})

	t.Run("first_entry_starts_at_zero", func(t *testing.T) {
		m := newTestHud()
		populateStream(&m, 3)
		if m.streamEntryStart[0] != 0 {
			t.Errorf("first entry should start at line 0, got %d", m.streamEntryStart[0])
		}
	})
}

// ── Mini mode ────────────────────────────────────────────────────────────────

func TestHudMiniModeRendering(t *testing.T) {
	m := newTestHud()
	m.height = 3
	m.width = 80
	m.agentName = "lead"

	out := m.View().Content
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	if len(lines) > m.height {
		t.Errorf("mini mode output has %d lines, want <= %d", len(lines), m.height)
	}

	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, "lead") {
		t.Errorf("mini mode output missing agent name; got:\n%s", joined)
	}

	// Metrics are in chrome bar, not HUD mini footer.
	if strings.Contains(joined, "tokens") {
		t.Errorf("mini mode output should not contain 'tokens' (metrics moved to chrome bar); got:\n%s", joined)
	}

	// Must not contain tab bar labels.
	tabNames := []string{"[Stream]", "Agent Logs", "Sys Logs"}
	for _, tab := range tabNames {
		if strings.Contains(joined, tab) {
			t.Errorf("mini mode output contains tab name %q; got:\n%s", tab, joined)
		}
	}
}

func TestHudMiniModeThreshold(t *testing.T) {
	t.Run("height_5_is_mini", func(t *testing.T) {
		m := newTestHud()
		m.height = 5
		m.width = 80
		m.agentName = "lead"

		out := m.View().Content
		// Mini mode must not contain tab bar.
		if strings.Contains(out, "[Stream]") {
			t.Errorf("height=5 should be mini mode but contains tab bar; got:\n%s", out)
		}
	})

	t.Run("height_6_is_full", func(t *testing.T) {
		m := newTestHud()
		m.height = 6
		m.width = 80
		m.agentName = "lead"

		out := m.View().Content
		// Full mode must contain tab bar.
		if !strings.Contains(out, "[Stream]") {
			t.Errorf("height=6 should be full mode but missing tab bar; got:\n%s", out)
		}
	})
}

func TestHudMiniModeTwoRows(t *testing.T) {
	m := newTestHud()
	m.height = 2
	m.width = 80
	m.agentName = "lead"

	out := m.View().Content
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	if len(lines) > m.height {
		t.Errorf("height=2 mini mode has %d lines, want <= %d", len(lines), m.height)
	}

	// height=2 shows name+state only — no metrics.
	if !strings.Contains(out, "lead") {
		t.Errorf("height=2 mini mode missing agent name; got:\n%s", out)
	}
	if strings.Contains(out, "tokens") {
		t.Errorf("height=2 mini mode should not show 'tokens' (metrics in chrome bar); got:\n%s", out)
	}
}
