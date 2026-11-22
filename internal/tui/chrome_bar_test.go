package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/tmux"
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

// ── windowstate-driven view tests ─────────────────────────────────────────────

// chromeFixture is a runtime dir with windowstate records and an optional
// active-window file, plus a fake tmux window list. No state.json exists.
type chromeFixture struct {
	dir       string
	wins      []tmux.WindowInfo
	listErr   error
	listCalls int
}

func newChromeFixture(t *testing.T, wins []tmux.WindowInfo) *chromeFixture {
	t.Helper()
	isolateConfig(t)
	return &chromeFixture{dir: t.TempDir(), wins: wins}
}

func (f *chromeFixture) writeRecord(t *testing.T, id, body string) {
	t.Helper()
	dir := filepath.Join(f.dir, "windows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Temp file plus rename, like windowstate's writer: the watcher relies
	// on it (a directory-level change).
	tmp := filepath.Join(dir, "."+id+".json.tmp")
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, id+".json")); err != nil {
		t.Fatal(err)
	}
}

func (f *chromeFixture) setActive(t *testing.T, id string) {
	t.Helper()
	tmp := filepath.Join(f.dir, ".active-window.tmp")
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(f.dir, "active-window")); err != nil {
		t.Fatal(err)
	}
}

func (f *chromeFixture) model(windowID string) *ChromeBarModel {
	return newChromeBarModel(windowID, f.dir,
		func() ([]tmux.WindowInfo, error) { f.listCalls++; return f.wins, f.listErr },
		func() bool { return false })
}

func tabByID(tabs []TabInfo, id string) (TabInfo, bool) {
	for _, tb := range tabs {
		if tb.WindowID == id {
			return tb, true
		}
	}
	return TabInfo{}, false
}

// TestChromeBarRebuildWithoutStateJSON: tabs, state, ctx% and the active
// highlight all come from tmux + windowstate + the active-window file.
func TestChromeBarRebuildWithoutStateJSON(t *testing.T) {
	f := newChromeFixture(t, []tmux.WindowInfo{
		{ID: "@5", Name: "shell"},
		{ID: "@1", Name: "● go-engineer"},
		{ID: "@2", Name: "python-eng"},
		{ID: "@3", Name: "✓ finished"},
		{ID: "@4", Name: "improve~go-engineer"},
	})
	f.writeRecord(t, "@1", `{"state":"working","instance":"go-engineer","ticket":"12","ctx_pct":42}`)
	f.writeRecord(t, "@3", `{"state":"idle","instance":"finished","done":true}`)
	f.writeRecord(t, "@9", `{"state":"working","instance":"ghost"}`) // no such window
	f.setActive(t, "@1")

	m := f.model("@1")
	if !m.rebuild(true) {
		t.Fatal("first rebuild should report a change")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "state.json")); err == nil {
		t.Fatal("fixture must not have a state.json")
	}

	var names []string
	for _, tb := range m.tabs {
		names = append(names, tb.Name)
	}
	if got, want := strings.Join(names, ","), "shell,go-engineer,python-eng,finished"; got != want {
		t.Fatalf("tabs = %s, want %s (shell first, tmux order, no ghost, no improve~)", got, want)
	}
	if tb, _ := tabByID(m.tabs, "@5"); tb.State != "idle" {
		t.Errorf("shell state = %q", tb.State)
	}
	if tb, _ := tabByID(m.tabs, "@1"); tb.State != "working" || tb.Ticket != 12 || tb.CtxPct != 42 || !tb.IsActive {
		t.Errorf("@1 tab = %+v", tb)
	}
	if tb, _ := tabByID(m.tabs, "@2"); tb.State != "idle" {
		t.Errorf("untracked tab = %+v, want idle", tb)
	}
	if tb, _ := tabByID(m.tabs, "@3"); tb.State != "done" {
		t.Errorf("done tab = %+v, want done", tb)
	}
	if m.ctxPct != 42 || m.activeWindowID != "@1" {
		t.Errorf("ctxPct=%d active=%q", m.ctxPct, m.activeWindowID)
	}
}

