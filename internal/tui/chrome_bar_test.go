package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/state"
)

// isolateConfig points both the global and project-local config paths at
// nonexistent files in a fresh temp dir, so tests get deterministic
// defaults (ContextWarnPct=75, ContextCriticalPct=90) regardless of the
// developer's real ~/.config/agentctl/config.json.
func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	config.SetGlobalConfigPathForTest(filepath.Join(dir, "global-config.json"))
	config.SetConfigPathForTest(filepath.Join(dir, "config.json"))
}

// testTabs returns a canonical set of tabs for testing.
func testTabs() []TabInfo {
	return []TabInfo{
		{Name: "go-engineer", State: "working", CtxPct: 42, WindowID: "@1", IsActive: true},
		{Name: "python-eng", State: "idle", CtxPct: 0, WindowID: "@2", IsActive: false},
		{Name: "test-agent", State: "blocked", CtxPct: 80, WindowID: "@3", IsActive: false},
		{Name: "done-agent", State: "done", CtxPct: 0, WindowID: "@4", IsActive: false},
	}
}

// newTestChromeBar returns a ChromeBarModel pre-populated with deterministic
// test data. No tmux or filesystem calls are made — state is set directly.
func newTestChromeBar(tabs []TabInfo) *ChromeBarModel {
	m := NewChromeBarModel("@1", "")
	m.width = 200
	m.tabs = tabs
	m.clock = "16:57"
	return m
}

// TestChromeBarViewTwoLines verifies that View() returns exactly 2 lines
// (the chrome bar shrank from 4 rows to 2 once rate limits and
// per-window metrics moved to the "rich" statusline format).
func TestChromeBarViewTwoLines(t *testing.T) {
	m := newTestChromeBar(testTabs())
	out := m.View().Content
	// 2 lines means exactly 1 newline.
	got := strings.Count(out, "\n")
	if got != 1 {
		t.Errorf("View() should have exactly 1 newline (2 lines), got %d\noutput: %q", got, out)
	}
}

// TestChromeBarViewLine1Keybinds verifies that line 1 contains the ctrl+b
// badge and representative keybind hints.
func TestChromeBarViewLine1Keybinds(t *testing.T) {
	m := newTestChromeBar(testTabs())
	lines := strings.SplitN(m.View().Content, "\n", 3)
	line1 := lines[0]

	wantTokens := []string{"ctrl+b", "launch", "kill", "logs"}
	for _, want := range wantTokens {
		if !strings.Contains(line1, want) {
			t.Errorf("line 1 should contain %q; got: %q", want, line1)
		}
	}
}

// TestChromeBarViewLine2Tabs verifies that line 2 contains all tab names.
func TestChromeBarViewLine2Tabs(t *testing.T) {
	m := newTestChromeBar(testTabs())
	lines := strings.SplitN(m.View().Content, "\n", 3)
	line2 := lines[1]

	wantNames := []string{"go-engineer", "python-eng", "test-agent", "done-agent"}
	for _, name := range wantNames {
		if !strings.Contains(line2, name) {
			t.Errorf("line 2 should contain tab name %q; got: %q", name, line2)
		}
	}
}

// TestChromeBarViewLine2Clock verifies that line 2 shows the clock.
func TestChromeBarViewLine2Clock(t *testing.T) {
	m := newTestChromeBar(nil)
	m.clock = "16:57"
	lines := strings.SplitN(m.View().Content, "\n", 3)
	line2 := lines[1]
	if !strings.Contains(line2, "16:57") {
		t.Errorf("line 2 should contain clock '16:57'; got: %q", line2)
	}
}

// TestChromeBarViewFillsWidth verifies both lines fill exactly to m.width,
// including narrow widths (80-120) where the ~166-char keybind hint line must
// be trimmed rather than overflow or wrap.
func TestChromeBarViewFillsWidth(t *testing.T) {
	lipgloss.Writer.Profile = colorprofile.TrueColor

	tests := []struct {
		name  string
		width int
	}{
		{"width_80", 80},
		{"width_100", 100},
		{"width_120", 120},
		{"width_160", 160},
		{"width_200", 200},
		{"width_240", 240},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestChromeBar(testTabs())
			m.width = tt.width
			lines := strings.SplitN(m.View().Content, "\n", 3)
			for i, line := range lines {
				got := lipgloss.Width(line)
				if got != tt.width {
					t.Errorf("line %d width = %d, want %d (output: %q)", i+1, got, tt.width, line)
				}
			}
		})
	}
}

// TestChromeBarViewZeroTabs verifies View() handles no tabs gracefully.
func TestChromeBarViewZeroTabs(t *testing.T) {
	m := newTestChromeBar(nil)
	out := m.View().Content
	if strings.Count(out, "\n") != 1 {
		t.Errorf("View() with zero tabs should still have 2 lines")
	}
}

