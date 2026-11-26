package dispatch

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/tmux/launchtest"
	"github.com/blsmth/agentctl/internal/worktree"
)

// goldenAgentMD is the agent definition the launch golden tests use.
const goldenAgentMD = "---\nname: golden\ndescription: golden test agent\n---\nYou are the golden test agent.\n"

// goldenConfig configures the golden agent with every launch-affecting key
// the Claude command line and env carry.
const goldenConfig = `{
  "agents": {
    "golden": {"model": "opus", "effort_level": "high", "max_thinking_tokens": 4096}
  }
}`

// setupGolden isolates a launch environment and stubs every seam that would
// reach outside the test (orchestrator, parent account, hook binary path).
func setupGolden(t *testing.T, cfg string) *launchtest.Env {
	t.Helper()
	env := launchtest.Setup(t, cfg, map[string]string{"golden": goldenAgentMD})
	stubGoldenSeams(t, Orchestrator{})
	return env
}

// goldenStart is the launch time the golden tests' first messages show.
var goldenStart = time.Date(2026, 11, 24, 21, 5, 43, 0, time.UTC)

// stubGoldenSeams fixes every seam a golden launch would otherwise read from
// the machine: the orchestrator, the parent account, the hook binary path
// the clock and the checkout inspection (a main checkout).
func stubGoldenSeams(t *testing.T, orch Orchestrator) {
	t.Helper()
	prevOrch, prevParent, prevExec, prevNow, prevInspect := resolveOrchestratorFn, parentAccountFn, execPath, launchNow, inspectCheckoutFn
	resolveOrchestratorFn = func() Orchestrator { return orch }
	inspectCheckoutFn = func(string) worktree.Checkout { return worktree.Checkout{Repo: true, Branch: "main"} }
	parentAccountFn = func() string { return "" }
	execPath = func() (string, error) { return "/opt/bin/agentctl", nil }
	launchNow = func() time.Time { return goldenStart }
	t.Cleanup(func() {
		resolveOrchestratorFn, parentAccountFn, execPath, launchNow, inspectCheckoutFn = prevOrch, prevParent, prevExec, prevNow, prevInspect
	})
}

// renderGolden renders a captured launch plus the window record, the
// --settings file and the system prompt file it wrote. The system file is
// pinned whole: it is the cache-stable half of the prompt.
func renderGolden(env *launchtest.Env, l launchtest.Launch, settingsName string) string {
	out := l.Render() + launchtest.WindowRecord("@9001") +
		launchtest.File("settings", filepath.Join(env.Project, ".agentctl", "settings", settingsName+".json")) +
		goldenSystemFile(l)
	return env.Normalize(out, launchtest.BinPair(l)...)
}

// goldenSystemFile renders the system prompt file a launch names, if any.
func goldenSystemFile(l launchtest.Launch) string {
	m := sysFileRe.FindStringSubmatch(l.Command)
	if m == nil {
		return ""
	}
	return launchtest.File("system-prompt-file", m[1])
}

// TestLaunchGolden pins the exact claude command line, window env and
// settings file dispatch.Launch produces. It exists to prove the harness
// refactor changed nothing for Claude; regenerate with
// AGENTCTL_UPDATE_GOLDEN=1 only for an intended change.
func TestLaunchGolden(t *testing.T) {
	no := false
	tests := []struct {
		name string
		cfg  string
		req  Request
	}{
		{"adhoc", goldenConfig, Request{
			AgentName: "golden", Mode: ModeAdhoc, CustomPrompt: "say hi", RepoSlug: "o/r", UseWorktree: &no, Background: true,
		}},
		{"bare", goldenConfig, Request{
			AgentName: "golden", Mode: ModeBare, UseWorktree: &no, Background: true,
		}},
		{"dispatch-ticket-overrides", goldenConfig, Request{
			AgentName: "golden", Mode: ModeDispatch, Ticket: 7, TicketTitle: "Fix it", TicketBody: "body", RepoSlug: "o/r",
			UseWorktree: &no, Background: true, Model: "sonnet", Effort: "low", PermissionMode: "plan",
		}},
		{"account", `{
  "accounts": {"work": {"config_dir": "~/work-claude"}},
  "agents": {"golden": {"account": "work"}}
}`, Request{
			AgentName: "golden", Mode: ModeAdhoc, CustomPrompt: "say hi", UseWorktree: &no, Background: true,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupGolden(t, tt.cfg)
			if err := os.MkdirAll(filepath.Join(env.Home, "work-claude"), 0o700); err != nil {
				t.Fatal(err)
			}
			rec := launchtest.Capture(t)
			req := tt.req
			req.WorkDir = env.Project
			if _, err := Launch(req); err != nil {
				t.Fatalf("Launch: %v", err)
			}
			got := renderGolden(env, rec.Only(), "golden")
			launchtest.CheckGolden(t, filepath.Join("testdata", "golden", "launch-"+tt.name+".txt"), got)
			if w := rec.Watches(); !slices.Equal(w, []string{"@9001"}) {
				t.Errorf("launch watcher started for %v, want [@9001]", w)
			}
		})
	}
}

// TestRestoreGolden pins dispatch.Restore's command line and env for a
// resumed and a fresh (duplicate session) restore.
func TestRestoreGolden(t *testing.T) {
	tests := []struct {
		name string
		dup  bool
	}{
		{"resume", false},
		{"duplicate-fresh", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupGolden(t, goldenConfig)
			rec := launchtest.Capture(t)
			_, err := Restore(RestoreRequest{
				AgentName:        "golden",
				InstanceName:     "golden:2",
				SessionID:        "11111111-2222-3333-4444-555555555555",
				DuplicateSession: tt.dup,
				WorkDir:          env.Project,
				LogPath:          filepath.Join(env.Home, ".claude", "projects", "p", "11111111-2222-3333-4444-555555555555.jsonl"),
				Mode:             "adhoc",
				SessionName:      "golden-proj-2",
			})
			if err != nil {
				t.Fatalf("Restore: %v", err)
			}
			got := renderGolden(env, rec.Only(), "golden")
			launchtest.CheckGolden(t, filepath.Join("testdata", "golden", "restore-"+tt.name+".txt"), got)
			if w := rec.Watches(); !slices.Equal(w, []string{"@9001"}) {
				t.Errorf("launch watcher started for %v, want [@9001]", w)
			}
		})
	}
}
