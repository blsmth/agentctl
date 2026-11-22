package dispatch

import (
	"errors"
	"testing"
)

func stubAdhoc(t *testing.T, slug string, slugErr error) *Request {
	t.Helper()
	oldSlug, oldLaunch := adhocRepoSlug, adhocLaunch
	t.Cleanup(func() { adhocRepoSlug, adhocLaunch = oldSlug, oldLaunch })
	adhocRepoSlug = func(string) (string, error) { return slug, slugErr }
	got := &Request{}
	adhocLaunch = func(req Request) (Result, error) {
		*got = req
		return Result{WindowID: "@9", AgentName: req.AgentName, Branch: "agent/x/adhoc-1"}, nil
	}
	return got
}

func TestLaunchAdhoc(t *testing.T) {
	tests := []struct {
		name     string
		req      AdhocRequest
		slug     string
		slugErr  error
		wantSlug string
	}{
		{
			name:     "CLI prompt with repo",
			req:      AdhocRequest{Prompt: "do it", PromptKey: "k", AgentName: "go-engineer", WorkDir: "/w", Model: "opus", Backend: "none", UseWorktree: true},
			slug:     "acme/widgets",
			wantSlug: "acme/widgets",
		},
		{
			// Ad-hoc dispatch into a non-GitHub directory is legitimate.
			name:    "no repo is not fatal",
			req:     AdhocRequest{Prompt: "do it", AgentName: "go-engineer", WorkDir: "/w"},
			slugErr: errors.New("no remote"),
		},
		{
			name: "detached (serve)",
			req:  AdhocRequest{Prompt: "p", AgentName: "go-engineer", WorkDir: "/w", UseWorktree: true, Detached: true},
			slug: "acme/widgets", wantSlug: "acme/widgets",
		},
		{
			name: "scheduled (cron)",
			req: AdhocRequest{Prompt: "p", AgentName: "go-engineer", WorkDir: "/w", UseWorktree: true, Detached: true,
				Effort: "high", Branch: "cron/nightly-20261117-0200", ScheduledBy: "nightly"},
			slug: "acme/widgets", wantSlug: "acme/widgets",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stubAdhoc(t, tt.slug, tt.slugErr)
			res, err := LaunchAdhoc(tt.req)
			if err != nil {
				t.Fatalf("LaunchAdhoc error = %v", err)
			}
			if res.WindowID != "@9" || res.Branch != "agent/x/adhoc-1" {
				t.Errorf("result = %+v", res)
			}
			if got.Mode != ModeAdhoc || !got.Background {
				t.Errorf("Mode = %q Background = %v, want adhoc/true", got.Mode, got.Background)
			}
			if got.AgentName != tt.req.AgentName || got.CustomPrompt != tt.req.Prompt || got.PromptKey != tt.req.PromptKey ||
				got.WorkDir != tt.req.WorkDir || got.Model != tt.req.Model || got.Backend != tt.req.Backend ||
				got.Detached != tt.req.Detached || got.Ticket != 0 || got.Effort != tt.req.Effort ||
				got.Branch != tt.req.Branch || got.ScheduledBy != tt.req.ScheduledBy {
				t.Errorf("Request = %+v, from %+v", *got, tt.req)
			}
			if got.UseWorktree == nil || *got.UseWorktree != tt.req.UseWorktree {
				t.Errorf("UseWorktree = %v, want %v", got.UseWorktree, tt.req.UseWorktree)
			}
			if got.RepoSlug != tt.wantSlug {
				t.Errorf("RepoSlug = %q, want %q", got.RepoSlug, tt.wantSlug)
			}
		})
	}
}

func TestLaunchAdhocRequiresWorkDir(t *testing.T) {
	stubAdhoc(t, "", nil)
	if _, err := LaunchAdhoc(AdhocRequest{Prompt: "p", AgentName: "go-engineer"}); err == nil {
		t.Fatal("want error for empty WorkDir")
	}
}

// A detached launch records no parent even when the launching process's
// environment names a window (serve started from a tmux pane).
func TestApplyParentEnvDetached(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-1")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "@36")

	attached := map[string]string{}
	applyParentEnv(Request{}, attached)
	if attached["AGENTCTL_SPAWNED_BY"] != "@36" || attached["AGENTCTL_SPAWNED_BY_SESSION"] != "sess-1" {
		t.Errorf("attached env = %v, want parent recorded", attached)
	}

	detached := map[string]string{}
	applyParentEnv(Request{Detached: true}, detached)
	if len(detached) != 0 {
		t.Errorf("detached env = %v, want no parent keys", detached)
	}
}

// A caller that resolved the slug already (agentctl serve, before taking its
// session lock) is trusted, even when it resolved to no repo: LaunchAdhoc
// must not run `gh repo view` again.
func TestLaunchAdhocKnownSlugSkipsLookup(t *testing.T) {
	for _, slug := range []string{"acme/widgets", ""} {
		got := stubAdhoc(t, "wrong/slug", nil)
		adhocRepoSlug = func(string) (string, error) {
			t.Error("repo slug looked up again")
			return "", nil
		}
		if _, err := LaunchAdhoc(AdhocRequest{Prompt: "p", AgentName: "a", WorkDir: "/w", RepoSlug: slug, RepoSlugKnown: true}); err != nil {
			t.Fatal(err)
		}
		if got.RepoSlug != slug {
			t.Errorf("RepoSlug = %q, want %q", got.RepoSlug, slug)
		}
	}
}

// A Detached launch (agentctl serve) has no dispatching window: it must not
// resolve an orchestrator from whatever window the process inherited.
func TestLaunchOrchestratorDetached(t *testing.T) {
	old := resolveOrchestratorFn
	t.Cleanup(func() { resolveOrchestratorFn = old })
	calls := 0
	resolveOrchestratorFn = func() Orchestrator {
		calls++
		return Orchestrator{WindowID: "@1", Pane: "%1"}
	}
	worker := modeSpec{Role: RoleWorker}
	if o := launchOrchestrator(Request{Detached: true}, worker); o != (Orchestrator{}) || calls != 0 {
		t.Errorf("detached: orchestrator %+v, %d resolves", o, calls)
	}
	if o := launchOrchestrator(Request{}, modeSpec{Role: RoleOrchestrator}); o != (Orchestrator{}) || calls != 0 {
		t.Errorf("orchestrator role: orchestrator %+v, %d resolves", o, calls)
	}
	if o := launchOrchestrator(Request{}, worker); o.WindowID != "@1" || calls != 1 {
		t.Errorf("worker: orchestrator %+v, %d resolves", o, calls)
	}
}

func TestLaunchAdhocExtraInstructions(t *testing.T) {
	got := stubAdhoc(t, "", nil)
	if _, err := LaunchAdhoc(AdhocRequest{Prompt: "p", AgentName: "a", WorkDir: "/w", ExtraInstructions: "be terse"}); err != nil {
		t.Fatal(err)
	}
	if got.ExtraInstructions != "be terse" {
		t.Errorf("ExtraInstructions = %q", got.ExtraInstructions)
	}
}
