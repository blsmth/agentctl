package herd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/registry"
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

func TestTagDispatchedSetsParentOnlyWhenTrusted(t *testing.T) {
	rows := []Row{{}, {}, {}}
	rows[0].WindowID, rows[1].WindowID, rows[2].WindowID = "@1", "@2", "@3"
	meta := map[string]Meta{
		"@1": {Mode: "dispatch", SpawnedBy: "@9", SpawnedBySession: "sess"},
		"@2": {Mode: "bare", SpawnedBy: "@9"}, // untrusted: env leak
	}
	TagDispatched(rows, meta)
	want := []string{"@9", "", ""}
	for i, r := range rows {
		if r.Parent != want[i] {
			t.Errorf("%s Parent = %q, want %q", r.WindowID, r.Parent, want[i])
		}
	}
}
