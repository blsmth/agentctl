package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
)

func TestShortModel(t *testing.T) {
	tests := []struct {
		in, short, slot string
	}{
		{"", "", ""},
		{"claude-opus-4-7", "opus-4-7", "opus"},
		{"claude-opus-4-7[1m]", "opus-4-7", "opus"},
		{"claude-sonnet-4-5-20250929", "sonnet-4-5", "sonnet"},
		{"claude-haiku-4-5", "haiku-4-5", "haiku"},
		{"claude-3-5-sonnet-20241022", "3-5-sonnet", "sonnet"},
		{"claude-fable-1", "fable-1", "fable"},
		{"gpt-4o", "gpt-4o", "gpt-4o"},
		{"qwen3-coder:30b", "qwen3-coder:30b", "qwen3…"},
		{"hermes-4-405b", "hermes-4-405b", "herme…"},
		{"4.5", "4.5", "4.5"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := shortModel(tt.in); got != tt.short {
				t.Errorf("shortModel(%q) = %q, want %q", tt.in, got, tt.short)
			}
			got := slotModel(tt.in)
			if got != tt.slot {
				t.Errorf("slotModel(%q) = %q, want %q", tt.in, got, tt.slot)
			}
			if w := lipgloss.Width(got); w > fleetModelSlotW {
				t.Errorf("slotModel(%q) is %d cols, slot is %d", tt.in, w, fleetModelSlotW)
			}
		})
	}
}

func TestCtxStyleThresholds(t *testing.T) {
	InitStyles()
	tests := []struct {
		name string
		d    fleetDisplay
		pct  int
		want lipgloss.Style
	}{
		{"low is dim", fleetDisplay{}, 42, fleetDimStyle},
		{"default warn", fleetDisplay{}, 75, fleetBlockedStyle},
		{"default crit", fleetDisplay{}, 90, fleetErrStyle},
		{"configured warn", fleetDisplay{ctxWarn: 50, ctxCrit: 60}, 55, fleetBlockedStyle},
		{"configured crit", fleetDisplay{ctxWarn: 50, ctxCrit: 60}, 60, fleetErrStyle},
		{"incoherent pair falls back", fleetDisplay{ctxWarn: 80, ctxCrit: 20}, 50, fleetDimStyle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := tt.d.ctxStyle(tt.pct).Render("x"), tt.want.Render("x"); got != want {
				t.Errorf("ctxStyle(%d) renders %q, want %q", tt.pct, got, want)
			}
		})
	}
}

func chipTexts(cs []fleetChip) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.text
	}
	return out
}

func TestRowChips(t *testing.T) {
	full := func(r *fleet.Row) {
		r.PRNumber = 186
		r.Ticket = 42
		r.ModelName = "claude-opus-4-7"
		r.ContextPct = 42
		r.Backend = "nous"
		r.Account = "work"
		r.ScheduledBy = "nightly"
	}
	tests := []struct {
		name     string
		mod      func(*fleet.Row)
		nameFits bool
		d        fleetDisplay
		want     []string
	}{
		{"nothing", nil, true, fleetDisplay{}, []string{}},
		{"phone order", full, false, fleetDisplay{}, []string{"PR #186", "#42", "go-engineer", "opus-4-7", "ctx 42%", "nous", "acct work", "⏲ nightly"}},
		{"agent hidden when the name fits", full, true, fleetDisplay{}, []string{"PR #186", "#42", "opus-4-7", "ctx 42%", "nous", "acct work", "⏲ nightly"}},
		{"default account hidden", full, true, fleetDisplay{defaultAccount: "work"}, []string{"PR #186", "#42", "opus-4-7", "ctx 42%", "nous", "⏲ nightly"}},
		{"pr url only", func(r *fleet.Row) { r.PRURL = "https://x/pull/x" }, true, fleetDisplay{}, []string{"PR"}},
		{"default backends hidden", func(r *fleet.Row) { r.Backend = "Anthropic" }, true, fleetDisplay{}, []string{}},
		{"claude backend hidden", func(r *fleet.Row) { r.Backend = "claude" }, true, fleetDisplay{}, []string{}},
		{"ctx 0 has no chip", func(r *fleet.Row) { r.ModelName = "claude-haiku-4-5" }, true, fleetDisplay{}, []string{"haiku-4-5"}},
		{"shell has none", func(r *fleet.Row) { full(r); r.Shell = true }, false, fleetDisplay{}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testRow("go-engineer:2", "s", "idle", "")
			r.AgentName = "go-engineer"
			if tt.mod != nil {
				tt.mod(&r)
			}
			got := chipTexts(rowChips(r, tt.nameFits, tt.d))
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("chips = %q, want %q", got, tt.want)
			}
		})
	}
}