func TestChromeBarRebuildDiffAndRelist(t *testing.T) {
	f := newChromeFixture(t, []tmux.WindowInfo{{ID: "@1", Name: "alpha"}})
	f.writeRecord(t, "@1", `{"state":"working","instance":"alpha"}`)
	f.setActive(t, "@1")
	m := f.model("@1")

	if !m.rebuild(true) {
		t.Fatal("first rebuild should change")
	}
	if f.listCalls != 1 {
		t.Fatalf("list calls = %d, want 1", f.listCalls)
	}

	// Nothing changed (e.g. a statusline push of unrendered fields).
	f.writeRecord(t, "@1", `{"state":"working","instance":"alpha","cost_usd":3.5,"last_text":"x"}`)
	if m.rebuild(false) {
		t.Error("unrendered field change must not report a re-render")
	}
	if f.listCalls != 1 {
		t.Errorf("rebuild(false) re-ran list-windows (%d calls)", f.listCalls)
	}

	// A rendered field changed.
	f.writeRecord(t, "@1", `{"state":"blocked","instance":"alpha"}`)
	if !m.rebuild(false) {
		t.Error("state change must report a re-render")
	}
	if tb, _ := tabByID(m.tabs, "@1"); tb.State != "blocked" {
		t.Errorf("state = %q", tb.State)
	}

	// A new window only appears after a relist.
	f.wins = append(f.wins, tmux.WindowInfo{ID: "@2", Name: "beta"})
	if m.rebuild(false); len(m.tabs) != 2 { // shell + alpha
		t.Errorf("tabs = %d before relist, want 2", len(m.tabs))
	}
	if !m.rebuild(true) || len(m.tabs) != 3 {
		t.Errorf("tabs = %d after relist, want 3", len(m.tabs))
	}
}

// TestChromeBarRebuildListFailureKeepsLastRender: tmux going away must not
// blank or crash the bar.
func TestChromeBarRebuildListFailureKeepsLastRender(t *testing.T) {
	f := newChromeFixture(t, []tmux.WindowInfo{{ID: "@1", Name: "alpha"}})
	f.writeRecord(t, "@1", `{"state":"working","instance":"alpha"}`)
	m := f.model("@1")
	m.rebuild(true)
	before := slices.Clone(m.tabs)

	f.listErr = errors.New("no server")
	if m.rebuild(true) {
		t.Error("failed list must report no change")
	}
	if !slices.Equal(before, m.tabs) {
		t.Errorf("tabs changed on list failure: %+v", m.tabs)
	}
}

func TestChromeBarOnlyShellWindow(t *testing.T) {
	f := newChromeFixture(t, []tmux.WindowInfo{{ID: "@1", Name: "shell"}})
	m := f.model("@1")
	m.rebuild(true)
	if len(m.tabs) != 1 || m.tabs[0].Name != "shell" {
		t.Errorf("tabs = %+v, want only shell", m.tabs)
	}
}

// TestChromeBarWindowsChangedMsgReturnsWatchCmd: the handler re-arms the wait.
func TestChromeBarWindowsChangedMsgReturnsWatchCmd(t *testing.T) {
	f := newChromeFixture(t, nil)
	m := f.model("@1")
	_, cmd := m.Update(windowsChangedMsg{})
	if cmd == nil {
		t.Error("Update(windowsChangedMsg) should return a non-nil command")
	}
}

// TestChromeBarActiveWindowChangedMsg: the active-window file drives the
// highlight without touching tmux.
func TestChromeBarActiveWindowChangedMsg(t *testing.T) {
	f := newChromeFixture(t, nil)
	f.setActive(t, "@2")
	m := f.model("@1")
	m.tabs = []TabInfo{
		{Name: "alpha", WindowID: "@1", IsActive: true},
		{Name: "beta", WindowID: "@2"},
	}
	m.activeWindowID = "@1"

	newModel, cmd := m.Update(activeWindowChangedMsg{})
	updated := newModel.(ChromeBarModel)
	if cmd == nil {
		t.Error("should return a non-nil watch cmd")
	}
	if updated.activeWindowID != "@2" {
		t.Errorf("activeWindowID = %q, want @2", updated.activeWindowID)
	}
	for _, tab := range updated.tabs {
		if want := tab.WindowID == "@2"; tab.IsActive != want {
			t.Errorf("tab %q IsActive = %v, want %v", tab.Name, tab.IsActive, want)
		}
	}
	if f.listCalls != 0 {
		t.Error("active-window change must not list windows")
	}
}

