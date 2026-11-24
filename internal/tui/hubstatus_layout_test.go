package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
)

func layoutStatus() *hubStatus {
	return &hubStatus{
		Version: hubStatusVersion, Glyph: "●", Title: "Status pane", Project: "agentctl",
		State: "working", StateSince: statusNow.Add(-4 * time.Minute),
		Detail: rowDetailParts{Recap: "Moving the detail out of the hub list into a pane."},
		Ticket: 42, Agent: "go-engineer", Branch: "agent/go-engineer/issue-42",
		PRNumber: 7, PRURL: "https://example.test/pull/7", ParentTitle: "Fleet view overhaul work",
		Model: "opus-4-7", CtxPct: 42, CtxWarn: 70, CtxCrit: 90, CostUSD: 1.234,
		StartTime: statusNow.Add(-47 * time.Minute).Unix(),
	}
}

func renderPlain(st *hubStatus, w, h int) []string {
	sel := HubSelection{Session: "s", WindowID: "@1", RuntimeDir: "/p", Status: st}
	return strings.Split(ansi.Strip(renderHubStatus(sel, false, w, h, statusNow)), "\n")
}

func TestHubStatusLayout(t *testing.T) {
	InitStyles()
	blocked := layoutStatus()
	blocked.State, blocked.Glyph = "blocked", "⏸"
	blocked.PendingTool, blocked.PendingToolDetail = "Bash", "rm -rf node_modules && npm install --legacy-peer-deps --foreground-scripts"
	blockedNoTool := layoutStatus()
	blockedNoTool.State, blockedNoTool.Glyph = "blocked", "⏸"
	cron := &hubStatus{Version: hubStatusVersion, Glyph: "○", Title: "nightly", State: "idle",
		SchedBy: "nightly", SchedAt: statusNow.Add(-3 * time.Hour), Note: "no prompt yet · 3h"}
	fresh := &hubStatus{Version: hubStatusVersion, Glyph: "○", Title: "go-engineer", State: "idle", Agent: "go-engineer"}

	tests := []struct {
		name    string
		st      *hubStatus
		w, h    int
		want    []string // substrings of the plain output
		wantNot []string
		lastRow string // substring required on row h (empty: no footer expected)
	}{
		{"working wide", layoutStatus(), 166, 6,
			[]string{"working 4m · agentctl", "#42 · go-engineer · agent/go-engineer/issue-42 · PR #7 · ← Fleet view overhaul work", "opus-4-7 · ctx 42% · $1.23 · 47m"}, nil, "47m"},
		{"blocked with tool", blocked, 90, 6,
			[]string{"blocked 4m", "needs you: Bash  rm -rf node_modules"}, nil, "PR #7"},
		{"blocked without tool", blockedNoTool, 90, 6, []string{"blocked 4m"}, []string{"needs you"}, "PR #7"},
		{"cron no recap", cron, 90, 6, []string{"nightly", "no prompt yet", "⏲ nightly · fired 3h"}, nil, "fired 3h"},
		{"fresh window", fresh, 60, 6, []string{"idle"}, []string{"ctx", "$", "·  ·"}, ""},
		{"narrow 60 drops length, branch", layoutStatus(), 60, 6, []string{"working 4m", "PR #7", "opus-4-7"}, []string{"47m", "issue-42"}, "PR #7"},
		{"narrow 40", layoutStatus(), 40, 6, []string{"working", "PR #7"}, []string{"47m"}, "PR #7"},
		{"h=2 recap wins", layoutStatus(), 90, 2, []string{"Moving the detail"}, []string{"PR #7"}, ""},
		{"h=1 title only", layoutStatus(), 90, 1, []string{"working"}, []string{"Moving"}, ""},
		{"h=3 recap before needs", blocked, 90, 3, []string{"Moving the detail", "PR #7"}, []string{"needs you"}, "PR #7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := renderPlain(tt.st, tt.w, tt.h)
			if len(lines) != tt.h {
				t.Fatalf("%d lines, want %d", len(lines), tt.h)
			}
			for i, l := range lines {
				if lipgloss.Width(l) > tt.w {
					t.Errorf("line %d is %d wide, over %d: %q", i, lipgloss.Width(l), tt.w, l)
				}
			}
			all := strings.Join(lines, "\n")
			for _, w := range tt.want {
				if !strings.Contains(all, w) {
					t.Errorf("missing %q in:\n%s", w, all)
				}
			}
			for _, w := range tt.wantNot {
				if strings.Contains(all, w) {
					t.Errorf("unexpected %q in:\n%s", w, all)
				}
			}
			if tt.lastRow != "" && !strings.Contains(lines[tt.h-1], tt.lastRow) {
				t.Errorf("footer not pinned to row %d: %q", tt.h, lines[tt.h-1])
			}
		})
	}
}

