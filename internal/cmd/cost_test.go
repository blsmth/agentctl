package cmd

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/tokens"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func mkRow(agent, project, model string, day int, b sessions.Billing, usd float64, known bool) costRow {
	return costRow{
		Start:   time.Date(2026, 11, day, 12, 0, 0, 0, time.Local),
		Project: project, Agent: agent, Model: model, Turns: 2,
		Cost: sessions.SessionCost{
			USD: usd, Known: known, Billing: b,
			Usage:   tokens.Usage{CacheRead: 1000, Output: 10},
			ByModel: []sessions.ModelCost{{Model: model, Turns: 2, USD: usd, Known: known, Usage: tokens.Usage{CacheRead: 1000, Output: 10}}},
		},
	}
}

func TestParseSince(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 7 * 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"36h", 36 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"abc", 0, true},
		{"-3d", 0, true},
	}
	for _, tt := range tests {
		got, err := parseSince(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("parseSince(%q) = (%v, %v), want (%v, err=%v)", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestSumCosts_UnknownExcludedAndBillingSplit(t *testing.T) {
	rows := []costRow{
		mkRow("a", "p", "claude-opus-5-5", 1, sessions.BillingSub, 10, true),
		mkRow("a", "p", "openai/gpt-6-astra", 2, sessions.BillingMetered, 7.22, true),
		mkRow("a", "p", "qwen3", 3, sessions.BillingFree, 0, true),
		mkRow("a", "p", "mystery", 4, sessions.BillingUnknown, 0, false),
	}
	got := sumCosts(rows)
	if got.Sessions != 4 || got.Excluded != 1 {
		t.Errorf("sessions=%d excluded=%d, want 4 and 1", got.Sessions, got.Excluded)
	}
	if math.Abs(got.Metered-7.22) > 1e-9 || math.Abs(got.Sub-10) > 1e-9 || math.Abs(got.Total()-17.22) > 1e-9 {
		t.Errorf("metered=%v sub=%v total=%v", got.Metered, got.Sub, got.Total())
	}
}

func TestGroupCosts(t *testing.T) {
	rows := []costRow{
		mkRow("lead", "agentctl", "claude-opus-5-5", 1, sessions.BillingSub, 10, true),
		mkRow("lead", "other", "claude-opus-5-5", 1, sessions.BillingSub, 5, true),
		mkRow("review", "agentctl", "openai/gpt-6-astra", 2, sessions.BillingMetered, 7, true),
		mkRow("review", "agentctl", "mystery", 2, sessions.BillingUnknown, 0, false),
	}
	byAgent, err := groupCosts(rows, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(byAgent) != 2 || byAgent[0].Key != "lead" || byAgent[0].Total() != 15 || byAgent[1].Excluded != 1 {
		t.Errorf("by agent = %+v", byAgent)
	}
	byDay, _ := groupCosts(rows, "day")
	if len(byDay) != 2 || byDay[0].Key != "2026-11-01" {
		t.Errorf("by day should be chronological: %+v", byDay)
	}
	byModel, _ := groupCosts(rows, "model")
	if len(byModel) != 3 {
		t.Errorf("by model = %+v", byModel)
	}
	byProject, _ := groupCosts(rows, "project")
	if len(byProject) != 2 || byProject[0].Key != "agentctl" {
		t.Errorf("by project = %+v", byProject)
	}
	if _, err := groupCosts(rows, "bogus"); err == nil {
		t.Error("invalid --by should error")
	}
}

func withTokens(r costRow, out int) costRow {
	r.Cost.Usage.Output = out
	return r
}

func TestSortRowsAndTop(t *testing.T) {
	a := mkRow("a", "p", "m", 1, sessions.BillingSub, 5, true)
	b := mkRow("a", "p", "m", 2, sessions.BillingSub, 20, true)
	c := mkRow("a", "p", "m", 3, sessions.BillingSub, 0, false)
	d := mkRow("a", "p", "m", 4, sessions.BillingSub, 10, true)
	a.Turns, b.Turns, c.Turns, d.Turns = 9, 1, 3, 2
	a = withTokens(a, 9000)
	rows := func() []costRow { return []costRow{a, b, c, d} }

	days := func(rs []costRow) []int {
		var out []int
		for _, r := range rs {
			out = append(out, r.Start.Day())
		}
		return out
	}
	tests := []struct {
		by   string
		top  int
		want []int
	}{
		{"", 0, []int{1, 2, 3, 4}},
		{"date", 2, []int{3, 4}}, // newest two, still chronological
		{"cost", 0, []int{2, 4, 1, 3}},
		{"cost", 2, []int{2, 4}},
		{"turns", 3, []int{1, 3, 4}},
		{"tokens", 1, []int{1}},
	}
	for _, tt := range tests {
		t.Run(tt.by+"/"+strconv.Itoa(tt.top), func(t *testing.T) {
			rs := rows()
			sortRows(rs, tt.by)
			rs = topRows(rs, tt.top, tt.by)
			if got := days(rs); !slices.Equal(got, tt.want) {
				t.Errorf("got days %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGroupCostsByAccount(t *testing.T) {
	x := mkRow("a", "p", "m", 1, sessions.BillingSub, 1, true)
	x.Account = "ll"
	y := mkRow("a", "p", "m", 1, sessions.BillingSub, 9, true)
	y.Account = "personal"
	z := mkRow("a", "p", "m", 1, sessions.BillingSub, 0.5, true)
	g, err := groupCosts([]costRow{x, y, z}, "account")
	if err != nil {
		t.Fatal(err)
	}
	if len(g) != 3 || g[0].Key != "personal" || g[2].Key != "(none)" {
		t.Errorf("groups = %+v", g)
	}
	// --by day with --sort cost orders by cost, not chronologically.
	d1 := mkRow("a", "p", "m", 1, sessions.BillingSub, 1, true)
	d2 := mkRow("a", "p", "m", 2, sessions.BillingSub, 9, true)
	byDay, _ := groupCosts([]costRow{d1, d2}, "day")
	sortGroups(byDay, "day", true)
	if byDay[0].Key != "2026-11-02" {
		t.Errorf("by day --sort cost = %+v", byDay)
	}
}

func TestSharePct(t *testing.T) {
	a := mkRow("a", "p", "m", 1, sessions.BillingSub, 25, true)
	b := mkRow("a", "p", "m", 1, sessions.BillingSub, 75, true)
	u := mkRow("a", "p", "m", 1, sessions.BillingUnknown, 0, false)
	tot := sumCosts([]costRow{a, b, u})
	if p, ok := sharePct(a, tot); !ok || math.Abs(p-25) > 1e-9 {
		t.Errorf("share = %v, %v", p, ok)
	}
	if _, ok := sharePct(u, tot); ok {
		t.Error("unknown cost has no share")
	}
	if got := sharePctCell(u, tot); got != "-" {
		t.Errorf("cell = %q", got)
	}
}

func TestTitleIndex(t *testing.T) {
	idx := newTitleIndex()
	idx.add("s1", "", "Fix the flaky login test in the auth package please\nmore")
	idx.add("s1", "Login test fix", "ignored")
	idx.add("s2", "", "")
	idx.add("s3", "", "short prompt")
	if got := idx.get("s1"); got != "Login test fix" {
		t.Errorf("short title should beat prompt, got %q", got)
	}
	if got := idx.get("s2"); got != "" {
		t.Errorf("no title = %q", got)
	}
	if got := idx.get("s3"); got != "short prompt" {
		t.Errorf("prompt fallback = %q", got)
	}
	idx2 := newTitleIndex()
	idx2.add("s", "", strings.Repeat("word ", 20))
	if got := idx2.get("s"); utf8.RuneCountInString(got) > costTitleWidth {
		t.Errorf("title too long (%d): %q", utf8.RuneCountInString(got), got)
	}
	if got := idx.get("unknown"); got != "" {
		t.Errorf("unknown session = %q", got)
	}
}

func TestLoadCostTitles(t *testing.T) {
	rt := t.TempDir()
	restore := paths.Bind(rt)
	t.Cleanup(restore)
	winDir := windowstate.WindowsDirFor(rt)
	if err := os.MkdirAll(winDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ws := `{"session_id":"sess-live","short_title":{"title":"Live title"},"first_prompt":"ignored"}`
	if err := os.WriteFile(filepath.Join(winDir, "@1.json"), []byte(ws), 0o644); err != nil {
		t.Fatal(err)
	}
	ws2 := `{"session_id":"sess-prompt","first_prompt":"Investigate the slow build"}`
	if err := os.WriteFile(filepath.Join(winDir, "@2.json"), []byte(ws2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := closedlog.Append(closedlog.Record{WindowID: "@9", SessionID: "sess-closed", ShortTitle: "Closed title"}); err != nil {
		t.Fatal(err)
	}
	if err := closedlog.Append(closedlog.Record{WindowID: "@8", SessionID: "sess-closed-prompt", FirstPrompt: "Closed prompt text"}); err != nil {
		t.Fatal(err)
	}
	get := loadCostTitles(24 * time.Hour)
	for id, want := range map[string]string{
		"sess-live": "Live title", "sess-prompt": "Investigate the slow build",
		"sess-closed": "Closed title", "sess-closed-prompt": "Closed prompt text", "plain": "",
	} {
		if got := get(id); got != want {
			t.Errorf("title(%s) = %q, want %q", id, got, want)
		}
	}
}

func TestAccountOfLog(t *testing.T) {
	accts := []claudehome.Account{{Name: "ll", Dir: "/h/.claude"}, {Name: "personal", Dir: "/h/.claude-personal"}}
	tests := map[string]string{
		"/h/.claude/projects/x/a.jsonl":          "ll",
		"/h/.claude-personal/projects/x/a.jsonl": "personal",
		"/elsewhere/pi/a.jsonl":                  "",
	}
	for path, want := range tests {
		if got := accountOfLog(path, accts); got != want {
			t.Errorf("accountOfLog(%s) = %q, want %q", path, got, want)
		}
	}
}

func TestPrintCostTable(t *testing.T) {
	a := mkRow("lead", "proj", "opus", 1, sessions.BillingSub, 30, true)
	a.Title, a.Account = "Fix login", "personal"
	b := mkRow("lead", "proj", "opus", 2, sessions.BillingSub, 10, true)
	rows := []costRow{a, b}
	tot := sumCosts(rows)
	out := captureStdout(t, func() { printCostTable(rows[:1], nil, tot, CostOptions{Sort: "cost"}, 2, 1) })
	for _, want := range []string{"TITLE", "ACCOUNT", "Fix login", "personal", "75%", "showing 1 of 2 sessions", "sub = API-equivalent"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	out = captureStdout(t, func() { printCostTable(rows, nil, tot, CostOptions{}, 2, 2) })
	if strings.Contains(out, "showing") || strings.Contains(out, "%") && strings.Contains(out, "75%") {
		t.Errorf("unexpected top/share output:\n%s", out)
	}
	m := mkRow("a", "p", "m", 1, sessions.BillingMetered, 2, true)
	out = captureStdout(t, func() { printCostTable([]costRow{m}, nil, sumCosts([]costRow{m}), CostOptions{}, 1, 1) })
	if strings.Contains(out, "API-equivalent cost;") {
		t.Errorf("footnote shown without sub rows:\n%s", out)
	}
	out = captureStdout(t, func() { printCostTable(nil, nil, costTotals{}, CostOptions{}, 0, 0) })
	if !strings.Contains(out, "No sessions in range") {
		t.Errorf("empty output = %q", out)
	}
}

func TestPrintCostJSONFields(t *testing.T) {
	a := mkRow("lead", "proj", "opus", 1, sessions.BillingSub, 25, true)
	a.Title, a.Account = "Fix login", "ll"
	tot := sumCosts([]costRow{a})
	out := captureStdout(t, func() {
		if err := printCostJSON([]costRow{a}, nil, tot, nil, CostOptions{Sort: "cost"}, 5, 1); err != nil {
			t.Fatal(err)
		}
	})
	var got struct {
		Matched  int `json:"matched"`
		Shown    int `json:"shown"`
		Sessions []struct {
			ID       string   `json:"id"`
			Title    string   `json:"title"`
			Account  string   `json:"account"`
			SharePct *float64 `json:"share_pct"`
			CostUSD  *float64 `json:"cost_usd"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got.Matched != 5 || got.Shown != 1 || len(got.Sessions) != 1 {
		t.Fatalf("got %+v", got)
	}
	s := got.Sessions[0]
	if s.Title != "Fix login" || s.Account != "ll" || s.SharePct == nil || *s.SharePct != 100 || s.CostUSD == nil {
		t.Errorf("row = %+v", s)
	}
}

func TestRunCostRejectsBadFlags(t *testing.T) {
	if err := RunCost(CostOptions{Sort: "bogus"}); err == nil {
		t.Error("bad --sort should error")
	}
	if err := RunCost(CostOptions{Top: -1}); err == nil {
		t.Error("negative --top should error")
	}
	if err := RunCost(CostOptions{Account: "nope"}); err == nil {
		t.Error("unknown --account should error")
	}
}
