package reconcile

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/metrics/events"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

type fakeEnv struct {
	t       *testing.T
	rt      string
	mu      sync.Mutex
	windows []tmux.WindowInfo
	listErr error
	session bool
	lists   int
	handled []state.Transition
	inboxes []string
	// onList, when set, runs at the start of each List call (1-based count).
	onList func(n int)
}

func newEnv(t *testing.T, live ...string) *fakeEnv {
	t.Helper()
	windowstate.SetDirForTest(filepath.Join(t.TempDir(), "windows"))
	if err := os.MkdirAll(windowstate.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	e := &fakeEnv{t: t, rt: t.TempDir(), session: true}
	for _, id := range live {
		e.windows = append(e.windows, tmux.WindowInfo{ID: id})
	}
	return e
}

func (e *fakeEnv) record(id, st, instance string) {
	e.t.Helper()
	if err := windowstate.Write(id, windowstate.WindowState{State: st, Instance: instance, AgentName: tmux.BaseAgentName(instance)}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *fakeEnv) deps() Deps {
	return Deps{
		RuntimeDir: e.rt,
		List: func() ([]tmux.WindowInfo, error) {
			e.mu.Lock()
			e.lists++
			n := e.lists
			hook := e.onList
			e.mu.Unlock()
			if hook != nil {
				hook(n)
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			return append([]tmux.WindowInfo(nil), e.windows...), e.listErr
		},
		HasSession: func() bool { return e.session },
		Settle:     time.Millisecond,
		Sleep:      func(time.Duration) {},
		Handle: func(tr state.Transition, _ config.AgentConfig) {
			e.mu.Lock()
			e.handled = append(e.handled, tr)
			e.mu.Unlock()
		},
		AgentConfig: func(string) config.AgentConfig { return config.AgentConfig{} },
		RemoveInbox: func(id string) error {
			e.mu.Lock()
			e.inboxes = append(e.inboxes, id)
			e.mu.Unlock()
			return nil
		},
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(e *fakeEnv)
		wantHandled []string // window IDs with a Gone handler call
		wantReaped  []string
		wantKept    []string
	}{
		{
			name:        "absent window is reaped with one handler and mailbox cleanup",
			setup:       func(e *fakeEnv) { e.record("@1", "working", "go-engineer:1"); e.record("@2", "idle", "reviewer") },
			wantHandled: []string{"@1", "@2"},
			wantReaped:  []string{"@1", "@2"},
		},
		{
			name: "present window untouched",
			setup: func(e *fakeEnv) {
				e.windows = []tmux.WindowInfo{{ID: "@1"}}
				e.record("@1", "working", "go-engineer")
				e.record("@2", "idle", "reviewer")
			},
			wantHandled: []string{"@2"},
			wantReaped:  []string{"@2"},
			wantKept:    []string{"@1"},
		},
		{
			name: "list error does nothing",
			setup: func(e *fakeEnv) {
				e.listErr = errors.New("tmux died")
				e.record("@1", "working", "go-engineer")
			},
			wantKept: []string{"@1"},
		},
		{
			name: "missing session does nothing",
			setup: func(e *fakeEnv) {
				e.session = false
				e.record("@1", "working", "go-engineer")
			},
			wantKept: []string{"@1"},
		},
		{
			name: "zero windows with live session reaps",
			setup: func(e *fakeEnv) {
				e.record("@1", "working", "go-engineer")
			},
			wantHandled: []string{"@1"},
			wantReaped:  []string{"@1"},
		},
		{
			name: "only the shell window present",
			setup: func(e *fakeEnv) {
				e.windows = []tmux.WindowInfo{{ID: "@0"}}
				e.record("@0", "", "shell")
				e.record("@1", "idle", "go-engineer")
			},
			wantHandled: []string{"@1"},
			wantReaped:  []string{"@1"},
			wantKept:    []string{"@0"},
		},
		{
			name: "shell and improve windows are removed without handlers",
			setup: func(e *fakeEnv) {
				e.record("@5", "", "shell")
				e.record("@6", "idle", "improve~go-engineer")
			},
			wantReaped: []string{"@5", "@6"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(e)
			got := Run(e.deps())

			if !sameSet(got, tc.wantReaped) {
				t.Errorf("reaped = %v, want %v", got, tc.wantReaped)
			}
			var handled []string
			for _, tr := range e.handled {
				if tr.To != state.StatusGone {
					t.Errorf("transition To = %v, want Gone", tr.To)
				}
				handled = append(handled, tr.WindowID)
			}
			if !sameSet(handled, tc.wantHandled) {
				t.Errorf("handled = %v, want %v", handled, tc.wantHandled)
			}
			if !sameSet(e.inboxes, tc.wantReaped) {
				t.Errorf("inboxes removed = %v, want %v", e.inboxes, tc.wantReaped)
			}
			for _, id := range tc.wantReaped {
				if windowstate.Exists(id) {
					t.Errorf("record %s still exists", id)
				}
			}
			for _, id := range tc.wantKept {
				if !windowstate.Exists(id) {
					t.Errorf("record %s was removed", id)
				}
			}
		})
	}
}

func TestRunTransitionFromRecord(t *testing.T) {
	e := newEnv(t)
	e.record("@3", "blocked", "go-engineer:2")
	Run(e.deps())
	if len(e.handled) != 1 {
		t.Fatalf("handled %d, want 1", len(e.handled))
	}
	tr := e.handled[0]
	if tr.Agent != "go-engineer" || tr.From != state.StatusBlocked || tr.WindowID != "@3" {
		t.Errorf("transition = %+v", tr)
	}
}

func TestRunNoRuntimeDirIsNoop(t *testing.T) {
	e := newEnv(t)
	e.record("@1", "working", "go-engineer")
	d := e.deps()
	d.RuntimeDir = ""
	if got := Run(d); len(got) != 0 || !windowstate.Exists("@1") {
		t.Fatalf("Run with no runtime dir reaped %v", got)
	}
}

func TestRunConcurrentOneHandler(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"@1", "@2", "@3"} {
		e.record(id, "working", "go-engineer")
	}
	d := e.deps()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); Run(d) }()
	}
	wg.Wait()
	if len(e.handled) != 3 {
		t.Fatalf("handler ran %d times for 3 windows: %v", len(e.handled), e.handled)
	}
}

