package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func names(a []AgentState) []string {
	out := make([]string, len(a))
	for i, x := range a {
		out[i] = x.Name
	}
	return out
}

func TestBuildRestoreAgents(t *testing.T) {
	rec := func(inst, agent string, idx int) windowstate.WindowState {
		return windowstate.WindowState{Instance: inst, AgentName: agent, WindowIndex: idx, WorkDir: "/w"}
	}
	tests := []struct {
		name    string
		records map[string]windowstate.WindowState
		live    map[string]LiveWindow
		want    []string
	}{
		{
			name: "skips shell hub utility improve and empty",
			records: map[string]windowstate.WindowState{
				"@1": rec("shell", "shell", 0),
				"@2": rec("hub", "hub", 1),
				"@3": rec("tickets", "tickets", 2),
				"@4": rec("improve~go-engineer", "go-engineer", 3),
				"@5": {WorkDir: "/w"},
				"@6": rec("go-engineer", "go-engineer", 4),
			},
			want: []string{"go-engineer"},
		},
		{
			name: "crash: no live windows, order by recorded index",
			records: map[string]windowstate.WindowState{
				"@9": rec("c", "c", 3), "@3": rec("a", "a", 1), "@5": rec("b", "b", 2),
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "unknown index ties break by window id numerically",
			records: map[string]windowstate.WindowState{
				"@10": rec("b", "b", 0), "@2": rec("a", "a", 0),
			},
			want: []string{"a", "b"},
		},
		{
			name: "live index wins over stale record",
			records: map[string]windowstate.WindowState{
				"@1": rec("a", "a", 1), "@2": rec("b", "b", 2),
			},
			live: map[string]LiveWindow{"@1": {Index: 5}, "@2": {Index: 2}},
			want: []string{"b", "a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := names(BuildRestoreAgents(tt.records, tt.live))
			if len(got) != len(tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v want %v", got, tt.want)
				}
			}
		})
	}
}

func TestBuildRestoreAgentsFields(t *testing.T) {
	got := BuildRestoreAgents(map[string]windowstate.WindowState{
		"@1": {Instance: "a:2", AgentName: "a", LogPath: "/p/sess-1.jsonl", Backend: "nous", Account: "personal", SessionName: "agent-a-issue-1"},
		"@2": {Instance: "b", AgentName: "b", WorkDir: "/rec"},
	}, map[string]LiveWindow{"@1": {Index: 1, Path: "/pane"}, "@2": {Index: 2, Path: "/pane"}})
	if got[0].SessionID != "sess-1" || got[0].WorkDir != "/pane" || got[0].Backend != "nous" || got[0].Account != "personal" || got[0].SessionName != "agent-a-issue-1" || got[0].WindowID != "@1" {
		t.Errorf("entry 0 = %+v", got[0])
	}
	if got[1].WorkDir != "/rec" || got[1].SessionID != "" {
		t.Errorf("entry 1 = %+v (no LogPath must yield no session id)", got[1])
	}
}

func TestBuildRestoreAgentsExcludesTombstoned(t *testing.T) {
	windowstate.SetDirForTest(t.TempDir())
	for _, id := range []string{"@1", "@2"} {
		if err := windowstate.Update(id, func(ws *windowstate.WindowState) { ws.Instance = "x" + id; ws.AgentName = "x" }); err != nil {
			t.Fatal(err)
		}
	}
	windowstate.Remove("@1")
	got := BuildRestoreAgents(windowstate.All(), nil)
	if len(got) != 1 || got[0].WindowID != "@2" {
		t.Errorf("got %+v, want only @2", got)
	}
}

func TestRestoreFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	SetStatePathForTest(filepath.Join(dir, "state.json"))
	if s, err := LoadRestore(); err != nil || s != nil {
		t.Fatalf("LoadRestore() missing = %v, %v", s, err)
	}
	if err := SaveRestoreAgents(dir, []AgentState{{Name: "a", AgentName: "a"}}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, RestoreFileName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", fi, err)
	}
	s, err := LoadRestore()
	if err != nil || s == nil || len(s.Agents) != 1 || time.Since(s.Timestamp) > time.Minute {
		t.Fatalf("LoadRestore() = %+v, %v", s, err)
	}
	if err := ClearRestore(); err != nil {
		t.Fatal(err)
	}
	if err := ClearRestore(); err != nil {
		t.Fatalf("second ClearRestore: %v", err)
	}
	if s, _ := LoadRestore(); s != nil {
		t.Error("restore.json still present")
	}
}

