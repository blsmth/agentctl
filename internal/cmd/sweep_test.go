package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/blsmth/agentctl/internal/cleanup"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/reconcile"
	"github.com/blsmth/agentctl/internal/state"
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

// A sweep-style close kills the window and leaves its record; the reconcile
// the kill spawns then claims it, firing Gone exactly once for tracked agents
// and never for shell/improve~ windows.
func TestSweepCloseThenReconcileFiresGoneOnce(t *testing.T) {
	windowstate.SetDirForTest(filepath.Join(t.TempDir(), "windows"))
	if err := os.MkdirAll(windowstate.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	recs := map[string]windowstate.WindowState{
		"@3": {State: "idle", Done: true, Instance: "go-engineer:1", AgentName: "go-engineer"},
		"@6": {State: "idle", Done: true, Instance: "shell"},
		"@8": {State: "idle", Done: true, Instance: "improve~go", AgentName: "improve~go"},
	}
	var mu sync.Mutex
	live := []tmux.WindowInfo{{ID: "@3", Name: "c"}, {ID: "@6", Name: "shell"}, {ID: "@8", Name: "improve~go"}}
	for id, ws := range recs {
		if err := windowstate.Write(id, ws); err != nil {
			t.Fatal(err)
		}
	}
	d := sweepDeps{
		Windows:   func() []tmux.WindowInfo { mu.Lock(); defer mu.Unlock(); return append([]tmux.WindowInfo(nil), live...) },
		States:    windowstate.All,
		ReadState: func(id string) (windowstate.WindowState, bool) { ws, ok := recs[id]; return ws, ok },
		Capture:   func(string) string { return "" },
		Mail:      func(string) int { return 0 },
		Agent:     func(string) (string, string) { return "", "" },
		DirExists: func(string) bool { return false },
		CloseWindow: func(id string) error {
			mu.Lock()
			defer mu.Unlock()
			for i, w := range live {
				if w.ID == id {
					live = append(live[:i], live[i+1:]...)
					break
				}
			}
			return nil
		},
		RemoveMail: func(string) error { return nil },
	}
	runSweep(d, "sess", "", SweepOptions{})
	if !windowstate.Exists("@3") {
		t.Fatal("sweep removed the record before the Gone claim")
	}

	// Window-unlinked hook and KillWindow's spawn can both run reconcile.
	var handled []string
	var hmu sync.Mutex
	rd := reconcile.Deps{
		RuntimeDir: t.TempDir(),
		List:       func() ([]tmux.WindowInfo, error) { return d.Windows(), nil },
		HasSession: func() bool { return true },
		Handle: func(tr state.Transition, _ config.AgentConfig) {
			hmu.Lock()
			handled = append(handled, tr.WindowID)
			hmu.Unlock()
		},
		AgentConfig: func(string) config.AgentConfig { return config.AgentConfig{} },
		RemoveInbox: func(string) error { return nil },
	}
	reconcile.Run(rd)
	reconcile.Run(rd)
	if len(handled) != 1 || handled[0] != "@3" {
		t.Fatalf("Gone handlers = %v, want exactly [@3]", handled)
	}
}

func TestCheckSweepSession(t *testing.T) {
	if err := checkSweepSession("gone", func(string) bool { return false }); err == nil {
		t.Fatal("missing session accepted")
	}
	if err := checkSweepSession("here", func(string) bool { return true }); err != nil {
		t.Fatalf("live session: %v", err)
	}
}