// The monitor and a reconcile racing on the same window: the claim is what
// makes exactly one of them fire the handlers, bypassing the run lock.
func TestClaimOnlyOneWins(t *testing.T) {
	e := newEnv(t)
	e.record("@9", "working", "go-engineer")
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, ok := Claim("@9", nil); ok {
				won.Add(1)
				Finish(c, e.deps())
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 || len(e.handled) != 1 {
		t.Fatalf("winners=%d handler calls=%d, want 1 and 1", won.Load(), len(e.handled))
	}
}

// A window absent from the first listing that reappears before the second
// (a flaky read) is left alone.
func TestRunTransientAbsenceKept(t *testing.T) {
	e := newEnv(t)
	e.record("@1", "working", "go-engineer")
	e.onList = func(n int) {
		if n == 1 {
			return
		}
		e.mu.Lock()
		e.windows = []tmux.WindowInfo{{ID: "@1"}}
		e.mu.Unlock()
	}
	if got := Run(e.deps()); len(got) != 0 {
		t.Fatalf("reaped %v on transient absence", got)
	}
	if !windowstate.Exists("@1") || len(e.handled) != 0 {
		t.Fatal("record removed or handler fired on a transient absence")
	}
}

// tmux recycles @N: a new window reuses the ID after the old one died. If it
// shows up between the settle listing and the claim, its (new) record is kept.
func TestRunRecycledIDNotRemoved(t *testing.T) {
	e := newEnv(t)
	e.record("@4", "working", "go-engineer")
	e.onList = func(n int) {
		if n < 3 { // the third listing is the one under the record lock
			return
		}
		e.mu.Lock()
		e.windows = []tmux.WindowInfo{{ID: "@4"}}
		e.mu.Unlock()
	}
	if got := Run(e.deps()); len(got) != 0 {
		t.Fatalf("reaped recycled window: %v", got)
	}
	if !windowstate.Exists("@4") || len(e.handled) != 0 {
		t.Fatal("recycled window's record removed or handler fired")
	}
}

// A tombstoned ID that a new agent revives and rewrites, with its window live,
// is untouched by a later run.
func TestRunRevivedRecordKept(t *testing.T) {
	e := newEnv(t, "@7")
	e.record("@7", "working", "go-engineer")
	windowstate.Remove("@7")
	windowstate.Revive("@7")
	e.record("@7", "working", "reviewer")
	Run(e.deps())
	if !windowstate.Exists("@7") || len(e.handled) != 0 {
		t.Fatal("new window's record was reaped")
	}
}

func TestProtectedSessionLogs(t *testing.T) {
	recs := map[string]windowstate.WindowState{
		"@1": {SessionID: "aaa", LogPath: "/x/bbb.jsonl"},
		"@2": {SessionID: "aaa"},
		"@3": {},
	}
	got := ProtectedSessionLogs("/logs", recs)
	if !sameSet(got, []string{"/logs/aaa.log", "/logs/bbb.log"}) {
		t.Errorf("got %v", got)
	}
	if ProtectedSessionLogs("", recs) != nil {
		t.Error("empty dir must yield nil")
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

// Finish emits a Gone event into the project runtime dir when metrics are
// enabled (the in-process tracker no longer exists in a detached reconcile),
// exactly once per claim, and none for shell windows or when disabled.
func TestFinishEmitsGoneEvent(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		instance string
		want     int
	}{
		{"enabled", true, "go-engineer:1", 1},
		{"disabled", false, "go-engineer:1", 0},
		{"shell", true, "shell", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			oe := events.Enabled
			events.Enabled = func() bool { return tt.enabled }
			t.Cleanup(func() { events.Enabled = oe })
			e.record("@4", "working", tt.instance)
			c, ok := Claim("@4", nil)
			if !ok {
				t.Fatal("claim failed")
			}
			Finish(c, e.deps())

			n := 0
			for _, f := range events.Files(e.rt) {
				b, _ := os.ReadFile(f)
				ev, ok := events.Decode(b)
				if !ok || ev.To != "gone" || ev.From != "working" || ev.WindowID != "@4" {
					t.Fatalf("bad event %q", b)
				}
				n += 1
			}
			if n != tt.want {
				t.Errorf("gone events = %d, want %d", n, tt.want)
			}
		})
	}
}
