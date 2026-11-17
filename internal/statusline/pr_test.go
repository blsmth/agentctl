package statusline

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/windowstate"
)

const prA = "https://github.com/o/r/pull/7"
const prB = "https://github.com/o/r/pull/9"

func prLine(n int, url string) string {
	return `{"type":"pr-link","prNumber":` + itoa(n) + `,"prUrl":"` + url + `"}` + "\n"
}

func itoa(n int) string { return strconv.Itoa(n) }

func writeLog(t *testing.T, path, content string, appendTo bool) {
	t.Helper()
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if appendTo {
		flag = os.O_WRONLY | os.O_APPEND | os.O_CREATE
	}
	f, err := os.OpenFile(path, flag, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func TestResolvePR(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "s.jsonl")
	writeLog(t, logPath, prLine(7, prA), false)
	emptyLog := filepath.Join(dir, "e.jsonl")
	writeLog(t, emptyLog, `{"type":"user"}`+"\n", false)

	payloadPR := &PR{Number: f64Ptr(3), URL: strPtr("https://x/pull/3")}
	tests := []struct {
		name    string
		p       *Payload
		ws      windowstate.WindowState
		wantN   int
		wantURL string
	}{
		{"payload wins", &Payload{PR: payloadPR, TranscriptPath: &logPath}, windowstate.WindowState{PRNumber: 5, PRURL: "https://x/pull/5"}, 3, "https://x/pull/3"},
		{"windowstate", &Payload{}, windowstate.WindowState{PRNumber: 5, PRURL: "https://x/pull/5", LogPath: logPath}, 5, "https://x/pull/5"},
		{"url only number", &Payload{}, windowstate.WindowState{PRURL: "https://x/pull/12/"}, 12, "https://x/pull/12/"},
		{"log via transcript", &Payload{TranscriptPath: &logPath}, windowstate.WindowState{}, 7, prA},
		{"log via windowstate path", &Payload{}, windowstate.WindowState{LogPath: logPath}, 7, prA},
		{"log without pr", &Payload{}, windowstate.WindowState{LogPath: emptyLog}, 0, ""},
		{"missing log", &Payload{}, windowstate.WindowState{LogPath: filepath.Join(dir, "nope")}, 0, ""},
		{"nothing", &Payload{}, windowstate.WindowState{}, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr, _, _ := resolvePR(tt.p, tt.ws)
			if tt.wantN == 0 {
				if pr != nil {
					t.Fatalf("got PR %v, want none", *pr.Number)
				}
				return
			}
			if pr == nil || int(*pr.Number) != tt.wantN {
				t.Fatalf("pr = %+v, want #%d", pr, tt.wantN)
			}
			if pr.URL == nil || *pr.URL != tt.wantURL {
				t.Errorf("url = %v, want %q", pr.URL, tt.wantURL)
			}
		})
	}
}

func TestResolvePRIncrementalCache(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "s.jsonl")
	writeLog(t, logPath, prLine(7, prA)+`{"type":"user"}`+"\n", false)
	p := &Payload{TranscriptPath: &logPath}

	pr, cache, changed := resolvePR(p, windowstate.WindowState{})
	if !changed || pr == nil || int(*pr.Number) != 7 {
		t.Fatalf("first: pr=%v changed=%v", pr, changed)
	}

	// Unchanged log: no rescan, no write. Poison the cached PR to prove the
	// log isn't re-read.
	poisoned := cache
	poisoned.PRNumber, poisoned.PRURL = 99, "https://x/pull/99"
	pr, _, changed = resolvePR(p, windowstate.WindowState{LogPR: poisoned})
	if changed || int(*pr.Number) != 99 {
		t.Fatalf("unchanged: pr=%d changed=%v, want cached 99 and no change", int(*pr.Number), changed)
	}

	// Appended newer pr-link replaces the old one.
	writeLog(t, logPath, prLine(9, prB), true)
	pr, cache2, changed := resolvePR(p, windowstate.WindowState{LogPR: cache})
	if !changed || int(*pr.Number) != 9 {
		t.Fatalf("appended: pr=%d changed=%v", int(*pr.Number), changed)
	}

	// Appended bytes with no pr-link keep the cached PR.
	writeLog(t, logPath, `{"type":"user"}`+"\n", true)
	pr, cache3, changed := resolvePR(p, windowstate.WindowState{LogPR: cache2})
	if !changed || int(*pr.Number) != 9 {
		t.Fatalf("appended no pr: pr=%d changed=%v", int(*pr.Number), changed)
	}

	// Shrunk/replaced log rescans from the tail and drops the stale PR.
	writeLog(t, logPath, `{"type":"user"}`+"\n", false)
	pr, _, changed = resolvePR(p, windowstate.WindowState{LogPR: cache3})
	if pr != nil || !changed {
		t.Fatalf("shrunk: pr=%v changed=%v, want none and changed", pr, changed)
	}
}