func TestChromeBarPrefixPoll(t *testing.T) {
	f := newChromeFixture(t, nil)
	polls := 0
	m := newChromeBarModel("@1", f.dir, func() ([]tmux.WindowInfo, error) { return nil, nil },
		func() bool { polls++; return true })

	// Inactive window: tick must not poll tmux, and ends the chain (see
	// TestChromeBarIdleTickChainsStop).
	m.activeWindowID = "@2"
	_, cmd := m.Update(prefixTickMsg{})
	if cmd != nil {
		t.Fatal("inactive tick must not reschedule")
	}
	if polls != 0 {
		t.Errorf("polled %d times for an inactive window", polls)
	}

	// Active window: tick returns a batch whose poll cmd yields the result.
	m.activeWindowID = "@1"
	_, cmd = m.Update(prefixTickMsg{})
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("active tick = %T, want batch of 2", cmd())
	}
	res, ok := batch[1]().(prefixResultMsg)
	if !ok || !res.active || polls != 1 {
		t.Fatalf("poll result = %#v polls=%d", res, polls)
	}
	newModel, _ := m.Update(res)
	if !newModel.(ChromeBarModel).prefixActive {
		t.Error("prefixActive not set from poll result")
	}
}

// ── Context warn/critical toast latching ──────────────

// chromeBarWithCtxPct builds a model whose own window (@1) has the given
// ctx% pushed into windowstate, ready to receive windowsChangedMsg.
func chromeBarWithCtxPct(t *testing.T, _ string, ctxPct, ctxTokens, ctxWindow int) *ChromeBarModel {
	t.Helper()
	f := newChromeFixture(t, []tmux.WindowInfo{{ID: "@1", Name: "go-engineer"}})
	f.writeRecord(t, "@1", fmt.Sprintf(`{"state":"working","instance":"go-engineer","ctx_pct":%d,"context_tokens":%d,"context_window_size":%d}`, ctxPct, ctxTokens, ctxWindow))
	return f.model("@1")
}

// toastFromCmd safely extracts a ToastMsg from the tea.Cmd returned by the
// windowsChangedMsg handler, without ever risking a hang.
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

	newModel, cmd := m.Update(windowsChangedMsg{})
	updated := newModel.(ChromeBarModel)
	toast := toastMsgFrom(t, cmd)
	if !strings.Contains(toast.msg, "80%") {
		t.Errorf("first warn toast = %q, want it to mention 80%%", toast.msg)
	}
	if !updated.ctxWarnFired {
		t.Error("ctxWarnFired = false after crossing warn threshold, want true")
	}

	// Still above threshold — must not re-fire.
	newModel2, cmd2 := updated.Update(windowsChangedMsg{})
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
	newModel3, cmd3 := dropped.Update(windowsChangedMsg{})
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
	_, cmd4 := risen.Update(windowsChangedMsg{})
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
	newModel, cmd := warnOnly.Update(windowsChangedMsg{})
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
	newModel2, cmd2 := critical.Update(windowsChangedMsg{})
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

// TestChromeBarRebuildActiveTabFlag: IsActive follows the active-window file.
func TestChromeBarRebuildActiveTabFlag(t *testing.T) {
	f := newChromeFixture(t, []tmux.WindowInfo{{ID: "@1", Name: "alpha"}, {ID: "@2", Name: "beta"}})
	f.writeRecord(t, "@1", `{"state":"working","instance":"alpha"}`)
	f.writeRecord(t, "@2", `{"state":"idle","instance":"beta"}`)
	f.setActive(t, "@2")
	m := f.model("@1")
	m.rebuild(true)
	if len(m.tabs) != 3 {
		t.Fatalf("tabs = %d, want 3 (shell + 2)", len(m.tabs))
	}
	for _, tab := range m.tabs {
		if want := tab.WindowID == "@2"; tab.IsActive != want {
			t.Errorf("tab %q IsActive = %v, want %v", tab.Name, tab.IsActive, want)
		}
	}
	if m.activeWindowID != "@2" {
		t.Errorf("activeWindowID = %q, want @2 (active-window file)", m.activeWindowID)
	}
}

// TestChromeBarRebuildDoneState: done+unseen shows as "done"; done+seen does not.
func TestChromeBarRebuildDoneState(t *testing.T) {
	f := newChromeFixture(t, []tmux.WindowInfo{{ID: "@3", Name: "finished"}, {ID: "@4", Name: "looked"}})
	f.writeRecord(t, "@3", `{"state":"idle","instance":"finished","done":true}`)
	f.writeRecord(t, "@4", `{"state":"idle","instance":"looked","done":true,"seen":true}`)
	f.setActive(t, "@1")
	m := f.model("@3")
	m.rebuild(true)
	if tb, _ := tabByID(m.tabs, "@3"); tb.State != "done" {
		t.Errorf("unseen done tab state = %q, want done", tb.State)
	}
	if tb, _ := tabByID(m.tabs, "@4"); tb.State != "idle" {
		t.Errorf("seen done tab state = %q, want idle", tb.State)
	}
}

