package hooks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func liveSet(ids ...string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// An idle window that finished turns before titles shipped gets one final
// title from its task, last response and recap.
func TestBackfillTitlesFinalAfterFirstStop(t *testing.T) {
	e := setupTitle(t)
	e.recap = "Rebuilt the hub sort order and added tests."
	e.fake.out = "fix | Fixing Hub Sort Order"
	seedWindow(t, "@1", windowstate.WindowState{SessionID: "s1", State: "idle", Task: "https://github.com/x/y/issues/9 fix it", TurnCount: 3, LastText: "Done, PR opened."})

	if n := BackfillTitles(context.Background(), liveSet("@1")); n != 1 {
		t.Fatalf("sent = %d, want 1", n)
	}
	st := readTitle(t, "@1")
	if st.Title != "Fixing Hub Sort Order" || st.Kind != "fix" || !st.Final {
		t.Fatalf("title = %+v, want final", st)
	}
	for _, want := range []string{"fix it", "Done, PR opened.", "Rebuilt the hub sort order"} {
		if !strings.Contains(e.fake.prompts[0], want) {
			t.Errorf("prompt missing %q:\n%s", want, e.fake.prompts[0])
		}
	}
	if e.backfillTakes.Load() != 1 {
		t.Errorf("backfill budget takes = %d, want 1", e.backfillTakes.Load())
	}
	// Frozen: a second run sends nothing.
	if n := BackfillTitles(context.Background(), liveSet("@1")); n != 0 || e.fake.calls != 1 {
		t.Fatalf("second run sent %d (calls %d)", n, e.fake.calls)
	}
}

// Before its first Stop a window gets a normal, unfrozen first title, and is
// not titled again until a Stop refines it.
func TestBackfillTitlesBeforeFirstStopNotFinal(t *testing.T) {
	e := setupTitle(t)
	e.recap = "should not be read"
	seedWindow(t, "@1", windowstate.WindowState{SessionID: "s1", State: "working", FirstPrompt: "organize my photos"})

	if n := BackfillTitles(context.Background(), liveSet("@1")); n != 1 {
		t.Fatalf("sent = %d, want 1", n)
	}
	st := readTitle(t, "@1")
	if st.Title == "" || st.Final {
		t.Fatalf("title = %+v, want an unfrozen first title", st)
	}
	if strings.Contains(e.fake.prompts[0], "should not be read") {
		t.Error("recap used before the first Stop")
	}
	if n := BackfillTitles(context.Background(), liveSet("@1")); n != 0 {
		t.Fatalf("titled window re-titled before a Stop (%d)", n)
	}
}

// A window that only has a recap (no task or first prompt recorded) is
// titled from it.
func TestBackfillTitlesFromRecapOnly(t *testing.T) {
	e := setupTitle(t)
	e.recap = "Migrated the photo library to year folders."
	seedWindow(t, "@1", windowstate.WindowState{SessionID: "s1", State: "idle", TurnCount: 1})
	if n := BackfillTitles(context.Background(), liveSet("@1")); n != 1 {
		t.Fatalf("sent = %d, want 1", n)
	}
	if st := readTitle(t, "@1"); !st.Final {
		t.Fatalf("title = %+v, want final", st)
	}
}

func TestBackfillTitlesCapPerRun(t *testing.T) {
	e := setupTitle(t)
	ids := []string{"@1", "@2", "@3", "@4", "@5"}
	for _, id := range ids {
		seedWindow(t, id, windowstate.WindowState{SessionID: "s" + id, Task: "task " + id})
	}
	// One already titled, awaiting refinement: untitled windows go first.
	seedWindow(t, "@0", windowstate.WindowState{SessionID: "s0", Task: "t", TurnCount: 1, LastText: "done",
		ShortTitle: windowstate.ShortTitle{Title: "Old Title"}})
	if n := BackfillTitles(context.Background(), liveSet(append(ids, "@0")...)); n != BackfillPerRun {
		t.Fatalf("sent = %d, want %d", n, BackfillPerRun)
	}
	if e.fake.calls != BackfillPerRun {
		t.Fatalf("calls = %d", e.fake.calls)
	}
	if st := readTitle(t, "@0"); st.Title != "Old Title" {
		t.Errorf("titled window went before untitled ones: %+v", st)
	}
	// The next run picks up the rest, then the refinement.
	if n := BackfillTitles(context.Background(), liveSet(append(ids, "@0")...)); n != 3 {
		t.Fatalf("second run sent %d, want 3", n)
	}
	if st := readTitle(t, "@0"); st.Title == "Old Title" || !st.Final {
		t.Errorf("refinement not backfilled: %+v", st)
	}
}

// The budget running out stops the run and leaves no lease or backoff.
func TestBackfillTitlesBudgetStopsRun(t *testing.T) {
	e := setupTitle(t)
	e.budgetCap = 1
	for _, id := range []string{"@1", "@2", "@3"} {
		seedWindow(t, id, windowstate.WindowState{SessionID: "s" + id, Task: "task " + id})
	}
	if n := BackfillTitles(context.Background(), liveSet("@1", "@2", "@3")); n != 1 {
		t.Fatalf("sent = %d, want 1", n)
	}
	if got := e.backfillTakes.Load(); got != 2 {
		t.Fatalf("budget asked %d times, want 2 (stop after the first refusal)", got)
	}
	st := readTitle(t, "@2")
	if st.Title != "" || !st.RetryAfter.IsZero() || st.Failures != 0 {
		t.Fatalf("refused window recorded state: %+v", st)
	}
}

func TestBackfillTitlesSkips(t *testing.T) {
	cases := []struct {
		name  string
		ws    windowstate.WindowState
		live  bool
		setup func(*testing.T, *titleEnv)
	}{
		{name: "not live (other session or vanished)", ws: windowstate.WindowState{SessionID: "s", Task: "t"}},
		{name: "shell", live: true, ws: windowstate.WindowState{SessionID: "s", Task: "t", Instance: "shell"}},
		{name: "improve", live: true, ws: windowstate.WindowState{SessionID: "s", Task: "t", Instance: "improve~1"}},
		{name: "cron", live: true, ws: windowstate.WindowState{SessionID: "s", Task: "t", ScheduledBy: "nightly"}},
		{name: "no session", live: true, ws: windowstate.WindowState{Task: "t"}},
		{name: "no input", live: true, ws: windowstate.WindowState{SessionID: "s"}},
		{name: "final", live: true, ws: windowstate.WindowState{SessionID: "s", Task: "t", ShortTitle: windowstate.ShortTitle{Title: "X", Final: true}}},
		{name: "manual", live: true, ws: windowstate.WindowState{SessionID: "s", Task: "t", TurnCount: 2, ShortTitle: windowstate.ShortTitle{Title: "Mine", Manual: true}}},
		{name: "backoff", live: true, ws: windowstate.WindowState{SessionID: "s", Task: "t", ShortTitle: windowstate.ShortTitle{Failures: 1, RetryAfter: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}}},
		{name: "titles disabled", live: true, ws: windowstate.WindowState{SessionID: "s", Task: "t"},
			setup: func(_ *testing.T, e *titleEnv) { e.settings.Enabled = false }},
		{name: "no key", live: true, ws: windowstate.WindowState{SessionID: "s", Task: "t"},
			setup: func(_ *testing.T, e *titleEnv) { e.keyErr = errors.New("no key") }},
		{name: "no credits", live: true, ws: windowstate.WindowState{SessionID: "s", Task: "t"},
			setup: func(t *testing.T, _ *titleEnv) {
				old := titleNoCredits
				titleNoCredits = func(string, time.Time) (backendauth.CreditState, bool) { return backendauth.CreditState{}, true }
				t.Cleanup(func() { titleNoCredits = old })
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := setupTitle(t)
			if tc.setup != nil {
				tc.setup(t, e)
			}
			seedWindow(t, "@1", tc.ws)
			live := liveSet("@9")
			if tc.live {
				live = liveSet("@1")
			}
			if n := BackfillTitles(context.Background(), live); n != 0 || e.fake.calls != 0 || e.backfillTakes.Load() != 0 {
				t.Fatalf("sent=%d calls=%d takes=%d, want none", n, e.fake.calls, e.backfillTakes.Load())
			}
		})
	}
}

// A window claimed by a concurrent run (hook-title or another housekeeping)
// is not requested twice.
func TestBackfillTitlesClaimPreventsDoubleRequest(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@1", windowstate.WindowState{SessionID: "s1", Task: "t"})
	e.fake.block = make(chan struct{})
	done := make(chan int)
	go func() { done <- BackfillTitles(context.Background(), liveSet("@1")) }()
	// Wait for the first run to hold its lease.
	for readTitle(t, "@1").RetryAfter.IsZero() {
		time.Sleep(time.Millisecond)
	}
	if n := BackfillTitles(context.Background(), liveSet("@1")); n != 0 {
		t.Fatalf("second run sent %d while the first held the claim", n)
	}
	close(e.fake.block)
	if n := <-done; n != 1 || e.fake.calls != 1 {
		t.Fatalf("first run sent %d, calls %d", n, e.fake.calls)
	}
}

// A /clear between the scan and the claim (new session ID) is not titled
// from the old work.
func TestBackfillTitlesSkipsWindowReset(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@1", windowstate.WindowState{SessionID: "old", Task: "t"})
	oldRecords := titleRecords
	titleRecords = func() map[string]windowstate.WindowState {
		recs := oldRecords()
		seedWindow(t, "@1", windowstate.WindowState{SessionID: "new", Task: "t"})
		return recs
	}
	t.Cleanup(func() { titleRecords = oldRecords })
	if n := BackfillTitles(context.Background(), liveSet("@1")); n != 0 || e.fake.calls != 0 {
		t.Fatalf("sent=%d calls=%d after the window was reset", n, e.fake.calls)
	}
}

// The window vanishing (tombstoned) before the claim sends nothing.
func TestBackfillTitlesVanishedWindow(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@1", windowstate.WindowState{SessionID: "s1", Task: "t"})
	oldRecords := titleRecords
	titleRecords = func() map[string]windowstate.WindowState {
		recs := oldRecords()
		windowstate.Remove("@1")
		return recs
	}
	t.Cleanup(func() { titleRecords = oldRecords })
	if n := BackfillTitles(context.Background(), liveSet("@1")); n != 0 || e.fake.calls != 0 {
		t.Fatalf("sent=%d calls=%d for a vanished window", n, e.fake.calls)
	}
}
