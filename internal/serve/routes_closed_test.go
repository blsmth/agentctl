package serve

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/fleet"
)

const (
	sidOK    = "aaaa1111-0000-0000-0000-000000000001"
	sidGone  = "bbbb2222-0000-0000-0000-000000000002"
	sidAcct  = "cccc3333-0000-0000-0000-000000000003"
	sidLive  = "dddd4444-0000-0000-0000-000000000004"
	sidOld   = "eeee5555-0000-0000-0000-000000000005"
	sidNoLog = "ffff6666-0000-0000-0000-000000000006"
)

// closedEnv is a write env over a fake closed ledger and resume spawn.
type closedEnv struct {
	*writeEnv
	mu       sync.Mutex
	recs     []closedlog.Record
	loadErr  error
	since    []time.Time
	resumed  []closedlog.Record
	out      string
	spawnErr error
	proj     string
	// spawnStarted, when set, is signalled as a resume begins; spawnBlock,
	// when set, holds the resume until it is closed.
	spawnStarted chan struct{}
	spawnBlock   chan struct{}
}

func (c *closedEnv) load(since time.Time) ([]closedlog.Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.since = append(c.since, since)
	if c.loadErr != nil {
		return nil, c.loadErr
	}
	var out []closedlog.Record
	for _, r := range c.recs {
		if !r.ClosedAt.Before(since) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (c *closedEnv) resume(r closedlog.Record) (string, error) {
	c.mu.Lock()
	c.resumed = append(c.resumed, r)
	out, err, started, block := c.out, c.spawnErr, c.spawnStarted, c.spawnBlock
	c.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		<-block
	}
	return out, err
}

func (c *closedEnv) calls() []closedlog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]closedlog.Record(nil), c.resumed...)
}