// TestChromeBarRebuildShellTab: the shell tab is first, keeps its real window
// ID from the tmux list, and highlights when it is the active window.
func TestChromeBarRebuildShellTab(t *testing.T) {
	f := newChromeFixture(t, []tmux.WindowInfo{{ID: "@5", Name: "shell"}, {ID: "@6", Name: "go-engineer"}})
	f.writeRecord(t, "@6", `{"state":"working","instance":"go-engineer"}`)
	f.setActive(t, "@5")
	m := f.model("@6")
	m.rebuild(true)
	shell := m.tabs[0]
	if shell.Name != "shell" || shell.WindowID != "@5" || shell.State != "idle" || !shell.IsActive {
		t.Errorf("shell tab = %+v, want first, @5, idle, active", shell)
	}
	if m.tabs[1].IsActive {
		t.Error("agent tab must not be active when shell is")
	}
}

// TestChromeBarIdleTickChainsStop is the regression test for idle bars
// burning CPU: Bubble Tea re-renders on every message, and the spinner
// (12/s) and prefix poll (4/s) used to tick in every bar forever. In an idle,
// inactive bar both chains must end on their first tick, and restart once
// there is something to show.
func TestChromeBarIdleTickChainsStop(t *testing.T) {
	f := newChromeFixture(t, []tmux.WindowInfo{{ID: "@1", Name: "alpha"}, {ID: "@2", Name: "beta"}})
	f.writeRecord(t, "@1", `{"state":"idle","instance":"alpha"}`)
	f.writeRecord(t, "@2", `{"state":"idle","instance":"beta"}`)
	f.setActive(t, "@2")
	m := f.model("@1")
	m.rebuild(true)

	next, cmd := m.Update(m.spinner.Tick())
	if cmd != nil {
		t.Error("spinner tick with no working tab must not reschedule")
	}
	next, cmd = next.Update(prefixTickMsg{})
	if cmd != nil {
		t.Error("prefix tick in an inactive bar must not reschedule")
	}
	idle := next.(ChromeBarModel)
	if idle.spinning || idle.prefixPolling {
		t.Fatalf("spinning=%v prefixPolling=%v, want both stopped", idle.spinning, idle.prefixPolling)
	}

	// Other events while idle do not restart them.
	next, _ = idle.Update(chromeBarClockTickMsg{})
	if c := next.(ChromeBarModel); c.spinning || c.prefixPolling {
		t.Fatal("clock tick restarted an idle chain")
	}

	// A tab starts working: the spinner restarts, once.
	f.writeRecord(t, "@2", `{"state":"working","instance":"beta"}`)
	next, _ = next.Update(windowsChangedMsg{})
	if c := next.(ChromeBarModel); !c.spinning || c.prefixPolling {
		t.Fatalf("after working: spinning=%v prefixPolling=%v, want true/false", c.spinning, c.prefixPolling)
	}
	if got := next.(ChromeBarModel).ensureTicksCmd(); got != nil {
		t.Error("a running spinner must not get a second chain")
	}

	// This window becomes active: the prefix poll restarts.
	f.setActive(t, "@1")
	next, _ = next.Update(activeWindowChangedMsg{})
	if c := next.(ChromeBarModel); !c.prefixPolling {
		t.Fatal("prefix poll did not restart in the active bar")
	}
	if _, cmd = next.Update(prefixTickMsg{}); cmd == nil {
		t.Error("active bar's prefix tick must reschedule")
	}

	// The spinner keeps ticking while a tab works.
	next, _ = next.Update(windowsChangedMsg{})
	if _, cmd = next.Update(next.(ChromeBarModel).spinner.Tick()); cmd == nil {
		t.Error("spinner must keep ticking while a tab is working")
	}
}

// ensureTicksCmd is ensureTicks on a copy, for asserting it starts nothing.
func (m ChromeBarModel) ensureTicksCmd() tea.Cmd { return m.ensureTicks() }

// TestChromeBarThemeFollowsConfigEdit: View no longer reads config on every
// frame, but a theme change in the config file still shows on the next one.
func TestChromeBarThemeFollowsConfigEdit(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	config.SetConfigPathForTest(cfg)
	t.Cleanup(func() { config.SetConfigPathForTest("") })
	m := newTestChromeBar(testTabs())

	renameWrite(t, cfg, `{"theme":"catppuccin-mocha"}`)
	before := m.View().Content
	renameWrite(t, cfg, `{"theme":"bitchx"}`)
	if after := m.View().Content; after == before {
		t.Error("theme change in config did not change the rendered bar")
	}
}
