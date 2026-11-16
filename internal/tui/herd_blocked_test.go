package tui

import (
	"testing"
)

func TestJumpToNextBlockedStaysInHub(t *testing.T) {
	m := mouseTestModel(t, 48, 40)
	m.groups[1].Rows[0].State = "blocked" // s2 / @3
	m.rebuildLines()

	switched, focused := false, false
	oldS, oldF := switchToSessionWindow, focusLivePane
	t.Cleanup(func() { switchToSessionWindow, focusLivePane = oldS, oldF })
	switchToSessionWindow = func(string, string) error { switched = true; return nil }
	focusLivePane = func() error { focused = true; return nil }

	next, cmd := m.jumpToNextBlocked()
	got := next.(HerdModel)
	if switched {
		t.Error("SwitchToSessionWindow must not be called")
	}
	if got.selSession != "s2" || got.selWindowID != "@3" {
		t.Errorf("selection = %s|%s, want s2|@3", got.selSession, got.selWindowID)
	}
	if cmd == nil {
		t.Fatal("expected a command to show live and focus")
	}
	_ = focused // focus runs inside the command; show would hit real tmux
}

func TestJumpToNextBlockedNone(t *testing.T) {
	m := mouseTestModel(t, 48, 40)
	next, cmd := m.jumpToNextBlocked()
	if cmd != nil || next.(HerdModel).jumpErr != "no blocked agents" {
		t.Errorf("cmd=%v err=%q", cmd, next.(HerdModel).jumpErr)
	}
}
