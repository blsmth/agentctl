package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestReloadJitter(t *testing.T) {
	for _, id := range []string{"@1", "@2", "@47", "@1000", ""} {
		j := reloadJitter(id)
		if j < 0 || j >= reloadMaxJitter {
			t.Errorf("jitter(%q) = %v out of bounds", id, j)
		}
		if j != reloadJitter(id) {
			t.Errorf("jitter(%q) not deterministic", id)
		}
	}
	if reloadJitter("@1") == reloadJitter("@2") && reloadJitter("@2") == reloadJitter("@3") {
		t.Error("jitter does not vary by id")
	}
}

func TestReloaderDue(t *testing.T) {
	now := time.Unix(1000, 0)
	mk := func(changed bool) (*reloader, *int) {
		calls := 0
		r := &reloader{now: func() time.Time { return now }, jitter: time.Second,
			changed: func() bool { calls++; return changed }}
		return r, &calls
	}
	t.Run("unchanged", func(t *testing.T) {
		r, _ := mk(false)
		if r.due() || r.triggered() {
			t.Error("unchanged binary must not trigger")
		}
	})
	t.Run("capture error disables", func(t *testing.T) {
		r := &reloader{now: time.Now}
		if r.due() {
			t.Error("nil changed must not trigger")
		}
		var nilR *reloader
		if nilR.due() || nilR.triggered() {
			t.Error("nil reloader must not trigger")
		}
	})
	t.Run("changed waits for jitter then fires once", func(t *testing.T) {
		r, _ := mk(true)
		if r.due() {
			t.Fatal("fired before jitter elapsed")
		}
		now = now.Add(time.Second)
		if !r.due() || !r.triggered() {
			t.Fatal("expected trigger after jitter")
		}
		if r.due() {
			t.Error("must fire only once")
		}
	})
	t.Run("throttled stat", func(t *testing.T) {
		r, calls := mk(false)
		r.due()
		r.due()
		if *calls != 1 {
			t.Errorf("stat calls = %d, want 1", *calls)
		}
		now = now.Add(reloadCheckInterval)
		r.due()
		if *calls != 2 {
			t.Errorf("stat calls = %d, want 2", *calls)
		}
	})
	t.Run("disable", func(t *testing.T) {
		r, _ := mk(true)
		r.disable()
		now = now.Add(time.Minute)
		if r.due() {
			t.Error("disabled reloader fired")
		}
	})
}

func TestChromeBarReloadTick(t *testing.T) {
	newM := func(changed bool) *ChromeBarModel {
		m := newChromeBarModel("@1", "", nil, nil)
		m.reload = &reloader{now: time.Now, changed: func() bool { return changed }}
		return m
	}
	t.Run("changed quits and flags reexec", func(t *testing.T) {
		got, cmd := newM(true).Update(chromeBarReloadTickMsg{})
		if !got.(ChromeBarModel).Reexec() {
			t.Error("reexec not flagged")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Error("expected quit")
		}
	})
	t.Run("unchanged reschedules", func(t *testing.T) {
		got, cmd := newM(false).Update(chromeBarReloadTickMsg{})
		if got.(ChromeBarModel).Reexec() || cmd == nil {
			t.Error("unchanged must only reschedule")
		}
	})
}

func TestHudReloadTick(t *testing.T) {
	m := NewHudModel("@1")
	m.reload = &reloader{now: time.Now, changed: func() bool { return true }}
	got, cmd := m.Update(hudTickMsg{})
	if !got.(HudModel).Reexec() {
		t.Error("reexec not flagged")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("expected quit")
	}
	m.reload = &reloader{now: time.Now}
	if got, _ := m.Update(hudTickMsg{}); got.(HudModel).Reexec() {
		t.Error("disabled reload flagged reexec")
	}
}