// TestChromeBarViewOneTab verifies View() renders a single tab correctly.
func TestChromeBarViewOneTab(t *testing.T) {
	tabs := []TabInfo{{Name: "go-engineer", State: "working", CtxPct: 5, WindowID: "@1", IsActive: true}}
	m := newTestChromeBar(tabs)
	lines := strings.SplitN(m.View().Content, "\n", 3)
	if !strings.Contains(lines[1], "go-engineer") {
		t.Errorf("single-tab line 2 should contain 'go-engineer'; got: %q", lines[1])
	}
}

// TestChromeBarViewManyTabs verifies that many tabs still render without panic.
func TestChromeBarViewManyTabs(t *testing.T) {
	var tabs []TabInfo
	for i := 0; i < 10; i++ {
		tabs = append(tabs, TabInfo{
			Name:     fmt.Sprintf("agent-%d", i),
			State:    "idle",
			WindowID: fmt.Sprintf("@%d", i+1),
			IsActive: i == 0,
		})
	}
	m := newTestChromeBar(tabs)
	out := m.View().Content
	if strings.Count(out, "\n") != 1 {
		t.Errorf("View() with many tabs should still produce 2 lines")
	}
}

// TestChromeBarViewActiveTabHighlight verifies that switching the active tab
// changes the rendered output.
func TestChromeBarViewActiveTabHighlight(t *testing.T) {
	lipgloss.Writer.Profile = colorprofile.TrueColor

	makeModel := func(activeIdx int) string {
		tabs := []TabInfo{
			{Name: "alpha", State: "idle", WindowID: "@1", IsActive: activeIdx == 0},
			{Name: "beta", State: "idle", WindowID: "@2", IsActive: activeIdx == 1},
		}
		return newTestChromeBar(tabs).View().Content
	}

	if makeModel(0) == makeModel(1) {
		t.Error("View() should differ when active tab changes")
	}
}

// TestChromeBarViewStateBadges verifies state indicators on line 2 (tab bar).
func TestChromeBarViewStateBadges(t *testing.T) {
	lipgloss.Writer.Profile = colorprofile.TrueColor

	tests := []struct {
		state    string
		wantText string
	}{
		{"working", "go-engineer"},
		{"blocked", "⏸"},
		{"done", "✓"},
		{"idle", "○"},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			tabs := []TabInfo{{Name: "go-engineer", State: tt.state, WindowID: "@1"}}
			m := newTestChromeBar(tabs)
			lines := strings.SplitN(m.View().Content, "\n", 3)
			if !strings.Contains(lines[1], tt.wantText) {
				t.Errorf("state %q: expected %q in line 2; got: %q", tt.state, tt.wantText, lines[1])
			}
		})
	}
}

// TestChromeBarWindowSizeMsg verifies that a WindowSizeMsg updates m.width.
func TestChromeBarWindowSizeMsg(t *testing.T) {
	m := NewChromeBarModel("", "")
	m.width = 80

	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 3})
	updated, ok := newModel.(ChromeBarModel)
	if !ok {
		t.Fatal("Update(WindowSizeMsg) did not return a ChromeBarModel")
	}
	if updated.width != 160 {
		t.Errorf("width after WindowSizeMsg = %d, want 160", updated.width)
	}
}

// TestChromeBarPrefixActiveDiffers verifies line 1 (keybinds) changes between
// active and inactive prefix states.
func TestChromeBarPrefixActiveDiffers(t *testing.T) {
	lipgloss.Writer.Profile = colorprofile.TrueColor

	m := newTestChromeBar(nil)
	m.prefixActive = false
	dim := strings.SplitN(m.View().Content, "\n", 3)[0]

	m.prefixActive = true
	lit := strings.SplitN(m.View().Content, "\n", 3)[0]

	if dim == lit {
		t.Error("line 1 should differ between prefixActive=false and prefixActive=true")
	}
}

// ── file-watch mode tests ─────────────────────────────────────────────────────

// writeTestStateFile writes a state.State to a JSON file at path and returns
// the path. It uses a temp directory so tests are isolated.
func writeTestStateFile(t *testing.T, dir string, s *state.State) string {
	t.Helper()
	path := filepath.Join(dir, "state.json")
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write state.json: %v", err)
	}
	return path
}

