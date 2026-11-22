package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
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

func TestRowChips(t *testing.T) {
	full := func(r *fleet.Row) {
		r.PRNumber = 186
		r.Ticket = 42
		r.ModelName = "claude-opus-4-7"
		r.ContextPct = 92
		r.Backend = "nous"
		r.Account = "work"
		r.ScheduledBy = "nightly"
	}
	tests := []struct {
		name string
		mod  func(*fleet.Row)
		d    fleetDisplay
		want []string
	}{
		// Rows no longer draw the agent name, so the chip line always has it.
		{"agent always shown", nil, fleetDisplay{}, []string{"go-engineer"}},
		// Model, context, PR and the cron job are on the row itself: no chips.
		{"row facts are not chips", full, fleetDisplay{}, []string{"#42", "go-engineer", "nous", "acct work"}},
		{"default account hidden", full, fleetDisplay{defaultAccount: "work"}, []string{"#42", "go-engineer", "nous"}},
		{"pr url only has no chip", func(r *fleet.Row) { r.PRURL = "https://x/pull/x" }, fleetDisplay{}, []string{"go-engineer"}},
		{"model only has no chip", func(r *fleet.Row) { r.ModelName = "claude-haiku-4-5"; r.ContextPct = 99 }, fleetDisplay{}, []string{"go-engineer"}},
		{"scheduled only has no chip", func(r *fleet.Row) { r.ScheduledBy = "nightly" }, fleetDisplay{}, []string{"go-engineer"}},
		{"default backends hidden", func(r *fleet.Row) { r.Backend = "Anthropic" }, fleetDisplay{}, []string{"go-engineer"}},
		{"claude backend hidden", func(r *fleet.Row) { r.Backend = "claude" }, fleetDisplay{}, []string{"go-engineer"}},
		{"shell has none", func(r *fleet.Row) { full(r); r.Shell = true }, fleetDisplay{}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testRow("go-engineer:2", "s", "idle", "")
			r.AgentName = "go-engineer"
			if tt.mod != nil {
				tt.mod(&r)
			}
			got := rowChips(r, tt.d)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("chips = %q, want %q", got, tt.want)
			}
		})
	}
}

// chipRow is a selected-row fixture with an issue, a backend, a two-line
// recap, a PR and a model and context use.
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
				out := m.renderTreeRow(l.row, l.treePrefix, true, cw)
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
				if !strings.Contains(chips, "[#42]") {
					t.Errorf("row %d: last line is not the chip line: %q", i, chips)
				}
				for _, gone := range []string{"[PR", "[opus", "[ctx"} {
					if strings.Contains(chips, gone) {
						t.Errorf("row %d: chip line repeats a row fact %q: %q", i, gone, chips)
					}
				}
				if width >= 120 {
					want := "[#42] [" + l.row.AgentName + "] [nous]"
					if !strings.Contains(chips, want) {
						t.Errorf("row %d chips = %q, want %q", i, chips, want)
					}
				}
			}
		})
	}
}

func TestSelectedRowChipLineIsJustTheAgent(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	m := newFleetModel()
	m.termWidth, m.termHeight, m.liveLeft = 120, 30, 120
	m.configureSplitPane()
	m.loaded = true
	r := sampleRow("s", "proj", "@1", "dev:1", "dev", "working", false,
		fleet.LogInfo{Recap: "One short recap.", PRNumber: 186}, func(r *fleet.Row) {
			r.ModelName, r.ContextPct, r.ScheduledBy = "claude-opus-4-7", 95, "nightly"
		})
	m.groups = []fleet.Group{{Session: "s", Project: "proj", Rows: []fleet.Row{r}}}
	m.rebuildLines()
	cw := m.contentWidth()
	for i, l := range m.lines {
		if l.kind != fleetLineRow {
			continue
		}
		m.splitPane.SetCursor(i)
		lines := strings.Split(m.renderTreeRow(l.row, l.treePrefix, true, cw), "\n")
		// Rows never draw the agent name, so even a row with nothing else to
		// say has a chip line naming it, and no other chip.
		if len(lines) != 3 || m.lineHeight(i, cw) != 3 {
			t.Fatalf("want the row, its recap and the agent chip, got %d lines (lineHeight %d): %q", len(lines), m.lineHeight(i, cw), ansi.Strip(strings.Join(lines, "\n")))
		}
		if got := strings.TrimSpace(strings.TrimLeft(ansi.Strip(lines[2]), "▎")); got != "[dev]" {
			t.Errorf("chip line = %q, want [dev]", got)
		}
	}
}