// chipRow is a selected-row fixture with every chip and a two-line recap.
func chipRow(name string) fleet.Row {
	r := sampleRow("s", "proj", "@1", name+":1", name, "working", false,
		fleet.LogInfo{Recap: "Bringing the phone fleet chips to the terminal hub rows.", PRNumber: 186, PRURL: "https://github.com/blsmth/agentctl/pull/186"},
		func(r *fleet.Row) {
			r.Ticket = 42
			r.ModelName = "claude-opus-4-7"
			r.ContextPct = 81
			r.Backend = "nous"
			r.LastToolDetail = "Editing internal/tui/fleet.go"
		})
	return r
}

func TestSelectedRowChipLine(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	for _, width := range []int{30, 36, 40, 56, 80, 120} {
		t.Run(fmt.Sprintf("cw%d", width), func(t *testing.T) {
			m := newFleetModel()
			m.termWidth, m.termHeight, m.liveLeft = width, 30, width
			m.liveShown = "x"
			m.configureSplitPane()
			m.loaded = true
			m.groups = []fleet.Group{{Session: "s", Project: "proj", Rows: []fleet.Row{
				chipRow("go-engineer-opus-with-a-long-name"),
				chipRow("dev"),
			}}}
			m.rebuildLines()
			cw := m.contentWidth()
			for i, l := range m.lines {
				if l.kind != fleetLineRow {
					continue
				}
				m.splitPane.SetCursor(i)
				out := m.renderTreeRow(l.row, l.treePrefix, l.nameCol, true, cw)
				lines := strings.Split(out, "\n")
				if got := m.lineHeight(i, cw); got != len(lines) {
					t.Errorf("row %d: lineHeight %d, rendered %d lines", i, got, len(lines))
				}
				for j, line := range lines {
					if w := lipgloss.Width(line); w != cw {
						t.Errorf("row %d line %d width %d, want %d: %q", i, j, w, cw, ansi.Strip(line))
					}
				}
				if len(lines) < 2 {
					t.Fatalf("row %d has no detail lines", i)
				}
				chips := ansi.Strip(lines[len(lines)-1])
				if !strings.Contains(chips, "[PR #186]") {
					t.Errorf("row %d: last line is not the chip line: %q", i, chips)
				}
				if width >= 120 {
					want := "[PR #186] [#42] "
					if l.row.AgentName != "dev" {
						want += "[" + l.row.AgentName + "] "
					}
					want += "[opus-4-7] [ctx 81%] [nous]"
					if !strings.Contains(chips, want) {
						t.Errorf("row %d chips = %q, want %q", i, chips, want)
					}
					if !strings.Contains(lines[len(lines)-1], fleetBlockedStyle.Render("ctx 81%")) {
						t.Errorf("ctx chip above the warn threshold should use the warn style")
					}
				} else if width <= 40 && !strings.Contains(chips, "…") {
					t.Errorf("narrow chip line should truncate with …: %q", chips)
				}
			}
		})
	}
}

func TestNarrowRowKeepsRecap(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	for cw := 30; cw <= 40; cw++ {
		r := chipRow("go-engineer")
		r.StateSince = sidebarNow
		m := newFleetModel()
		plain := firstLine(m.renderTreeRowHover(r, "", 11, false, fleetHoverNone, cw))
		if strings.Contains(plain, "opus") {
			t.Errorf("cw %d: model slot must drop before starving the recap: %q", cw, plain)
		}
		if !strings.Contains(plain, "Bringin") {
			t.Errorf("cw %d: recap missing: %q", cw, plain)
		}
	}
	// Wide enough for the model but not the age: the age goes first.
	r := chipRow("go-engineer")
	r.StateSince = sidebarNow.Add(-4 * 60e9)
	m := newFleetModel()
	for cw := 40; cw < 120; cw++ {
		plain := firstLine(m.renderTreeRowHover(r, "", 11, false, fleetHoverNone, cw))
		hasModel, hasAge := strings.Contains(plain, "opus"), strings.Contains(plain, " 4m")
		if hasAge && !hasModel {
			t.Fatalf("cw %d: age shown without the model: %q", cw, plain)
		}
	}
}

func TestFleetLoadedStoresDisplay(t *testing.T) {
	m := newFleetModel()
	d := fleetDisplay{ctxWarn: 50, ctxCrit: 60, defaultAccount: "work"}
	got, _ := m.Update(fleetLoadedMsg{display: d})
	if fm := got.(FleetModel); fm.display != d {
		t.Errorf("display = %+v, want %+v", fm.display, d)
	}
}
