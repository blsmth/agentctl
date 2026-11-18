package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// liveTestModel is mouseTestModel as the hub pane sees it: the
// pane is only the list column wide.
func liveTestModel(t *testing.T, paneWidth, height int) HerdModel {
	t.Helper()
	m := mouseTestModel(t, paneWidth, height)
	m.liveLeft, m.liveRight = paneWidth, 200
	m.configureSplitPane()
	for i := range m.groups {
		m.groups[i].ProjectDir, m.groups[i].RuntimeDir = "/p", "/p/.agentctl"
	}
	m.rebuildLines()
	return m
}

func TestHerdLiveLeftCols(t *testing.T) {
	tests := []struct {
		total, want int
	}{
		{80, 44},
		{120, 44},
		{160, 50},
		{200, 56},
		{345, 56},
	}
	for _, tt := range tests {
		if got := herdLiveLeftCols(tt.total); got != tt.want {
			t.Errorf("herdLiveLeftCols(%d) = %d, want %d", tt.total, got, tt.want)
		}
	}
}

func TestHerdLiveSplit(t *testing.T) {
	left, right, err := herdLiveSplit(345)
	if err != nil || left != 56 || right != 345-56-1 {
		t.Errorf("split(345) = %d,%d,%v", left, right, err)
	}
	if _, _, err := herdLiveSplit(60); err == nil {
		t.Error("expected too-narrow error")
	}
}

func TestHerdLiveView(t *testing.T) {
	m := mouseTestModel(t, 140, 40)
	out := ansi.Strip(m.render())
	if strings.ContainsAny(out, "╭╮╰╯") {
		t.Errorf("list must not draw a detail panel border:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if len([]rune(line)) > m.liveLeft {
			t.Errorf("line wider than the list column (%d): %q", m.liveLeft, line)
		}
	}
}

func TestHerdLiveHints(t *testing.T) {
	m := liveTestModel(t, 48, 40)
	out := ansi.Strip(m.render())
	for _, want := range []string{"j/k move", "⏎ focus", "? more"} {
		if !strings.Contains(out, want) {
			t.Errorf("hints missing %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"v preview", "v live", "unlink", "open"} {
		if strings.Contains(out, gone) {
			t.Errorf("stale hint %q:\n%s", gone, out)
		}
	}
	if n := m.liveHintRows(); n != 0 {
		t.Errorf("hint bar must stay on one line, got %d extra rows", n)
	}
}

func TestHerdNoViewHint(t *testing.T) {
	m := liveTestModel(t, 48, 40)
	if out := ansi.Strip(m.render()); !strings.Contains(out, herdNoViewHint) {
		t.Errorf("expected hint before any agent was viewed:\n%s", out)
	}
	m.liveShown = "s1|@1"
	if out := ansi.Strip(m.render()); strings.Contains(out, herdNoViewHint) {
		t.Errorf("hint must disappear once an agent was viewed:\n%s", out)
	}
}

func TestHerdListPaddedToFixedHeight(t *testing.T) {
	m := liveTestModel(t, 48, 40)
	a := strings.Count(m.render(), "\n")
	m.groups = m.groups[:1]
	m.rebuildLines()
	if b := strings.Count(m.render(), "\n"); a != b {
		t.Errorf("render height changed with row count: %d vs %d", a, b)
	}
}

func TestHerdLiveClicks(t *testing.T) {
	m := liveTestModel(t, 48, 40)
	top := m.herdListTop()
	w := m.listColWidth()
	if w != 48 {
		t.Fatalf("listColWidth = %d, want 48", w)
	}
	tests := []struct {
		name       string
		x, y       int
		wantIdx    int
		wantAction herdClickAction
	}{
		{"header body selects", 3, top, 0, herdClickSelect},
		{"header + new", w - herdSidebarRightGutter - 2, top, 0, herdClickNew},
		{"subheader ignored", 5, top + 1, 1, herdClickIgnore},
		{"row body selects", 5, top + 2, 2, herdClickSelect},
		{"row close", w - herdSidebarRightGutter - 1, top + 2, 2, herdClickClose},
		{"past the list", 5, top + 40, -1, herdClickIgnore},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, action := m.clickDecision(tt.x, tt.y)
			if idx != tt.wantIdx || action != tt.wantAction {
				t.Errorf("clickDecision(%d,%d) = (%d,%d), want (%d,%d)",
					tt.x, tt.y, idx, action, tt.wantIdx, tt.wantAction)
			}
		})
	}
}

func TestHerdLiveShowCmd_OncePerSelection(t *testing.T) {
	m := NewHerdModel()
	m.selSession, m.selWindowID = "agentctl-a", "@1"
	if m.liveShowCmd() == nil {
		t.Fatal("first selection must hand off to the viewer")
	}
	if m.liveShowCmd() != nil {
		t.Error("same selection must not re-fire")
	}
	m.selWindowID = "@2"
	if m.liveShowCmd() == nil {
		t.Error("changed selection must fire")
	}
}

func TestHerdMaybeShowCmd_HeaderKeepsLastViewed(t *testing.T) {
	m := NewHerdModel()
	m.selSession, m.selWindowID, m.selIsHeader = "agentctl-a", "", true
	m.liveShown = "agentctl-a|@1"
	if cmd := m.maybeShowCmd(); cmd != nil {
		t.Error("a header selection must not touch the viewer")
	}
	if m.liveShown != "agentctl-a|@1" {
		t.Errorf("liveShown changed to %q", m.liveShown)
	}
	m.selSession, m.selWindowID, m.selIsHeader = "", "", false
	if cmd := m.maybeShowCmd(); cmd != nil {
		t.Error("no selection must not touch the viewer")
	}
}

