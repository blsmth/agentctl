package reconcile

import (
	"errors"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/metrics/events"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestHousekeepStampsEachSweep(t *testing.T) {
	var reaps, mail, logs int
	var protected []string
	now := time.Now()
	d := HousekeepingDeps{
		RuntimeDir:   t.TempDir(),
		Now:          func() time.Time { return now },
		ReapOrphans:  func() { reaps++ },
		PruneMailbox: func() (int, error) { mail++; return 0, nil },
		PruneSessionLogs: func(p []string) (int, error) {
			logs++
			protected = p
			return 0, errors.New("ignored")
		},
		Records: func() map[string]windowstate.WindowState {
			return map[string]windowstate.WindowState{"@1": {SessionID: "s1"}}
		},
		SessionLogDir: func() string { return "/logs" },
	}
	Housekeep(d)
	now = now.Add(time.Minute) // janitor cadence: reap again, sweeps still stamped
	Housekeep(d)
	if reaps != 2 || mail != 1 || logs != 1 {
		t.Fatalf("reaps=%d mail=%d logs=%d, want 2/1/1", reaps, mail, logs)
	}
	if len(protected) != 1 || protected[0] != "/logs/s1.log" {
		t.Errorf("protected = %v", protected)
	}
	now = now.Add(PruneInterval)
	Housekeep(d)
	if mail != 2 || logs != 2 {
		t.Fatalf("after interval mail=%d logs=%d, want 2/2", mail, logs)
	}
}

func TestHousekeepNoRuntimeDir(t *testing.T) {
	called := false
	Housekeep(HousekeepingDeps{Now: time.Now, ReapOrphans: func() { called = true }})
	if called {
		t.Fatal("Housekeep ran without a runtime dir")
	}
}

func TestHousekeepPrunesEventFiles(t *testing.T) {
	rt := t.TempDir()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	old := events.Append(rt, events.Event{TS: now.AddDate(0, 0, -10), WindowID: "@1", Agent: "a", From: "idle", To: "working"})
	cur := events.Append(rt, events.Event{TS: now, WindowID: "@1", Agent: "a", From: "working", To: "idle"})
	if old != nil || cur != nil {
		t.Fatalf("append: %v %v", old, cur)
	}
	if n := len(events.Files(rt)); n != 2 {
		t.Fatalf("setup: %d files, want 2", n)
	}
	d := DefaultHousekeepingDeps(rt)
	d.Now = func() time.Time { return now }
	d.ReapOrphans = nil
	d.PruneMailbox = nil
	d.PruneSessionLogs = nil
	Housekeep(d)
	if n := len(events.Files(rt)); n != 1 {
		t.Fatalf("after housekeeping: %d files, want 1", n)
	}
}

func TestHousekeepPrunesUploadsOnce(t *testing.T) {
	var n int
	now := time.Now()
	d := HousekeepingDeps{
		RuntimeDir:   t.TempDir(),
		Now:          func() time.Time { return now },
		PruneUploads: func(time.Time) (int, error) { n++; return 1, nil },
	}
	Housekeep(d)
	Housekeep(d)
	if n != 1 {
		t.Fatalf("PruneUploads ran %d times within the interval, want 1", n)
	}
}