// TestChromeBarReadStateFile verifies that readStateFile() populates the tab
// list, active window, prefix state, and this window's ctx% (used for the
// warn/critical toast) from a state.json with known values. Per-window
// token/model/turn/duration metrics moved to the "rich" statusline format
// and are no longer read here.
func TestChromeBarReadStateFile(t *testing.T) {
	dir := t.TempDir()

	s := &state.State{
		Agents: []state.AgentState{
			{
				Name:       "go-engineer",
				AgentName:  "go-engineer",
				WindowID:   "@1",
				State:      "working",
				ContextPct: 42,
			},
			{
				Name:      "python-eng",
				AgentName: "python-eng",
				WindowID:  "@2",
				State:     "idle",
			},
		},
		Timestamp:      time.Now(),
		ActiveWindowID: "@1",
		PrefixActive:   true,
	}
	path := writeTestStateFile(t, dir, s)
	state.SetStatePathForTest(path)

	m := NewChromeBarModel("@1", path)
	m.shellWindowID = "@0" // pre-seed for test; avoids live tmux lookup
	m.readStateFile()

	// Tabs should include the shell tab (prepended) plus both agents = 3 total.
	if len(m.tabs) != 3 {
		t.Fatalf("tabs = %d, want 3 (shell + 2 agents)", len(m.tabs))
	}
	// Shell tab must be first.
	if m.tabs[0].Name != "shell" || m.tabs[0].WindowID != "@0" {
		t.Errorf("tabs[0] = {Name:%q WindowID:%q}, want {Name:\"shell\" WindowID:\"@0\"}", m.tabs[0].Name, m.tabs[0].WindowID)
	}

	// Active window.
	if m.activeWindowID != "@1" {
		t.Errorf("activeWindowID = %q, want '@1'", m.activeWindowID)
	}
	if !m.prefixActive {
		t.Error("prefixActive = false, want true")
	}

	// Per-window ctx% for our window (@1) -- used by the warn/critical toast.
	if m.ctxPct != 42 {
		t.Errorf("ctxPct = %d, want 42", m.ctxPct)
	}
}

// TestChromeBarReadStateFileActiveTabFlag verifies that the IsActive flag is
// set correctly on each tab based on the ActiveWindowID in the snapshot.
func TestChromeBarReadStateFileActiveTabFlag(t *testing.T) {
	dir := t.TempDir()

	s := &state.State{
		Agents: []state.AgentState{
			{Name: "alpha", AgentName: "alpha", WindowID: "@1", State: "working"},
			{Name: "beta", AgentName: "beta", WindowID: "@2", State: "idle"},
		},
		Timestamp:      time.Now(),
		ActiveWindowID: "@2",
	}
	path := writeTestStateFile(t, dir, s)
	state.SetStatePathForTest(path)

	m := NewChromeBarModel("@1", path)
	m.readStateFile()

	// shell + 2 agents = 3 tabs.
	if len(m.tabs) != 3 {
		t.Fatalf("tabs = %d, want 3", len(m.tabs))
	}
	for _, tab := range m.tabs {
		wantActive := tab.WindowID == "@2"
		if tab.IsActive != wantActive {
			t.Errorf("tab %q IsActive = %v, want %v", tab.Name, tab.IsActive, wantActive)
		}
	}
}

// TestChromeBarReadStateFileDoneState verifies that agents with Done=true get
// state "done" in the tab list.
func TestChromeBarReadStateFileDoneState(t *testing.T) {
	dir := t.TempDir()

	s := &state.State{
		Agents: []state.AgentState{
			{Name: "finished", AgentName: "finished", WindowID: "@3", State: "idle", Done: true},
		},
		Timestamp: time.Now(),
	}
	path := writeTestStateFile(t, dir, s)
	state.SetStatePathForTest(path)

	m := NewChromeBarModel("@3", path)
	m.readStateFile()

	// shell + 1 agent = 2 tabs.
	if len(m.tabs) != 2 {
		t.Fatalf("tabs = %d, want 2 (shell + 1 agent)", len(m.tabs))
	}
	// Shell is tabs[0]; the agent tab is tabs[1].
	if m.tabs[1].State != "done" {
		t.Errorf("tab state = %q, want 'done'", m.tabs[1].State)
	}
}

// TestReadStateFileShellTabFirst verifies that after readStateFile() the shell
// tab appears first in m.tabs with Name "shell", WindowID "@0", and State "idle".
func TestReadStateFileShellTabFirst(t *testing.T) {
	dir := t.TempDir()

	s := &state.State{
		Agents: []state.AgentState{
			{Name: "go-engineer", AgentName: "go-engineer", WindowID: "@1", State: "working"},
		},
		Timestamp:      time.Now(),
		ActiveWindowID: "@1",
	}
	path := writeTestStateFile(t, dir, s)
	state.SetStatePathForTest(path)

	m := NewChromeBarModel("@1", path)
	m.shellWindowID = "@0" // pre-seed for test; avoids live tmux lookup
	m.readStateFile()

	if len(m.tabs) == 0 {
		t.Fatal("tabs is empty after readStateFile")
	}
	shell := m.tabs[0]
	if shell.Name != "shell" {
		t.Errorf("tabs[0].Name = %q, want \"shell\"", shell.Name)
	}
	if shell.WindowID != "@0" {
		t.Errorf("tabs[0].WindowID = %q, want \"@0\"", shell.WindowID)
	}
	if shell.State != "idle" {
		t.Errorf("tabs[0].State = %q, want \"idle\"", shell.State)
	}
}