func TestHerdLiveErrorShownAndRetriedOnSelectionChange(t *testing.T) {
	m := liveTestModel(t, 48, 40)
	m.selSession, m.selWindowID = "s1", "@1"
	if m.maybeShowCmd() == nil {
		t.Fatal("first show must fire")
	}
	next, _ := m.Update(herdLiveMsg{key: "s1|@1", err: errors.New("boom")})
	got := next.(HerdModel)
	if got.jumpErr != "boom" {
		t.Errorf("jumpErr = %q, want boom", got.jumpErr)
	}
	if !strings.Contains(ansi.Strip(got.render()), "boom") {
		t.Error("error must show in the status line")
	}
	if got.maybeShowCmd() != nil {
		t.Error("failed show must not retry every tick")
	}
	got.selWindowID = "@2"
	if got.maybeShowCmd() == nil {
		t.Error("selection change must retry")
	}
	next, _ = got.Update(herdLiveMsg{key: "s1|@2", left: 44, right: 70})
	if next.(HerdModel).jumpErr != "" {
		t.Error("a successful show clears the error")
	}
}

func TestHerdEnterAndTabNoopOffRowsOrFocus(t *testing.T) {
	// Enter on a header does nothing (and never links anything).
	m := liveTestModel(t, 48, 40)
	m.selectLine(0)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("enter on a header must be a no-op")
	}
}

func TestHerdRemovedKeysAreInert(t *testing.T) {
	for _, k := range []rune{'v', 'u'} {
		m := liveTestModel(t, 48, 40)
		before := m.liveShown
		next, cmd := m.Update(tea.KeyPressMsg{Code: k, Text: string(k)})
		if cmd != nil || next.(HerdModel).liveShown != before {
			t.Errorf("key %q should do nothing", k)
		}
	}
}

func TestDecideLiveResize(t *testing.T) {
	tests := []struct {
		name                                         string
		win, lastWin, appliedLeft, pane, userLeft    int
		wantApply                                    bool
		wantLeft, wantRight, wantUserLeft, wantWinOK int
	}{
		{"unknown window width is a no-op", 0, 200, 56, 40, 0, false, 0, 0, 0, 200},
		{"first sizing applies the computed split", 200, 0, 44, 44, 0, true, 56, 143, 0, 200},
		{"first sizing after re-exec keeps the seeded width", 200, 0, 0, 0, 70, true, 70, 129, 70, 200},
		{"window grew, no user ratio: recompute", 300, 200, 56, 56, 0, true, 56, 243, 0, 300},
		{"window shrank, no user ratio: recompute", 100, 200, 56, 56, 0, true, 44, 55, 0, 100},
		{"window resized keeps the dragged width", 300, 200, 70, 70, 70, true, 70, 229, 70, 300},
		{"window resized clamps a dragged width to the new window", 100, 200, 90, 90, 90, true, 79, 20, 90, 100},
		{"pane matches applied split: nothing to do", 200, 200, 56, 56, 0, false, 0, 0, 0, 200},
		{"drag is remembered and not fought", 200, 200, 56, 80, 0, false, 80, 119, 80, 200},
		{"drag below the minimum is clamped and re-applied", 200, 200, 56, 10, 0, true, 30, 169, 30, 200},
		{"drag past the maximum is clamped and re-applied", 200, 200, 56, 190, 0, true, 179, 20, 179, 200},
		{"pane size unknown is ignored", 200, 200, 56, 0, 0, false, 0, 0, 0, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := decideLiveResize(tt.win, tt.lastWin, tt.appliedLeft, tt.pane, tt.userLeft)
			if d.apply != tt.wantApply || d.left != tt.wantLeft || d.userLeft != tt.wantUserLeft || d.winWidth != tt.wantWinOK {
				t.Errorf("got apply=%v left=%d userLeft=%d win=%d, want apply=%v left=%d userLeft=%d win=%d",
					d.apply, d.left, d.userLeft, d.winWidth, tt.wantApply, tt.wantLeft, tt.wantUserLeft, tt.wantWinOK)
			}
			if tt.wantApply || tt.wantLeft > 0 {
				if d.right != tt.wantRight {
					t.Errorf("right = %d, want %d", d.right, tt.wantRight)
				}
			}
		})
	}
}

func TestHerdProbeMsgPreservesUserRatio(t *testing.T) {
	m := liveTestModel(t, 48, 40)
	m.liveWinWidth, m.liveLeft = 200, 56
	// A drag: same window width, pane now 80 wide. No tmux command, ratio kept.
	next, cmd := m.Update(herdProbeMsg{pane: 80, win: 200})
	got := next.(HerdModel)
	if cmd != nil || got.userLeft != 80 || got.liveLeft != 80 {
		t.Fatalf("drag: cmd=%v userLeft=%d liveLeft=%d", cmd != nil, got.userLeft, got.liveLeft)
	}
	// The window then grows: re-applied at the dragged width.
	next, cmd = got.Update(herdProbeMsg{pane: 100, win: 300})
	got = next.(HerdModel)
	if cmd == nil || got.liveLeft != 80 || got.liveRight != 219 || got.liveWinWidth != 300 {
		t.Fatalf("window resize: cmd=%v left=%d right=%d win=%d", cmd != nil, got.liveLeft, got.liveRight, got.liveWinWidth)
	}
}
