package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestBuildAgents(t *testing.T) {
	now := time.Now()
	wins := []tmux.WindowInfo{
		{ID: "@1", Name: "shell"},
		{ID: "@5", Name: "✓ go-engineer"},
		{ID: "@2", Name: "bare-thing"},
		{ID: "@7", Name: "● py-eng:2"},
		{ID: "@8", Name: "improve~go-engineer"},
	}
	records := map[string]windowstate.WindowState{
		"@5": {State: "working", Done: true, Seen: false, Instance: "go-engineer", AgentName: "go-engineer", Ticket: "42", TurnCount: 3,
			Model: "claude-x", CostUSD: 1.5, CtxPct: 61, ContextTokens: 100, ContextWindowSize: 200,
			SpawnedBy: "@1", SpawnedBySession: "s", Mode: "dispatch", Branch: "b", WorktreeDir: "/r/.worktrees/x",
			LastToolDetail: "Edit a.go", LastText: "done", Backend: "nous", MainPane: "%4", LogPath: "/l/a.jsonl", SessionID: "a"},
		"@7": {State: "working", Done: true, Seen: true, Instance: "py-eng:2"},
		"@9": {State: "working", Instance: "gone"}, // window no longer exists
	}
	freshSnap := func(age time.Duration) *state.Snapshot {
		return &state.Snapshot{State: &state.State{Timestamp: now.Add(-age),
			Agents: []state.AgentState{{WindowID: "@5", InputTokens: 10, OutputTokens: 5, LastTurnInput: 7, StartTime: 99, MailboxPending: 2}}}}
	}
	// Snapshot indexes are unexported; build through a file-free path.
	snapOf := func(age time.Duration) *state.Snapshot {
		s := freshSnap(age)
		return state.NewSnapshot(s.State)
	}

	tests := []struct {
		name    string
		snap    *state.Snapshot
		wantIDs []string
		check   func(t *testing.T, got []state.AgentState)
	}{
		{
			name: "tracked, untracked, stale record, order", wantIDs: []string{"@5", "@2", "@7", "@8"},
			check: func(t *testing.T, got []state.AgentState) {
				if got[1].State != "idle" || got[1].Name != "bare-thing" || got[1].AgentName != "bare-thing" {
					t.Errorf("untracked = %+v", got[1])
				}
				if got[0].WindowIndex != 1 || got[1].WindowIndex != 2 || got[2].WindowIndex != 3 {
					t.Errorf("indexes = %d %d %d, want tmux list positions", got[0].WindowIndex, got[1].WindowIndex, got[2].WindowIndex)
				}
			},
		},
		{
			name: "done+unseen is done and never working", wantIDs: []string{"@5", "@2", "@7", "@8"},
			check: func(t *testing.T, got []state.AgentState) {
				if !got[0].Done || got[0].State != "idle" {
					t.Errorf("@5 done=%v state=%q, want done idle", got[0].Done, got[0].State)
				}
			},
		},
		{
			name: "done+seen is not done", wantIDs: []string{"@5", "@2", "@7", "@8"},
			check: func(t *testing.T, got []state.AgentState) {
				if got[2].Done {
					t.Error("@7 done+seen must not report Done")
				}
			},
		},
		{
			name: "fields from windowstate", wantIDs: []string{"@5", "@2", "@7", "@8"},
			check: func(t *testing.T, got []state.AgentState) {
				a := got[0]
				if a.ContextPct != 61 || a.ContextTokens != 100 || a.ContextWindow != 200 || a.ModelName != "claude-x" || a.CostUSD != 1.5 ||
					a.TurnCount != 3 || a.Ticket != 42 || a.TicketRef != "42" || a.Mode != "dispatch" || a.Branch != "b" ||
					a.WorktreePath != "/r/.worktrees/x" || a.SpawnedBy != "@1" || a.SpawnedBySession != "s" ||
					a.LastToolDetail != "Edit a.go" || a.LastText != "done" || a.Backend != "nous" || a.MainPane != "%4" ||
					a.LogPath != "/l/a.jsonl" || a.SessionID != "a" || a.Name != "go-engineer" {
					t.Errorf("fields = %+v", a)
				}
			},
		},
		{
			name: "no snapshot: enrichment zero", snap: nil, wantIDs: []string{"@5", "@2", "@7", "@8"},
			check: func(t *testing.T, got []state.AgentState) {
				if got[0].InputTokens != 0 || got[0].StartTime != 0 {
					t.Errorf("enrichment leaked without state.json: %+v", got[0])
				}
			},
		},
		{
			name: "fresh snapshot enriches by window id", snap: snapOf(time.Second), wantIDs: []string{"@5", "@2", "@7", "@8"},
			check: func(t *testing.T, got []state.AgentState) {
				a := got[0]
				if a.InputTokens != 10 || a.OutputTokens != 5 || a.LastTurnInput != 7 || a.StartTime != 99 || a.MailboxPending != 2 {
					t.Errorf("enrichment missing: %+v", a)
				}
				if got[1].InputTokens != 0 {
					t.Error("enrichment leaked to another window")
				}
			},
		},
		{
			name: "stale snapshot ignored", snap: snapOf(time.Minute), wantIDs: []string{"@5", "@2", "@7", "@8"},
			check: func(t *testing.T, got []state.AgentState) {
				if got[0].InputTokens != 0 || got[0].StartTime != 0 {
					t.Errorf("stale snapshot merged: %+v", got[0])
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildAgents(wins, records, tt.snap, now)
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("got %d agents, want %v: %+v", len(got), tt.wantIDs, got)
			}
			for i, id := range tt.wantIDs {
				if got[i].WindowID != id {
					t.Fatalf("agent %d = %s, want %s", i, got[i].WindowID, id)
				}
			}
			tt.check(t, got)
		})
	}
}

