package dispatch

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/tmux/launchtest"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// otherWindowEnv is another window's identity, as tmux would seed it into a
// new window from the session environment (older builds published
// AGENTCTL_WINDOW_ID there after every launch) or as a launcher started from
// another agent window carries it.
var otherWindowEnv = map[string]string{
	"AGENTCTL_WINDOW_ID":          "@943",
	"AGENTCTL_WINDOW_INDEX":       "8",
	"AGENTCTL_BRANCH":             "agent/brandon/adhoc-1df2788c",
	"AGENTCTL_WORKTREE":           "/elsewhere/.worktrees/agent-brandon-adhoc-1df2788c",
	"AGENTCTL_TICKET":             "9",
	"AGENTCTL_TICKET_TITLE":       "someone else's ticket",
	"AGENTCTL_MODE":               "dispatch",
	"AGENTCTL_REPO":               "o/other",
	"AGENTCTL_SPAWNED_BY":         "@943",
	"AGENTCTL_SPAWNED_BY_SESSION": "other-session",
	"AGENTCTL_CLAUDE_NAME":        "brandon-personal-8",
	"AGENTCTL_BACKEND":            "ollama",
	"AGENTCTL_ACCOUNT":            "ll",
	"AGENTCTL_HARNESS":            "pi",
	"AGENTCTL_PI_PROVIDER":        "{}",
}

// TestLaunchChildEnvNeverInheritsIdentity pins the new window's env when
// tmux and the launcher both carry another window's AGENTCTL_* values: every
// identity var is either the new window's own or unset, never the other
// window's. The bare case is the phone's new-session launch in a directory
// that is not a git repo, where the requested worktree falls back to the
// shared dir.
func TestLaunchChildEnvNeverInheritsIdentity(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name     string
		req      Request
		wantMode string
	}{
		{"bare worktree fallback", Request{AgentName: "golden", Mode: ModeBare, CustomPrompt: "hi", UseWorktree: &yes, Background: true}, "bare"},
		{"adhoc shared dir", Request{AgentName: "golden", Mode: ModeAdhoc, CustomPrompt: "hi", UseWorktree: &no, Background: true}, "adhoc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupGolden(t, goldenConfig)
			// The launcher's own env: everything but the window-ID chain
			// (TMUX_PANE, AGENTCTL_SESSION_ID, AGENTCTL_WINDOW_ID), which
			// legitimately names a dispatching parent.
			for k, v := range otherWindowEnv {
				if k != "AGENTCTL_WINDOW_ID" {
					t.Setenv(k, v)
				}
			}
			rec := launchtest.Capture(t)
			req := tt.req
			req.WorkDir = env.Project
			res, err := Launch(req)
			if err != nil {
				t.Fatalf("Launch: %v", err)
			}
			child := rec.Only().ChildEnv(otherWindowEnv)
			for k, other := range otherWindowEnv {
				if got, ok := child[k]; ok && got == other {
					t.Errorf("%s=%q inherited from another window", k, got)
				}
			}
			for _, k := range []string{"AGENTCTL_WINDOW_ID", "AGENTCTL_BRANCH", "AGENTCTL_WORKTREE", "AGENTCTL_TICKET", "AGENTCTL_SPAWNED_BY"} {
				if v, ok := child[k]; ok {
					t.Errorf("%s=%q set, want unset", k, v)
				}
			}
			if child["AGENTCTL_MODE"] != tt.wantMode {
				t.Errorf("AGENTCTL_MODE = %q, want %q", child["AGENTCTL_MODE"], tt.wantMode)
			}
			if child["AGENTCTL_INSTANCE_NAME"] != "golden" {
				t.Errorf("AGENTCTL_INSTANCE_NAME = %q, want golden", child["AGENTCTL_INSTANCE_NAME"])
			}
			ws, err := windowstate.Read(res.WindowID)
			if err != nil {
				t.Fatal(err)
			}
			if ws.Branch != "" || ws.WorktreeDir != "" || res.Branch != "" {
				t.Errorf("no worktree was created, yet branch=%q worktree=%q result branch=%q", ws.Branch, ws.WorktreeDir, res.Branch)
			}
		})
	}
}

// TestLaunchDoesNotPublishWindowIDToSession: tmux has no window-scoped
// environment, so a session-level AGENTCTL_WINDOW_ID is the NEXT window's
// wrong identity. A launch clears it instead of setting it.
func TestLaunchDoesNotPublishWindowIDToSession(t *testing.T) {
	no := false
	env := setupGolden(t, goldenConfig)
	rec := launchtest.Capture(t)
	if _, err := Launch(Request{AgentName: "golden", Mode: ModeBare, WorkDir: env.Project, UseWorktree: &no, Background: true}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	cleared := false
	for _, c := range rec.Calls() {
		if len(c) == 0 || c[0] != "set-environment" || !slices.Contains(c, "AGENTCTL_WINDOW_ID") {
			continue
		}
		if !slices.Contains(c, "-u") || slices.Contains(c, "@9001") {
			t.Errorf("launch publishes the window ID to the session env: %q", c)
			continue
		}
		cleared = true
	}
	if !cleared {
		t.Error("launch does not clear a stale session AGENTCTL_WINDOW_ID")
	}
}

// When worktree creation falls back to the shared dir, the prompt must not
// tell the agent to push a branch the window does not record (dispatch
// completion looks the PR up by the recorded branch): it gets the
// shared-dir, commit-only finishing steps.
func TestLaunchWorktreeFallbackPromptIsSharedDir(t *testing.T) {
	yes := true
	env := setupGolden(t, goldenConfig)
	rec := launchtest.Capture(t)
	if _, err := Launch(Request{AgentName: "golden", Mode: ModeAdhoc, CustomPrompt: "hi", WorkDir: env.Project, UseWorktree: &yes, Background: true}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	m := regexp.MustCompile(`--append-system-prompt-file '([^']+)'`).FindStringSubmatch(rec.Only().Command)
	if m == nil {
		t.Fatalf("no system prompt file: %s", rec.Only().Command)
	}
	sysData, err := os.ReadFile(m[1])
	if err != nil {
		t.Fatal(err)
	}
	// System file and first message (in the command) together.
	all := string(sysData) + rec.Only().Command
	for _, bad := range []string{"git push -u origin", "git worktree", "gh pr create", "- Branch:"} {
		if strings.Contains(all, bad) {
			t.Errorf("fallback prompt contains %q", bad)
		}
	}
	if !strings.Contains(rec.Only().Command, "No worktree: this is the shared checkout") {
		t.Error("fallback launch facts do not say there is no worktree")
	}
	if !strings.Contains(rec.Only().Command, "working directly in the shared repo directory") {
		t.Error("fallback prompt lacks the shared-dir finishing steps")
	}
}