// newClosedEnv builds the ledger fixture (newest first, as Load returns it):
// a resumable record closed twice, a record whose project is gone, one from
// another account, one open again, one with no session ID, one past the
// default window, one whose transcript is gone.
func newClosedEnv(t *testing.T, allowWrite bool) *closedEnv {
	t.Helper()
	proj := t.TempDir()
	logPath := filepath.Join(proj, "log.jsonl")
	if err := os.WriteFile(logPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := newClock().Now()
	at := func(d time.Duration) time.Time { return now.Add(-d) }
	base := closedlog.Record{Session: "agentctl-proj", ProjectDir: proj, WorkDir: proj, Agent: "go-engineer", LogPath: logPath}
	rec := func(d time.Duration, win, sid string, mut func(*closedlog.Record)) closedlog.Record {
		r := base
		r.ClosedAt, r.WindowID, r.SessionID, r.Instance = at(d), win, sid, "go-engineer-"+win[1:]
		if mut != nil {
			mut(&r)
		}
		return r
	}
	c := &closedEnv{proj: proj, out: "starting\n@77\n"}
	c.recs = []closedlog.Record{
		rec(2*time.Hour, "@1", sidOK, func(r *closedlog.Record) {
			r.ShortTitle, r.PRNumber, r.PRURL = "Fix the parser", 42, "https://github.com/o/r/pull/42"
			r.Reason, r.ClosedBy = closedlog.ReasonCascade, "@9"
		}),
		rec(3*time.Hour, "@2", sidGone, func(r *closedlog.Record) {
			r.ProjectDir = filepath.Join(proj, "gone") // the worktree survives, the project does not
		}),
		rec(4*time.Hour, "@3", sidAcct, func(r *closedlog.Record) { r.Account = "work"; r.PRURL = "javascript:alert(1)" }),
		rec(5*time.Hour, "@4", sidLive, nil),
		rec(6*time.Hour, "@5", "", nil),
		rec(30*time.Hour, "@6", sidOK, func(r *closedlog.Record) { r.ShortTitle = "older close" }),
		rec(31*time.Hour, "@8", sidNoLog, func(r *closedlog.Record) { r.LogPath = filepath.Join(proj, "missing.jsonl") }),
		rec(8*24*time.Hour, "@7", sidOld, nil),
	}
	c.writeEnv = newWriteEnvWith(t, allowWrite, func(o *Options) {
		o.ClosedRecords = c.load
		o.ResumeClosed = c.resume
		o.HandleIndex = func() *fleet.Index { return fleet.NewIndex(nil, c.recs) }
	})
	g := writeGroup("idle", false, "go-engineer", "bare")
	g.Rows[0].SessionID = sidLive
	c.fleet.set(g)
	return c
}

func decodeClosed(t *testing.T, w interface{ Bytes() []byte }) closedBody {
	t.Helper()
	var b closedBody
	if err := json.Unmarshal(w.Bytes(), &b); err != nil {
		t.Fatalf("decoding %s: %v", w.Bytes(), err)
	}
	return b
}

func TestClosedList(t *testing.T) {
	c := newClosedEnv(t, true)
	w := c.get("/v1/closed")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	b := decodeClosed(t, w.Body)
	if b.Error != "" || b.Since != "2026-11-09T12:00:00Z" {
		t.Errorf("since %q error %q", b.Since, b.Error)
	}
	var wins []string
	byWin := map[string]closedItem{}
	for _, it := range b.Closed {
		wins = append(wins, it.Instance)
		byWin[it.Instance] = it
	}
	// Newest first; the session closed twice shows once (its newest close);
	// the 8-day-old record is past the default window.
	want := []string{"go-engineer-1", "go-engineer-2", "go-engineer-3", "go-engineer-4", "go-engineer-5", "go-engineer-8"}
	if !slices.Equal(wins, want) {
		t.Fatalf("rows = %v, want %v", wins, want)
	}
	ok := byWin["go-engineer-1"]
	if !ok.Resumable || ok.Reason != "" || ok.Title != "Fix the parser" || ok.Project != filepath.Base(c.proj) ||
		ok.PRNumber != 42 || ok.PRURL != "https://github.com/o/r/pull/42" || ok.CloseReason != "cascade" || ok.ClosedBy != "@9" ||
		ok.Handle == "" || ok.ClosedAt != "2026-11-16T10:00:00Z" || ok.Agent != "go-engineer" || ok.ProjectDir != c.proj {
		t.Errorf("resumable row = %+v", ok)
	}
	tests := []struct {
		inst      string
		resumable bool
		reason    string
	}{
		{"go-engineer-2", false, "project directory"},
		{"go-engineer-3", true, ""},
		{"go-engineer-4", false, reasonLive},
		{"go-engineer-5", false, "no session id"},
		{"go-engineer-8", false, "is missing"},
	}
	for _, tt := range tests {
		it := byWin[tt.inst]
		if it.Resumable != tt.resumable || (tt.reason == "" && it.Reason != "") || !strings.Contains(it.Reason, tt.reason) {
			t.Errorf("%s: resumable %v reason %q, want %v %q", tt.inst, it.Resumable, it.Reason, tt.resumable, tt.reason)
		}
	}
	if acct := byWin["go-engineer-3"]; acct.Account != "work" || acct.PRURL != "" {
		t.Errorf("other-account row = %+v (non-https PR URL must be dropped)", acct)
	}
	if live := byWin["go-engineer-4"]; !live.Live || live.Session != "agentctl-proj" || live.WindowID != "@523" {
		t.Errorf("live row = %+v", live)
	}
	if h := byWin["go-engineer-5"].Handle; h != "" {
		t.Errorf("no-session row handle = %q", h)
	}
}

func TestClosedListSince(t *testing.T) {
	now := newClock().Now()
	tests := []struct {
		name, q string
		status  int
		rows    int
		since   time.Time
	}{
		{"duration", "?since=3h", 200, 2, now.Add(-3 * time.Hour)},
		{"rfc3339", "?since=2026-11-01T00:00:00Z", 200, 7, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{"clamped to retention", "?since=2020-01-01T00:00:00Z", 200, 7, now.Add(-closedlog.MaxAge)},
		{"garbage", "?since=yesterday", 400, 0, time.Time{}},
		{"negative", "?since=-3h", 400, 0, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClosedEnv(t, false)
			w := c.get("/v1/closed" + tt.q)
			if w.Code != tt.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if tt.status != 200 {
				return
			}
			if b := decodeClosed(t, w.Body); len(b.Closed) != tt.rows {
				t.Errorf("rows = %d, want %d", len(b.Closed), tt.rows)
			}
			if len(c.since) != 1 || !c.since[0].Equal(tt.since) {
				t.Errorf("since = %v, want %v", c.since, tt.since)
			}
		})
	}
}

func TestClosedListEdgeCases(t *testing.T) {
	t.Run("empty ledger", func(t *testing.T) {
		c := newClosedEnv(t, true)
		c.recs = nil
		w := c.get("/v1/closed")
		if b := decodeClosed(t, w.Body); w.Code != 200 || b.Closed == nil || len(b.Closed) != 0 || b.Error != "" {
			t.Errorf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("unreadable ledger", func(t *testing.T) {
		c := newClosedEnv(t, true)
		c.loadErr = errors.New("permission denied")
		w := c.get("/v1/closed")
		if b := decodeClosed(t, w.Body); w.Code != 200 || b.Closed == nil || len(b.Closed) != 0 || b.Error == "" {
			t.Errorf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("fleet unavailable", func(t *testing.T) {
		c := newClosedEnv(t, true)
		c.fleet.mu.Lock()
		c.fleet.err = errors.New("no tmux")
		c.fleet.mu.Unlock()
		if w := c.get("/v1/closed"); w.Code != 200 || len(decodeClosed(t, w.Body).Closed) != 6 {
			t.Errorf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("needs a device", func(t *testing.T) {
		c := newClosedEnv(t, true)
		if w := do(c.Handler(), "GET", "/v1/closed"); w.Code != http.StatusUnauthorized {
			t.Errorf("status %d", w.Code)
		}
	})
}

func TestClosedResume(t *testing.T) {
	c := newClosedEnv(t, true)
	h := fleet.NewIndex(nil, c.recs).HandleFor(sidOK)
	w := c.post("/v1/closed/"+h+"/resume", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var res ResumeResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Session != "agentctl-proj" || res.WindowID != "@77" || res.Handle != h {
		t.Errorf("result = %+v", res)
	}
	calls := c.calls()
	// The newest record of the session is the one resumed.
	if len(calls) != 1 || calls[0].WindowID != "@1" || calls[0].SessionID != sidOK || calls[0].ProjectDir != c.proj {
		t.Errorf("resumed = %+v", calls)
	}
	lines := c.auditLines(t)
	last := lines[len(lines)-1]
	if last.Action != "closed-resume" || last.Result != "ok" || last.Session != "agentctl-proj" || last.Window != "@77" || last.Agent != "go-engineer" {
		t.Errorf("audit = %+v", lines)
	}
}

func TestClosedResumeRefusals(t *testing.T) {
	tests := []struct {
		name, ref, body string
		status          int
		code            string
	}{
		{"project gone", sidGone[:8], `{}`, http.StatusConflict, codeNotResumable},
		{"transcript gone", sidNoLog[:8], `{}`, http.StatusConflict, codeNotResumable},
		{"live again", sidLive[:8], `{}`, http.StatusConflict, codeLive},
		{"unknown handle", "abcdef12", `{}`, http.StatusNotFound, codeNotFound},
		{"not a handle", "xyz~1", `{}`, http.StatusNotFound, codeNotFound},
		{"body carries a dir", sidOK[:8], `{"project_dir":"/tmp"}`, http.StatusBadRequest, codeBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClosedEnv(t, true)
			w := c.post("/v1/closed/"+tt.ref+"/resume", tt.body)
			if w.Code != tt.status || errCode(t, w) != tt.code {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if n := len(c.calls()); n != 0 {
				t.Errorf("spawned %d times", n)
			}
		})
	}
}

func TestClosedResumeBusy(t *testing.T) {
	c := newClosedEnv(t, true)
	c.spawnStarted, c.spawnBlock = make(chan struct{}, 1), make(chan struct{})
	path := "/v1/closed/" + sidOK[:8] + "/resume"
	first := make(chan int, 1)
	go func() { first <- c.post(path, `{}`).Code }()
	<-c.spawnStarted
	w := c.post(path, `{}`)
	if w.Code != http.StatusConflict || errCode(t, w) != codeBusy {
		t.Errorf("second resume: %d %s", w.Code, w.Body)
	}
	close(c.spawnBlock)
	if code := <-first; code != http.StatusOK {
		t.Errorf("first resume: %d", code)
	}
	if n := len(c.calls()); n != 1 {
		t.Errorf("spawned %d times, want 1", n)
	}
	// Once the first finishes, the session can be resumed again.
	c.spawnStarted = nil
	if w := c.post(path, `{}`); w.Code != http.StatusOK {
		t.Errorf("resume after release: %d %s", w.Code, w.Body)
	}
}

func TestClosedResumeAmbiguous(t *testing.T) {
	c := newClosedEnv(t, true)
	twin := c.recs[0]
	twin.SessionID, twin.WindowID, twin.ClosedAt = "aaaa1111-0000-0000-0000-000000000099", "@20", twin.ClosedAt.Add(-time.Minute)
	c.recs = append([]closedlog.Record{c.recs[0], twin}, c.recs[1:]...)
	w := c.post("/v1/closed/aaaa1111/resume", `{}`)
	if w.Code != http.StatusConflict || errCode(t, w) != codeAmbiguous {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if n := len(c.calls()); n != 0 {
		t.Errorf("spawned %d times", n)
	}
}

func TestClosedResumeSpawnFails(t *testing.T) {
	c := newClosedEnv(t, true)
	c.spawnErr = errors.New("tmux: no server")
	w := c.post("/v1/closed/"+sidAcct[:8]+"/resume", `{}`)
	if w.Code != http.StatusBadGateway || errCode(t, w) != codeActionFailed {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if calls := c.calls(); len(calls) != 1 || calls[0].Account != "work" {
		t.Errorf("resumed = %+v", calls)
	}
}

func TestClosedWriteOff(t *testing.T) {
	c := newClosedEnv(t, false)
	if w := c.get("/v1/closed"); w.Code != 200 || len(decodeClosed(t, w.Body).Closed) == 0 {
		t.Fatalf("list with write off: %d %s", w.Code, w.Body)
	}
	feats := c.Server.router.Features()
	if !slices.Contains(feats, "closed") || slices.Contains(feats, "resume") {
		t.Errorf("features = %v", feats)
	}
	w := c.post("/v1/closed/"+sidOK[:8]+"/resume", `{}`)
	if w.Code != http.StatusForbidden || errCode(t, w) != codeWriteDisabled {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if n := len(c.calls()); n != 0 {
		t.Errorf("spawned %d times", n)
	}
	lines := c.auditLines(t)
	if len(lines) == 0 || lines[len(lines)-1].Action != "closed-resume" {
		t.Errorf("audit = %+v", lines)
	}

	on := newClosedEnv(t, true)
	if feats := on.Server.router.Features(); !slices.Contains(feats, "resume") || !slices.Contains(feats, "write") {
		t.Errorf("features with write on = %v", feats)
	}
}

func TestLastWindowID(t *testing.T) {
	tests := map[string]string{
		"@12\n":          "@12",
		"noise\n@3":      "@3",
		"":               "",
		"@":              "",
		"@1x":            "",
		"%3":             "",
		"@4\ntrailing\n": "",
	}
	for in, want := range tests {
		if got := lastWindowID(in); got != want {
			t.Errorf("lastWindowID(%q) = %q, want %q", in, got, want)
		}
	}
}
