package cmd

import (
	"errors"
	"testing"

	"github.com/blsmth/agentctl/internal/cleanup"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestResolveSweepSession(t *testing.T) {
	boom := func(string) string { t.Fatal("must not query tmux"); return "" }
	if _, err := resolveSweepSession("", "", boom); !errors.Is(err, errSweepNoTmux) {
		t.Fatalf("outside tmux: err = %v, want errSweepNoTmux", err)
	}
	if got, err := resolveSweepSession("other", "", boom); err != nil || got != "other" {
		t.Fatalf("--session override = %q, %v", got, err)
	}
	if got, err := resolveSweepSession("", "%3", func(p string) string { return "sess-" + p }); err != nil || got != "sess-%3" {
		t.Fatalf("pane session = %q, %v", got, err)
	}
	if _, err := resolveSweepSession("", "%3", func(string) string { return "" }); err == nil {
		t.Fatal("expected error when pane session unresolvable")
	}
}

func TestRunSweepScopingAndRecheck(t *testing.T) {
	states := map[string]windowstate.WindowState{
		"@1": {State: "idle", Done: true},                   // closable, flips to working at recheck
		"@2": {State: "idle", Done: true, Mode: "bare"},     // bare, kept
		"@3": {State: "idle", Done: true},                   // closes
		"@5": {State: "idle", Done: true},                   // self
		"@7": {State: "idle", Done: true, Branch: "feat/x"}, // open PR
	}
	windows := []tmux.WindowInfo{
		{ID: "@1", Name: "a"}, {ID: "@2", Name: "b"}, {ID: "@3", Name: "c"},
		{ID: "@4", Name: "untracked"}, {ID: "@5", Name: "me"}, {ID: "@6", Name: "shell"},
		{ID: "@7", Name: "pr"}, {ID: "@8", Name: "improve~go"},
	}
	reads := 0
	var closed []string
	d := sweepDeps{
		Windows: func() []tmux.WindowInfo { return windows },
		States:  func() map[string]windowstate.WindowState { return states },
		ReadState: func(id string) (windowstate.WindowState, bool) {
			ws, ok := states[id]
			if id == "@1" {
				reads++
				if reads > 1 { // recheck sees it working again
					ws.State = "working"
				}
			}
			return ws, ok
		},
		Capture:   func(string) string { return "" },
		Mail:      func(string) int { return 0 },
		Agent:     func(string) (string, string) { return "", "" },
		DirExists: func(string) bool { return false },
		BranchPR: func(string) cleanup.PRInfo {
			return cleanup.PRInfo{State: cleanup.PROpen, Number: 9, URL: "http://pr/9"}
		},
		CloseWindow: func(id string) error { closed = append(closed, id); return nil },
		Reclaim:     func(*cleanup.Candidate) { t.Fatal("no worktrees here") },
		RemoveState: func(string) {},
		RemoveMail:  func(string) error { return nil },
	}
	rep := runSweep(d, "sess", "@5", SweepOptions{DryRun: false})

	if len(closed) != 1 || closed[0] != "@3" {
		t.Fatalf("closed windows = %v, want only @3", closed)
	}
	if len(rep.InProgress) != 1 || rep.InProgress[0].WindowID != "@1" {
		t.Errorf("in progress = %+v, want @1", rep.InProgress)
	}
	if len(rep.Attention) != 2 {
		t.Errorf("attention = %+v, want bare @2 and PR @7", rep.Attention)
	}
	if len(rep.Skipped) != 4 {
		t.Errorf("skipped = %+v, want untracked/self/shell/improve", rep.Skipped)
	}
}

func TestRunSweepDryRunClosesNothing(t *testing.T) {
	d := sweepDeps{
		Windows: func() []tmux.WindowInfo { return []tmux.WindowInfo{{ID: "@1", Name: "a"}} },
		States: func() map[string]windowstate.WindowState {
			return map[string]windowstate.WindowState{"@1": {Done: true}}
		},
		ReadState:   func(string) (windowstate.WindowState, bool) { return windowstate.WindowState{Done: true}, true },
		Capture:     func(string) string { return "" },
		Mail:        func(string) int { return 0 },
		Agent:       func(string) (string, string) { return "", "" },
		DirExists:   func(string) bool { return false },
		CloseWindow: func(string) error { t.Fatal("dry-run closed a window"); return nil },
	}
	rep := runSweep(d, "s", "", SweepOptions{DryRun: true})
	if len(rep.Closed) != 1 {
		t.Fatalf("closed = %+v, want 1 planned", rep.Closed)
	}
}