// TestReadStateFileShellTabHighlightWhenActive verifies that the shell tab is
// marked IsActive when the active window matches the shell window's real ID.
func TestReadStateFileShellTabHighlightWhenActive(t *testing.T) {
	dir := t.TempDir()

	// Simulate the shell window having ID "@5" (not @0) — realistic when the
	// tmux server has been running and ID counters have advanced.
	const shellWindowID = "@5"

	s := &state.State{
		Agents: []state.AgentState{
			{Name: "go-engineer", AgentName: "go-engineer", WindowID: "@6", State: "working"},
		},
		Timestamp:      time.Now(),
		ActiveWindowID: shellWindowID, // user switched to the shell window
	}
	path := writeTestStateFile(t, dir, s)
	state.SetStatePathForTest(path)

	m := NewChromeBarModel("@6", path)
	m.shellWindowID = shellWindowID // pre-seed the real shell window ID
	m.readStateFile()

	if len(m.tabs) == 0 {
		t.Fatal("tabs is empty after readStateFile")
	}
	shell := m.tabs[0]
	if shell.Name != "shell" {
		t.Fatalf("tabs[0].Name = %q, want \"shell\"", shell.Name)
	}
	if !shell.IsActive {
		t.Errorf("shell tab IsActive = false, want true when active window = %q", shellWindowID)
	}
	// Agent tab should not be active.
	if m.tabs[1].IsActive {
		t.Errorf("agent tab IsActive = true, want false when shell is active")
	}
}

// TestChromeBarStateFileChangedMsgReturnsWatchCmd verifies that a
// stateFileChangedMsg triggers a readStateFile and returns a new watchStateFile cmd.
func TestChromeBarStateFileChangedMsgReturnsWatchCmd(t *testing.T) {
	dir := t.TempDir()

	s := &state.State{
		Agents:    []state.AgentState{{Name: "go-engineer", AgentName: "go-engineer", WindowID: "@1", State: "idle"}},
		Timestamp: time.Now(),
	}
	path := writeTestStateFile(t, dir, s)
	state.SetStatePathForTest(path)

	m := NewChromeBarModel("@1", path)
	_, cmd := m.Update(stateFileChangedMsg{})
	if cmd == nil {
		t.Error("Update(stateFileChangedMsg) should return a non-nil command (new watchStateFile)")
	}
}

// TestChromeBarActiveWindowChangedMsg verifies that activeWindowChangedMsg reads
// the active-window file and updates m.activeWindowID + the IsActive flag on tabs.
func TestChromeBarActiveWindowChangedMsg(t *testing.T) {
	dir := t.TempDir()

	// Write a state.json so statePath is valid (required for the watcher restart).
	s := &state.State{
		Agents: []state.AgentState{
			{Name: "alpha", AgentName: "alpha", WindowID: "@1", State: "working"},
			{Name: "beta", AgentName: "beta", WindowID: "@2", State: "idle"},
		},
		Timestamp:      time.Now(),
		ActiveWindowID: "@1",
	}
	statePath := writeTestStateFile(t, dir, s)

	// Write the active-window file pointing at @2.
	activeWindowPath := filepath.Join(dir, "active-window")
	if err := os.WriteFile(activeWindowPath, []byte("@2\n"), 0o644); err != nil {
		t.Fatalf("write active-window: %v", err)
	}

	state.SetStatePathForTest(statePath)
	m := NewChromeBarModel("@1", statePath)
	m.tabs = []TabInfo{
		{Name: "alpha", WindowID: "@1", IsActive: true},
		{Name: "beta", WindowID: "@2", IsActive: false},
	}
	m.activeWindowID = "@1"

	newModel, cmd := m.Update(activeWindowChangedMsg{})
	updated, ok := newModel.(ChromeBarModel)
	if !ok {
		t.Fatal("Update(activeWindowChangedMsg) did not return ChromeBarModel")
	}
	if cmd == nil {
		t.Error("Update(activeWindowChangedMsg) should return a non-nil watchStateFile cmd")
	}
	if updated.activeWindowID != "@2" {
		t.Errorf("activeWindowID = %q, want '@2'", updated.activeWindowID)
	}
	for _, tab := range updated.tabs {
		wantActive := tab.WindowID == "@2"
		if tab.IsActive != wantActive {
			t.Errorf("tab %q IsActive = %v, want %v", tab.Name, tab.IsActive, wantActive)
		}
	}
}

