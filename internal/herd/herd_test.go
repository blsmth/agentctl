package herd

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/registry"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
)

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

// TestMergeSessionsThenLoadGroupMissingStateJSON verifies the end-to-end
// path a session with no monitor ever having run (or one whose monitor is
// too old to have registered) takes: MergeSessions still surfaces it purely
// from tmux, and LoadGroup renders it as a degraded (Stale) group rather
// than silently dropping it.
func TestMergeSessionsThenLoadGroupMissingStateJSON(t *testing.T) {
	dir := t.TempDir() // no .agentctl/state.json ever written
	sessions := []tmux.SessionInfo{{Name: "agentctl-missing", Path: dir}}

	merged := MergeSessions(sessions, nil)
	if len(merged) != 1 {
		t.Fatalf("MergeSessions: got %d entries, want 1", len(merged))
	}

	g := LoadGroup(merged[0])
	if !g.Stale {
		t.Error("LoadGroup for a session with no state.json should produce a Stale group")
	}
	if g.Session != "agentctl-missing" {
		t.Errorf("Session = %q, want %q", g.Session, "agentctl-missing")
	}
}

// ── TagRows ──────────────────────────────────────────────────────────────────

func TestTagRows(t *testing.T) {
	t.Run("nil snapshot returns nil", func(t *testing.T) {
		got := TagRows("agentctl-x", "x", nil)
		if got != nil {
			t.Errorf("TagRows(nil snap) = %v, want nil", got)
		}
	})

	t.Run("tags every agent row with session and project", func(t *testing.T) {
		snap := &state.Snapshot{State: &state.State{Agents: []state.AgentState{
			{Name: "go-engineer", State: "working"},
			{Name: "python-engineer", State: "idle"},
		}}}
		got := TagRows("agentctl-acme", "acme", snap)
		if len(got) != 2 {
			t.Fatalf("TagRows: got %d rows, want 2", len(got))
		}
		for _, r := range got {
			if r.Session != "agentctl-acme" || r.Project != "acme" {
				t.Errorf("row %+v not tagged with session/project", r)
			}
		}
	})

	t.Run("skips shell and improve~ windows", func(t *testing.T) {
		snap := &state.Snapshot{State: &state.State{Agents: []state.AgentState{
			{Name: "shell", State: "idle"},
			{Name: "improve~go-engineer", State: "working"},
			{Name: "go-engineer", State: "idle"},
		}}}
		got := TagRows("agentctl-acme", "acme", snap)
		if len(got) != 1 {
			t.Fatalf("TagRows: got %d rows, want 1 (shell/improve~ skipped): %+v", len(got), got)
		}
		if got[0].Name != "go-engineer" {
			t.Errorf("surviving row = %q, want %q", got[0].Name, "go-engineer")
		}
	})

	t.Run("zero agents produces empty, not nil-panicking, slice", func(t *testing.T) {
		snap := &state.Snapshot{State: &state.State{Agents: nil}}
		got := TagRows("agentctl-empty", "empty", snap)
		if len(got) != 0 {
			t.Errorf("TagRows(empty snap) = %v, want empty", got)
		}
	})
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
			name: "blocked floats above everything",
			rows: []Row{
				row("idle-agent", "idle", false, 0),
				row("working-agent", "working", false, 1),
				row("blocked-agent", "blocked", false, 2),
				row("done-agent", "idle", true, 3),
			},
			wantOrder: []string{"blocked-agent", "done-agent", "working-agent", "idle-agent"},
		},
		{
			name: "done-unseen floats above working",
			rows: []Row{
				row("working-agent", "working", false, 0),
				row("done-agent", "idle", true, 1),
			},
			wantOrder: []string{"done-agent", "working-agent"},
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
			name: "tie-break within same rank (blocked)",
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
			name: "blocked group floats to top",
			groups: []Group{
				{Session: "agentctl-idle", Rollup: RollupIdle},
				{Session: "agentctl-working", Rollup: RollupWorking},
				{Session: "agentctl-blocked", Rollup: RollupBlocked},
			},
			want: []string{"agentctl-blocked", "agentctl-working", "agentctl-idle"},
		},
		{
			name: "tie-break by session name",
			groups: []Group{
				{Session: "agentctl-zebra", Rollup: RollupWorking},
				{Session: "agentctl-apple", Rollup: RollupWorking},
			},
			want: []string{"agentctl-apple", "agentctl-zebra"},
		},
		{
			name: "full precedence ordering",
			groups: []Group{
				{Session: "agentctl-idle", Rollup: RollupIdle},
				{Session: "agentctl-done", Rollup: RollupDone},
				{Session: "agentctl-blocked", Rollup: RollupBlocked},
				{Session: "agentctl-working", Rollup: RollupWorking},
			},
			want: []string{"agentctl-blocked", "agentctl-working", "agentctl-done", "agentctl-idle"},
		},
		{
			name: "stale group with zero agents sorts by its idle rollup",
			groups: []Group{
				{Session: "agentctl-stale", Rollup: RollupIdle, Stale: true},
				{Session: "agentctl-blocked", Rollup: RollupBlocked},
			},
			want: []string{"agentctl-blocked", "agentctl-stale"},
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

// ── LoadGroup: missing/stale/degraded snapshot handling ─────────────────────

func TestLoadGroupMissingSnapshot(t *testing.T) {
	dir := t.TempDir() // no state.json written
	e := registry.Entry{
		SessionName: "agentctl-missing",
		ProjectDir:  filepath.Join(dir, "missing-project"),
		RuntimeDir:  dir,
	}

	got := LoadGroup(e)

	if !got.Stale {
		t.Error("LoadGroup with no state.json should produce a Stale group")
	}
	if len(got.Rows) != 0 {
		t.Errorf("LoadGroup with no state.json should have zero rows, got %d", len(got.Rows))
	}
	if got.Session != e.SessionName {
		t.Errorf("Session = %q, want %q", got.Session, e.SessionName)
	}
}

// TestLoadGroupStaleSnapshot is the regression test for the "stale groups
// render with zero rows" bug: a snapshot that exists but has gone stale
// (monitor briefly wedged) must still surface its last-known rows, flagged
// Stale, rather than making the whole project's agents vanish from the hub.
func TestLoadGroupStaleSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, state.State{
		Agents:    []state.AgentState{{Name: "go-engineer", State: "working"}},
		Timestamp: time.Now().Add(-1 * time.Hour), // far older than SnapshotStaleAfter
	})

	e := registry.Entry{SessionName: "agentctl-stale", ProjectDir: "/tmp/stale-project", RuntimeDir: dir}
	got := LoadGroup(e)

	if !got.Stale {
		t.Error("LoadGroup with an old timestamp should produce a Stale group")
	}
	if len(got.Rows) != 1 {
		t.Fatalf("stale group should keep its last-known rows, got %d", len(got.Rows))
	}
	if got.Rows[0].Name != "go-engineer" {
		t.Errorf("stale group row = %q, want %q", got.Rows[0].Name, "go-engineer")
	}
}

func TestLoadGroupFreshSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, state.State{
		Agents: []state.AgentState{
			{Name: "go-engineer", State: "working", WindowIndex: 0},
			{Name: "python-engineer", State: "blocked", WindowIndex: 1},
		},
		Timestamp: time.Now(),
	})

	e := registry.Entry{SessionName: "agentctl-fresh", ProjectDir: "/tmp/myproject", RuntimeDir: dir}
	got := LoadGroup(e)

	if got.Stale {
		t.Error("LoadGroup with a fresh snapshot should not be Stale")
	}
	if got.Project != "myproject" {
		t.Errorf("Project = %q, want %q", got.Project, "myproject")
	}
	if got.Rollup != RollupBlocked {
		t.Errorf("Rollup = %v, want %v (blocked agent present)", got.Rollup, RollupBlocked)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(got.Rows))
	}
	// Blocked row must sort first.
	if got.Rows[0].Name != "python-engineer" {
		t.Errorf("first row = %q, want %q (blocked floats to top)", got.Rows[0].Name, "python-engineer")
	}
}

// writeStateFile marshals s to <dir>/state.json using the package's own
// atomic writer, via state.SetStatePathForTest + state.Save. Restores the
// package-global path afterwards so other tests aren't affected.
func writeStateFile(t *testing.T, dir string, s state.State) {
	t.Helper()
	path := filepath.Join(dir, "state.json")
	prev := state.FilePath()
	state.SetStatePathForTest(path)
	defer state.SetStatePathForTest(prev)
	if err := state.Save(&s); err != nil {
		t.Fatalf("writeStateFile: %v", err)
	}
}
