package reconcile

import (
	"errors"
	"reflect"
	"testing"

	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// cascadeEnv extends fakeEnv with fake cascade deps. Kill never touches tmux:
// it records the call and removes the window from the fake listing, which is
// what lets a second Run reconcile the child (the grandchild case).
type cascadeEnv struct {
	*fakeEnv
	enabled  bool
	killed   []string
	killErr  map[string]error
	listErr  error
	extraWin []LiveWindow // appended to the live listing (duplicates, other sessions)
}

func newCascadeEnv(t *testing.T, live ...string) *cascadeEnv {
	t.Helper()
	return &cascadeEnv{fakeEnv: newEnv(t, live...), enabled: true}
}

func (e *cascadeEnv) write(id string, ws windowstate.WindowState) {
	e.t.Helper()
	if ws.Instance == "" {
		ws.Instance = "go-engineer"
	}
	if ws.AgentName == "" {
		ws.AgentName = tmux.BaseAgentName(ws.Instance)
	}
	if err := windowstate.Write(id, ws); err != nil {
		e.t.Fatal(err)
	}
}

func (e *cascadeEnv) live() ([]LiveWindow, error) {
	if e.listErr != nil {
		return nil, e.listErr
	}
	recs := windowstate.All()
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []LiveWindow
	for _, w := range e.windows {
		if r, ok := recs[w.ID]; ok {
			out = append(out, LiveWindow{Session: "agentctl-test", RuntimeDir: e.rt, WindowID: w.ID, Record: r})
		}
	}
	return append(out, e.extraWin...), nil
}

func (e *cascadeEnv) deps() Deps {
	d := e.fakeEnv.deps()
	d.Cascade = CascadeDeps{
		Enabled:     func() bool { return e.enabled },
		HasSession:  func() bool { return e.session },
		Snapshotted: func(id, sid string) bool { return Snapshotted(e.rt, id, sid) },
		LiveWindows: e.live,
		Kill: func(w LiveWindow) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.killed = append(e.killed, w.WindowID)
			if err := e.killErr[w.WindowID]; err != nil {
				return err
			}
			kept := e.windows[:0]
			for _, lw := range e.windows {
				if lw.ID != w.WindowID {
					kept = append(kept, lw)
				}
			}
			e.windows = kept
			return nil
		},
	}
	return d
}

func child(parent, parentSID, sid string) windowstate.WindowState {
	return windowstate.WindowState{State: "working", Mode: "adhoc", SessionID: sid, SpawnedBy: parent, SpawnedBySession: parentSID}
}