// TestChromeBarStartWatcherActiveWindow verifies that the persistent watcher
// delivers an activeWindowChangedMsg when the active-window file is written,
// and that the same channel is reused (no watcher recreation).
func TestChromeBarStartWatcherActiveWindow(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write state.json: %v", err)
	}
	activeWindowPath := filepath.Join(dir, "active-window")

	m := NewChromeBarModel("@1", statePath)
	ch := m.watchEvents

	// Give the watcher goroutine a moment to set up the inotify/kqueue watch.
	time.Sleep(50 * time.Millisecond)

	if err := os.WriteFile(activeWindowPath, []byte("@3\n"), 0o644); err != nil {
		t.Fatalf("write active-window: %v", err)
	}

	done := make(chan tea.Msg, 1)
	go func() { done <- waitForStateChange(ch)() }()

	select {
	case msg := <-done:
		if _, ok := msg.(activeWindowChangedMsg); !ok {
			t.Errorf("got %T, want activeWindowChangedMsg", msg)
		}
	case <-time.After(2 * time.Second):
		t.Error("persistent watcher did not deliver activeWindowChangedMsg within 2s")
	}

	// Channel must be the same — watcher was not recreated.
	if m.watchEvents != ch {
		t.Error("watchEvents channel changed after event — watcher was unexpectedly recreated")
	}
}

// TestChromeBarPersistentWatcherTwoEvents verifies that writing state.json twice
// produces two stateFileChangedMsg values on the same channel without growing
// the watcher count.
func TestChromeBarPersistentWatcherTwoEvents(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write state.json: %v", err)
	}

	m := NewChromeBarModel("@1", statePath)
	ch := m.watchEvents

	// Give the watcher goroutine a moment to register the directory watch.
	time.Sleep(50 * time.Millisecond)

	receive := func(label string) tea.Msg {
		t.Helper()
		done := make(chan tea.Msg, 1)
		go func() { done <- waitForStateChange(ch)() }()
		select {
		case msg := <-done:
			return msg
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: timed out waiting for message", label)
			return nil
		}
	}

	// First write.
	if err := os.WriteFile(statePath, []byte(`{"agents":[]}`), 0o644); err != nil {
		t.Fatalf("write state.json (1): %v", err)
	}
	msg1 := receive("first write")
	if _, ok := msg1.(stateFileChangedMsg); !ok {
		t.Errorf("first event: got %T, want stateFileChangedMsg", msg1)
	}

	// Second write.
	if err := os.WriteFile(statePath, []byte(`{"agents":[]}`), 0o644); err != nil {
		t.Fatalf("write state.json (2): %v", err)
	}
	msg2 := receive("second write")
	if _, ok := msg2.(stateFileChangedMsg); !ok {
		t.Errorf("second event: got %T, want stateFileChangedMsg", msg2)
	}

	// The channel is the same — no new watcher was created.
	if m.watchEvents != ch {
		t.Error("watchEvents channel changed between events — watcher was recreated")
	}
}

// TestReadStateFilePreferActiveWindowFile verifies that readStateFile() uses the
// active-window file value when it differs from state.json's ActiveWindowID.
func TestReadStateFilePreferActiveWindowFile(t *testing.T) {
	dir := t.TempDir()

	// state.json has ActiveWindowID "@1" but the hook already wrote "@2".
	s := &state.State{
		Agents: []state.AgentState{
			{Name: "alpha", AgentName: "alpha", WindowID: "@1", State: "idle"},
			{Name: "beta", AgentName: "beta", WindowID: "@2", State: "idle"},
		},
		Timestamp:      time.Now(),
		ActiveWindowID: "@1",
	}
	statePath := writeTestStateFile(t, dir, s)
	state.SetStatePathForTest(statePath)

	// Write active-window file with the authoritative (newer) value.
	awPath := filepath.Join(dir, "active-window")
	if err := os.WriteFile(awPath, []byte("@2\n"), 0o644); err != nil {
		t.Fatalf("write active-window: %v", err)
	}

	m := NewChromeBarModel("@1", statePath)
	m.readStateFile()

	if m.activeWindowID != "@2" {
		t.Errorf("activeWindowID = %q, want '@2' (active-window file should win)", m.activeWindowID)
	}
	for _, tab := range m.tabs {
		wantActive := tab.WindowID == "@2"
		if tab.IsActive != wantActive {
			t.Errorf("tab %q IsActive = %v, want %v", tab.Name, tab.IsActive, wantActive)
		}
	}
}