func TestMostCommonModel(t *testing.T) {
	row := func(model string) fleet.Row {
		return fleet.Row{AgentState: state.AgentState{ModelName: model}}
	}
	shell := fleet.Row{Shell: true, AgentState: state.AgentState{ModelName: "claude-sonnet-4-5"}}
	tests := []struct {
		name string
		rows [][]fleet.Row
		want string
	}{
		{"none", nil, ""},
		{"no models", [][]fleet.Row{{row(""), row("")}}, ""},
		{"single", [][]fleet.Row{{row("claude-opus-4-7")}}, "opus"},
		{"majority across groups", [][]fleet.Row{{row("claude-opus-4-7"), row("claude-sonnet-4-5")}, {row("claude-opus-4-5")}}, "opus"},
		{"tie shows both", [][]fleet.Row{{row("claude-opus-4-7"), row("claude-sonnet-4-5")}}, ""},
		{"shells do not count", [][]fleet.Row{{row("claude-opus-4-7"), shell, shell}}, "opus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var groups []fleet.Group
			for _, rs := range tt.rows {
				groups = append(groups, fleet.Group{Rows: rs})
			}
			if got := mostCommonModel(groups); got != tt.want {
				t.Errorf("mostCommonModel = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRowModelException(t *testing.T) {
	ex := fleetExceptions{common: "opus"}
	tests := []struct {
		name string
		row  fleet.Row
		want string
	}{
		{"usual model hidden", fleet.Row{AgentState: state.AgentState{ModelName: "claude-opus-4-7", TurnCount: 1}}, ""},
		{"other model shown", fleet.Row{AgentState: state.AgentState{ModelName: "claude-sonnet-4-5", TurnCount: 1}}, "sonnet"},
		{"no prompt yet hides it", fleet.Row{AgentState: state.AgentState{ModelName: "claude-sonnet-4-5"}}, ""},
		{"shell hides it", fleet.Row{Shell: true, AgentState: state.AgentState{ModelName: "claude-sonnet-4-5", TurnCount: 1}}, ""},
		{"unknown model", fleet.Row{AgentState: state.AgentState{TurnCount: 1}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ex.rowModel(tt.row); got != tt.want {
				t.Errorf("rowModel = %q, want %q", got, tt.want)
			}
		})
	}
	tie := fleetExceptions{}
	if got := tie.rowModel(fleet.Row{AgentState: state.AgentState{ModelName: "claude-opus-4-7", TurnCount: 1}}); got != "opus" {
		t.Errorf("with no usual model every model shows, got %q", got)
	}
}

func TestRowCtxWarn(t *testing.T) {
	tests := []struct {
		name string
		pct  int
		d    fleetDisplay
		want bool
	}{
		{"unknown", 0, fleetDisplay{}, false},
		{"below default warn", 74, fleetDisplay{}, false},
		{"at default warn", 75, fleetDisplay{}, true},
		{"critical", 96, fleetDisplay{}, true},
		{"configured warn", 55, fleetDisplay{ctxWarn: 50, ctxCrit: 60}, true},
		{"below configured warn", 49, fleetDisplay{ctxWarn: 50, ctxCrit: 60}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fleet.Row{AgentState: state.AgentState{ContextPct: tt.pct}}
			if got := rowCtxWarn(r, tt.d); got != tt.want {
				t.Errorf("rowCtxWarn(%d) = %v, want %v", tt.pct, got, tt.want)
			}
		})
	}
	if rowCtxWarn(fleet.Row{Shell: true, AgentState: state.AgentState{ContextPct: 99}}, fleetDisplay{}) {
		t.Error("shell rows never warn")
	}
}

func TestRowRightExceptions(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	base := func(model string, ctx, pr int) fleet.Row {
		return fleet.Row{StateSince: sidebarNow.Add(-4 * time.Minute), PRNumber: pr,
			AgentState: state.AgentState{State: "working", ModelName: model, ContextPct: ctx, TurnCount: 1}}
	}
	wide := 200
	tests := []struct {
		name string
		row  fleet.Row
		ex   fleetExceptions
		want string
	}{
		{"usual model, low ctx: PR and age only", base("claude-opus-4-7", 42, 108), fleetExceptions{common: "opus"}, "#108  4m"},
		{"no PR keeps the age column", base("claude-opus-4-7", 42, 0), fleetExceptions{common: "opus"}, "4m"},
		{"model exception", base("claude-sonnet-4-5", 42, 108), fleetExceptions{common: "opus", modelW: 6}, "sonnet  #108  4m"},
		{"usual model in a list with exceptions", base("claude-opus-4-7", 42, 108), fleetExceptions{common: "opus", modelW: 6}, "#108  4m"},
		{"ctx warning", base("claude-opus-4-7", 82, 108), fleetExceptions{common: "opus", ctx: true}, "82%  #108  4m"},
		{"ctx column blank on calm rows", base("claude-opus-4-7", 42, 108), fleetExceptions{common: "opus", ctx: true}, "#108  4m"},
		{"model and ctx", base("claude-sonnet-4-5", 96, 0), fleetExceptions{common: "opus", modelW: 6, ctx: true}, "sonnet  96%        4m"},
		{"model with calm ctx keeps the ctx slot", base("claude-sonnet-4-5", 10, 7), fleetExceptions{common: "opus", modelW: 6, ctx: true}, "sonnet         #7  4m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := fleetDisplay{}
			cols := fitRightCols(wide, tt.ex, rowCtxWarn(tt.row, d), tt.ex.rowModel(tt.row) != "")
			if got := ansi.Strip(rowRight(tt.row, cols, tt.ex, d)); got != tt.want {
				t.Errorf("rowRight = %q, want %q", got, tt.want)
			}
		})
	}
	r := base("claude-opus-4-7", 92, 0)
	cols := fitRightCols(wide, fleetExceptions{ctx: true}, true, false)
	if got := rowRight(r, cols, fleetExceptions{ctx: true}, fleetDisplay{}); !strings.Contains(got, fleetErrStyle.Render("92%")) {
		t.Errorf("critical ctx should use the error style: %q", got)
	}
}

func TestFitRightColsDropOrder(t *testing.T) {
	ex := fleetExceptions{common: "opus", modelW: 6, ctx: true}
	full := joinedWidth(6, fleetCtxSlotW, fleetPRSlotW, fleetAgeSlotW) + 1 + fleetMinRecapWidth
	noAge := joinedWidth(6, fleetCtxSlotW, fleetPRSlotW) + 1 + fleetMinRecapWidth
	noModel := joinedWidth(fleetCtxSlotW, fleetPRSlotW) + 1 + fleetMinRecapWidth
	type want struct {
		model, pr, age bool
	}
	tests := []struct {
		available int
		want      want
	}{
		{full + 10, want{true, true, true}},
		{full, want{true, true, true}},
		{full - 1, want{true, true, false}},   // age goes first
		{noAge, want{true, true, false}},      //
		{noAge - 1, want{false, true, false}}, // then the model
		{noModel, want{false, true, false}},   //
		{noModel - 1, want{false, false, false}},
		{0, want{false, false, false}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.available), func(t *testing.T) {
			c := fitRightCols(tt.available, ex, true, true)
			got := want{c.modelW > 0, c.pr, c.age}
			if got != tt.want {
				t.Errorf("fitRightCols(%d) = %+v, want %+v", tt.available, got, tt.want)
			}
			if !c.ctx {
				t.Error("the ctx warning is never dropped")
			}
		})
	}
	// Another row's context warning or model costs this row nothing to the
	// left of its PR: at a 40-column sidebar's 17 columns the PR stays.
	calm := fitRightCols(17, ex, false, false)
	if !calm.pr || calm.age || calm.modelW != 0 {
		t.Errorf("calm row at 17 columns: %+v", calm)
	}
	if warned := fitRightCols(17, ex, true, false); warned.pr {
		t.Errorf("a warned row at 17 columns keeps its ctx and drops the PR: %+v", warned)
	}
	// With no exceptions in the list the model and ctx columns take no
	// width, so the PR and age survive to narrower rows.
	plain := fitRightCols(joinedWidth(fleetPRSlotW, fleetAgeSlotW)+1+fleetMinRecapWidth, fleetExceptions{}, false, false)
	if plain.modelW != 0 || plain.ctx || !plain.pr || !plain.age {
		t.Errorf("no exceptions: %+v", plain)
	}
}

func TestNarrowRowKeepsRecap(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	ex := fleetExceptions{common: "opus", modelW: 6, ctx: true}
	for cw := 20; cw <= 28; cw++ {
		r := chipRow("go-engineer")
		r.ModelName = "claude-sonnet-4-5"
		r.StateSince = sidebarNow
		m := newFleetModel()
		m.except = ex
		plain := firstLine(m.renderTreeRowHover(r, "", false, fleetHoverNone, cw))
		if strings.Contains(plain, "sonnet") {
			t.Errorf("cw %d: model slot must drop before starving the recap: %q", cw, plain)
		}
		if !strings.Contains(plain, "81%") {
			t.Errorf("cw %d: the ctx warning is never dropped: %q", cw, plain)
		}
		if !strings.Contains(plain, "Bringin") {
			t.Errorf("cw %d: recap missing: %q", cw, plain)
		}
	}
	// Age, then model, then PR.
	r := chipRow("go-engineer")
	r.ModelName = "claude-sonnet-4-5"
	r.StateSince = sidebarNow.Add(-4 * 60e9)
	m := newFleetModel()
	m.except = ex
	for cw := 40; cw < 120; cw++ {
		plain := firstLine(m.renderTreeRowHover(r, "", false, fleetHoverNone, cw))
		hasModel, hasAge, hasPR := strings.Contains(plain, "sonnet"), strings.Contains(plain, " 4m"), strings.Contains(plain, "#186")
		if hasAge && !hasModel {
			t.Fatalf("cw %d: age shown without the model: %q", cw, plain)
		}
		if hasModel && !hasPR {
			t.Fatalf("cw %d: model shown without the PR: %q", cw, plain)
		}
		if !strings.Contains(plain, "81%") {
			t.Fatalf("cw %d: ctx warning dropped: %q", cw, plain)
		}
	}
}

func TestBlankExceptionWidthGoesToRecap(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	r := sampleRow("s", "proj", "@1", "dev:1", "dev", "working", false,
		fleet.LogInfo{Recap: strings.Repeat("word ", 40)}, func(r *fleet.Row) {
			r.ModelName, r.ContextPct, r.TurnCount = "claude-opus-4-7", 10, 1
			r.StateSince = sidebarNow.Add(-time.Minute)
		})
	m := newFleetModel()
	m.except = fleetExceptions{common: "opus", modelW: 6, ctx: true}
	withEx := firstLine(m.renderTreeRowHover(r, "", false, fleetHoverNone, 100))
	m.except = fleetExceptions{common: "opus"}
	without := firstLine(m.renderTreeRowHover(r, "", false, fleetHoverNone, 100))
	if withEx != without {
		t.Errorf("a row with no exceptions must not reserve their columns:\n%q\n%q", withEx, without)
	}
	if strings.Contains(without, "opus") || strings.Contains(without, "10%") {
		t.Errorf("usual model and calm ctx must not show: %q", without)
	}
}

func TestComputeExceptions(t *testing.T) {
	mk := func(model string, ctx int) fleet.Row {
		return fleet.Row{AgentState: state.AgentState{ModelName: model, ContextPct: ctx, TurnCount: 1}}
	}
	groups := []fleet.Group{{Rows: []fleet.Row{mk("claude-opus-4-7", 10), mk("claude-opus-4-7", 20)}}, {Rows: []fleet.Row{mk("claude-haiku-4-5", 80)}}}
	ex := computeExceptions(groups, groups, fleetDisplay{})
	if ex.common != "opus" || ex.modelW != len("haiku") || !ex.ctx {
		t.Errorf("exceptions = %+v", ex)
	}
	ex = computeExceptions(groups[:1], groups[:1], fleetDisplay{})
	if ex.modelW != 0 || ex.ctx {
		t.Errorf("all-usual list = %+v", ex)
	}
	// A hidden (folded or filtered-out) haiku row still shapes the usual
	// model but reserves no column width.
	ex = computeExceptions(groups, groups[:1], fleetDisplay{})
	if ex.common != "opus" || ex.modelW != 0 || ex.ctx {
		t.Errorf("hidden exception reserved width: %+v", ex)
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
