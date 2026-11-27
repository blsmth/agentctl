package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestActivityBuckets(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 30, 20, 0, time.UTC) // 20s into 12:30
	at := func(h, m, s int) time.Time { return time.Date(2026, 11, 27, h, m, s, 0, time.UTC) }
	tests := []struct {
		name  string
		calls []time.Time
		want  map[int]int // bucket index -> count; every other bucket is 0
	}{
		{name: "empty window"},
		{name: "current minute is the last cell", calls: []time.Time{at(12, 30, 0), at(12, 30, 19)}, want: map[int]int{29: 2}},
		{name: "minute boundary", calls: []time.Time{at(12, 29, 59), at(12, 30, 0)}, want: map[int]int{28: 1, 29: 1}},
		{name: "oldest minute is the first cell", calls: []time.Time{at(12, 1, 0), at(12, 1, 59)}, want: map[int]int{0: 2}},
		{name: "older than the window is dropped", calls: []time.Time{at(12, 0, 59), at(11, 0, 0)}},
		{name: "future counts now", calls: []time.Time{at(12, 31, 30)}, want: map[int]int{29: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := activityBuckets(tt.calls, now)
			if len(got) != hubActivityMinutes {
				t.Fatalf("%d buckets, want %d", len(got), hubActivityMinutes)
			}
			for i, c := range got {
				if c != tt.want[i] {
					t.Errorf("bucket %d = %d, want %d (all: %v)", i, c, tt.want[i], got)
				}
			}
		})
	}
}

func TestActivityBucketsClampsToWindow(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	var calls []time.Time
	for i := range 90 { // one call a minute for 90 minutes
		calls = append(calls, now.Add(-time.Duration(i)*time.Minute))
	}
	b := activityBuckets(calls, now)
	if len(b) != 30 {
		t.Fatalf("%d buckets", len(b))
	}
	for i, c := range b {
		if c != 1 {
			t.Errorf("bucket %d = %d, want 1", i, c)
		}
	}
}

func TestRenderSparkline(t *testing.T) {
	InitStyles()
	if s, w := renderSparkline(make([]int, 30)); s != "" || w != 0 {
		t.Errorf("no calls rendered %q (%d)", s, w)
	}
	b := make([]int, 30)
	b[0], b[15], b[29] = 1, 4, 8
	s, w := renderSparkline(b)
	plain := ansi.Strip(s)
	if w != 34 || lipgloss.Width(s) != w {
		t.Errorf("width = %d (rendered %d), want 34", w, lipgloss.Width(s))
	}
	r := []rune(plain)
	if r[0] != '▂' || r[1] != '▁' || r[15] != '▅' || r[29] != '█' || !strings.HasSuffix(plain, " 30m") {
		t.Errorf("sparkline = %q", plain)
	}
}

func TestToolCallTimes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	lines := []string{
		`{"type":"assistant","timestamp":"2026-11-27T11:00:00Z","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-27T11:50:00Z","message":{"content":[{"type":"tool_use","name":"Bash"},{"type":"tool_use","name":"Read"}]}}`,
		`{"type":"user","timestamp":"2026-11-27T11:50:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"x","content":"has \"tool_use\" in it"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-27T11:51:00Z","message":{"content":[{"type":"text","text":"done"}]}}`,
		// pi: a toolCall block, normalized to tool_use.
		`{"type":"message","id":"a","parentId":"p","timestamp":"2026-11-27T11:52:00Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c","name":"bash","arguments":{}}]}}`,
		`{"type":"assistant","timestamp":"2026-11-27T11:53:`, // torn last line
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 11, 27, 11, 30, 0, 0, time.UTC)
	got := toolCallTimes(path, since)
	want := []string{"11:50:00", "11:50:00", "11:52:00"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, ts := range got {
		if f := ts.UTC().Format("15:04:05"); f != want[i] {
			t.Errorf("call %d at %s, want %s", i, f, want[i])
		}
	}
	if got := toolCallTimes(filepath.Join(dir, "missing.jsonl"), since); got != nil {
		t.Errorf("missing file = %v", got)
	}
}