// TestReadStateFileNoActiveWindowFile verifies that readStateFile() falls back
// to the state.json ActiveWindowID when the active-window file does not exist.
func TestReadStateFileNoActiveWindowFile(t *testing.T) {
	dir := t.TempDir()

	s := &state.State{
		Agents: []state.AgentState{
			{Name: "alpha", AgentName: "alpha", WindowID: "@1", State: "working"},
			{Name: "beta", AgentName: "beta", WindowID: "@2", State: "idle"},
		},
		Timestamp:      time.Now(),
		ActiveWindowID: "@1",
	}
	statePath := writeTestStateFile(t, dir, s)
	state.SetStatePathForTest(statePath)

	// No active-window file — fallback to state.json.
	m := NewChromeBarModel("@1", statePath)
	m.readStateFile()

	if m.activeWindowID != "@1" {
		t.Errorf("activeWindowID = %q, want '@1' (state.json fallback)", m.activeWindowID)
	}
	for _, tab := range m.tabs {
		wantActive := tab.WindowID == "@1"
		if tab.IsActive != wantActive {
			t.Errorf("tab %q IsActive = %v, want %v", tab.Name, tab.IsActive, wantActive)
		}
	}
}

// ── Context warn/critical toast latching ──────────────

// chromeBarWithCtxPct writes a state.json with a single agent at the given
// ContextPct/ContextTokens/ContextWindow and returns a ChromeBarModel ready
// to receive stateFileChangedMsg.
func chromeBarWithCtxPct(t *testing.T, dir string, ctxPct, ctxTokens, ctxWindow int) *ChromeBarModel {
	t.Helper()
	s := &state.State{
		Agents: []state.AgentState{
			{
				Name:          "go-engineer",
				AgentName:     "go-engineer",
				WindowID:      "@1",
				State:         "working",
				ContextPct:    ctxPct,
				ContextTokens: ctxTokens,
				ContextWindow: ctxWindow,
			},
		},
		Timestamp: time.Now(),
	}
	path := writeTestStateFile(t, dir, s)
	state.SetStatePathForTest(path)
	return NewChromeBarModel("@1", path)
}

// toastFromCmd safely extracts a ToastMsg from the tea.Cmd returned by the
// stateFileChangedMsg handler, without ever risking a hang.
//
// The handler always returns tea.Batch(waitForStateChange(m.watchEvents),
// toastCmd). tea.Batch collapses to a *single*, unwrapped Cmd when only one
// argument is non-nil — so when no toast fired, the returned Cmd IS
// waitForStateChange itself, which blocks forever reading an unbuffered
// event channel nothing will ever write to in this test. When a toast DID
// fire, the returned Cmd is a fast, non-blocking closure that immediately
// returns a tea.BatchMsg (the pending sub-commands, not their results) —
// this executes fast in either case.
//
// So: invoke cmd() on a goroutine with a short timeout. A fast return means
// a toast fired (batch of 2: waitForStateChange, then the toast); a timeout
// means no toast fired (we were blocked on the bare waitForStateChange).
func toastFromCmd(cmd tea.Cmd) (ToastMsg, bool) {
	if cmd == nil {
		return ToastMsg{}, false
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		batch, ok := msg.(tea.BatchMsg)
		if !ok {
			toast, ok := msg.(ToastMsg)
			return toast, ok
		}
		if len(batch) == 0 {
			return ToastMsg{}, false
		}
		// Structurally, the toast cmd is always the LAST entry (see
		// tea.Batch(waitForStateChange(...), toastCmd) in the handler) — a
		// pure closure, safe to call directly. We deliberately do not call
		// any earlier entry: index 0 is waitForStateChange, which blocks.
		last := batch[len(batch)-1]
		if last == nil {
			return ToastMsg{}, false
		}
		toast, ok := last().(ToastMsg)
		return toast, ok
	case <-time.After(200 * time.Millisecond):
		return ToastMsg{}, false
	}
}

// toastMsgFrom is toastFromCmd, but fails the test when no toast is found.
func toastMsgFrom(t *testing.T, cmd tea.Cmd) ToastMsg {
	t.Helper()
	toast, ok := toastFromCmd(cmd)
	if !ok {
		t.Fatal("expected a toast to fire, got none")
	}
	return toast
}