func TestHubStatusFooterDropOrder(t *testing.T) {
	InitStyles()
	st := layoutStatus()
	st.Kids, st.KidsBlocked, st.KidsWorking = 3, 1, 2
	st.Extra = []string{"acct work"}
	st.CtxPct = 95 // at critical: never dropped
	full := ansi.Strip(hubStatusFooter(st, 400, statusNow))
	if !strings.Contains(full, "↳ 3 sub-agents ⏸1 ●2 · acct work") || !strings.Contains(full, "ctx 95%!") {
		t.Fatalf("full footer = %q", full)
	}
	// Walk the width down: each droppable fact disappears (for good) in the
	// documented order, and the never-dropped ones stay while it does.
	markers := []string{"47m", "issue-42", "←", "go-engineer", "opus", "$1.23"}
	gone := make([]int, len(markers)) // widest width at which each is absent
	for w := 400; w >= 60; w-- {
		got := ansi.Strip(hubStatusFooter(st, w, statusNow))
		if lipgloss.Width(got) > w {
			t.Fatalf("w=%d: footer is %d wide: %q", w, lipgloss.Width(got), got)
		}
		for i, m := range markers {
			if !strings.Contains(got, m) && gone[i] == 0 {
				gone[i] = w
			}
			if strings.Contains(got, m) && gone[i] != 0 {
				t.Fatalf("w=%d: %q came back after dropping at %d", w, m, gone[i])
			}
		}
		for _, keep := range []string{"PR #7", "#42", "⏸1", "acct work", "ctx 95%!"} {
			if !strings.Contains(got, keep) {
				t.Fatalf("w=%d: never-dropped %q missing in %q", w, keep, got)
			}
		}
	}
	for i := 1; i < len(markers); i++ {
		if gone[i] == 0 || gone[i] > gone[i-1] {
			t.Errorf("%q dropped at %d, before %q at %d", markers[i], gone[i], markers[i-1], gone[i-1])
		}
	}
	// Short branch and a 16-column parent before either is dropped.
	st2 := layoutStatus()
	st2.Model, st2.CostUSD, st2.StartTime, st2.CtxPct = "", 0, 0, 0
	got := ansi.Strip(hubStatusFooter(st2, 70, statusNow))
	if !strings.Contains(got, "…/issue-42") {
		t.Errorf("branch not shortened: %q", got)
	}
	if st2.Branch = "main"; !strings.Contains(ansi.Strip(hubStatusFooter(st2, 200, statusNow)), "main") {
		t.Error("plain branch lost")
	}
	// Overflowing never-drop facts are end-truncated.
	st3 := &hubStatus{Version: hubStatusVersion, PRNumber: 1234, Ticket: 99, Extra: []string{"acct a-very-long-account-name"}}
	if got := ansi.Strip(hubStatusFooter(st3, 20, statusNow)); lipgloss.Width(got) > 20 || !strings.HasSuffix(got, "…") {
		t.Errorf("not truncated: %q", got)
	}
}

func TestHubStatusFooterCtxWarnKept(t *testing.T) {
	InitStyles()
	st := &hubStatus{Version: hubStatusVersion, PRNumber: 5, Model: "opus", CtxPct: 75, CtxWarn: 70, CtxCrit: 90, CostUSD: 3}
	if got := ansi.Strip(hubStatusFooter(st, 16, statusNow)); !strings.Contains(got, "ctx 75%") {
		t.Errorf("ctx at warn dropped: %q", got)
	}
	st.CtxPct = 40
	if got := ansi.Strip(hubStatusFooter(st, 12, statusNow)); strings.Contains(got, "ctx") {
		t.Errorf("ctx below warn kept at 12 cols: %q", got)
	}
}

func TestHubStatusPRHyperlink(t *testing.T) {
	InitStyles()
	got := hubStatusFooter(layoutStatus(), 200, statusNow)
	if !strings.Contains(got, "\x1b]8;;https://example.test/pull/7") {
		t.Errorf("no OSC 8 link in %q", got)
	}
}