func TestRenderPRSegmentLinkedAndWidth(t *testing.T) {
	p := &Payload{
		Workspace: &Workspace{CurrentDir: strPtr("/tmp/project"), GitWorktree: strPtr("issue-1")},
		PR:        &PR{Number: f64Ptr(42), URL: strPtr(prA), ReviewState: strPtr("approved")},
	}
	line, ok := renderRichLine3(p, 999)
	if !ok {
		t.Fatal("ok = false")
	}
	if !strings.Contains(line, "\x1b]8;;"+prA+"\x1b\\") {
		t.Errorf("line %q lacks OSC 8 open with URL", line)
	}
	if !strings.HasSuffix(strings.Split(line, "approved")[0], "\x1b]8;;\x1b\\") && !strings.Contains(line, "\x1b]8;;\x1b\\") {
		t.Errorf("line %q lacks OSC 8 reset", line)
	}
	stripped := stripANSI(line)
	if i, j := strings.Index(stripped, "PR #42"), strings.Index(stripped, "issue-1"); i < 0 || j < 0 || i > j {
		t.Errorf("stripped %q: PR must precede the worktree name", stripped)
	}
	plain := "/tmp/project · PR #42 approved · issue-1"
	if got, want := lipgloss.Width(line), lipgloss.Width(plain); got != want {
		t.Errorf("Width = %d, want visible width %d (OSC 8 must not count)", got, want)
	}
	// Branch (if any) precedes PR; with no git repo none is shown, so just
	// verify an unlinked URL is never emitted.
	p.PR.URL = strPtr("javascript:alert(1)")
	if l, _ := renderRichLine3(p, 999); strings.Contains(l, "\x1b]8;;") {
		t.Errorf("non-http URL was linked: %q", l)
	}
}

func TestPersistWindowStatePRScan(t *testing.T) {
	windowstate.SetDirForTest(t.TempDir())
	t.Cleanup(func() { windowstate.SetDirForTest("") })
	const w = "@9"
	logPath := filepath.Join(t.TempDir(), "s.jsonl")
	writeLog(t, logPath, prLine(7, prA), false)
	p := &Payload{TranscriptPath: &logPath}
	if err := windowstate.Update(w, func(ws *windowstate.WindowState) { ws.Model = "m" }); err != nil {
		t.Fatal(err)
	}
	cursor := func() windowstate.LogPRScan {
		var c windowstate.LogPRScan
		_ = windowstate.Update(w, func(ws *windowstate.WindowState) { c = ws.LogPR })
		return c
	}

	// Non-rich: no scan, LogPR untouched.
	if pr := persistWindowState(p, w, false); pr != nil {
		t.Errorf("non-rich returned PR %v", pr)
	}
	if c := cursor(); c != (windowstate.LogPRScan{}) {
		t.Errorf("non-rich touched LogPR: %+v", c)
	}

	// Rich: the single persist write stores the cursor and returns the PR.
	pr := persistWindowState(p, w, true)
	if pr == nil || int(*pr.Number) != 7 {
		t.Fatalf("rich pr = %v, want #7", pr)
	}
	if c := cursor(); c.PRNumber != 7 || c.Size == 0 || c.Path != logPath {
		t.Errorf("LogPR = %+v, want #7 with cursor", c)
	}

	// Grown log: same call updates the cursor and PR.
	writeLog(t, logPath, prLine(9, prB), true)
	pr = persistWindowState(p, w, true)
	if pr == nil || int(*pr.Number) != 9 {
		t.Fatalf("grown pr = %v, want #9", pr)
	}
	if c := cursor(); c.PRNumber != 9 {
		t.Errorf("LogPR = %+v, want #9", c)
	}
}
