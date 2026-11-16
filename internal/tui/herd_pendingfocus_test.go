package tui

import (
	"testing"

	"github.com/blsmth/agentctl/internal/herd"
)

func TestParseLaunchWindowID(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "@123\n", "@123"},
		{"after noise", "starting\nlaunched\n@7\n", "@7"},
		{"empty", "", ""},
		{"no at", "123\n", ""},
		{"bare at", "@\n", ""},
		{"non-numeric", "@abc\n", ""},
		{"trailing text", "@12 done\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseLaunchWindowID(tt.in); got != tt.want {
				t.Errorf("got %q want %q", got, tt.want)
			}
		})
	}
}

func selectedWindow(m *HerdModel) string {
	if m.selIsHeader {
		return ""
	}
	return m.selWindowID
}

func withNewRow(m *HerdModel, win string) {
	r := testRow("new", "s1", "idle", "")
	r.WindowID = win
	m.groups[0].Rows = append(m.groups[0].Rows, r)
	m.rebuildLines()
}

func TestPendingFocus(t *testing.T) {
	t.Run("present on first load", func(t *testing.T) {
		m := mouseTestModel(t, 80, 24)
		m.pendingFocus = &herdPendingFocus{session: "s1", windowID: "@9"}
		withNewRow(&m, "@9")
		if !m.applyPendingFocus() || selectedWindow(&m) != "@9" || m.pendingFocus != nil {
			t.Fatalf("sel=%q pending=%v", selectedWindow(&m), m.pendingFocus)
		}
	})
	t.Run("absent then present", func(t *testing.T) {
		m := mouseTestModel(t, 80, 24)
		before := selectedWindow(&m)
		m.pendingFocus = &herdPendingFocus{session: "s1", windowID: "@9"}
		if m.applyPendingFocus() || m.pendingFocus == nil || selectedWindow(&m) != before {
			t.Fatal("should keep waiting without moving selection")
		}
		withNewRow(&m, "@9")
		if !m.applyPendingFocus() || selectedWindow(&m) != "@9" {
			t.Fatal("should select once present")
		}
	})
	t.Run("never appears gives up", func(t *testing.T) {
		m := mouseTestModel(t, 80, 24)
		before := selectedWindow(&m)
		m.pendingFocus = &herdPendingFocus{session: "s1", windowID: "@9"}
		msg := herdFocusRetryMsg{session: "s1", windowID: "@9"}
		for i := 0; i < herdFocusMaxRetries; i++ {
			if m.focusRetry(msg) == nil || m.pendingFocus == nil {
				t.Fatalf("gave up early at %d", i)
			}
		}
		if m.focusRetry(msg) != nil || m.pendingFocus != nil {
			t.Fatal("should give up after limit")
		}
		if selectedWindow(&m) != before {
			t.Fatal("selection changed")
		}
	})
	t.Run("stale retry ignored", func(t *testing.T) {
		m := mouseTestModel(t, 80, 24)
		m.pendingFocus = &herdPendingFocus{session: "s1", windowID: "@10"}
		if m.focusRetry(herdFocusRetryMsg{session: "s1", windowID: "@9"}) != nil || m.pendingFocus.tries != 0 {
			t.Fatal("stale tick should be a no-op")
		}
	})
	t.Run("manual selection clears", func(t *testing.T) {
		m := mouseTestModel(t, 80, 24)
		m.pendingFocus = &herdPendingFocus{session: "s1", windowID: "@9"}
		m.selectLine(2)
		if m.pendingFocus != nil {
			t.Fatal("selectLine should clear")
		}
		m.pendingFocus = &herdPendingFocus{session: "s1", windowID: "@9"}
		m.moveCursor(1)
		if m.pendingFocus != nil {
			t.Fatal("moveCursor should clear")
		}
	})
	t.Run("launched msg sets or skips", func(t *testing.T) {
		for _, tt := range []struct {
			win  string
			want bool
		}{{"@9", true}, {"", false}} {
			m := mouseTestModel(t, 80, 24)
			mm, _ := m.Update(herdLaunchedMsg{session: "s1", windowID: tt.win})
			got := mm.(HerdModel).pendingFocus != nil
			if got != tt.want {
				t.Errorf("win %q pending=%v want %v", tt.win, got, tt.want)
			}
		}
	})
	t.Run("loaded msg selects", func(t *testing.T) {
		m := mouseTestModel(t, 80, 24)
		m.pendingFocus = &herdPendingFocus{session: "s1", windowID: "@9"}
		groups := append([]herd.Group(nil), m.groups...)
		r := testRow("new", "s1", "idle", "")
		r.WindowID = "@9"
		groups[0].Rows = append(append([]herd.Row(nil), groups[0].Rows...), r)
		mm, _ := m.Update(herdLoadedMsg{groups: groups})
		got := mm.(HerdModel)
		if selectedWindow(&got) != "@9" || got.pendingFocus != nil {
			t.Fatalf("sel=%q", selectedWindow(&got))
		}
	})
}