func TestHubActivityCacheRefresh(t *testing.T) {
	reads := 0
	orig := readToolCallTimes
	readToolCallTimes = func(string, time.Time) []time.Time { reads++; return []time.Time{statusNow} }
	t.Cleanup(func() { readToolCallTimes = orig })

	c := newHubActivityCache()
	st := &hubStatus{SessionID: "s1", LogPath: "/l/s1.jsonl"}
	now := statusNow
	cmd := c.refresh(st, now)
	if cmd == nil {
		t.Fatal("first refresh returned no command")
	}
	if c.refresh(st, now) != nil {
		t.Error("a second read started while one is in flight")
	}
	c.store(cmd().(hubActivityMsg))
	if len(c.calls(st)) != 1 || reads != 1 {
		t.Fatalf("calls = %v, reads = %d", c.calls(st), reads)
	}
	if c.refresh(st, now.Add(hubActivityRefresh-time.Second)) != nil {
		t.Error("re-read before the refresh interval")
	}
	if c.refresh(st, now.Add(hubActivityRefresh)) == nil {
		t.Error("no re-read after the refresh interval")
	}
	if c.refresh(&hubStatus{SessionID: "s2"}, now) != nil {
		t.Error("read started with no log path")
	}
}

// sparkStatus is a status block with a long title and tool calls over the
// last half hour.
func sparkStatus() *hubStatus {
	st := &hubStatus{
		Version: hubStatusVersion, Glyph: "●", State: "working", StateSince: statusNow.Add(-4 * time.Minute),
		Title:   "Add an activity sparkline and CI chip to the hub status pane",
		Project: "agentctl",
	}
	for i := range 30 {
		for range i % 7 {
			st.activity = append(st.activity, statusNow.Add(-time.Duration(i)*time.Minute))
		}
	}
	return st
}

func TestHubStatusTitleSparkline(t *testing.T) {
	InitStyles()
	tests := []struct {
		name      string
		w         int
		wantSpark bool
	}{
		{"wide", 190, true},
		{"100 cols", 100, true},
		{"narrow drops it first", 70, false},
		{"very narrow", 40, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, _ := hubStatusTitleAt(sparkStatus(), tt.w, statusNow)
			plain := ansi.Strip(line)
			if lipgloss.Width(line) > tt.w {
				t.Errorf("line is %d wide, over %d: %q", lipgloss.Width(line), tt.w, plain)
			}
			if got := strings.Contains(plain, " 30m"); got != tt.wantSpark {
				t.Errorf("sparkline shown = %v, want %v: %q", got, tt.wantSpark, plain)
			}
			if tt.w >= 70 && !strings.Contains(plain, "WORKING") {
				t.Errorf("the state pill gave way: %q", plain)
			}
			if tt.wantSpark {
				if !strings.HasSuffix(strings.TrimRight(plain, " "), "30m  agentctl") {
					t.Errorf("sparkline not left of the project: %q", plain)
				}
				if title := titleCells(plain); title < hubActivityMinTitle {
					t.Errorf("title squeezed to %d cells: %q", title, plain)
				}
			}
		})
	}
}

// titleCells is how many cells the title takes in a plain title line: the
// text from "Add" up to the gap before the sparkline.
func titleCells(plain string) int {
	i := strings.Index(plain, "Add")
	if i < 0 {
		return 0
	}
	rest := plain[i:]
	if j := strings.Index(rest, "  "); j >= 0 {
		rest = rest[:j]
	}
	return lipgloss.Width(rest)
}

func TestHubStatusTitleNoSparklineWithoutCalls(t *testing.T) {
	InitStyles()
	st := sparkStatus()
	st.activity = []time.Time{statusNow.Add(-time.Hour)} // outside the window
	line, _ := hubStatusTitleAt(st, 190, statusNow)
	if strings.Contains(ansi.Strip(line), "30m") {
		t.Errorf("sparkline with no calls in the window: %q", ansi.Strip(line))
	}
}

// TestHubStatusRenderSamples prints the pane at 100 and 190 columns (go test
// -v -run Samples) for review.
func TestHubStatusRenderSamples(t *testing.T) {
	InitStyles()
	for _, w := range []int{100, 190} {
		st := sparkStatus()
		st.Detail.Recap = "Reading the transcript tail in the status pane process and caching tool-call times per session; the CI chip asks GitHub's REST API for the selected PR only."
		st.Ticket, st.PRNumber, st.PRURL, st.ci = 418, 421, "https://github.com/blsmth/agentctl/pull/421", ciPassed
		sel := HubSelection{Session: "s", WindowID: "@1", RuntimeDir: "/p", Status: st}
		out := ansi.Strip(renderHubStatus(sel, false, w, fleetStatusRows, statusNow))
		t.Logf("%d cols:\n%s", w, out)
		if !strings.Contains(out, "PR #421 ✓") {
			t.Errorf("no CI chip at %d cols:\n%s", w, out)
		}
		for i, l := range strings.Split(out, "\n") {
			if lipgloss.Width(l) > w {
				t.Errorf("%d cols: line %d is %d wide", w, i, lipgloss.Width(l))
			}
		}
	}
}
