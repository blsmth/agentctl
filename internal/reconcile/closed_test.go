package reconcile

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// captureClosed replaces appendClosed with a recorder for the test.
func captureClosed(t *testing.T) func() []closedlog.Record {
	t.Helper()
	var mu sync.Mutex
	var got []closedlog.Record
	old := appendClosed
	appendClosed = func(r closedlog.Record) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, r)
		return nil
	}
	t.Cleanup(func() { appendClosed = old })
	return func() []closedlog.Record {
		mu.Lock()
		defer mu.Unlock()
		return append([]closedlog.Record(nil), got...)
	}
}

func TestFinishRecordsTrackedWindowsOnly(t *testing.T) {
	tests := []struct {
		name     string
		ws       windowstate.WindowState
		wantLine bool
	}{
		{"tracked", windowstate.WindowState{State: "idle", Instance: "go-engineer-2", AgentName: "go-engineer"}, true},
		{"shell", windowstate.WindowState{State: "idle", Instance: "shell"}, false},
		{"improve", windowstate.WindowState{State: "idle", Instance: "improve~go-engineer", AgentName: "go-engineer", Mode: "improve"}, true},
		{"unidentifiable", windowstate.WindowState{State: "idle"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := captureClosed(t)
			e := newEnv(t)
			d := e.deps()
			d.Handle = nil // monitor:false and no handler still records
			Finish(Claimed{WindowID: "@7", Record: tt.ws}, d)
			if n := len(got()); (n == 1) != tt.wantLine || n > 1 {
				t.Fatalf("appended %d lines, want line=%v", n, tt.wantLine)
			}
		})
	}
}

func TestClosedRecordFields(t *testing.T) {
	got := captureClosed(t)
	e := newEnv(t)
	d := e.deps()
	d.Session = "agentctl-proj"
	d.RuntimeDir = filepath.Join("/repo/proj", ".agentctl")
	ws := windowstate.WindowState{
		State: "idle", Done: true, Instance: "go-engineer-2", AgentName: "go-engineer",
		Harness: "pi", Account: "work", Model: "claude-opus-5-5", SessionID: "u1", LogPath: "/l/u1.jsonl",
		FirstPrompt: "fix the thing", TurnCount: 4, Branch: "agent/x", WorktreeDir: "/repo/proj/.worktrees/x",
		SpawnedBy: "@1", SpawnedBySession: "p1",
		ShortTitle: windowstate.ShortTitle{Title: "Fix thing", Kind: "go"},
		LogPR:      windowstate.LogPRScan{PRNumber: 12, PRURL: "https://x/pull/12"},
	}
	Finish(Claimed{WindowID: "@7", Record: ws}, d)
	recs := got()
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	want := closedlog.Record{
		WindowID: "@7", Session: "agentctl-proj", ProjectDir: "/repo/proj", RuntimeDir: d.RuntimeDir,
		WorkDir: "/repo/proj/.worktrees/x", Agent: "go-engineer", Instance: "go-engineer-2", Harness: "pi",
		Account: "work", Model: "claude-opus-5-5", SessionID: "u1", LogPath: "/l/u1.jsonl",
		ShortTitle: "Fix thing", TitleKind: "go", FirstPrompt: "fix the thing", State: "idle", Done: true,
		TurnCount: 4, Branch: "agent/x", Worktree: "/repo/proj/.worktrees/x", PRNumber: 12, PRURL: "https://x/pull/12",
		SpawnedBy: "@1", SpawnedBySession: "p1", Reason: closedlog.ReasonClosed,
	}
	if r != want {
		t.Errorf("record =\n%+v\nwant\n%+v", r, want)
	}
}

func TestFinishAppendErrorIsNonFatal(t *testing.T) {
	old := appendClosed
	appendClosed = func(closedlog.Record) error { return errors.New("disk full") }
	t.Cleanup(func() { appendClosed = old })
	e := newEnv(t)
	Finish(Claimed{WindowID: "@7", Record: windowstate.WindowState{Instance: "go-engineer"}}, e.deps())
	if len(e.inboxes) != 1 {
		t.Errorf("Finish stopped after the ledger error: inboxes = %v", e.inboxes)
	}
}

// TestFinishWritesLedger runs Finish against the real (testenv-isolated)
// ledger: Run claims one gone window and its line is loadable.
func TestFinishWritesLedger(t *testing.T) {
	e := newEnv(t)
	e.record("@41", "idle", "go-engineer")
	d := e.deps()
	d.Session = "agentctl-ledger-" + t.Name()
	before := time.Now().Add(-time.Second)
	if ids := Run(d); len(ids) != 1 {
		t.Fatalf("Run = %v", ids)
	}
	recs, err := closedlog.Load(before)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.Session == d.Session {
			n++
			if r.WindowID != "@41" || r.Agent != "go-engineer" {
				t.Errorf("record = %+v", r)
			}
		}
	}
	if n != 1 {
		t.Errorf("found %d ledger lines for the session, want 1", n)
	}
}

func TestCascadeChildRecordsReason(t *testing.T) {
	got := captureClosed(t)
	e := newCascadeEnv(t, "@2")
	e.write("@1", windowstate.WindowState{State: "idle", SessionID: "p1"})
	e.write("@2", child("@1", "p1", "c1"))
	d := e.deps()
	d.Cascade.Mark = func(w LiveWindow, parentID string) error {
		return windowstate.Update(w.WindowID, func(ws *windowstate.WindowState) { ws.ClosedBy = parentID })
	}
	Run(d) // parent gone: closes @2
	Run(d) // child gone
	recs := got()
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(recs), recs)
	}
	if recs[0].WindowID != "@1" || recs[0].Reason != closedlog.ReasonClosed {
		t.Errorf("parent = %+v", recs[0])
	}
	if recs[1].WindowID != "@2" || recs[1].Reason != closedlog.ReasonCascade || recs[1].ClosedBy != "@1" {
		t.Errorf("child = %+v", recs[1])
	}
}

func TestCascadeFailedKillClearsMark(t *testing.T) {
	captureClosed(t)
	e := newCascadeEnv(t, "@2")
	e.write("@1", windowstate.WindowState{State: "idle", SessionID: "p1"})
	e.write("@2", child("@1", "p1", "c1"))
	e.killErr = map[string]error{"@2": errors.New("no such window")}
	d := e.deps()
	d.Cascade.Mark = func(w LiveWindow, parentID string) error {
		return windowstate.Update(w.WindowID, func(ws *windowstate.WindowState) { ws.ClosedBy = parentID })
	}
	Run(d)
	ws, err := windowstate.Read("@2")
	if err != nil {
		t.Fatal(err)
	}
	if ws.ClosedBy != "" {
		t.Errorf("ClosedBy = %q after a failed kill, want cleared", ws.ClosedBy)
	}
}

func TestClosedRecordImproveReason(t *testing.T) {
	d := newEnv(t).deps()
	ws := windowstate.WindowState{Instance: "improve~go-engineer", AgentName: "go-engineer", Mode: "improve"}
	if got := closedRecord(Claimed{WindowID: "@7", Record: ws}, d, "improve~go-engineer", "go-engineer").Reason; got != closedlog.ReasonImprove {
		t.Errorf("reason = %q, want %q", got, closedlog.ReasonImprove)
	}
	ws.ClosedBy = "@1"
	if got := closedRecord(Claimed{WindowID: "@7", Record: ws}, d, "improve~go-engineer", "go-engineer").Reason; got != closedlog.ReasonCascade {
		t.Errorf("cascade reason = %q, want %q", got, closedlog.ReasonCascade)
	}
}
