package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func layoutStatus() *hubStatus {
	return &hubStatus{
		Version: hubStatusVersion, Glyph: "●", Title: "Status pane", Project: "agentctl",
		State: "working", StateSince: statusNow.Add(-4 * time.Minute),
		Detail: rowDetailParts{Recap: "Moving the detail out of the hub list into a pane."},
		Ticket: 42, Agent: "go-engineer",
		PRNumber: 7, PRURL: "https://example.test/pull/7", ParentTitle: "Fleet view overhaul work",
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
	trust := layoutStatus()
	trust.State, trust.Glyph = "blocked", "⏸"
	trust.PendingTool, trust.PendingToolDetail = windowstate.PendingTrust, "/repo/.worktrees/agent-x"
	blockedNoTool := layoutStatus()
	blockedNoTool.State, blockedNoTool.Glyph = "blocked", "⏸"
	cron := &hubStatus{Version: hubStatusVersion, Glyph: "○", Title: "nightly", State: "idle",
		SchedBy: "nightly", SchedAt: statusNow.Add(-3 * time.Hour), Note: "no prompt yet · 3h"}
	fresh := &hubStatus{Version: hubStatusVersion, Glyph: "○", Title: "go-engineer", State: "idle", Agent: "go-engineer"}

	donePR := layoutStatus()
	donePR.State, donePR.Glyph, donePR.StateSince, donePR.PRNumber = "done", "✓", statusNow.Add(-time.Hour), 12
	kids := layoutStatus()
	kids.Kids, kids.KidsBlocked, kids.KidsWorking, kids.Extra = 3, 1, 2, []string{"acct work"}
	agentOnly := &hubStatus{Version: hubStatusVersion, Glyph: "●", Title: "Status pane", Project: "agentctl", Agent: "go-engineer",
		State: "working", StateSince: statusNow.Add(-4 * time.Minute), Detail: rowDetailParts{Recap: "Recap sentence."}}
	untitled := &hubStatus{Version: hubStatusVersion, Glyph: "●", Title: "Fixed the build", TitleFromRecap: true, State: "working",
		Detail: rowDetailParts{Recap: "Fixed the build. Then added tests."}}

	untitledLong := &hubStatus{Version: hubStatusVersion, Glyph: "●", TitleFromRecap: true, State: "working",
		Title:  "Refactoring the authentication middleware to use the new session store",
		Detail: rowDetailParts{Recap: "Refactoring the authentication middleware to use the new session store."}}

	tests := []struct {
		name    string
		st      *hubStatus
		w, h    int
		want    []string // substrings of the plain output
		wantNot []string
		lastRow string // substring required on row h (empty: no footer expected)
	}{
		{"working wide", layoutStatus(), 166, 6,
			[]string{"working 4m · agentctl", "#42 · go-engineer · PR #7 · ← Fleet view overhaul work"},
			[]string{"opus", "ctx", "$1", "47m", "issue-42"}, "← Fleet view"},
		{"width 90", layoutStatus(), 90, 6, []string{"working 4m · agentctl", "#42 · go-engineer · PR #7"}, nil, "PR #7"},
		{"blocked with tool", blocked, 90, 6,
			[]string{"blocked 4m", "needs you: Bash  rm -rf node_modules"}, nil, "PR #7"},
		{"blocked on folder trust", trust, 90, 6,
			[]string{"needs you: folder trust  /repo/.worktrees/agent-x"}, []string{"needs you: trust"}, "PR #7"},
		{"blocked without tool", blockedNoTool, 90, 6, []string{"blocked 4m"}, []string{"needs you"}, "PR #7"},
		{"cron no recap", cron, 90, 6, []string{"nightly", "no prompt yet", "⏲ nightly · fired 3h"}, nil, "fired 3h"},
		{"done with PR", donePR, 90, 6, []string{"done 1h", "PR #12"}, nil, "PR #12"},
		{"child tally", kids, 90, 6, []string{"↳ 3 sub-agents ⏸1 ●2 · acct work"}, nil, "acct work"},
		{"agent only collapses", fresh, 60, 6, []string{"idle"}, []string{"ctx", "$", "·  ·"}, ""},
		{"agent on title line", agentOnly, 90, 6, []string{"go-engineer  working 4m · agentctl", "Recap sentence"}, nil, ""},
		{"narrow 60", layoutStatus(), 60, 6, []string{"working 4m", "PR #7"}, nil, "PR #7"},
		{"narrow 40", layoutStatus(), 40, 6, []string{"working", "PR #7"}, nil, "PR #7"},
		{"h=2 recap wins", layoutStatus(), 90, 2, []string{"Moving the detail"}, []string{"PR #7"}, ""},
		{"h=1 title only", layoutStatus(), 90, 1, []string{"working"}, []string{"Moving"}, ""},
		{"h=3 recap before needs", blocked, 90, 3, []string{"Moving the detail", "PR #7"}, []string{"needs you"}, "PR #7"},
		{"untitled narrow keeps full recap", untitledLong, 40, 6, []string{"middleware to use the new session", "store."}, nil, ""},
		{"untitled recap not repeated", untitled, 90, 6, []string{"Fixed the build", "Then added tests"}, nil, ""},
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
	full := ansi.Strip(hubStatusFooter(st, 400, statusNow))
	if !strings.Contains(full, "↳ 3 sub-agents ⏸1 ●2 · acct work") {
		t.Fatalf("full footer = %q", full)
	}
	markers := []string{"←", "go-engineer"}
	gone := make([]int, len(markers)) // widest width at which each is absent
	for w := 400; w >= 50; w-- {
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
		for _, keep := range []string{"PR #7", "#42", "⏸1", "acct work"} {
			if !strings.Contains(got, keep) {
				t.Fatalf("w=%d: never-dropped %q missing in %q", w, keep, got)
			}
		}
	}
	if gone[1] == 0 || gone[1] > gone[0] {
		t.Errorf("agent dropped at %d, before parent at %d", gone[1], gone[0])
	}
	// Overflowing never-drop facts are end-truncated.
	st3 := &hubStatus{Version: hubStatusVersion, PRNumber: 1234, Ticket: 99, Extra: []string{"acct a-very-long-account-name"}}
	if got := ansi.Strip(hubStatusFooter(st3, 20, statusNow)); lipgloss.Width(got) > 20 || !strings.HasSuffix(got, "…") {
		t.Errorf("not truncated: %q", got)
	}
	// Agent only: no footer at all.
	if got := hubStatusFooter(&hubStatus{Version: hubStatusVersion, Agent: "go-engineer"}, 80, statusNow); got != "" {
		t.Errorf("agent-only footer = %q", got)
	}
}

func TestAfterFirstSentence(t *testing.T) {
	for in, want := range map[string]string{
		"One. Two three.":      "Two three.",
		"Only one sentence.":   "",
		"No end":               "",
		"See a.b.c now. Next!": "Next!",
		"  A!  B ":             "B",
	} {
		if got := afterFirstSentence(in); got != want {
			t.Errorf("afterFirstSentence(%q) = %q, want %q", in, got, want)
		}
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
	// A file from the previous layout (branch, model, cost) still decodes.
	var prev hubStatus
	if err := json.Unmarshal([]byte(`{"v":2,"glyph":"●","branch":"b","model":"opus","ctx_pct":5,"cost_usd":1,"start_time":9}`), &prev); err != nil || prev.Version != 2 {
		t.Fatalf("previous file: %v %+v", err, prev)
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
	if st.State != "blocked" || st.PendingTool != "Bash" || st.ParentTitle != "Orchestrating" ||
		st.PRURL != "https://x/9" || !st.StateSince.Equal(statusNow) || st.Version != hubStatusVersion {
		t.Errorf("child status = %+v", st)
	}
	if len(st.Extra) != 2 || st.Extra[0] != "nous" || st.Extra[1] != "acct work" {
		t.Errorf("extra = %v", st.Extra)
	}
	// Pending tool is meaningless unless blocked.
	w.PendingTool = "Read"
	if st := hubStatusFor(w, fleetDisplay{}, groups); st.PendingTool != "" || st.State != "working" {
		t.Errorf("working status = %+v", st)
	}
	done := mk("@5", "", "idle")
	done.Done = true
	if st := hubStatusFor(done, fleetDisplay{}, nil); st.State != "done" {
		t.Errorf("done state = %q", st.State)
	}
}
