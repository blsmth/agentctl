package winname

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blsmth/agentctl/internal/metrics/events"
	"github.com/blsmth/agentctl/internal/windowstate"
)

type fakes struct {
	mu      sync.Mutex
	renames []string
	active  string
	fail    bool
	current string
}

func install(t *testing.T) *fakes {
	t.Helper()
	windowstate.SetDirForTest(t.TempDir())
	t.Cleanup(func() { windowstate.SetDirForTest("") })
	f := &fakes{}
	restore := SetSeamsForTest(
		func(id, name string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.fail {
				return errors.New("no such window")
			}
			f.renames = append(f.renames, id+"="+name)
			return nil
		},
		func(string) string { return f.current },
		func() string { return f.active },
	)
	t.Cleanup(restore)
	return f
}

func TestCompute(t *testing.T) {
	tests := []struct {
		name string
		ws   windowstate.WindowState
		want string
	}{
		{"working", windowstate.WindowState{Instance: "a:1", State: "working"}, "● a:1"},
		{"blocked", windowstate.WindowState{Instance: "a:1", State: "blocked"}, "⏸ a:1"},
		{"idle", windowstate.WindowState{Instance: "a:1", State: "idle"}, "a:1"},
		{"no state", windowstate.WindowState{Instance: "a:1"}, "a:1"},
		{"done unseen", windowstate.WindowState{Instance: "a:1", State: "idle", Done: true}, "✓ a:1"},
		{"done seen", windowstate.WindowState{Instance: "a:1", State: "idle", Done: true, Seen: true}, "a:1"},
		{"working beats done", windowstate.WindowState{Instance: "a:1", State: "working", Done: true}, "● a:1"},
		{"fallback working glyph", windowstate.WindowState{RenderedName: "● b", State: "idle"}, "b"},
		{"fallback blocked glyph", windowstate.WindowState{RenderedName: "⏸ b", State: "idle"}, "b"},
		{"fallback done glyph", windowstate.WindowState{RenderedName: "✓ b", State: "working"}, "● b"},
		{"fallback bare", windowstate.WindowState{RenderedName: "b", State: "blocked"}, "⏸ b"},
		{"no base", windowstate.WindowState{State: "working"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Compute(tt.ws); got != tt.want {
				t.Errorf("Compute() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSyncName_FallsBackToCurrentTmuxName(t *testing.T) {
	for _, cur := range []string{"● x", "⏸ x", "✓ x", "x"} {
		f := install(t)
		f.current = cur
		ws := windowstate.WindowState{State: "working"}
		SyncName("@1", &ws)
		if len(f.renames) != 1 || f.renames[0] != "@1=● x" {
			t.Errorf("current %q: renames = %v, want [@1=● x]", cur, f.renames)
		}
	}
}

func TestUpdate_NoRenameOnNonEdge(t *testing.T) {
	f := install(t)
	if err := windowstate.Write("@1", windowstate.WindowState{Instance: "a", State: "working", RenderedName: "● a"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := Update("@1", func(ws *windowstate.WindowState) { ws.State = "working"; ws.PendingTool = "Bash" }); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.renames) != 0 {
		t.Errorf("renames = %v, want none", f.renames)
	}
}

func TestUpdate_RenameFailureRetriesNextWriter(t *testing.T) {
	f := install(t)
	f.fail = true
	set := func(ws *windowstate.WindowState) { ws.Instance = "a"; ws.State = "working" }
	if err := Update("@1", set); err != nil {
		t.Fatalf("a rename failure must not fail the writer: %v", err)
	}
	if ws, _ := windowstate.Read("@1"); ws.RenderedName != "" {
		t.Errorf("RenderedName = %q after failed rename, want empty", ws.RenderedName)
	}
	f.fail = false
	if err := Update("@1", set); err != nil {
		t.Fatal(err)
	}
	if len(f.renames) != 1 || f.renames[0] != "@1=● a" {
		t.Errorf("renames = %v, want retry to land", f.renames)
	}
}

func TestUpdate_RacingWritersEndConsistent(t *testing.T) {
	f := install(t)
	if err := windowstate.Write("@1", windowstate.WindowState{Instance: "a"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state := "working"
			if i%2 == 0 {
				state = "blocked"
			}
			_ = Update("@1", func(ws *windowstate.WindowState) { ws.State = state })
		}()
	}
	wg.Wait()
	ws, err := windowstate.Read("@1")
	if err != nil {
		t.Fatal(err)
	}
	if want := Compute(ws); ws.RenderedName != want {
		t.Errorf("RenderedName = %q, want %q", ws.RenderedName, want)
	}
	// The last rename the fake saw is the one persisted.
	if last := f.renames[len(f.renames)-1]; last != "@1="+ws.RenderedName {
		t.Errorf("last rename %q disagrees with RenderedName %q", last, ws.RenderedName)
	}
}

func TestApply_Seen(t *testing.T) {
	tests := []struct {
		name     string
		active   string
		start    windowstate.WindowState
		mutate   func(*windowstate.WindowState)
		wantSeen bool
		wantName string
	}{
		{
			name:     "done on background window is unseen",
			active:   "@9",
			start:    windowstate.WindowState{Instance: "a", State: "working", Seen: true},
			mutate:   func(ws *windowstate.WindowState) { ws.State = "idle"; windowstate.ClaimComplete(ws, false, ws.UpdatedAt) },
			wantSeen: false,
			wantName: "✓ a",
		},
		{
			name:     "done on active window is seen at once",
			active:   "@1",
			start:    windowstate.WindowState{Instance: "a", State: "working"},
			mutate:   func(ws *windowstate.WindowState) { ws.State = "idle"; windowstate.ClaimComplete(ws, false, ws.UpdatedAt) },
			wantSeen: true,
			wantName: "a",
		},
		{
			name:     "state change clears seen",
			active:   "@9",
			start:    windowstate.WindowState{Instance: "a", State: "idle", Seen: true},
			mutate:   func(ws *windowstate.WindowState) { ws.State = "working" },
			wantSeen: false,
			wantName: "● a",
		},
		{
			name:     "no edge keeps seen",
			active:   "@9",
			start:    windowstate.WindowState{Instance: "a", State: "idle", Done: true, Seen: true},
			mutate:   func(ws *windowstate.WindowState) { ws.LastText = "x" },
			wantSeen: true,
			wantName: "a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := install(t)
			f.active = tt.active
			tt.start.RenderedName = Compute(tt.start)
			if err := windowstate.Write("@1", tt.start); err != nil {
				t.Fatal(err)
			}
			if err := Update("@1", tt.mutate); err != nil {
				t.Fatal(err)
			}
			ws, _ := windowstate.Read("@1")
			if ws.Seen != tt.wantSeen {
				t.Errorf("Seen = %v, want %v", ws.Seen, tt.wantSeen)
			}
			if got := Compute(ws); got != tt.wantName {
				t.Errorf("name = %q, want %q", got, tt.wantName)
			}
			if ws.RenderedName != tt.wantName {
				t.Errorf("RenderedName = %q, want %q", ws.RenderedName, tt.wantName)
			}
		})
	}
}

func TestMarkSeen_ClearsCheckAndKeepsDone(t *testing.T) {
	f := install(t)
	if err := windowstate.Write("@1", windowstate.WindowState{Instance: "a", State: "idle", Done: true, RenderedName: "✓ a"}); err != nil {
		t.Fatal(err)
	}
	if err := MarkSeen("@1"); err != nil {
		t.Fatal(err)
	}
	ws, _ := windowstate.Read("@1")
	if !ws.Seen || !ws.Done {
		t.Errorf("Seen=%v Done=%v, want true/true (Done drives completion and sweep)", ws.Seen, ws.Done)
	}
	if len(f.renames) != 1 || f.renames[0] != "@1=a" {
		t.Errorf("renames = %v, want [@1=a]", f.renames)
	}
}

func TestMarkSeen_UntrackedAndGone(t *testing.T) {
	f := install(t)
	if err := MarkSeen("@7"); err != nil {
		t.Fatal(err)
	}
	if windowstate.Exists("@7") {
		t.Error("MarkSeen created a record for an untracked window")
	}
	if err := windowstate.Write("@8", windowstate.WindowState{Instance: "a"}); err != nil {
		t.Fatal(err)
	}
	windowstate.Remove("@8")
	if err := MarkSeen("@8"); err != nil {
		t.Errorf("gone window: %v", err)
	}
	if len(f.renames) != 0 {
		t.Errorf("renames = %v, want none", f.renames)
	}
}

func TestDoneUnseenSurvivesRestart(t *testing.T) {
	install(t)
	if err := Update("@1", func(ws *windowstate.WindowState) {
		ws.Instance = "a"
		ws.State = "idle"
		windowstate.ClaimComplete(ws, false, ws.UpdatedAt)
	}); err != nil {
		t.Fatal(err)
	}
	// A fresh process sees only the persisted record.
	ws, err := windowstate.Read("@1")
	if err != nil {
		t.Fatal(err)
	}
	if !ws.Done || ws.Seen {
		t.Fatalf("persisted Done=%v Seen=%v, want true/false", ws.Done, ws.Seen)
	}
	if got := Compute(ws); got != "✓ a" {
		t.Errorf("Compute = %q, want ✓ a", got)
	}
}

// TestUpdate_EmitsOneEventPerStateChange: the state writers emit exactly one
// metrics event per real State change, and none for a same-state write (a
// PreTool while already working).
func TestUpdate_EmitsOneEventPerStateChange(t *testing.T) {
	install(t)
	rt := filepath.Join(t.TempDir(), "proj", ".agentctl")
	oe, or := events.Enabled, events.RuntimeDir
	events.Enabled = func() bool { return true }
	events.RuntimeDir = func() string { return rt }
	t.Cleanup(func() { events.Enabled, events.RuntimeDir = oe, or })

	if err := windowstate.Write("@1", windowstate.WindowState{State: "idle", Instance: "go-engineer:1", AgentName: "go-engineer"}); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		state, tool string
		wantEvents  int // cumulative
	}{
		{"working", "Bash", 1}, // idle -> working
		{"working", "Read", 1}, // PreTool while already working: no event
		{"working", "Edit", 1},
		{"blocked", "Edit", 2}, // working -> blocked
		{"blocked", "Edit", 2},
		{"working", "Edit", 3}, // blocked -> working
		{"idle", "", 4},        // working -> idle
		{"idle", "", 4},
	}
	for i, st := range steps {
		if err := Update("@1", func(ws *windowstate.WindowState) {
			ws.State = st.state
			ws.PendingTool = st.tool
		}); err != nil {
			t.Fatal(err)
		}
		files := events.Files(rt)
		n := 0
		for _, f := range files {
			b, _ := os.ReadFile(f)
			n += strings.Count(string(b), "\n")
		}
		if n != st.wantEvents {
			t.Fatalf("step %d (%s): %d events, want %d", i, st.state, n, st.wantEvents)
		}
	}
}