func TestBuildAgentsOnlyShellWindow(t *testing.T) {
	got := BuildAgents([]tmux.WindowInfo{{ID: "@1", Name: "shell"}}, nil, nil, time.Now())
	if len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestBuildAgentsCtxFromLastPush(t *testing.T) {
	now := time.Now()
	stale := windowstate.WindowState{CtxPct: 40, CtxPctSource: "statusline", CtxPushedAt: now.Add(-time.Hour), ContextWindowSize: 1000, ContextTokens: 400}
	fresh := stale
	fresh.CtxPushedAt = now
	for name, ws := range map[string]windowstate.WindowState{"stale": stale, "fresh": fresh} {
		got := BuildAgents([]tmux.WindowInfo{{ID: "@2", Name: "a"}}, map[string]windowstate.WindowState{"@2": ws}, nil, now)
		if got[0].ContextPct != 40 || got[0].ContextTokens != 400 || got[0].ContextWindow != 1000 {
			t.Errorf("%s: ctx = %d/%d/%d, want last pushed 40/400/1000", name, got[0].ContextPct, got[0].ContextTokens, got[0].ContextWindow)
		}
	}
}

// BenchmarkBuildAgents20 measures the join for 20 windows, excluding the
// tmux exec and the directory scan.
func BenchmarkBuildAgents20(b *testing.B) {
	now := time.Now()
	var wins []tmux.WindowInfo
	records := map[string]windowstate.WindowState{}
	var agents []state.AgentState
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("@%d", i+2)
		wins = append(wins, tmux.WindowInfo{ID: id, Name: fmt.Sprintf("● agent:%d", i)})
		records[id] = windowstate.WindowState{State: "working", Instance: fmt.Sprintf("agent:%d", i), Ticket: "12", CtxPct: 30, TurnCount: i}
		agents = append(agents, state.AgentState{WindowID: id, InputTokens: i})
	}
	snap := state.NewSnapshot(&state.State{Timestamp: now, Agents: agents})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildAgents(wins, records, snap, now)
	}
}

// BenchmarkLoadAgents20 includes reading 20 windowstate files from disk (tmux
// faked) -- the realistic per-rebuild cost.
func BenchmarkLoadAgents20(b *testing.B) {
	dir := b.TempDir()
	var wins []tmux.WindowInfo
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("@%d", i+2)
		wins = append(wins, tmux.WindowInfo{ID: id, Name: fmt.Sprintf("agent:%d", i)})
		writeBenchWS(b, dir, id)
	}
	prev := listWindowsFn
	listWindowsFn = func(string) ([]tmux.WindowInfo, error) { return wins, nil }
	defer func() { listWindowsFn = prev }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := LoadAgents("s", dir); err != nil {
			b.Fatal(err)
		}
	}
}

func writeBenchWS(b *testing.B, dir, id string) {
	b.Helper()
	winDir := filepath.Join(dir, "windows")
	if err := os.MkdirAll(winDir, 0o755); err != nil {
		b.Fatal(err)
	}
	body := `{"state":"working","instance":"agent","ticket":"12","ctx_pct":30,"turn_count":4,"last_text":"hello world"}`
	if err := os.WriteFile(filepath.Join(winDir, id+".json"), []byte(body), 0o644); err != nil {
		b.Fatal(err)
	}
}

func TestLastActivity(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	tests := []struct {
		name string
		ws   windowstate.WindowState
		want int64
	}{
		{"empty", windowstate.WindowState{}, 0},
		{"tool only", windowstate.WindowState{LastToolTime: 100}, 100},
		{"state only", windowstate.WindowState{StateSince: now}, 2_000_000},
		{"later tool wins", windowstate.WindowState{LastToolTime: 2_000_100, StateSince: now}, 2_000_100},
		{"later state wins", windowstate.WindowState{LastToolTime: 5, StateSince: now}, 2_000_000},
		{"UpdatedAt ignored", windowstate.WindowState{UpdatedAt: now}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastActivity(tt.ws); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestShortRoleChildrenOnly(t *testing.T) {
	tests := []struct {
		name, mode, by, session, want string
	}{
		{"bare", "bare", "", "", ""},
		{"cron, no parent", "adhoc", "", "", ""},
		{"spawned_by alone is untrusted", "adhoc", "@1", "", ""},
		{"child fix", "adhoc", "@1", "s1", "fix"},
		{"child issue", "issue", "@1", "s1", "fix"},
		{"child review-pr", "review-pr", "@1", "s1", "review"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := map[string]windowstate.WindowState{
				"@5": {Instance: "go-engineer", Mode: tt.mode, SpawnedBy: tt.by, SpawnedBySession: tt.session,
					ShortTitle: windowstate.ShortTitle{Title: "Fixing X", Kind: "fix"}},
			}
			agents := BuildAgents([]tmux.WindowInfo{{ID: "@5", Name: "go-engineer"}}, records, nil, time.Now())
			if len(agents) != 1 {
				t.Fatalf("agents = %d", len(agents))
			}
			if got := agents[0].ShortRole; got != tt.want {
				t.Errorf("ShortRole = %q, want %q", got, tt.want)
			}
			if agents[0].ShortTitle != "Fixing X" {
				t.Errorf("title lost: %q", agents[0].ShortTitle)
			}
		})
	}
}