// TestChromeBarContextWarnReArmsAfterDrop verifies that the warn toast fires
// once when ctx% crosses the warn threshold, does NOT re-fire on subsequent
// ticks while still above (threshold-10), and fires again once ctx% has
// dropped back down and crossed the threshold a second time — regression
// coverage for the one-shot latch that never re-armed after /clear.
func TestChromeBarContextWarnReArmsAfterDrop(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()

	m := chromeBarWithCtxPct(t, dir, 80, 160_000, 200_000) // 80% >= warn(75)

	newModel, cmd := m.Update(stateFileChangedMsg{})
	updated := newModel.(ChromeBarModel)
	toast := toastMsgFrom(t, cmd)
	if !strings.Contains(toast.msg, "80%") {
		t.Errorf("first warn toast = %q, want it to mention 80%%", toast.msg)
	}
	if !updated.ctxWarnFired {
		t.Error("ctxWarnFired = false after crossing warn threshold, want true")
	}

	// Still above threshold — must not re-fire.
	newModel2, cmd2 := updated.Update(stateFileChangedMsg{})
	updated2 := newModel2.(ChromeBarModel)
	if toast2, fired := toastFromCmd(cmd2); fired {
		t.Errorf("expected no toast while still above warn threshold, got one: %q", toast2.msg)
	}

	// Drop below warnPct-10 (i.e. below 65) — this is a new session (e.g.
	// after /clear) and the latch must re-arm, but re-arming alone must not
	// itself fire a toast (ctxPct is now below the warn threshold).
	dropped := chromeBarWithCtxPct(t, dir, 10, 20_000, 200_000)
	dropped.ctxWarnFired = updated2.ctxWarnFired
	dropped.ctxCritFired = updated2.ctxCritFired
	newModel3, cmd3 := dropped.Update(stateFileChangedMsg{})
	updated3 := newModel3.(ChromeBarModel)
	if toast3, fired := toastFromCmd(cmd3); fired {
		t.Errorf("expected no toast while dropping below threshold, got one: %q", toast3.msg)
	}
	if updated3.ctxWarnFired {
		t.Error("ctxWarnFired should have re-armed (false) after dropping below warnPct-10")
	}

	// Cross the warn threshold again — must fire a second time now that the
	// latch has re-armed.
	risen := chromeBarWithCtxPct(t, dir, 80, 160_000, 200_000)
	risen.ctxWarnFired = updated3.ctxWarnFired
	risen.ctxCritFired = updated3.ctxCritFired
	_, cmd4 := risen.Update(stateFileChangedMsg{})
	toast2 := toastMsgFrom(t, cmd4)
	if !strings.Contains(toast2.msg, "80%") {
		t.Errorf("second warn toast = %q, want it to mention 80%%", toast2.msg)
	}
}

// TestChromeBarContextCriticalOnlyAboveCriticalThreshold verifies that the
// critical toast only fires at/above ContextCriticalPct (default 90), not at
// the warn threshold, and that its copy matches the documented critical
// message.
func TestChromeBarContextCriticalOnlyAboveCriticalThreshold(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()

	// 80% — above warn(75), below critical(90): warn fires, critical does not.
	warnOnly := chromeBarWithCtxPct(t, dir, 80, 160_000, 200_000)
	newModel, cmd := warnOnly.Update(stateFileChangedMsg{})
	updated := newModel.(ChromeBarModel)
	toast := toastMsgFrom(t, cmd)
	if toast.level != ToastWarn {
		t.Errorf("level at 80%% = %v, want ToastWarn", toast.level)
	}
	if updated.ctxCritFired {
		t.Error("ctxCritFired = true at 80%%, want false (below critical threshold)")
	}

	// 95% — above critical(90): critical fires with the documented copy.
	critical := chromeBarWithCtxPct(t, dir, 95, 190_000, 200_000)
	critical.ctxWarnFired = updated.ctxWarnFired
	critical.ctxCritFired = updated.ctxCritFired
	newModel2, cmd2 := critical.Update(stateFileChangedMsg{})
	updated2 := newModel2.(ChromeBarModel)
	toast2 := toastMsgFrom(t, cmd2)
	if toast2.level != ToastError {
		t.Errorf("level at 95%% = %v, want ToastError", toast2.level)
	}
	wantMsg := "context at 95% — compaction imminent, start a new session"
	if toast2.msg != wantMsg {
		t.Errorf("critical toast = %q, want %q", toast2.msg, wantMsg)
	}
	if !updated2.ctxCritFired {
		t.Error("ctxCritFired = false after crossing critical threshold, want true")
	}
	if !updated2.ctxWarnFired {
		t.Error("ctxWarnFired should also be true once critical has fired")
	}
}

// TestChromeBarToastReplacesKeybindLine verifies that an active toast replaces
// line 1 (keybinds), not line 2 (tabs), and that View() still returns exactly
// 2 lines while the toast is showing (the toast used to overlay the
// metrics line, which moved to the statusline).
func TestChromeBarToastReplacesKeybindLine(t *testing.T) {
	m := newTestChromeBar(testTabs())
	toastCmd := NewToast("dispatch complete", WithToastLevel(ToastInfo))
	msg := toastCmd()
	updated, _ := m.Update(msg)
	cm := updated.(ChromeBarModel)

	out := cm.View().Content
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("View() with active toast should still have exactly 2 lines, got: %q", out)
	}
	lines := strings.SplitN(out, "\n", 2)
	if !strings.Contains(lines[0], "dispatch complete") {
		t.Errorf("toast should replace line 1 (keybinds); got line 1: %q", lines[0])
	}
	if !strings.Contains(lines[1], "go-engineer") {
		t.Errorf("line 2 (tabs) should be unaffected by the toast; got: %q", lines[1])
	}
}