func TestHubStatusLegacyBlockStillRenders(t *testing.T) {
	InitStyles()
	st := &hubStatus{Glyph: "●", Title: "Old", Project: "p", Chips: []string{"#1", "go-engineer"},
		Detail: rowDetailParts{Recap: "recap text"}}
	lines := renderPlain(st, 60, 6)
	if !strings.Contains(lines[0], "Old") || !strings.Contains(strings.Join(lines, "\n"), "[#1] [go-engineer]") {
		t.Errorf("legacy block lost content: %q", lines)
	}
}

func TestHubStatusJSONRoundTrip(t *testing.T) {
	st := layoutStatus()
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sched_at") || strings.Contains(string(data), "pending_tool") {
		t.Errorf("zero fields not omitted: %s", data)
	}
	var back hubStatus
	if err := json.Unmarshal(data, &back); err != nil || !back.StateSince.Equal(st.StateSince) || back.PRURL != st.PRURL {
		t.Errorf("round trip: %v %+v", err, back)
	}
	// An old file (no new fields) decodes and renders.
	var old hubStatus
	if err := json.Unmarshal([]byte(`{"glyph":"●","title":"T","detail":{},"chips":["#1"]}`), &old); err != nil || old.Version != 0 {
		t.Fatalf("old file: %v %+v", err, old)
	}
}

func TestHubStatusForPublishes(t *testing.T) {
	InitStyles()
	mk := func(id, parent, state string) fleet.Row {
		r := testRow("w"+id, "s", state, "")
		r.Session, r.WindowID, r.Parent = "s", id, parent
		return r
	}
	parent := mk("@1", "", "idle")
	parent.Recap = fleet.RecapLine{Title: "Orchestrating", Text: "x", Source: fleet.RecapSummary}
	parent.TurnDone = true
	b, w, d := mk("@2", "@1", "blocked"), mk("@3", "@1", "working"), mk("@4", "@1", "idle")
	b.Dispatched, b.Detached = true, false
	b.Branch, b.PendingTool, b.PendingToolDetail = "agent/x/1", "Bash", "ls"
	b.ModelName, b.CostUSD, b.ContextPct, b.PRNumber, b.PRURL = "claude-opus-4-7[1m]", 2.5, 55, 9, "https://x/9"
	b.Backend, b.Account = "nous", "work"
	b.StateSince = statusNow
	groups := []fleet.Group{{Session: "s", Rows: []fleet.Row{parent, b, w, d}}}

	if st := hubStatusFor(parent, fleetDisplay{}, groups); st.State != "your turn" || st.Kids != 3 || st.KidsBlocked != 1 || st.KidsWorking != 1 {
		t.Errorf("parent status = %+v", st)
	}
	st := hubStatusFor(b, fleetDisplay{defaultAccount: "personal"}, groups)
	if st.State != "blocked" || st.PendingTool != "Bash" || st.ParentTitle != "Orchestrating" || st.Branch != "agent/x/1" ||
		st.Model != "opus-4-7" || st.CostUSD != 2.5 || st.CtxPct != 55 || st.CtxWarn == 0 || st.CtxCrit == 0 ||
		st.PRURL != "https://x/9" || !st.StateSince.Equal(statusNow) || st.Version != hubStatusVersion {
		t.Errorf("child status = %+v", st)
	}
	if len(st.Extra) != 2 || st.Extra[0] != "nous" || st.Extra[1] != "acct work" {
		t.Errorf("extra = %v", st.Extra)
	}
	// Pending tool is meaningless unless blocked; a bare row has no branch.
	w.PendingTool, w.Branch = "Read", "br"
	if st := hubStatusFor(w, fleetDisplay{}, groups); st.PendingTool != "" || st.Branch != "" || st.State != "working" {
		t.Errorf("working status = %+v", st)
	}
	done := mk("@5", "", "idle")
	done.Done = true
	if st := hubStatusFor(done, fleetDisplay{}, nil); st.State != "done" {
		t.Errorf("done state = %q", st.State)
	}
}

func TestHubStatusFooterSessionLength(t *testing.T) {
	InitStyles()
	for _, tt := range []struct {
		ago  time.Duration
		want string
	}{
		{47 * time.Minute, "47m"},
		{2*time.Hour + 14*time.Minute, "2h14m"},
		{3 * time.Hour, "3h"},
	} {
		st := &hubStatus{Version: hubStatusVersion, StartTime: statusNow.Add(-tt.ago).Unix()}
		if got := ansi.Strip(hubStatusFooter(st, 40, statusNow)); strings.TrimSpace(got) != tt.want {
			t.Errorf("%v: got %q, want %q", tt.ago, got, tt.want)
		}
	}
}
