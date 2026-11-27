package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The status pane's CI chip after "PR #N": ✓ all checks passed, ✗ any
// failed, … still running, nothing when unknown. It is fetched only for the
// selected agent's PR, in a tea.Cmd so rendering never waits on GitHub, over
// the REST API (the GraphQL quota is shared by the whole fleet), and cached
// per PR: hubCITTL after a good answer, longer after an error. A missing or
// unauthenticated gh shows nothing.

// ciResult is a PR head's combined check state.
type ciResult int

const (
	ciUnknown ciResult = iota
	ciPending
	ciPassed
	ciFailed
)

const (
	// hubCITTL is how long a fetched result is reused.
	hubCITTL = 60 * time.Second
	// hubCIErrorBackoff follows a failed call (network, an API error).
	hubCIErrorBackoff = 5 * time.Minute
	// hubCIRateLimitBackoff follows a rate-limit response.
	hubCIRateLimitBackoff = 15 * time.Minute
	// hubCINoGHBackoff follows gh being missing or not logged in.
	hubCINoGHBackoff = 30 * time.Minute
	// hubCICallTimeout bounds one gh call.
	hubCICallTimeout = 15 * time.Second
)

// ghAPI runs `gh api <args>` and returns its stdout; a seam that tests stub
// (tests never exec gh).
var ghAPI = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", append([]string{"api"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("gh api %s: %w: %s", args[0], err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("gh api %s: %w", args[0], err)
	}
	return out, nil
}

// prRef is a GitHub pull request named by its URL.
type prRef struct {
	owner, repo string
	number      int
}

