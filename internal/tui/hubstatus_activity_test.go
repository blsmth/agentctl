package tui

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestActivityBuckets(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 31, 20, 0, time.UTC) // in the 12:30-12:32 bucket
	at := func(h, m, s int) time.Time { return time.Date(2026, 11, 27, h, m, s, 0, time.UTC) }
	tests := []struct {
		name  string
		calls []time.Time
		want  map[int]int // bucket index -> count; every other bucket is 0
	}{
		{name: "empty window"},
		{name: "current bucket is the last cell", calls: []time.Time{at(12, 30, 0), at(12, 31, 19)}, want: map[int]int{14: 2}},
		{name: "bucket boundary", calls: []time.Time{at(12, 29, 59), at(12, 30, 0)}, want: map[int]int{13: 1, 14: 1}},
		{name: "two minutes per cell", calls: []time.Time{at(12, 28, 0), at(12, 29, 30)}, want: map[int]int{13: 2}},
		{name: "oldest bucket is the first cell", calls: []time.Time{at(12, 2, 0), at(12, 3, 59)}, want: map[int]int{0: 2}},
		{name: "older than the window is dropped", calls: []time.Time{at(12, 1, 59), at(11, 0, 0)}},
		{name: "future counts now", calls: []time.Time{at(12, 35, 30)}, want: map[int]int{14: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := activityBuckets(tt.calls, now)
			if len(got) != hubActivityCells {
				t.Fatalf("%d buckets, want %d", len(got), hubActivityCells)
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
	now := time.Date(2026, 11, 27, 12, 1, 0, 0, time.UTC)
	var calls []time.Time
	for i := range 90 { // one call a minute for 90 minutes
		calls = append(calls, now.Add(-time.Duration(i)*time.Minute))
	}
	b := activityBuckets(calls, now)
	if len(b) != 15 || hubActivityWindow != 30*time.Minute {
		t.Fatalf("%d buckets over %v", len(b), hubActivityWindow)
	}
	for i, c := range b {
		if c != 2 {
			t.Errorf("bucket %d = %d, want 2", i, c)
		}
	}
}

func TestRenderSparkline(t *testing.T) {
	InitStyles()
	if s, w := renderSparkline(make([]int, hubActivityCells)); s != "" || w != 0 {
		t.Errorf("no calls rendered %q (%d)", s, w)
	}
	b := make([]int, hubActivityCells)
	b[0], b[7], b[14] = 1, 4, 8
	s, w := renderSparkline(b)
	plain := ansi.Strip(s)
	if w != 19 || lipgloss.Width(s) != w {
		t.Errorf("width = %d (rendered %d), want 19", w, lipgloss.Width(s))
	}
	r := []rune(plain)
	if r[0] != '▂' || r[1] != '▁' || r[7] != '▅' || r[14] != '█' || !strings.HasSuffix(plain, " 30m") {
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
			if strings.Contains(plain, "WORKING") {
				t.Errorf("the state pill is on the title row: %q", plain)
			}
			if tt.w >= 100 && !strings.Contains(plain, sparkStatus().Title) {
				t.Errorf("title cut: %q", plain)
			}
			if tt.wantSpark && !strings.HasSuffix(strings.TrimRight(plain, " "), "30m  agentctl") {
				t.Errorf("sparkline not left of the project: %q", plain)
			}
		})
	}
}

// TestHubStatusTitleSparklineBoundary checks the drop rule at its edge: the
// sparkline shows from the first width where the whole title still fits
// beside it, and one column less drops it with the title left whole.
func TestHubStatusTitleSparklineBoundary(t *testing.T) {
	InitStyles()
	st := sparkStatus()
	first := 0
	for w := 60; w <= 190; w++ {
		line, _ := hubStatusTitleAt(st, w, statusNow)
		if strings.Contains(ansi.Strip(line), " 30m") {
			first = w
			break
		}
	}
	if first == 0 {
		t.Fatal("sparkline never shown")
	}
	for _, w := range []int{first - 1, first} {
		line, cut := hubStatusTitleAt(st, w, statusNow)
		if plain := ansi.Strip(line); cut || !strings.Contains(plain, st.Title) {
			t.Errorf("w=%d: title cut: %q", w, plain)
		}
	}
	if line, _ := hubStatusTitleAt(st, first-1, statusNow); strings.Contains(ansi.Strip(line), "30m") {
		t.Errorf("sparkline at w=%d", first-1)
	}
	// Exactly room for the title, the sparkline and their gaps.
	line, _ := hubStatusTitleAt(st, first, statusNow)
	if strings.Contains(ansi.Strip(line), st.Title+"   ") {
		t.Errorf("w=%d has slack, so the threshold is off: %q", first, ansi.Strip(line))
	}
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
	for _, c := range []struct {
		w     int
		title string
	}{{100, ""}, {100, "Live signals in the status pane"}, {190, ""}} {
		w := c.w
		st := sparkStatus()
		if c.title != "" {
			st.Title = c.title
		}
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

func TestToolCallTimesSkipsNonRegularFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fifo.jsonl")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan []time.Time, 1)
	go func() { done <- toolCallTimes(p, statusNow.Add(-time.Hour)) }()
	select {
	case got := <-done:
		if got != nil {
			t.Errorf("FIFO yielded %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
	if got := toolCallTimes(t.TempDir(), statusNow); got != nil {
		t.Errorf("directory yielded %v", got)
	}
}
