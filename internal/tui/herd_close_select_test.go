package tui

import (
	"slices"
	"testing"

	"github.com/blsmth/agentctl/internal/herd"
)

func closeTestGroups(ids ...string) []herd.Group {
	var rows []herd.Row
	for _, id := range ids {
		r := testRow("agent"+id, "s1", "idle", "")
		r.WindowID = id
		rows = append(rows, r)
	}
	return []herd.Group{{Session: "s1", Project: "p1", Rows: rows}}
}

func TestSelectionAfterClose(t *testing.T) {
	tests := []struct {
		name       string
		ids        []string
		closeID    string
		wantWindow string
		wantHeader bool
	}{
		{"middle row selects next", []string{"@1", "@2", "@3"}, "@2", "@3", false},
		{"last row selects previous", []string{"@1", "@2", "@3"}, "@3", "@2", false},
		{"first row selects next", []string{"@1", "@2"}, "@1", "@2", false},
		{"only row selects header", []string{"@1"}, "@1", "", true},
		{"live pane follows new selection", []string{"@1", "@2", "@3"}, "@2", "@3", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			InitStyles()
			m := NewHerdModel()
			m.termWidth, m.termHeight = 120, 40
			m.configureSplitPane()
			m.loaded = true
			m.groups = closeTestGroups(tt.ids...)
			m.rebuildLines()
			m.resolveSelection()
			for i, l := range m.lines {
				if l.kind == herdLineRow && l.row.WindowID == tt.closeID {
					m.selectLine(i)
				}
			}
			m.startClose(m.splitPane.Cursor())
			m.confirmClose()
			if m.closePending == nil {
				t.Fatal("close should record pending position")
			}

			var model = tea0(m)
			model = model.update(herdClosedMsg{name: "x"})
			// A reload that still lists the window must not consume it.
			model = model.update(herdLoadedMsg{groups: closeTestGroups(tt.ids...)})
			if model.m.closePending == nil {
				t.Fatal("pending consumed while window still listed")
			}
			rest := slices.DeleteFunc(slices.Clone(tt.ids), func(s string) bool { return s == tt.closeID })
			model = model.update(herdLoadedMsg{groups: closeTestGroups(rest...)})
			got := model.m
			if got.closePending != nil {
				t.Error("pending should be cleared after use")
			}
			if got.selIsHeader != tt.wantHeader || got.selWindowID != tt.wantWindow || got.selSession != "s1" {
				t.Errorf("selection = %q/%q header=%v, want s1/%q header=%v",
					got.selSession, got.selWindowID, got.selIsHeader, tt.wantWindow, tt.wantHeader)
			}
			if !tt.wantHeader && got.liveShown != "s1|"+tt.wantWindow {
				t.Errorf("liveShown = %q, want s1|%s", got.liveShown, tt.wantWindow)
			}
		})
	}
}

type herdHarness struct{ m HerdModel }

func tea0(m HerdModel) herdHarness { return herdHarness{m} }

func (h herdHarness) update(msg any) herdHarness {
	mod, _ := h.m.Update(msg)
	return herdHarness{mod.(HerdModel)}
}

func TestHerdColorFor(t *testing.T) {
	InitStyles()
	pal := herdPalette()
	for _, name := range []string{"go-engineer", "brandon", "", "project-x", "ÜñÏ"} {
		a, b := herdColorFor(name), herdColorFor(name)
		if a != b {
			t.Errorf("herdColorFor(%q) not deterministic: %q vs %q", name, a, b)
		}
		if !slices.Contains(pal, a) {
			t.Errorf("herdColorFor(%q) = %q, not in palette %v", name, a, pal)
		}
	}
}

func TestSplitAgentName(t *testing.T) {
	tests := []struct{ in, base, suffix string }{
		{"go-engineer:2", "go-engineer", ":2"},
		{"brandon", "brandon", ""},
		{"a:b", "a:b", ""},
		{"x:", "x:", ""},
		{":3", ":3", ""},
	}
	for _, tt := range tests {
		b, s := splitAgentName(tt.in)
		if b != tt.base || s != tt.suffix {
			t.Errorf("splitAgentName(%q) = %q,%q want %q,%q", tt.in, b, s, tt.base, tt.suffix)
		}
	}
}