// parsePRURL reads https://github.com/<owner>/<repo>/pull/<n>; ok is false
// for anything else (another host, a malformed URL).
func parsePRURL(raw string) (prRef, bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Host, "github.com") {
		return prRef{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" || parts[0] == "" || parts[1] == "" {
		return prRef{}, false
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n <= 0 {
		return prRef{}, false
	}
	return prRef{owner: parts[0], repo: parts[1], number: n}, true
}

// ghPull is the subset of GET /repos/{o}/{r}/pulls/{n} read here.
type ghPull struct {
	State string `json:"state"` // open, closed
	Head  struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// ghCheckRuns is the subset of GET /repos/{o}/{r}/commits/{sha}/check-runs.
type ghCheckRuns struct {
	TotalCount int `json:"total_count"`
	CheckRuns  []struct {
		Status     string `json:"status"`     // queued, in_progress, completed, ...
		Conclusion string `json:"conclusion"` // success, failure, neutral, ...
	} `json:"check_runs"`
}

// ghCombinedStatus is the subset of GET /repos/{o}/{r}/commits/{sha}/status
// (commit statuses, the pre-Checks API some CI still uses).
type ghCombinedStatus struct {
	State      string `json:"state"` // success, pending, failure, error
	TotalCount int    `json:"total_count"`
}

// checkRunsResult maps check runs to a result: any failed run fails, else any
// unfinished run is pending, else passed; no runs is unknown.
func checkRunsResult(cr ghCheckRuns) ciResult {
	if len(cr.CheckRuns) == 0 {
		return ciUnknown
	}
	res := ciPassed
	for _, r := range cr.CheckRuns {
		if r.Status != "completed" {
			res = ciPending
			continue
		}
		switch r.Conclusion {
		case "failure", "timed_out", "cancelled", "action_required", "startup_failure":
			return ciFailed
		}
	}
	return res
}

// combinedStatusResult maps a combined commit status to a result; a commit
// with no statuses is unknown (GitHub reports such a commit as pending).
func combinedStatusResult(cs ghCombinedStatus) ciResult {
	if cs.TotalCount == 0 {
		return ciUnknown
	}
	switch cs.State {
	case "success":
		return ciPassed
	case "failure", "error":
		return ciFailed
	case "pending":
		return ciPending
	}
	return ciUnknown
}

// mergeCI combines the check-run and commit-status results: failed wins, then
// pending, then passed; unknown only when both are.
func mergeCI(a, b ciResult) ciResult {
	for _, r := range []ciResult{ciFailed, ciPending, ciPassed} {
		if a == r || b == r {
			return r
		}
	}
	return ciUnknown
}

// fetchCI asks GitHub for pr's head check state. A PR that is not open is
// unknown (no chip).
func fetchCI(ctx context.Context, pr prRef) (ciResult, error) {
	base := fmt.Sprintf("repos/%s/%s", pr.owner, pr.repo)
	var pull ghPull
	if err := ghJSON(ctx, &pull, fmt.Sprintf("%s/pulls/%d", base, pr.number)); err != nil {
		return ciUnknown, err
	}
	if pull.State != "open" || pull.Head.SHA == "" {
		return ciUnknown, nil
	}
	var runs ghCheckRuns
	if err := ghJSON(ctx, &runs, fmt.Sprintf("%s/commits/%s/check-runs?per_page=100", base, pull.Head.SHA)); err != nil {
		return ciUnknown, err
	}
	var status ghCombinedStatus
	if err := ghJSON(ctx, &status, fmt.Sprintf("%s/commits/%s/status", base, pull.Head.SHA)); err != nil {
		return ciUnknown, err
	}
	return mergeCI(checkRunsResult(runs), combinedStatusResult(status)), nil
}

// ghJSON runs `gh api path` and decodes its output into v.
func ghJSON(ctx context.Context, v any, path string) error {
	out, err := ghAPI(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("decoding %s: %w", path, err)
	}
	return nil
}

// ciBackoff is how long to wait after err before asking again.
func ciBackoff(err error) time.Duration {
	if errors.Is(err, exec.ErrNotFound) {
		return hubCINoGHBackoff
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "http 429"):
		return hubCIRateLimitBackoff
	case strings.Contains(msg, "gh auth login"), strings.Contains(msg, "not logged"),
		strings.Contains(msg, "http 401"), strings.Contains(msg, "bad credentials"):
		return hubCINoGHBackoff
	}
	return hubCIErrorBackoff
}

// hubCIEntry is one PR's cached result.
type hubCIEntry struct {
	result   ciResult
	next     time.Time // when to ask again
	inflight bool
}

// hubCICache holds the CI result per PR URL. It is mutated only from the
// pane's Update, so it needs no lock.
type hubCICache struct {
	entries map[string]*hubCIEntry
	fetch   func(context.Context, prRef) (ciResult, error)
}

func newHubCICache() *hubCICache {
	return &hubCICache{entries: map[string]*hubCIEntry{}, fetch: fetchCI}
}

// hubCIMsg carries a finished fetch back to Update.
type hubCIMsg struct {
	key    string
	result ciResult
	err    error
}

// refresh returns a command fetching st's PR state when the cached entry is
// due and no fetch is in flight, else nil. Only a selected row with a GitHub
// PR is ever asked about.
func (c *hubCICache) refresh(st *hubStatus, now time.Time) tea.Cmd {
	if c == nil || st == nil || st.PRNumber <= 0 || st.PRURL == "" {
		return nil
	}
	pr, ok := parsePRURL(st.PRURL)
	if !ok {
		return nil
	}
	key := st.PRURL
	e := c.entries[key]
	if e == nil {
		e = &hubCIEntry{}
		c.entries[key] = e
	}
	if e.inflight || now.Before(e.next) {
		return nil
	}
	e.inflight = true
	fetch := c.fetch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), hubCICallTimeout)
		defer cancel()
		res, err := fetch(ctx, pr)
		return hubCIMsg{key: key, result: res, err: err}
	}
}

// store records a finished fetch at now: the result for hubCITTL, or after an
// error no chip until the backoff ends.
func (c *hubCICache) store(msg hubCIMsg, now time.Time) {
	if c == nil {
		return
	}
	e := c.entries[msg.key]
	if e == nil {
		e = &hubCIEntry{}
		c.entries[msg.key] = e
	}
	e.inflight = false
	if msg.err != nil {
		e.result, e.next = ciUnknown, now.Add(ciBackoff(msg.err))
		return
	}
	e.result, e.next = msg.result, now.Add(hubCITTL)
}

// result is st's cached CI result (unknown when never fetched).
func (c *hubCICache) result(st *hubStatus) ciResult {
	if c == nil || st == nil {
		return ciUnknown
	}
	if e := c.entries[st.PRURL]; e != nil {
		return e.result
	}
	return ciUnknown
}

// ciChip is the glyph drawn after "PR #N" for r, styled, and its width; ""
// for unknown.
func ciChip(r ciResult) (string, int) {
	switch r {
	case ciPassed:
		return fleetSuccessStyle.Render("✓"), 1
	case ciFailed:
		return fleetErrStyle.Render("✗"), 1
	case ciPending:
		return fleetDimStyle.Render("…"), 1
	}
	return "", 0
}

// withCIChip appends st's CI glyph to the PR segment, after the link.
func withCIChip(seg footSeg, st *hubStatus) footSeg {
	if chip, w := ciChip(st.ci); w > 0 {
		seg.text += " " + chip
		seg.w += 1 + w
	}
	return seg
}
