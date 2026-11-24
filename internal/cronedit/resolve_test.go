package cronedit

import (
	"os"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/cron"
)

var t0 = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

func fired(sid string) cron.Entry {
	return cron.Entry{Time: t0, Job: "nightly", Result: cron.ResultFired, WindowID: "@5", Session: "agentctl-p", SessionID: sid}
}

type fakeEnv struct {
	live   []LiveWindow
	closed []closedlog.Record
	logs   map[string]string
	exist  map[string]bool
}

func (f fakeEnv) resolver() *Resolver {
	return &Resolver{
		Live:    func() []LiveWindow { return f.live },
		Closed:  func(time.Time) []closedlog.Record { return f.closed },
		FindLog: func(id string) string { return f.logs[id] },
		Stat: func(p string) (os.FileInfo, error) {
			if f.exist[p] {
				return os.Stat(".")
			}
			return nil, os.ErrNotExist
		},
		Now: func() time.Time { return t0.Add(time.Hour) },
	}
}

func TestResolveRun(t *testing.T) {
	closedRec := closedlog.Record{
		ClosedAt: t0.Add(10 * time.Minute), WindowID: "@5", Session: "agentctl-p", ProjectDir: "/proj",
		WorkDir: "/proj", Agent: "go-engineer", SessionID: "S1", LogPath: "/logs/S1.jsonl", ScheduledBy: "nightly",
	}
	exist := map[string]bool{"/logs/S1.jsonl": true, "/proj": true}
	tests := []struct {
		name      string
		entry     cron.Entry
		env       fakeEnv
		kind      RunKind
		resumable bool
		reason    string
	}{
		{"live by session id", fired("S1"), fakeEnv{live: []LiveWindow{{Session: "agentctl-p", WindowID: "@9", SessionID: "S1"}}}, RunLive, false, ""},
		{"live window id reused by another session", fired("S1"),
			fakeEnv{live: []LiveWindow{{Session: "agentctl-p", WindowID: "@5", SessionID: "OTHER", ScheduledBy: "nightly", ScheduledAt: t0}}}, RunMissing, false, "no longer recorded"},
		{"still launching", fired("S1"),
			fakeEnv{live: []LiveWindow{{Session: "agentctl-p", WindowID: "@5", ScheduledBy: "nightly", ScheduledAt: t0}}}, RunLive, false, ""},
		{"closed by session id", fired("S1"), fakeEnv{closed: []closedlog.Record{closedRec}, exist: exist}, RunClosed, true, ""},
		{"closed, worktree gone", fired("S1"), fakeEnv{closed: []closedlog.Record{closedRec}, exist: map[string]bool{"/logs/S1.jsonl": true}}, RunClosed, false, "gone"},
		{"closed, transcript pruned", fired("S1"), fakeEnv{closed: []closedlog.Record{closedRec}, exist: map[string]bool{"/proj": true}}, RunMissing, false, "transcript no longer on disk"},
		{"closed, log found by session id", fired("S1"),
			fakeEnv{closed: []closedlog.Record{func() closedlog.Record { r := closedRec; r.LogPath = ""; return r }()},
				logs: map[string]string{"S1": "/logs/S1.jsonl"}, exist: exist}, RunClosed, true, ""},
		{"legacy live", fired(""),
			fakeEnv{live: []LiveWindow{{Session: "agentctl-p", WindowID: "@5", ScheduledBy: "nightly", ScheduledAt: t0.Add(5 * time.Second)}}}, RunLive, false, ""},
		{"legacy live, window id reused by other job", fired(""),
			fakeEnv{live: []LiveWindow{{Session: "agentctl-p", WindowID: "@5", ScheduledBy: "weekly", ScheduledAt: t0}}}, RunMissing, false, "predates"},
		{"legacy live, window id reused later", fired(""),
			fakeEnv{live: []LiveWindow{{Session: "agentctl-p", WindowID: "@5", ScheduledBy: "nightly", ScheduledAt: t0.Add(24 * time.Hour)}}}, RunMissing, false, "predates"},
		{"legacy closed", fired(""), fakeEnv{closed: []closedlog.Record{closedRec}, exist: exist}, RunClosed, true, ""},
		{"legacy closed before the run is not it", fired(""),
			fakeEnv{closed: []closedlog.Record{func() closedlog.Record { r := closedRec; r.ClosedAt = t0.Add(-time.Hour); return r }()}, exist: exist}, RunMissing, false, "predates"},
		{"legacy closed other job", fired(""),
			fakeEnv{closed: []closedlog.Record{func() closedlog.Record { r := closedRec; r.ScheduledBy = "weekly"; return r }()}, exist: exist}, RunMissing, false, "predates"},
		{"nothing matched", fired("S1"), fakeEnv{}, RunMissing, false, "no longer recorded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.env.resolver().ResolveRun(tt.entry)
			if got.Kind != tt.kind || got.Resumable != tt.resumable {
				t.Fatalf("got kind=%s resumable=%v reason=%q, want %s/%v", got.Kind, got.Resumable, got.Reason, tt.kind, tt.resumable)
			}
			if tt.reason != "" && !contains(got.Reason, tt.reason) {
				t.Errorf("reason %q lacks %q", got.Reason, tt.reason)
			}
		})
	}
}

func TestResolveLegacyClosedPicksClosest(t *testing.T) {
	mk := func(sid string, d time.Duration) closedlog.Record {
		return closedlog.Record{ClosedAt: t0.Add(d), WindowID: "@5", Session: "agentctl-p", ScheduledBy: "nightly", SessionID: sid, LogPath: "/l"}
	}
	env := fakeEnv{closed: []closedlog.Record{mk("LATE", 48*time.Hour), mk("NEAR", time.Hour)}, exist: map[string]bool{"/l": true}}
	if got := env.resolver().ResolveRun(fired("")); got.SessionID != "NEAR" {
		t.Errorf("session = %q, want NEAR", got.SessionID)
	}
}

func TestResolveNotResolvable(t *testing.T) {
	env := fakeEnv{live: []LiveWindow{{Session: "agentctl-p", WindowID: "@5", SessionID: "S1"}}}
	for _, res := range []string{cron.ResultSkippedOverlap, cron.ResultTimeout, cron.Failed("launch")} {
		e := fired("S1")
		e.Result = res
		if got := env.resolver().ResolveRun(e); got.Kind != RunMissing {
			t.Errorf("%s resolved to %s", res, got.Kind)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