func TestBuildRestoreAgentsCopiesLaunchMeta(t *testing.T) {
	recs := map[string]windowstate.WindowState{"@1": {
		Instance: "go-engineer", AgentName: "go-engineer", WorkDir: "/w",
		Mode: "adhoc", Ticket: "42", TicketTitle: "T", Task: "do x", FirstPrompt: "first", Branch: "agent/x",
		WorktreeDir: "/w/.worktrees/x", SpawnedBy: "@9", SpawnedBySession: "sess",
	}}
	got := BuildRestoreAgents(recs, nil)
	if len(got) != 1 {
		t.Fatalf("got %d agents", len(got))
	}
	a := got[0]
	if a.Mode != "adhoc" || a.TicketRef != "42" || a.TicketTitle != "T" || a.Task != "do x" || a.FirstPrompt != "first" || a.Branch != "agent/x" ||
		a.WorktreeDir != "/w/.worktrees/x" || a.SpawnedBy != "@9" || a.SpawnedBySession != "sess" {
		t.Errorf("meta not copied: %+v", a)
	}
}

func TestLoadRestoreWithoutLaunchMeta(t *testing.T) {
	p := filepath.Join(t.TempDir(), "restore.json")
	old := `{"agents":[{"name":"a","session_id":"s","work_dir":"/w"}],"timestamp":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadFrom(p)
	if err != nil || s == nil || len(s.Agents) != 1 {
		t.Fatalf("LoadFrom = %v, %v", s, err)
	}
	if a := s.Agents[0]; a.Mode != "" || a.SpawnedBy != "" || a.TicketRef != "" {
		t.Errorf("unexpected meta: %+v", a)
	}
}

func TestBuildRestoreAgentsCarriesPR(t *testing.T) {
	got := BuildRestoreAgents(map[string]windowstate.WindowState{
		"@1": {Instance: "a", AgentName: "a", PRNumber: 7, PRURL: "https://github.com/o/r/pull/7"},
	}, nil)
	if len(got) != 1 || got[0].PRNumber != 7 || got[0].PRURL != "https://github.com/o/r/pull/7" {
		t.Errorf("got %+v", got)
	}
}

// TestBuildRestoreAgentsCarriesHarness: restore.json records a pi window's
// harness, and never derives a pi session ID from its log file name
// (<timestamp>_<id>.jsonl is not an ID). Old records mean claude.
func TestBuildRestoreAgentsCarriesHarness(t *testing.T) {
	got := BuildRestoreAgents(map[string]windowstate.WindowState{
		"@1": {Instance: "p", AgentName: "p", WorkDir: "/w", Harness: "pi", LogPath: "/s/2026_sid.jsonl"},
		"@2": {Instance: "c", AgentName: "c", WorkDir: "/w", LogPath: "/p/sess-2.jsonl"},
	}, map[string]LiveWindow{"@1": {Index: 1}, "@2": {Index: 2}})
	if got[0].Harness != "pi" || got[0].SessionID != "" {
		t.Errorf("pi entry = harness %q session %q", got[0].Harness, got[0].SessionID)
	}
	if got[1].Harness != "" || got[1].SessionID != "sess-2" {
		t.Errorf("claude entry = harness %q session %q", got[1].Harness, got[1].SessionID)
	}
}

func TestLoadRestoreWithoutHarnessMeansClaude(t *testing.T) {
	path := filepath.Join(t.TempDir(), RestoreFileName)
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"a","agent_name":"a","session_id":"s"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Agents) != 1 || s.Agents[0].Harness != "" {
		t.Errorf("agents = %+v", s.Agents)
	}
}

func TestRestoreCarriesTitleAndStart(t *testing.T) {
	manual := windowstate.ShortTitle{Title: "my name", Manual: true, Final: true, InputHash: "h1", Kind: "go"}
	final := windowstate.ShortTitle{Title: "hub fix", Final: true, InputHash: "h2", At: time.Unix(1700000000, 0).UTC()}
	recs := map[string]windowstate.WindowState{
		"@1": {Instance: "a:1", AgentName: "a", WorkDir: "/w", ShortTitle: manual, StartedAt: 1700000100},
		"@2": {Instance: "a:2", AgentName: "a", WorkDir: "/w", ShortTitle: final, StartedAt: 1700000200},
		"@3": {Instance: "a:3", AgentName: "a", WorkDir: "/w"},
	}
	dir := t.TempDir()
	if err := SaveRestoreAgents(dir, BuildRestoreAgents(recs, nil)); err != nil {
		t.Fatal(err)
	}
	s, err := LoadFrom(filepath.Join(dir, RestoreFileName))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]AgentState{}
	for _, a := range s.Agents {
		byID[a.WindowID] = a
	}
	for id, want := range map[string]windowstate.ShortTitle{"@1": manual, "@2": final} {
		got := byID[id].ShortTitleRec
		if got == nil || !got.At.Equal(want.At) || got.Title != want.Title || got.Manual != want.Manual ||
			got.Final != want.Final || got.InputHash != want.InputHash || got.Kind != want.Kind {
			t.Errorf("%s: short title %+v, want %+v", id, got, want)
		}
	}
	if byID["@1"].StartedAt != 1700000100 || byID["@2"].StartedAt != 1700000200 {
		t.Errorf("started_at lost: %d %d", byID["@1"].StartedAt, byID["@2"].StartedAt)
	}
	if byID["@3"].ShortTitleRec != nil {
		t.Errorf("untitled window must carry no short title")
	}
}