func TestCascadeCloseChildren(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *cascadeEnv)
		want  []string
	}{
		{
			name: "child matched by window and session",
			setup: func(e *cascadeEnv) {
				e.write("@2", child("@1", "P", "C"))
			},
			want: []string{"@2"},
		},
		{
			name: "children closed regardless of state",
			setup: func(e *cascadeEnv) {
				for id, st := range map[string]string{"@2": "working", "@3": "blocked", "@4": "idle"} {
					ws := child("@1", "P", "C"+id)
					ws.State = st
					e.write(id, ws)
				}
			},
			want: []string{"@2", "@3", "@4"},
		},
		{
			name: "spawned_by match with session mismatch ignored",
			setup: func(e *cascadeEnv) {
				e.write("@2", child("@1", "OTHER", "C"))
			},
		},
		{
			name: "bare window with untrusted spawned_by ignored",
			setup: func(e *cascadeEnv) {
				ws := child("@1", "", "C")
				ws.Mode = "bare"
				e.write("@2", ws)
			},
		},
		{
			name: "detached and cron windows are never targets",
			setup: func(e *cascadeEnv) {
				e.write("@2", windowstate.WindowState{State: "working", Mode: "adhoc", SessionID: "D"})
				cron := child("@1", "P", "S")
				cron.ScheduledBy = "nightly"
				e.write("@3", cron)
			},
		},
		{
			name: "recycled parent ID: child of the old @1 ignored",
			setup: func(e *cascadeEnv) {
				// The gone @1 ran session P; this child belongs to an earlier
				// window that also had ID @1.
				e.write("@2", child("@1", "P-old", "C"))
			},
		},
		{
			name: "session going away: no cascade",
			setup: func(e *cascadeEnv) {
				e.write("@2", child("@1", "P", "C"))
				e.session = false
			},
		},
		{
			name: "parent saved for restore: no cascade",
			setup: func(e *cascadeEnv) {
				e.write("@2", child("@1", "P", "C"))
				if err := state.SaveRestoreAgents(e.rt, []state.AgentState{{Name: "go-engineer", WindowID: "@1", SessionID: "P"}}); err != nil {
					e.t.Fatal(err)
				}
			},
		},
		{
			name: "restore file for another window does not block",
			setup: func(e *cascadeEnv) {
				e.write("@2", child("@1", "P", "C"))
				if err := state.SaveRestoreAgents(e.rt, []state.AgentState{{Name: "go-engineer", WindowID: "@1", SessionID: "OLD"}}); err != nil {
					e.t.Fatal(err)
				}
			},
			want: []string{"@2"},
		},
		{
			name: "parent restored into a live window: no cascade",
			setup: func(e *cascadeEnv) {
				e.write("@2", child("@1", "P", "C"))
				e.write("@9", windowstate.WindowState{State: "idle", SessionID: "P"})
				e.windows = append(e.windows, tmux.WindowInfo{ID: "@9"})
			},
		},
		{
			name: "config off: no cascade",
			setup: func(e *cascadeEnv) {
				e.write("@2", child("@1", "P", "C"))
				e.enabled = false
			},
		},
		{
			name: "listing failure: no cascade",
			setup: func(e *cascadeEnv) {
				e.write("@2", child("@1", "P", "C"))
				e.listErr = errors.New("tmux died")
			},
		},
		{
			name: "duplicate listing is killed once",
			setup: func(e *cascadeEnv) {
				e.write("@2", child("@1", "P", "C"))
				e.extraWin = []LiveWindow{{Session: "agentctl-other", WindowID: "@2", Record: child("@1", "P", "C")}}
			},
			want: []string{"@2"},
		},
		{
			name: "child in another session is closed",
			setup: func(e *cascadeEnv) {
				e.extraWin = []LiveWindow{{Session: "agentctl-other", RuntimeDir: "/other/.agentctl", WindowID: "@20", Record: child("@1", "P", "C")}}
			},
			want: []string{"@20"},
		},
		{
			name: "a failed kill does not stop the rest",
			setup: func(e *cascadeEnv) {
				e.write("@2", child("@1", "P", "C2"))
				e.write("@3", child("@1", "P", "C3"))
				e.killErr = map[string]error{"@2": errors.New("gone already")}
			},
			want: []string{"@2", "@3"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newCascadeEnv(t)
			e.write("@1", windowstate.WindowState{State: "working", SessionID: "P"})
			tc.setup(e)
			for _, id := range []string{"@2", "@3", "@4"} {
				if windowstate.Exists(id) {
					e.windows = append(e.windows, tmux.WindowInfo{ID: id})
				}
			}
			c, ok := Claim("@1", nil)
			if !ok {
				t.Fatal("claim failed")
			}
			Finish(c, e.deps())
			if !sameSet(e.killed, tc.want) {
				t.Errorf("killed = %v, want %v", e.killed, tc.want)
			}
		})
	}
}

// A parent with no recorded session UUID cannot be matched to children.
func TestCascadeParentWithoutSessionID(t *testing.T) {
	e := newCascadeEnv(t, "@2")
	e.write("@1", windowstate.WindowState{State: "idle"})
	e.write("@2", child("@1", "", "C"))
	Run(e.deps())
	if len(e.killed) != 0 {
		t.Fatalf("killed %v", e.killed)
	}
}

// Run with the session gone claims nothing, so nothing cascades either.
func TestCascadeRunSessionGone(t *testing.T) {
	e := newCascadeEnv(t, "@2")
	e.write("@1", windowstate.WindowState{State: "working", SessionID: "P"})
	e.write("@2", child("@1", "P", "C"))
	e.session = false
	Run(e.deps())
	if len(e.killed) != 0 || !windowstate.Exists("@1") {
		t.Fatalf("killed %v, parent record kept=%v", e.killed, windowstate.Exists("@1"))
	}
}