func TestTabSpans(t *testing.T) {
	tabs := []TabInfo{
		{Name: "shell", State: "idle", WindowID: "@9"},
		{Name: "go-engineer", State: "working", Ticket: 12, WindowID: "@1", IsActive: true},
		{Name: "py", State: "working", WindowID: "@2"},
		{Name: "blk", State: "blocked", Ticket: 7, WindowID: "@3"},
		{Name: "dn", State: "done", WindowID: "@4"},
	}
	m := newTestChromeBar(tabs)
	st := newTabStyles()
	spans := m.tabSpans()
	if len(spans) != len(tabs) {
		t.Fatalf("got %d spans, want %d", len(spans), len(tabs))
	}
	badge, labels, _ := tabLayout(tabs, m.spinner.View(), st)
	written := badge
	for i, tab := range tabs {
		t.Run(tab.Name, func(t *testing.T) {
			written += st.sep.Render(" ")
			wantStart := lipgloss.Width(written)
			written += labels[i]
			wantEnd := lipgloss.Width(written)
			if spans[i].WindowID != tab.WindowID || spans[i].Start != wantStart || spans[i].End != wantEnd {
				t.Errorf("span = %+v, want [%d,%d) for %s", spans[i], wantStart, wantEnd, tab.WindowID)
			}
		})
	}
	// The rendered line's left part must be exactly what the spans describe.
	line := m.renderTabLine(200)
	if got := lipgloss.Width(line); got != 200 {
		t.Errorf("line width = %d, want 200", got)
	}
}

func TestTabAt(t *testing.T) {
	m := newTestChromeBar(testTabs())
	spans := m.tabSpans()
	first, last := spans[0], spans[len(spans)-1]
	badgeW := first.Start - 1
	tests := []struct {
		name   string
		x      int
		wantID string
		wantOK bool
	}{
		{"first cell of first tab", first.Start, "@1", true},
		{"last cell of first tab", first.End - 1, "@1", true},
		{"separator after first tab", first.End, "", false},
		{"first cell of second tab", spans[1].Start, "@2", true},
		{"last cell of last tab", last.End - 1, "@4", true},
		{"badge", badgeW - 1, "", false},
		{"separator before first tab", first.Start - 1, "", false},
		{"fill", last.End, "", false},
		{"clock", 195, "", false},
		{"negative", -1, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := m.tabAt(tt.x)
			if id != tt.wantID || ok != tt.wantOK {
				t.Errorf("tabAt(%d) = %q,%v want %q,%v", tt.x, id, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestClickTarget(t *testing.T) {
	m := newTestChromeBar(testTabs())
	x := m.tabSpans()[1].Start
	tests := []struct {
		name       string
		msg        tea.MouseClickMsg
		wantTarget string
		wantAct    bool
	}{
		{"left on tab", tea.MouseClickMsg{X: x, Y: 1, Button: tea.MouseLeft}, "@2", true},
		{"left on line 1 only refocuses", tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft}, "", true},
		{"left on fill only refocuses", tea.MouseClickMsg{X: 150, Y: 1, Button: tea.MouseLeft}, "", true},
		{"wheel ignored", tea.MouseClickMsg{X: x, Y: 1, Button: tea.MouseWheelUp}, "", false},
		{"right ignored", tea.MouseClickMsg{X: x, Y: 1, Button: tea.MouseRight}, "", false},
		{"middle ignored", tea.MouseClickMsg{X: x, Y: 1, Button: tea.MouseMiddle}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, act := m.clickTarget(tt.msg)
			if target != tt.wantTarget || act != tt.wantAct {
				t.Errorf("got %q,%v want %q,%v", target, act, tt.wantTarget, tt.wantAct)
			}
			if !tt.wantAct {
				if _, cmd := m.Update(tt.msg); cmd != nil {
					t.Error("ignored click produced a command")
				}
			}
		})
	}
}

func TestChromeBarViewEnablesMouse(t *testing.T) {
	if got := newTestChromeBar(testTabs()).View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("MouseMode = %v, want cell motion", got)
	}
}

func TestKeyPressNeverQuitsAndRefocuses(t *testing.T) {
	m := newTestChromeBar(testTabs())
	m.windowID = "" // focusAgentPane is a no-op, so no tmux calls are made
	for _, key := range []tea.KeyPressMsg{
		{Code: 'c', Mod: tea.ModCtrl},
		{Code: 'q', Text: "q"},
		{Code: tea.KeyEnter},
	} {
		t.Run(key.String(), func(t *testing.T) {
			_, cmd := m.Update(key)
			if cmd == nil {
				t.Fatal("key press returned nil cmd, want refocus cmd")
			}
			if _, quit := cmd().(tea.QuitMsg); quit {
				t.Error("key press quit the chrome bar")
			}
		})
	}
}
