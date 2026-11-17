package herd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/registry"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
)

// ── MergeSessions: tmux-primary, registry-enrichment discovery ─────────────

func TestMergeSessions(t *testing.T) {
	tests := []struct {
		name     string
		sessions []tmux.SessionInfo
		entries  []registry.Entry
		want     []registry.Entry
	}{
		{
			name:     "empty tmux and empty registry yields nothing",
			sessions: nil,
			entries:  nil,
			want:     nil,
		},
		{
			name:     "tmux-only session (no registry entry) is included",
			sessions: []tmux.SessionInfo{{Name: "agentctl-foo", Path: "/home/x/foo"}},
			entries:  nil,
			want: []registry.Entry{
				{SessionName: "agentctl-foo", ProjectDir: "/home/x/foo", RuntimeDir: "/home/x/foo/.agentctl", Host: "local"},
			},
		},
		{
			name:     "registry entry enriches matching tmux session, RuntimeDir wins",
			sessions: []tmux.SessionInfo{{Name: "agentctl-foo", Path: "/home/x/foo"}},
			entries: []registry.Entry{
				{
					SessionName: "agentctl-foo",
					ProjectDir:  "/home/x/foo",
					RuntimeDir:  "/custom/runtime/dir",
					Host:        "local",
					PID:         4242,
					StartedAt:   1000,
				},
			},
			want: []registry.Entry{
				{
					SessionName: "agentctl-foo",
					ProjectDir:  "/home/x/foo",
					RuntimeDir:  "/custom/runtime/dir",
					Host:        "local",
					PID:         4242,
					StartedAt:   1000,
				},
			},
		},
		{
			name:     "local registry entry with no tmux session is dropped",
			sessions: nil,
			entries: []registry.Entry{
				{SessionName: "agentctl-gone", ProjectDir: "/home/x/gone", RuntimeDir: "/home/x/gone/.agentctl", Host: "local"},
			},
			want: nil,
		},
		{
			name:     "non-local (remote) registry entry with no tmux session is kept",
			sessions: nil,
			entries: []registry.Entry{
				{SessionName: "agentctl-remote", ProjectDir: "/remote/proj", RuntimeDir: "/remote/proj/.agentctl", Host: "other-box"},
			},
			want: []registry.Entry{
				{SessionName: "agentctl-remote", ProjectDir: "/remote/proj", RuntimeDir: "/remote/proj/.agentctl", Host: "other-box"},
			},
		},
		{
			name:     "the hub session is excluded",
			sessions: []tmux.SessionInfo{{Name: tmux.HubSessionName, Path: "/home/x/.config/agentctl/hub"}},
			entries:  nil,
			want:     nil,
		},
		{
			name: "hub session excluded even alongside a real session",
			sessions: []tmux.SessionInfo{
				{Name: tmux.HubSessionName, Path: "/home/x/.config/agentctl/hub"},
				{Name: "agentctl-real", Path: "/home/x/real"},
			},
			entries: nil,
			want: []registry.Entry{
				{SessionName: "agentctl-real", ProjectDir: "/home/x/real", RuntimeDir: "/home/x/real/.agentctl", Host: "local"},
			},
		},
		{
			name: "hub and phone viewer sessions are excluded",
			sessions: []tmux.SessionInfo{
				{Name: "agentctl-hubview", Path: "/home/x/real"},
				{Name: "agentctl-mobileview-0a1b2c", Path: "/home/x/real"},
				{Name: "agentctl-real", Path: "/home/x/real"},
			},
			entries: nil,
			want: []registry.Entry{
				{SessionName: "agentctl-real", ProjectDir: "/home/x/real", RuntimeDir: "/home/x/real/.agentctl", Host: "local"},
			},
		},
		{
			name:     "non-agentctl session names are ignored",
			sessions: []tmux.SessionInfo{{Name: "unrelated-session", Path: "/home/x/whatever"}},
			entries:  nil,
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeSessions(tt.sessions, tt.entries)
			if len(got) != len(tt.want) {
				t.Fatalf("MergeSessions() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("entry %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// fakeWindows replaces the tmux list seam for one test.
func fakeWindows(t *testing.T, wins []tmux.WindowInfo, err error) {
	t.Helper()
	prev := listWindowsFn
	listWindowsFn = func(string) ([]tmux.WindowInfo, error) { return wins, err }
	t.Cleanup(func() { listWindowsFn = prev })
}

// TestMergeSessionsThenLoadGroupNoRecords: a session with no state.json and
// no windowstate records still loads; its windows are listed as untracked.
func TestMergeSessionsThenLoadGroupNoRecords(t *testing.T) {
	dir := t.TempDir()
	sessions := []tmux.SessionInfo{{Name: "agentctl-missing", Path: dir}}
	fakeWindows(t, []tmux.WindowInfo{{ID: "@1", Name: "shell"}, {ID: "@2", Name: "go-engineer"}}, nil)

	merged := MergeSessions(sessions, nil)
	if len(merged) != 1 {
		t.Fatalf("MergeSessions: got %d entries, want 1", len(merged))
	}

	g := LoadGroup(merged[0])
	if g.Session != "agentctl-missing" {
		t.Errorf("Session = %q, want %q", g.Session, "agentctl-missing")
	}
	if len(g.Rows) != 1 || g.Rows[0].Name != "go-engineer" || g.Rows[0].State != "idle" {
		t.Errorf("rows = %+v, want one untracked idle go-engineer", g.Rows)
	}
}

// row is a small helper for building test Rows tersely.
func row(name, agentState string, done bool, windowIndex int) Row {
	return Row{
		Session: "agentctl-test",
		Project: "test",
		AgentState: state.AgentState{
			Name:        name,
			State:       agentState,
			Done:        done,
			WindowIndex: windowIndex,
		},
	}
}

// ── SortRows: within-group ordering + tie-break ─────────────────────────────

func TestSortRows(t *testing.T) {
	tests := []struct {
		name      string
		rows      []Row
		wantOrder []string // expected Name order
	}{
		{
			name:      "empty input",
			rows:      nil,
			wantOrder: nil,
		},
		{
			name:      "single row",
			rows:      []Row{row("a", "idle", false, 0)},
			wantOrder: []string{"a"},
		},
		{
			name: "state does not reorder rows; WindowIndex only",
			rows: []Row{
				row("idle-agent", "idle", false, 0),
				row("working-agent", "working", false, 1),
				row("blocked-agent", "blocked", false, 2),
				row("done-agent", "idle", true, 3),
			},
			wantOrder: []string{"idle-agent", "working-agent", "blocked-agent", "done-agent"},
		},
		{
			name: "done-unseen does not float above working",
			rows: []Row{
				row("done-agent", "idle", true, 1),
				row("working-agent", "working", false, 0),
			},
			wantOrder: []string{"working-agent", "done-agent"},
		},
		{
			name: "tie-break by WindowIndex when states equal",
			rows: []Row{
				row("second", "idle", false, 2),
				row("first", "idle", false, 0),
				row("third", "idle", false, 5),
			},
			wantOrder: []string{"first", "second", "third"},
		},
		{
			name: "blocked rows ordered by WindowIndex",
			rows: []Row{
				row("blocked-2", "blocked", false, 2),
				row("blocked-1", "blocked", false, 1),
			},
			wantOrder: []string{"blocked-1", "blocked-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SortRows(tt.rows)
			if len(got) != len(tt.wantOrder) {
				t.Fatalf("SortRows: got %d rows, want %d", len(got), len(tt.wantOrder))
			}
			for i, name := range tt.wantOrder {
				if got[i].Name != name {
					t.Errorf("position %d: got %q, want %q (full order: %v)", i, got[i].Name, name, namesOf(got))
				}
			}
		})
	}
}

// TestSortRowsDoesNotMutateInput verifies SortRows returns a new slice and
// leaves the caller's slice order untouched, which matters for the "selection
// survives a refresh" requirement upstream in the TUI (re-resolving by
// session+windowID rather than index depends on stable underlying data).
func TestSortRowsDoesNotMutateInput(t *testing.T) {
	rows := []Row{
		row("b", "blocked", false, 1),
		row("a", "idle", false, 0),
	}
	original := append([]Row(nil), rows...)

	_ = SortRows(rows)

	for i := range rows {
		if rows[i].Name != original[i].Name {
			t.Errorf("input slice was mutated: got %v, want %v", namesOf(rows), namesOf(original))
		}
	}
}

func namesOf(rows []Row) []string {
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	return names
}

// ── RollupRows: group rollup precedence ─────────────────────────────────────

func TestRollupRows(t *testing.T) {
	tests := []struct {
		name string
		rows []Row
		want RollupStatus
	}{
		{"empty group rolls up idle", nil, RollupIdle},
		{
			name: "single idle",
			rows: []Row{row("a", "idle", false, 0)},
			want: RollupIdle,
		},
		{
			name: "single working",
			rows: []Row{row("a", "working", false, 0)},
			want: RollupWorking,
		},
		{
			name: "single done-unseen",
			rows: []Row{row("a", "idle", true, 0)},
			want: RollupDone,
		},
		{
			name: "single blocked",
			rows: []Row{row("a", "blocked", false, 0)},
			want: RollupBlocked,
		},
		{
			name: "blocked beats working",
			rows: []Row{row("a", "working", false, 0), row("b", "blocked", false, 1)},
			want: RollupBlocked,
		},
		{
			name: "blocked beats done-unseen",
			rows: []Row{row("a", "idle", true, 0), row("b", "blocked", false, 1)},
			want: RollupBlocked,
		},
		{
			name: "blocked beats idle",
			rows: []Row{row("a", "idle", false, 0), row("b", "blocked", false, 1)},
			want: RollupBlocked,
		},
		{
			name: "working beats done-unseen (group rollup differs from row order)",
			rows: []Row{row("a", "idle", true, 0), row("b", "working", false, 1)},
			want: RollupWorking,
		},
		{
			name: "working beats idle",
			rows: []Row{row("a", "idle", false, 0), row("b", "working", false, 1)},
			want: RollupWorking,
		},
		{
			name: "done-unseen beats idle",
			rows: []Row{row("a", "idle", false, 0), row("b", "idle", true, 1)},
			want: RollupDone,
		},
		{
			name: "all idle rolls up idle",
			rows: []Row{row("a", "idle", false, 0), row("b", "idle", false, 1)},
			want: RollupIdle,
		},
		{
			name: "every status present — blocked wins",
			rows: []Row{
				row("blocked", "blocked", false, 0),
				row("working", "working", false, 1),
				row("done", "idle", true, 2),
				row("idle", "idle", false, 3),
			},
			want: RollupBlocked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RollupRows(tt.rows)
			if got != tt.want {
				t.Errorf("RollupRows(%v) = %v, want %v", namesOf(tt.rows), got, tt.want)
			}
		})
	}
}

// ── OrderGroups ──────────────────────────────────────────────────────────────

func TestOrderGroups(t *testing.T) {
	tests := []struct {
		name   string
		groups []Group
		want   []string // expected Session order
	}{
		{"empty input", nil, nil},
		{
			name:   "single group",
			groups: []Group{{Session: "agentctl-a", Rollup: RollupIdle}},
			want:   []string{"agentctl-a"},
		},
		{
			name: "rollup does not float blocked groups",
			groups: []Group{
				{Session: "agentctl-c", Project: "c", Rollup: RollupBlocked},
				{Session: "agentctl-a", Project: "a", Rollup: RollupIdle},
				{Session: "agentctl-b", Project: "b", Rollup: RollupWorking},
			},
			want: []string{"agentctl-a", "agentctl-b", "agentctl-c"},
		},
		{
			name: "case-insensitive project name",
			groups: []Group{
				{Session: "agentctl-z", Project: "Zed"},
				{Session: "agentctl-b", Project: "banana"},
				{Session: "agentctl-a", Project: "Apple"},
			},
			want: []string{"agentctl-a", "agentctl-b", "agentctl-z"},
		},
		{
			name: "tie on project broken by session",
			groups: []Group{
				{Session: "agentctl-2", Project: "same"},
				{Session: "agentctl-1", Project: "Same"},
			},
			want: []string{"agentctl-1", "agentctl-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OrderGroups(tt.groups)
			if len(got) != len(tt.want) {
				t.Fatalf("OrderGroups: got %d groups, want %d", len(got), len(tt.want))
			}
			for i, sess := range tt.want {
				if got[i].Session != sess {
					t.Errorf("position %d: got %q, want %q", i, got[i].Session, sess)
				}
			}
		})
	}
}

func TestOrderGroupsDoesNotMutateInput(t *testing.T) {
	groups := []Group{
		{Session: "agentctl-b", Rollup: RollupWorking},
		{Session: "agentctl-a", Rollup: RollupBlocked},
	}
	original := append([]Group(nil), groups...)

	_ = OrderGroups(groups)

	for i := range groups {
		if groups[i].Session != original[i].Session {
			t.Errorf("input slice was mutated: got %+v, want %+v", groups, original)
		}
	}
}

// ── ActivityLine ─────────────────────────────────────────────────────────────

func TestActivityLine(t *testing.T) {
	tests := []struct {
		name string
		row  Row
		want string
	}{
		{
			name: "prefers tool detail",
			row: Row{AgentState: state.AgentState{
				LastToolDetail: "monitor.go",
				LastText:       "some assistant text",
			}},
			want: "monitor.go",
		},
		{
			name: "falls back to last text when no tool detail",
			row: Row{AgentState: state.AgentState{
				LastText: "some assistant text",
			}},
			want: "some assistant text",
		},
		{
			name: "empty when neither is set",
			row:  Row{},
			want: "",
		},
		{
			// Regression: state.json snapshots written by older monitors (or
			// by other projects' binaries this hub reads but doesn't
			// control) may contain un-flattened multi-line text. ActivityLine
			// must defensively flatten it even though logstream now flattens
			// at the source.
			name: "flattens multi-line last text",
			row: Row{AgentState: state.AgentState{
				LastText: "Done. Summary:\n\n1. **Task 1** — did the thing",
			}},
			want: "Done. Summary: 1. **Task 1** — did the thing",
		},
		{
			name: "flattens multi-line tool detail, still prefers it over last text",
			row: Row{AgentState: state.AgentState{
				LastToolDetail: "echo one\necho two",
				LastText:       "some assistant text",
			}},
			want: "echo one echo two",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ActivityLine(tt.row)
			if got != tt.want {
				t.Errorf("ActivityLine(%+v) = %q, want %q", tt.row, got, tt.want)
			}
		})
	}
}

// ── FirstBlocked ─────────────────────────────────────────────────────────────

func TestFirstBlocked(t *testing.T) {
	t.Run("no groups", func(t *testing.T) {
		if got := FirstBlocked(nil); got != nil {
			t.Errorf("FirstBlocked(nil) = %v, want nil", got)
		}
	})

	t.Run("no blocked agents anywhere", func(t *testing.T) {
		groups := []Group{
			{Session: "agentctl-a", Rows: []Row{row("a1", "working", false, 0)}},
			{Session: "agentctl-b", Rows: []Row{row("b1", "idle", false, 0)}},
		}
		if got := FirstBlocked(groups); got != nil {
			t.Errorf("FirstBlocked = %v, want nil", got)
		}
	})

	t.Run("finds the blocked row in the first group that has one", func(t *testing.T) {
		groups := []Group{
			{Session: "agentctl-a", Rows: []Row{row("a1", "working", false, 0)}},
			{Session: "agentctl-b", Rows: []Row{
				row("b1", "idle", false, 0),
				row("b2", "blocked", false, 1),
			}},
		}
		got := FirstBlocked(groups)
		if got == nil {
			t.Fatal("FirstBlocked = nil, want b2")
		}
		if got.Name != "b2" {
			t.Errorf("FirstBlocked = %q, want %q", got.Name, "b2")
		}
	})

	t.Run("respects group order — earlier group wins even with multiple blocked", func(t *testing.T) {
		groups := []Group{
			{Session: "agentctl-first", Rows: []Row{row("first-blocked", "blocked", false, 0)}},
			{Session: "agentctl-second", Rows: []Row{row("second-blocked", "blocked", false, 0)}},
		}
		got := FirstBlocked(groups)
		if got == nil || got.Name != "first-blocked" {
			t.Errorf("FirstBlocked = %v, want first-blocked", got)
		}
	})
}

// ── TagAgents ────────────────────────────────────────────────────────────────

func TestTagAgents(t *testing.T) {
	t.Run("empty input yields empty rows", func(t *testing.T) {
		if got := TagAgents("agentctl-x", "x", nil); len(got) != 0 {
			t.Errorf("TagAgents(nil) = %v, want empty", got)
		}
	})
	t.Run("tags session and project and orders by window index", func(t *testing.T) {
		got := TagAgents("agentctl-acme", "acme", []state.AgentState{
			{Name: "b", WindowIndex: 2}, {Name: "a", WindowIndex: 1},
		})
		if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
			t.Fatalf("rows = %+v, want a then b", got)
		}
		for _, r := range got {
			if r.Session != "agentctl-acme" || r.Project != "acme" {
				t.Errorf("row %+v not tagged", r)
			}
		}
	})
}

// ── LoadGroup ───────────────────────────────────────────────────────────────

// writeWS writes a windowstate record file into <runtimeDir>/windows.
func writeWS(t *testing.T, runtimeDir, id, body string) {
	t.Helper()
	winDir := filepath.Join(runtimeDir, "windows")
	if err := os.MkdirAll(winDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(winDir, id+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadGroupListFailureIsEmpty(t *testing.T) {
	fakeWindows(t, nil, errors.New("no server"))
	e := registry.Entry{SessionName: "agentctl-gone", ProjectDir: "/tmp/gone", RuntimeDir: t.TempDir()}
	got := LoadGroup(e)
	if len(got.Rows) != 0 || got.Rollup != RollupIdle {
		t.Errorf("got %+v, want empty idle group", got)
	}
	if got.Session != e.SessionName {
		t.Errorf("Session = %q", got.Session)
	}
}

// TestLoadGroupNoStateJSON: rows, rollup and order come from tmux and
// windowstate; no state.json exists anywhere.
func TestLoadGroupNoStateJSON(t *testing.T) {
	dir := t.TempDir()
	writeWS(t, dir, "@3", `{"state":"blocked","instance":"python-engineer"}`)
	writeWS(t, dir, "@2", `{"state":"working","instance":"go-engineer"}`)
	writeWS(t, dir, "@9", `{"state":"working"}`) // no such window: ignored
	fakeWindows(t, []tmux.WindowInfo{
		{ID: "@1", Name: "shell"}, {ID: "@3", Name: "⏸ python-engineer"}, {ID: "@2", Name: "● go-engineer"},
	}, nil)

	got := LoadGroup(registry.Entry{SessionName: "agentctl-fresh", ProjectDir: "/tmp/myproject", RuntimeDir: dir})
	if got.Project != "myproject" {
		t.Errorf("Project = %q", got.Project)
	}
	if got.Rollup != RollupBlocked {
		t.Errorf("Rollup = %v, want blocked", got.Rollup)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got.Rows), got.Rows)
	}
	// tmux order (list order), not file order.
	if got.Rows[0].Name != "python-engineer" || got.Rows[1].Name != "go-engineer" {
		t.Errorf("order = %q, %q; want tmux order", got.Rows[0].Name, got.Rows[1].Name)
	}
	if blocked := FirstBlocked([]Group{got}); blocked == nil || blocked.WindowID != "@3" {
		t.Errorf("FirstBlocked = %v, want @3 from windowstate only", blocked)
	}
}

func TestLoadGroupTagsDispatched(t *testing.T) {
	dir := t.TempDir()
	winDir := filepath.Join(dir, "windows")
	if err := os.MkdirAll(winDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modes := map[string]string{"@1": "dispatch", "@2": "adhoc", "@3": "custom", "@4": "bare"}
	for id, mode := range modes {
		data := []byte(`{"mode":"` + mode + `"}`)
		if err := os.WriteFile(filepath.Join(winDir, id+".json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var wins []tmux.WindowInfo
	for _, id := range []string{"@1", "@2", "@3", "@4", "@5"} { // @5 has no record
		wins = append(wins, tmux.WindowInfo{ID: id, Name: "a" + id})
	}
	fakeWindows(t, wins, nil)

	got := LoadGroup(registry.Entry{SessionName: "agentctl-x", ProjectDir: "/tmp/x", RuntimeDir: dir})
	want := map[string]bool{"@1": true, "@2": true, "@3": true, "@4": false, "@5": false}
	if len(got.Rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got.Rows), len(want))
	}
	for _, r := range got.Rows {
		if r.Dispatched != want[r.WindowID] {
			t.Errorf("%s Dispatched = %v, want %v", r.WindowID, r.Dispatched, want[r.WindowID])
		}
	}
}

func TestTagMetaSetsParentOnlyWhenTrusted(t *testing.T) {
	rows := []Row{{}, {}, {}}
	rows[0].WindowID, rows[1].WindowID, rows[2].WindowID = "@1", "@2", "@3"
	meta := map[string]Meta{
		"@1": {Mode: "dispatch", SpawnedBy: "@9", SpawnedBySession: "sess"},
		"@2": {Mode: "bare", SpawnedBy: "@9"}, // untrusted: env leak
	}
	TagMeta(rows, meta)
	want := []string{"@9", "", ""}
	for i, r := range rows {
		if r.Parent != want[i] {
			t.Errorf("%s Parent = %q, want %q", r.WindowID, r.Parent, want[i])
		}
	}
}

// TestLoadGroupNestedParentFirstBlocked: rows built from windowstate carry the
// trusted dispatcher as Parent (what the hub's tree view nests on), and
// FirstBlocked finds a blocked child.
func TestLoadGroupNestedParentFirstBlocked(t *testing.T) {
	dir := t.TempDir()
	writeWS(t, dir, "@2", `{"state":"working","instance":"lead","mode":"adhoc"}`)
	writeWS(t, dir, "@3", `{"state":"blocked","instance":"worker","mode":"dispatch","spawned_by":"@2","spawned_by_session":"s"}`)
	fakeWindows(t, []tmux.WindowInfo{{ID: "@2", Name: "lead"}, {ID: "@3", Name: "worker"}}, nil)

	g := LoadGroup(registry.Entry{SessionName: "agentctl-n", ProjectDir: "/tmp/n", RuntimeDir: dir})
	if len(g.Rows) != 2 {
		t.Fatalf("rows = %+v", g.Rows)
	}
	if g.Rows[0].Parent != "" || g.Rows[1].Parent != "@2" || !g.Rows[1].Dispatched {
		t.Errorf("parent/dispatched = %q/%q/%v, want ''/@2/true", g.Rows[0].Parent, g.Rows[1].Parent, g.Rows[1].Dispatched)
	}
	if b := FirstBlocked([]Group{g}); b == nil || b.WindowID != "@3" {
		t.Errorf("FirstBlocked = %v, want nested child @3", b)
	}
}