// Grandchildren close through the child's own reconcile: the parent's run
// closes only the child; the next run (the one the child's kill spawns)
// finds the child gone and closes the grandchild.
func TestCascadeGrandchildViaChildReconcile(t *testing.T) {
	e := newCascadeEnv(t, "@2", "@3", "@4")
	e.write("@1", windowstate.WindowState{State: "working", SessionID: "P"})
	e.write("@2", child("@1", "P", "C"))
	e.write("@3", child("@2", "C", "G"))
	e.write("@4", windowstate.WindowState{State: "idle", SessionID: "U"}) // unrelated

	if got := Run(e.deps()); !sameSet(got, []string{"@1"}) {
		t.Fatalf("first run reaped %v, want [@1]", got)
	}
	if !sameSet(e.killed, []string{"@2"}) {
		t.Fatalf("first run killed %v, want [@2]", e.killed)
	}
	if got := Run(e.deps()); !sameSet(got, []string{"@2"}) {
		t.Fatalf("second run reaped %v, want [@2]", got)
	}
	if !sameSet(e.killed, []string{"@2", "@3"}) {
		t.Fatalf("after child reconcile killed %v, want [@2 @3]", e.killed)
	}
	if got := Run(e.deps()); !sameSet(got, []string{"@3"}) {
		t.Fatalf("third run reaped %v, want [@3]", got)
	}
	if len(e.killed) != 2 {
		t.Fatalf("extra kills: %v", e.killed)
	}
}

func TestDescendants(t *testing.T) {
	lw := func(id string, ws windowstate.WindowState) LiveWindow {
		return LiveWindow{WindowID: id, Record: ws}
	}
	cron := child("@1", "P", "S")
	cron.ScheduledBy = "job"
	wins := []LiveWindow{
		lw("@2", child("@1", "P", "C")),
		lw("@3", child("@2", "C", "G")),
		lw("@4", child("@1", "X", "Z")),  // session mismatch
		lw("@5", child("@3", "G", "GG")), // great-grandchild
		lw("@6", cron),
		lw("@2", child("@1", "P", "C")), // duplicate listing
		lw("@7", child("@5", "GG", "P")), // cycle back to the parent's session
		lw("@1", child("@7", "P", "P")),  // the parent itself is never a descendant
	}
	var got []string
	for _, w := range Descendants("@1", "P", wins) {
		got = append(got, w.WindowID)
	}
	want := []string{"@2", "@3", "@5", "@7"}
	if len(got) != len(want) {
		t.Fatalf("descendants = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("descendants = %v, want %v", got, want)
		}
	}
	if d := Descendants("@1", "", wins); len(d) != 0 {
		t.Errorf("no session UUID must yield none, got %v", d)
	}
}

func TestSnapshotted(t *testing.T) {
	rt := t.TempDir()
	if Snapshotted(rt, "@1", "P") {
		t.Fatal("no restore file must report false")
	}
	if err := state.SaveRestoreAgents(rt, []state.AgentState{
		{Name: "a", WindowID: "@1", SessionID: "P"},
		{Name: "b", WindowID: "@2"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, sid string
		want    bool
	}{
		{"@1", "P", true},
		{"@1", "Q", false},
		{"@2", "anything", true},
		{"@3", "P", false},
	} {
		if got := Snapshotted(rt, tc.id, tc.sid); got != tc.want {
			t.Errorf("Snapshotted(%s, %s) = %v, want %v", tc.id, tc.sid, got, tc.want)
		}
	}
	if Snapshotted("", "@1", "P") {
		t.Error("empty runtime dir must report false")
	}
}

func TestLiveWindowName(t *testing.T) {
	for _, tc := range []struct {
		w    LiveWindow
		want string
	}{
		{LiveWindow{WindowID: "@1", Record: windowstate.WindowState{Instance: "go-engineer-3"}}, "go-engineer-3"},
		{LiveWindow{WindowID: "@1", Record: windowstate.WindowState{SessionName: "sess"}}, "sess"},
		{LiveWindow{WindowID: "@1"}, "@1"},
	} {
		if got := tc.w.Name(); got != tc.want {
			t.Errorf("Name() = %q, want %q", got, tc.want)
		}
	}
}

func TestCloseDescendantsDeepestFirst(t *testing.T) {
	mk := func(id, by, bySID, sid string) LiveWindow {
		return LiveWindow{WindowID: id, Record: windowstate.WindowState{SessionID: sid, SpawnedBy: by, SpawnedBySession: bySID}}
	}
	wins := []LiveWindow{mk("@2", "@1", "p", "c"), mk("@3", "@2", "c", "g"), mk("@4", "@1", "p", "c2")}
	var order []string
	got := CloseDescendants("@1", "p", wins, func(w LiveWindow) error {
		if w.WindowID == "@4" {
			return errors.New("boom")
		}
		order = append(order, w.WindowID)
		return nil
	})
	if want := []string{"@3", "@2"}; !reflect.DeepEqual(order, want) || len(got) != 2 {
		t.Errorf("order %v closed %d, want %v", order, len(got), want)
	}
}
