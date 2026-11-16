package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/herd"
)

// liveTestModel is mouseTestModel as the hub pane sees it in live mode: the
// pane is only the list column wide.
func liveTestModel(t *testing.T, paneWidth, height int) HerdModel {
	t.Helper()
	m := mouseTestModel(t, paneWidth, height)
	m.live, m.liveLeft, m.liveRight = true, paneWidth, 200
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
	tests := []struct {
		name        string
		live        bool
		wantDetail  bool
		wantPreview bool
	}{
		{"live hides detail and preview", true, false, false},
		{"preview layout keeps them", false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mouseTestModel(t, 140, 40)
			m.preview = "PREVIEW-MARKER"
			if tt.live {
				m.live, m.liveLeft = true, 48
				m.termWidth = 48
				m.configureSplitPane()
			}
			out := ansi.Strip(m.render())
			if got := strings.ContainsAny(out, "╭╮╰╯"); got != tt.wantDetail {
				t.Errorf("detail border present = %v, want %v\n%s", got, tt.wantDetail, out)
			}
			if got := strings.Contains(out, "PREVIEW-MARKER"); got != tt.wantPreview {
				t.Errorf("preview present = %v, want %v", got, tt.wantPreview)
			}
			for _, line := range strings.Split(out, "\n") {
				if tt.live && len([]rune(line)) > 48 {
					t.Errorf("live line wider than the pane: %q", line)
				}
			}
		})
	}
}

func TestHerdLiveHints(t *testing.T) {
	m := liveTestModel(t, 48, 40)
	out := ansi.Strip(m.render())
	for _, want := range []string{"v preview", "tab focus live", "^b o back"} {
		if !strings.Contains(out, want) {
			t.Errorf("live hints missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "live off") {
		t.Error("stale 'live off' hint")
	}
	if n := m.liveHintRows(); n < 1 {
		t.Errorf("narrow hint bar should wrap onto extra rows, got %d", n)
	}
	m.live, m.liveLeft = false, 0
	if out := ansi.Strip(m.render()); !strings.Contains(out, "v live") {
		t.Error("preview layout lost its 'v live' hint")
	}
}

func TestHerdLiveClicks(t *testing.T) {
	m := liveTestModel(t, 48, 40)
	top := m.herdListTop()
	w := m.listColWidth()
	if w != 44 {
		t.Fatalf("listColWidth = %d, want 44", w)
	}
	tests := []struct {
		name       string
		x, y       int
		wantIdx    int
		wantAction herdClickAction
	}{
		{"header body selects", 3, top, 0, herdClickSelect},
		{"header + new", w - herdSidebarRightGutter - 2, top, 0, herdClickNew},
		{"row body selects", 5, top + 1, 1, herdClickSelect},
		{"row close", w - herdSidebarRightGutter - 1, top + 1, 1, herdClickClose},
		{"past the list", 5, top + 20, -1, herdClickIgnore},
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
	// No detail panel to hit while live.
	if _, inDetail, _ := m.lineAt(200, top+1); inDetail {
		t.Error("live mode has no detail panel")
	}
}

func TestHerdLiveSkipsCapture(t *testing.T) {
	m := liveTestModel(t, 48, 40)
	m.selSession, m.selWindowID = "s1", "@1"
	m.liveShown = "s1|@1"
	if cmd := m.maybeCaptureCmd(); cmd != nil {
		t.Error("live mode must not capture the preview")
	}
}

func TestHerdLiveFallbackOnError(t *testing.T) {
	m := liveTestModel(t, 48, 40)
	m.selSession, m.selWindowID = "s1", "@1"
	next, _ := m.Update(herdLiveMsg{key: "s1|@1", err: errors.New("boom")})
	got := next.(HerdModel)
	if got.live || got.jumpErr != "boom" {
		t.Errorf("live=%v jumpErr=%q, want fallback with error", got.live, got.jumpErr)
	}
}

func TestHerdAutoLive(t *testing.T) {
	load := herdLoadedMsg{groups: []herd.Group{
		{Session: "s1", Rows: []herd.Row{testRow("a", "s1", "idle", "")}},
	}}
	load.groups[0].Rows[0].WindowID = "@1"

	t.Run("on when a row is selectable", func(t *testing.T) {
		InitStyles()
		m := NewHerdModel().WithAutoLive()
		m.termWidth = 200
		m.selSession, m.selWindowID = "s1", "@1" // resolveSelection keeps it
		next, _ := m.Update(load)
		if !next.(HerdModel).live {
			t.Error("expected live after load")
		}
	})
	t.Run("off without the option", func(t *testing.T) {
		InitStyles()
		next, _ := NewHerdModel().Update(load)
		if next.(HerdModel).live {
			t.Error("live must stay off by default")
		}
	})
	t.Run("v toggles back to preview", func(t *testing.T) {
		InitStyles()
		m := NewHerdModel().WithAutoLive()
		m.termWidth = 200
		next, _ := m.Update(load)
		m = next.(HerdModel)
		if !m.live {
			t.Skip("live not enabled (selection not resolved)")
		}
		next, _ = m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
		if next.(HerdModel).live {
			t.Error("v should leave live mode")
		}
	})
}
