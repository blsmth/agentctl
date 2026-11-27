package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/verdicts"
)

// stubGH replaces ghAPI with responses keyed by path prefix ("pulls",
// "check-runs", "status") and counts the calls.
func stubGH(t *testing.T, resp map[string]string, err error) *int {
	t.Helper()
	calls := 0
	orig := ghAPI
	ghAPI = func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		if err != nil {
			return nil, err
		}
		p := args[0]
		switch {
		case strings.Contains(p, "/check-runs"):
			return []byte(resp["check-runs"]), nil
		case strings.HasSuffix(p, "/status"):
			return []byte(resp["status"]), nil
		case strings.Contains(p, "/pulls/"):
			return []byte(resp["pulls"]), nil
		}
		return nil, fmt.Errorf("unexpected path %s", p)
	}
	t.Cleanup(func() { ghAPI = orig })
	return &calls
}

const openPull = `{"state":"open","head":{"sha":"abc"}}`

func TestFetchCIResultMapping(t *testing.T) {
	noStatus := `{"state":"pending","total_count":0}`
	tests := []struct {
		name string
		resp map[string]string
		want ciResult
	}{
		{"passed", map[string]string{"pulls": openPull, "check-runs": `{"check_runs":[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"skipped"}]}`, "status": noStatus}, ciPassed},
		{"failed", map[string]string{"pulls": openPull, "check-runs": `{"check_runs":[{"status":"in_progress"},{"status":"completed","conclusion":"failure"}]}`, "status": noStatus}, ciFailed},
		{"pending", map[string]string{"pulls": openPull, "check-runs": `{"check_runs":[{"status":"completed","conclusion":"success"},{"status":"queued"}]}`, "status": noStatus}, ciPending},
		{"commit status failure wins", map[string]string{"pulls": openPull, "check-runs": `{"check_runs":[{"status":"completed","conclusion":"success"}]}`, "status": `{"state":"failure","total_count":1}`}, ciFailed},
		{"commit status only", map[string]string{"pulls": openPull, "check-runs": `{"check_runs":[]}`, "status": `{"state":"success","total_count":2}`}, ciPassed},
		{"no checks is unknown", map[string]string{"pulls": openPull, "check-runs": `{"check_runs":[]}`, "status": noStatus}, ciUnknown},
		{"closed PR is unknown", map[string]string{"pulls": `{"state":"closed","head":{"sha":"abc"}}`}, ciUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubGH(t, tt.resp, nil)
			got, err := fetchCI(context.Background(), prRef{"o", "r", 1})
			if err != nil || got != tt.want {
				t.Errorf("fetchCI = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestCIChip(t *testing.T) {
	InitStyles()
	for r, want := range map[ciResult]string{ciPassed: "✓", ciFailed: "✗", ciPending: "…", ciUnknown: ""} {
		got, w := ciChip(r)
		if ansi.Strip(got) != want || w != len([]rune(want)) {
			t.Errorf("ciChip(%v) = %q (%d), want %q", r, ansi.Strip(got), w, want)
		}
	}
	st := &hubStatus{Version: hubStatusVersion, PRNumber: 418, PRURL: "https://github.com/o/r/pull/418", ci: ciFailed}
	if got := ansi.Strip(hubStatusFooter(st, 80, statusNow)); got != "PR #418 ✗" {
		t.Errorf("footer = %q", got)
	}
	st.ci = ciUnknown
	if got := ansi.Strip(hubStatusFooter(st, 80, statusNow)); got != "PR #418" {
		t.Errorf("footer with unknown CI = %q", got)
	}
}

func TestParsePRURL(t *testing.T) {
	if pr, ok := parsePRURL("https://github.com/blsmth/agentctl/pull/418"); !ok || pr != (prRef{"blsmth", "agentctl", 418}) {
		t.Errorf("parsed %+v, %v", pr, ok)
	}
	for _, bad := range []string{"", "https://gitlab.com/o/r/pull/1", "https://github.com/o/r/issues/1", "https://github.com/o/r/pull/x"} {
		if _, ok := parsePRURL(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
}

// runCI runs the refresh command, if any, and stores its result at now.
func runCI(t *testing.T, c *hubCICache, st *hubStatus, now time.Time) bool {
	t.Helper()
	cmd := c.refresh(st, now)
	if cmd == nil {
		return false
	}
	c.store(cmd().(hubCIMsg), now)
	return true
}

func TestHubCICacheTTL(t *testing.T) {
	calls := stubGH(t, map[string]string{
		"pulls": openPull, "check-runs": `{"check_runs":[{"status":"completed","conclusion":"success"}]}`, "status": `{"total_count":0}`,
	}, nil)
	c := newHubCICache()
	st := &hubStatus{PRNumber: 1, PRURL: "https://github.com/o/r/pull/1"}
	now := statusNow
	if !runCI(t, c, st, now) || c.result(st) != ciPassed || *calls != 3 {
		t.Fatalf("first fetch: result %v, %d calls", c.result(st), *calls)
	}
	if runCI(t, c, st, now.Add(hubCITTL-time.Second)) {
		t.Error("fetched again inside the TTL")
	}
	if !runCI(t, c, st, now.Add(hubCITTL)) || *calls != 6 {
		t.Errorf("no refetch after the TTL (%d calls)", *calls)
	}
	// No PR, or not a GitHub PR: never asked.
	if c.refresh(&hubStatus{}, now) != nil || c.refresh(&hubStatus{PRNumber: 2, PRURL: "https://example.com/x"}, now) != nil {
		t.Error("fetch started without a GitHub PR")
	}
}

func TestHubCICacheInFlight(t *testing.T) {
	stubGH(t, map[string]string{"pulls": openPull}, nil)
	c := newHubCICache()
	st := &hubStatus{PRNumber: 1, PRURL: "https://github.com/o/r/pull/1"}
	if c.refresh(st, statusNow) == nil {
		t.Fatal("no first fetch")
	}
	if c.refresh(st, statusNow.Add(time.Hour)) != nil {
		t.Error("second fetch while one is in flight")
	}
}

func TestHubCICacheErrorBackoff(t *testing.T) {
	tests := []struct {
		name string
		err  error
		wait time.Duration
	}{
		{"network", errors.New("gh api repos/o/r/pulls/1: exit status 1: connection refused"), hubCIErrorBackoff},
		{"rate limit", errors.New("gh api x: exit status 1: HTTP 403: API rate limit exceeded"), hubCIRateLimitBackoff},
		{"not logged in", errors.New("gh api x: exit status 4: To get started with GitHub CLI, please run:  gh auth login"), hubCINoGHBackoff},
		{"gh missing", fmt.Errorf("gh api x: %w", exec.ErrNotFound), hubCINoGHBackoff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := stubGH(t, nil, tt.err)
			c := newHubCICache()
			st := &hubStatus{PRNumber: 1, PRURL: "https://github.com/o/r/pull/1"}
			now := statusNow
			runCI(t, c, st, now)
			if c.result(st) != ciUnknown {
				t.Errorf("result after an error = %v, want unknown (no chip)", c.result(st))
			}
			if runCI(t, c, st, now.Add(tt.wait-time.Second)) {
				t.Errorf("retried before the %v backoff", tt.wait)
			}
			if !runCI(t, c, st, now.Add(tt.wait)) || *calls != 2 {
				t.Errorf("no retry after the backoff (%d calls)", *calls)
			}
		})
	}
}

func TestHubStatusModelLiveSignals(t *testing.T) {
	InitStyles()
	stubGH(t, map[string]string{
		"pulls": openPull, "check-runs": `{"check_runs":[{"status":"queued"}]}`, "status": `{"total_count":0}`,
	}, nil)
	orig := readToolCallTimes
	readToolCallTimes = func(string, time.Time) []time.Time { return []time.Time{statusNow.Add(-time.Minute)} }
	t.Cleanup(func() { readToolCallTimes = orig })

	st := sparkStatus()
	st.activity = nil
	st.SessionID, st.LogPath = "s1", "/l/s1.jsonl"
	st.PRNumber, st.PRURL = 421, "https://github.com/o/r/pull/421"
	sel := HubSelection{Session: "s", WindowID: "@1", RuntimeDir: "/p", Status: st}
	m := HubStatusModel{
		read:     func() (HubSelection, error) { return sel, nil },
		width:    190,
		height:   fleetStatusRows,
		activity: newHubActivityCache(),
		ci:       newHubCICache(),
		now:      func() time.Time { return statusNow },
	}
	next, cmd := m.Update(hubStatusTickMsg{})
	m = next.(HubStatusModel)
	for _, msg := range drainBatch(cmd) {
		next, _ = m.Update(msg)
		m = next.(HubStatusModel)
	}
	got := ansi.Strip(renderHubStatus(m.withLive(), false, 190, fleetStatusRows, statusNow))
	if !strings.Contains(got, "PR #421 …") || !strings.Contains(got, "30m") {
		t.Errorf("live signals missing:\n%s", got)
	}
	if sel.Status.activity != nil || sel.Status.ci != ciUnknown {
		t.Error("live signals written into the read selection")
	}
}

// drainBatch runs cmd and returns the messages of its non-tick parts (a
// tea.Batch is flattened one level).
func drainBatch(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	var out []tea.Msg
	var walk func(tea.Msg)
	walk = func(msg tea.Msg) {
		switch msg := msg.(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				if c != nil {
					walk(c())
				}
			}
		case hubActivityMsg, hubCIMsg:
			out = append(out, msg)
		}
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		walk(msg)
	case <-time.After(time.Second):
	}
	return out
}

func TestCheckRunsResultMorePages(t *testing.T) {
	cr := ghCheckRuns{TotalCount: 150}
	cr.CheckRuns = append(cr.CheckRuns, struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}{"completed", "success"})
	if got := checkRunsResult(cr); got != ciPending {
		t.Errorf("green first page of more = %v, want pending", got)
	}
}

func TestCIChipComposesWithLinkAndVerdict(t *testing.T) {
	InitStyles()
	url := "https://github.com/o/r/pull/418"
	tests := []struct {
		name    string
		ci      ciResult
		verdict string
		want    string
	}{
		{"passed, approved", ciPassed, verdicts.Merge, "#7 │ PR #418 ✓"},
		{"failed, changes", ciFailed, verdicts.Blocked, "#7 │ PR #418 ✗ · changes"},
		{"unknown, changes", ciUnknown, verdicts.Blocked, "#7 │ PR #418 · changes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &hubStatus{Version: hubStatusVersion, Ticket: 7, PRNumber: 418, PRURL: url, ci: tt.ci, Verdict: tt.verdict}
			line, spans := hubStatusFooterSpans(st, 80, statusNow)
			if got := ansi.Strip(line); got != tt.want {
				t.Errorf("footer = %q, want %q", got, tt.want)
			}
			var pr *footSpan
			for i := range spans {
				if spans[i].url == url {
					pr = &spans[i]
				}
			}
			if pr == nil {
				t.Fatalf("no PR span in %+v", spans)
			}
			start := lipgloss.Width(tt.want[:strings.Index(tt.want, "PR #418")])
			if pr.x0 != start || pr.x1 != start+len("PR #418") {
				t.Errorf("PR click span [%d,%d), want [%d,%d) (the PR text only)", pr.x0, pr.x1, start, start+len("PR #418"))
			}
		})
	}
}
