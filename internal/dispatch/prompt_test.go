package dispatch

import (
	"strings"
	"testing"
)

// TestPreamble_DoesNotMentionSignalDone verifies the Signaling section was
// fully removed from both preamble variants -- done is now derived from the
// Stop hook, not an agent-cooperative signal.
func TestPreamble_DoesNotMentionSignalDone(t *testing.T) {
	for name, preamble := range map[string]string{"WorkerPreamble": WorkerPreamble, "OrchestratorPreamble": OrchestratorPreamble} {
		if strings.Contains(preamble, "signal done") {
			t.Errorf("%s should no longer mention 'signal done'", name)
		}
		if strings.Contains(preamble, "### Signaling") {
			t.Errorf("%s should no longer contain a Signaling section", name)
		}
	}
}

// TestPreamble_RoleAware covers the split introduced for role-aware
// preambles: both variants keep the exact runtime-environment heading (which
// prompt_test.go asserts on elsewhere) and the shared context-store lines,
// but only the orchestrator variant carries the delegation guidance
// (dispatch), and the worker variant must NOT.
func TestPreamble_RoleAware(t *testing.T) {
	t.Run("both carry the runtime environment heading", func(t *testing.T) {
		for name, preamble := range map[string]string{"WorkerPreamble": WorkerPreamble, "OrchestratorPreamble": OrchestratorPreamble} {
			if !strings.Contains(preamble, "## agentctl Runtime Environment") {
				t.Errorf("%s missing the runtime environment heading", name)
			}
		}
	})

	t.Run("both carry the context store lines", func(t *testing.T) {
		for name, preamble := range map[string]string{"WorkerPreamble": WorkerPreamble, "OrchestratorPreamble": OrchestratorPreamble} {
			if !strings.Contains(preamble, "agentctl context get") {
				t.Errorf("%s missing 'agentctl context get'", name)
			}
		}
	})

	t.Run("orchestrator preamble contains dispatch", func(t *testing.T) {
		if !strings.Contains(OrchestratorPreamble, "agentctl dispatch") {
			t.Error("OrchestratorPreamble missing 'agentctl dispatch'")
		}
	})

	t.Run("worker preamble omits orchestrator-only dispatch guidance", func(t *testing.T) {
		if strings.Contains(WorkerPreamble, "agentctl dispatch") {
			t.Error("WorkerPreamble should not contain 'agentctl dispatch' delegation guidance")
		}
		if strings.Contains(WorkerPreamble, "### Delegation") {
			t.Error("WorkerPreamble should not contain the Delegation section")
		}
	})

	t.Run("both carry ListAgents/SendMessage guidance and no mailbox commands", func(t *testing.T) {
		for name, preamble := range map[string]string{"WorkerPreamble": WorkerPreamble, "OrchestratorPreamble": OrchestratorPreamble} {
			for _, want := range []string{"ListAgents", "SendMessage"} {
				if !strings.Contains(preamble, want) {
					t.Errorf("%s missing %q", name, want)
				}
			}
			for _, gone := range []string{"agentctl send", "agentctl inbox", "agentctl wake"} {
				if strings.Contains(preamble, gone) {
					t.Errorf("%s still references %q", name, gone)
				}
			}
		}
	})

	t.Run("preambleFor selects by role", func(t *testing.T) {
		if got := preambleFor(RoleOrchestrator); got != OrchestratorPreamble {
			t.Error("preambleFor(RoleOrchestrator) should return OrchestratorPreamble")
		}
		if got := preambleFor(RoleWorker); got != WorkerPreamble {
			t.Error("preambleFor(RoleWorker) should return WorkerPreamble")
		}
		if got := preambleFor(""); got != WorkerPreamble {
			t.Error("preambleFor(\"\") should default to WorkerPreamble")
		}
	})

	t.Run("dispatch and bare modes use expected roles", func(t *testing.T) {
		if modes[ModeDispatch].Role != RoleWorker {
			t.Error("ModeDispatch should be RoleWorker")
		}
		if modes[ModeAdhoc].Role != RoleWorker {
			t.Error("ModeAdhoc should be RoleWorker")
		}
		if modes[ModeBare].Role != RoleOrchestrator {
			t.Error("ModeBare should be RoleOrchestrator")
		}
	})
}

// TestBuildSystemPrompt_SharedConventions verifies the expanded @shared
// section is composed into the system prompt after the preamble and before
// the mode instructions, under its own authoritative heading.
func TestBuildSystemPrompt_SharedConventions(t *testing.T) {
	req := Request{
		AgentName:   "go-engineer",
		Mode:        ModeDispatch,
		Ticket:      1,
		TicketTitle: "T",
		TicketBody:  "B",
	}
	spec := modes[ModeDispatch]

	t.Run("absent when there is nothing to expand", func(t *testing.T) {
		got := buildSystemPrompt(req, spec, "/tmp/dir", "", false, nil, "")
		if strings.Contains(got, "## Shared Conventions") {
			t.Error("prompt should not contain '## Shared Conventions' when sharedConventions is empty")
		}
	})

	t.Run("present and ordered after preamble, before mode instructions", func(t *testing.T) {
		got := buildSystemPrompt(req, spec, "/tmp/dir", "", false, nil, "Some shared convention text.")
		if !strings.Contains(got, "## Shared Conventions") {
			t.Fatal("prompt should contain '## Shared Conventions' when sharedConventions is non-empty")
		}
		if !strings.Contains(got, "Some shared convention text.") {
			t.Error("prompt should contain the expanded shared conventions content")
		}
		preambleIdx := strings.Index(got, "## agentctl Runtime Environment")
		sharedIdx := strings.Index(got, "## Shared Conventions")
		execIdx := strings.Index(got, "Execute this ticket autonomously")
		if preambleIdx >= sharedIdx {
			t.Error("Shared Conventions section should come after the preamble")
		}
		if sharedIdx >= execIdx {
			t.Error("Shared Conventions section should come before the mode instructions")
		}
	})
}

// TestExecInstructions_DoesNotMentionSignalDone verifies execInstructions()
// dropped the "agentctl signal done" step. This string must never come back
// -- it was removed deliberately.
func TestExecInstructions_DoesNotMentionSignalDone(t *testing.T) {
	got := execInstructions()
	if strings.Contains(got, "signal done") {
		t.Errorf("execInstructions() should no longer mention 'signal done', got:\n%s", got)
	}
}

// TestExecInstructions_NoGitOrBuildSteps verifies execInstructions() no
// longer contains any numbered git/build steps or language-specific
// commands -- those now live in buildFinishingSteps (workflow) and the
// agent's own .md (toolchain).
func TestExecInstructions_NoGitOrBuildSteps(t *testing.T) {
	got := execInstructions()
	for _, s := range []string{"go build", "just restart", "git add", "git push", "gh pr create", "Closes #"} {
		if strings.Contains(got, s) {
			t.Errorf("execInstructions() should not mention %q, got:\n%s", s, got)
		}
	}
}

func TestBuildSystemPrompt_DiscussMode(t *testing.T) {
	req := Request{
		AgentName:   "go-engineer",
		Mode:        ModeDiscuss,
		Ticket:      7,
		TicketTitle: "Scope this feature",
		TicketBody:  "We need to scope this feature carefully.",
		RepoSlug:    "blsmth/agentctl",
	}
	spec := modes[ModeDiscuss]
	workDir := "/tmp/test-dir"

	got := buildBoth(req, spec, workDir, "", false, nil, "")

	t.Run("has preamble", func(t *testing.T) {
		if !strings.Contains(got, "## agentctl Runtime Environment") {
			t.Error("discuss prompt should contain preamble")
		}
	})
	t.Run("has discuss instructions", func(t *testing.T) {
		if !strings.Contains(got, "discuss and scope") {
			t.Error("discuss prompt should mention 'discuss and scope'")
		}
	})
	t.Run("no exec boilerplate", func(t *testing.T) {
		if strings.Contains(got, "Execute this ticket autonomously") {
			t.Error("discuss prompt should NOT contain exec boilerplate")
		}
	})
	t.Run("ticket body in system prompt", func(t *testing.T) {
		// The body lives in the system prompt (a file), not the user message
		// (an inline shell argument) -- see docs/prompt-architecture.md.
		if !strings.Contains(got, "We need to scope this feature carefully.") {
			t.Error("discuss prompt should embed ticket body in system prompt")
		}
	})
	t.Run("has issue heading in system prompt", func(t *testing.T) {
		if !strings.Contains(got, "## Issue #7") {
			t.Error("discuss prompt should have issue heading in system prompt")
		}
	})
}

func TestBuildSystemPrompt_TriageMode(t *testing.T) {
	req := Request{
		AgentName:   "ticket-triage",
		Mode:        ModeTriage,
		Ticket:      15,
		TicketTitle: "Add feature X",
		TicketBody:  "We want feature X to do Y.",
		RepoSlug:    "blsmth/agentctl",
	}
	spec := modes[ModeTriage]
	workDir := "/tmp/test-dir"

	got := buildBoth(req, spec, workDir, "", false, nil, "")

	checks := []struct {
		desc string
		want string
	}{
		{"has preamble", "## agentctl Runtime Environment"},
		{"has Do NOT implement", "Do NOT implement"},
		{"has ticket body in system prompt", "We want feature X to do Y."},
		{"has issue heading", "## Issue #15: Add feature X"},
		{"has gh issue edit instruction", "gh issue edit <number>"},
		{"has Context section", "## Context"},
		{"has What to build section", "## What to build"},
		{"has Implementation notes section", "## Implementation notes"},
		{"has Files to change section", "## Files to change"},
		{"has Testing section", "## Testing"},
		{"has Acceptance criteria section", "## Acceptance criteria"},
	}

	for _, tc := range checks {
		t.Run(tc.desc, func(t *testing.T) {
			if !strings.Contains(got, tc.want) {
				t.Errorf("triage prompt missing %q\n\ngot (first 2000 chars):\n%s", tc.want, truncateStr(got, 2000))
			}
		})
	}

	t.Run("no exec boilerplate", func(t *testing.T) {
		if strings.Contains(got, "Execute this ticket autonomously") {
			t.Error("triage prompt should NOT contain exec boilerplate")
		}
	})
}

func TestBuildSystemPrompt_ReviewMode(t *testing.T) {
	req := Request{
		AgentName:   "go-engineer",
		Mode:        ModeReview,
		Ticket:      22,
		TicketTitle: "Review PR for widget fix",
		TicketBody:  "The widget was fixed in PR #99.",
		RepoSlug:    "blsmth/agentctl",
	}
	spec := modes[ModeReview]
	workDir := "/tmp/test-dir"

	got := buildBoth(req, spec, workDir, "", false, nil, "")

	t.Run("has preamble", func(t *testing.T) {
		if !strings.Contains(got, "## agentctl Runtime Environment") {
			t.Error("review prompt should contain preamble")
		}
	})
	t.Run("has review instructions", func(t *testing.T) {
		if !strings.Contains(got, "code review") {
			t.Error("review prompt should mention 'code review'")
		}
	})
	t.Run("no exec boilerplate", func(t *testing.T) {
		if strings.Contains(got, "Execute this ticket autonomously") {
			t.Error("review prompt should NOT contain exec boilerplate")
		}
	})
	t.Run("ticket body in system prompt", func(t *testing.T) {
		// The body lives in the system prompt (a file), not the user message
		// (an inline shell argument) -- see docs/prompt-architecture.md.
		if !strings.Contains(got, "The widget was fixed in PR #99.") {
			t.Error("review prompt should embed ticket body in system prompt")
		}
	})
}

func TestBuildSystemPrompt_CustomMode(t *testing.T) {
	customInstructions := "You are a specialized migration agent. Follow the migration runbook exactly."
	req := Request{
		AgentName:    "go-engineer",
		Mode:         ModeCustom,
		Ticket:       33,
		TicketTitle:  "Migrate database",
		TicketBody:   "Run migration step by step.",
		CustomPrompt: customInstructions,
		RepoSlug:     "blsmth/agentctl",
	}
	spec := modes[ModeCustom]
	workDir := "/tmp/test-dir"

	got := buildBoth(req, spec, workDir, "", false, nil, "")

	t.Run("has preamble", func(t *testing.T) {
		if !strings.Contains(got, "## agentctl Runtime Environment") {
			t.Error("custom prompt should contain preamble")
		}
	})
	t.Run("has custom instructions", func(t *testing.T) {
		if !strings.Contains(got, customInstructions) {
			t.Error("custom prompt should contain the caller-supplied custom instructions")
		}
	})
	t.Run("has ticket body in system prompt", func(t *testing.T) {
		if !strings.Contains(got, "Run migration step by step.") {
			t.Error("custom prompt should embed ticket body in system prompt")
		}
	})
}

func TestBuildSystemPrompt_BareMode(t *testing.T) {
	req := Request{
		AgentName: "go-engineer",
		Mode:      ModeBare,
		RepoSlug:  "blsmth/agentctl",
	}
	spec := modes[ModeBare]
	workDir := "/tmp/test-dir"

	got := buildSystemPrompt(req, spec, workDir, "", false, nil, "")

	t.Run("has preamble", func(t *testing.T) {
		if !strings.Contains(got, "## agentctl Runtime Environment") {
			t.Error("bare prompt should contain preamble")
		}
	})
	t.Run("has context store capability", func(t *testing.T) {
		if !strings.Contains(got, "agentctl context") {
			t.Error("bare prompt should mention agentctl context")
		}
	})
	t.Run("does not mention agentctl signal done", func(t *testing.T) {
		if strings.Contains(got, "agentctl signal done") {
			t.Error("bare prompt should no longer mention agentctl signal done -- done is derived from the Stop hook")
		}
	})
	t.Run("no exec boilerplate", func(t *testing.T) {
		if strings.Contains(got, "Execute this ticket autonomously") {
			t.Error("bare prompt should NOT contain exec boilerplate")
		}
	})
	t.Run("no ticket content", func(t *testing.T) {
		if strings.Contains(got, "Issue #") {
			t.Error("bare prompt should NOT contain issue heading")
		}
	})
	t.Run("no closes reference", func(t *testing.T) {
		if strings.Contains(got, "Closes #") {
			t.Error("bare prompt should NOT contain Closes #N reference")
		}
	})
	t.Run("has agent in frontmatter", func(t *testing.T) {
		if !strings.Contains(got, "- Agent: go-engineer") {
			t.Error("bare prompt frontmatter should contain agent name")
		}
	})
	t.Run("has mode in frontmatter", func(t *testing.T) {
		if !strings.Contains(got, "- Mode: bare") {
			t.Error("bare prompt frontmatter should contain mode: bare")
		}
	})
	t.Run("no worktree orientation text", func(t *testing.T) {
		// Regression guard: bare's DefaultWorktree is false, so a default bare
		// launch (useWorktree=false, as passed to buildSystemPrompt above)
		// must never surface either flavor of worktree orientation note.
		for _, want := range []string{"You are working in a git worktree", "You are in a git worktree at"} {
			if strings.Contains(got, want) {
				t.Errorf("bare prompt should NOT contain worktree orientation text, found %q", want)
			}
		}
	})
}

// TestBuildSystemPrompt_WorktreeOrientationSplit is the regression test for
// the critical fix in this ticket: an interactive orchestrator session
// (ModeBare) must NOT be told "do not merge to main" (its job includes
// running `gh pr merge`) and must NOT get the autonomous-mode finishing-steps
// sequence appended, while a fully autonomous, code-producing mode
// (ModeDispatch) must keep both verbatim.
func TestBuildSystemPrompt_WorktreeOrientationSplit(t *testing.T) {
	t.Run("ModeBare worktree prompt", func(t *testing.T) {
		req := Request{
			AgentName: "lead",
			Mode:      ModeBare,
			RepoSlug:  "blsmth/agentctl",
		}
		spec := modes[ModeBare]
		branch := "agent/lead/session-abc123"
		got := buildSystemPrompt(req, spec, "/tmp/wt", branch, true, nil, "")

		if strings.Contains(got, "Do not merge to main") {
			t.Error("ModeBare worktree prompt should NOT contain 'Do not merge to main'")
		}
		if strings.Contains(got, "The PR is the deliverable, not the commit") {
			t.Error("ModeBare worktree prompt should NOT contain the finishing-steps sequence")
		}
		for _, want := range []string{branch, "gh pr merge", "git fetch origin", "origin/main"} {
			if !strings.Contains(got, want) {
				t.Errorf("ModeBare worktree prompt missing %q", want)
			}
		}
	})

	t.Run("ModeDispatch worktree prompt", func(t *testing.T) {
		req := Request{
			AgentName:   "go-engineer",
			Mode:        ModeDispatch,
			Ticket:      42,
			TicketTitle: "Fix the widget",
			TicketBody:  "Body",
			RepoSlug:    "blsmth/agentctl",
		}
		spec := modes[ModeDispatch]
		branch := "agent/go-engineer/issue-42"
		got := buildBoth(req, spec, "/tmp/wt", branch, true, nil, "")

		if !strings.Contains(got, "Do not merge to main") {
			t.Error("ModeDispatch worktree prompt should contain 'Do not merge to main'")
		}
		if !strings.Contains(got, "The PR is the deliverable, not the commit") {
			t.Error("ModeDispatch worktree prompt should contain the finishing-steps sequence")
		}
	})
}

// TestBuildFinishingSteps covers the worktree-aware branching of
// buildFinishingSteps: worktree+ticket gets push/PR/Closes, worktree+adhoc
// gets push/PR without Closes, and no-worktree gets neither (and explicitly
// warns against pushing to main). This is the regression test for the
// original bug: dispatched agents committed and stopped, never pushing or
// opening a PR.
func TestBuildFinishingSteps(t *testing.T) {
	tests := []struct {
		name        string
		req         Request
		useWorktree bool
		branch      string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name: "worktree with ticket",
			req: Request{
				Ticket: 42,
			},
			useWorktree: true,
			branch:      "agent/go-engineer/issue-42",
			wantContain: []string{
				"gh pr create",
				"git push -u origin agent/go-engineer/issue-42",
				"Closes #42",
				"--body-file",
			},
		},
		{
			name: "worktree adhoc (no ticket)",
			req: Request{
				Ticket: 0,
			},
			useWorktree: true,
			branch:      "agent/go-engineer/adhoc-abc123",
			wantContain: []string{
				"gh pr create",
				"git push -u origin agent/go-engineer/adhoc-abc123",
			},
			wantAbsent: []string{"Closes #"},
		},
		{
			name: "no worktree",
			req: Request{
				Ticket: 42,
			},
			useWorktree: false,
			branch:      "",
			wantAbsent: []string{
				"git push",
				"gh pr create",
			},
			wantContain: []string{"main"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildFinishingSteps(tc.req, tc.branch, tc.useWorktree)
			for _, want := range tc.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("buildFinishingSteps() missing %q, got:\n%s", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("buildFinishingSteps() should not contain %q, got:\n%s", absent, got)
				}
			}
			// Never hardcode a language or a repo-specific deploy/restart step.
			for _, forbidden := range []string{"go build", "just restart"} {
				if strings.Contains(got, forbidden) {
					t.Errorf("buildFinishingSteps() should never mention %q, got:\n%s", forbidden, got)
				}
			}
		})
	}
}

// TestBuildSystemPrompt_FinishingStepsIntegration verifies the finishing
// steps are actually wired into the composed system prompt for the two
// autonomous modes that need them (dispatch, adhoc), with the no-worktree
// case exercised separately since getting it wrong risks a push to main.
func TestBuildSystemPrompt_FinishingStepsIntegration(t *testing.T) {
	t.Run("dispatch worktree with ticket", func(t *testing.T) {
		req := Request{
			AgentName:   "go-engineer",
			Mode:        ModeDispatch,
			Ticket:      42,
			TicketTitle: "Fix the widget",
			TicketBody:  "Body",
			RepoSlug:    "blsmth/agentctl",
		}
		got := buildFirstMessage(req, modes[ModeDispatch], "/tmp/wt", "agent/go-engineer/issue-42", true, nil, "")
		for _, want := range []string{"gh pr create", "git push", "Closes #42"} {
			if !strings.Contains(got, want) {
				t.Errorf("dispatch+worktree prompt missing %q", want)
			}
		}
	})

	t.Run("adhoc worktree without ticket", func(t *testing.T) {
		req := Request{
			AgentName:    "go-engineer",
			Mode:         ModeAdhoc,
			CustomPrompt: "do the thing",
			RepoSlug:     "blsmth/agentctl",
		}
		got := buildFirstMessage(req, modes[ModeAdhoc], "/tmp/wt", "agent/go-engineer/adhoc-xyz", true, nil, "")
		if !strings.Contains(got, "gh pr create") {
			t.Error("adhoc+worktree prompt missing 'gh pr create'")
		}
		if !strings.Contains(got, "git push") {
			t.Error("adhoc+worktree prompt missing 'git push'")
		}
		if strings.Contains(got, "Closes #") {
			t.Error("adhoc+worktree prompt (no ticket) should not contain 'Closes #'")
		}
	})

	t.Run("no worktree never mentions push or PR", func(t *testing.T) {
		req := Request{
			AgentName:   "go-engineer",
			Mode:        ModeDispatch,
			Ticket:      42,
			TicketTitle: "Fix the widget",
			TicketBody:  "Body",
			RepoSlug:    "blsmth/agentctl",
		}
		got := buildFirstMessage(req, modes[ModeDispatch], "/tmp/dir", "", false, nil, "")
		if strings.Contains(got, "git push") {
			t.Error("no-worktree prompt should never mention 'git push'")
		}
		if strings.Contains(got, "gh pr create") {
			t.Error("no-worktree prompt should never mention 'gh pr create'")
		}
	})
}

// TestBuildSystemPrompt_NoHardcodedToolchain is a regression test across
// every mode: the composed prompt must never hardcode a language-specific
// build tool or a repo-specific deploy/restart command. Those belong to the
// agent's own .md, not internal/dispatch, which is shared by every agent and
// every repo.
func TestBuildSystemPrompt_NoHardcodedToolchain(t *testing.T) {
	tests := []struct {
		req         Request
		useWorktree bool
	}{
		{req: Request{AgentName: "go-engineer", Mode: ModeDispatch, Ticket: 1, TicketTitle: "T", TicketBody: "B", RepoSlug: "blsmth/agentctl"}, useWorktree: true},
		{req: Request{AgentName: "go-engineer", Mode: ModeDispatch, Ticket: 1, TicketTitle: "T", TicketBody: "B", RepoSlug: "blsmth/agentctl"}, useWorktree: false},
		{req: Request{AgentName: "go-engineer", Mode: ModeAdhoc, CustomPrompt: "do stuff", RepoSlug: "blsmth/agentctl"}, useWorktree: true},
		{req: Request{AgentName: "go-engineer", Mode: ModeAdhoc, CustomPrompt: "do stuff", RepoSlug: "blsmth/agentctl"}, useWorktree: false},
		{req: Request{AgentName: "go-engineer", Mode: ModeDiscuss, Ticket: 1, TicketTitle: "T", TicketBody: "B", RepoSlug: "blsmth/agentctl"}, useWorktree: false},
		{req: Request{AgentName: "go-engineer", Mode: ModeTriage, Ticket: 1, TicketTitle: "T", TicketBody: "B", RepoSlug: "blsmth/agentctl"}, useWorktree: false},
		{req: Request{AgentName: "go-engineer", Mode: ModeReview, Ticket: 1, TicketTitle: "T", TicketBody: "B", RepoSlug: "blsmth/agentctl"}, useWorktree: false},
		{req: Request{AgentName: "go-engineer", Mode: ModeCustom, CustomPrompt: "custom", RepoSlug: "blsmth/agentctl"}, useWorktree: false},
		{req: Request{AgentName: "go-engineer", Mode: ModeBare, RepoSlug: "blsmth/agentctl"}, useWorktree: false},
	}

	for _, tc := range tests {
		spec := modes[tc.req.Mode]
		got := buildBoth(tc.req, spec, "/tmp/dir", "agent/go-engineer/issue-1", tc.useWorktree, nil, "")
		for _, forbidden := range []string{"go build", "just restart"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("mode %s: composed prompt should never mention %q", tc.req.Mode, forbidden)
			}
		}
	}
}

// truncateStr is a test helper.
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func TestPreamble_Orchestrator(t *testing.T) {
	t.Run("worker with orchestrator: stable section, location left to the launch facts", func(t *testing.T) {
		o := Orchestrator{WindowID: "@26", Pane: "%77", Name: "brandon-proj-3"}
		got := preambleWithOrchestrator(RoleWorker, o)
		for _, want := range []string{"### Your orchestrator", "launch facts in your first message", "`@N.%P`", "ignore the tmux session-name prefix", "hint only", "same weight as this prompt", "SendMessage", "orchestrator is gone", "PR body"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
		if got != preambleWithOrchestrator(RoleWorker, Orchestrator{WindowID: "@1"}) {
			t.Error("the orchestrator section varies with the orchestrator")
		}
		for _, perLaunch := range []string{"@26", "%77", "brandon-proj-3"} {
			if strings.Contains(got, perLaunch) {
				t.Errorf("preamble contains per-launch %q", perLaunch)
			}
		}
	})
	t.Run("orchestrator fact", func(t *testing.T) {
		tests := []struct {
			o    Orchestrator
			want string
		}{
			{Orchestrator{WindowID: "@26", Pane: "%77", Name: "brandon-proj-3"}, "- Orchestrator: tmux window @26, pane %77 (`@26.%77`); its session name was brandon-proj-3 at launch"},
			{Orchestrator{WindowID: "@26", Pane: "%77"}, "- Orchestrator: tmux window @26, pane %77 (`@26.%77`)"},
			{Orchestrator{WindowID: "@26"}, "- Orchestrator: tmux window @26"},
		}
		for _, tt := range tests {
			if got := orchestratorFact(tt.o); got != tt.want {
				t.Errorf("orchestratorFact(%+v) = %q, want %q", tt.o, got, tt.want)
			}
		}
	})
	t.Run("bare launch gets only generic guidance", func(t *testing.T) {
		if got := preambleWithOrchestrator(RoleWorker, Orchestrator{}); got != WorkerPreamble {
			t.Error("no orchestrator should yield the plain worker preamble")
		}
		if got := preambleWithOrchestrator(RoleOrchestrator, Orchestrator{WindowID: "@1"}); got != OrchestratorPreamble {
			t.Error("orchestrator-role sessions never get the orchestrator section")
		}
	})
}

func TestFinishingSteps_ReportToOrchestrator(t *testing.T) {
	orch := Orchestrator{WindowID: "@26"}
	with := buildFinishingSteps(Request{Orchestrator: orch}, "agent/x/issue-1", true)
	if !strings.Contains(with, "7. SendMessage to your orchestrator (window @26) with the PR URL") {
		t.Errorf("worktree steps missing report step:\n%s", with)
	}
	without := buildFinishingSteps(Request{}, "agent/x/issue-1", true)
	if strings.Contains(without, "SendMessage") {
		t.Errorf("no orchestrator: steps must not mention SendMessage:\n%s", without)
	}
	shared := buildFinishingSteps(Request{Orchestrator: orch}, "", false)
	if !strings.Contains(shared, "SendMessage to your orchestrator (window @26)") {
		t.Errorf("shared-dir steps missing report:\n%s", shared)
	}
}

func TestOrchestratorQuickReference(t *testing.T) {
	groups := []string{"### Delegate", "### Watch", "### Wait", "### Clean up", "### Context for workers", "### Other"}
	for name, p := range map[string]string{
		"messaging":    OrchestratorPreamble,
		"no messaging": preambleForHarness(RoleOrchestrator, false),
	} {
		for _, g := range groups {
			if !strings.Contains(p, g) {
				t.Errorf("%s: missing group %q", name, g)
			}
		}
		for _, want := range []string{
			"agentctl status list", "agentctl status peek", "--dir <repo>", "gh pr checks <N> --watch",
			"exit code 8", "agentctl sweep --dry-run", "agentctl cleanup --yes", "WITHOUT `--agent`",
			"agentctl context clear", "agentctl account list", "agentctl cost",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("%s: missing %q", name, want)
			}
		}
	}

	nm := preambleForHarness(RoleOrchestrator, false)
	for _, bad := range []string{"SendMessage", "ListAgents"} {
		if strings.Contains(nm, bad) {
			t.Errorf("no-messaging orchestrator preamble mentions %s", bad)
		}
	}
	if !strings.Contains(OrchestratorPreamble, "SendMessage") {
		t.Error("messaging orchestrator preamble should mention SendMessage in Wait")
	}
}

func TestWorkerPreambleContextClearAndWhoami(t *testing.T) {
	for name, p := range map[string]string{
		"messaging":    WorkerPreamble,
		"no messaging": preambleForHarness(RoleWorker, false),
	} {
		for _, want := range []string{"agentctl context clear <key>", "agentctl status whoami"} {
			if !strings.Contains(p, want) {
				t.Errorf("%s worker preamble missing %q", name, want)
			}
		}
		for _, bad := range []string{"agentctl dispatch", "agentctl sweep", "### Delegate"} {
			if strings.Contains(p, bad) {
				t.Errorf("%s worker preamble must not contain %q", name, bad)
			}
		}
	}
}

// TestExtraInstructions: the user's additional instructions are per launch,
// so they go in the first message (after the task, before the launch facts
// and finishing steps) and never change the system file.
func TestExtraInstructions(t *testing.T) {
	for _, mode := range []Mode{ModeAdhoc, ModeBare, ModeDispatch} {
		t.Run(string(mode), func(t *testing.T) {
			req := Request{AgentName: "go-engineer", Mode: mode, Ticket: 1, TicketBody: "b", CustomPrompt: "do it", RepoSlug: "o/r"}
			if mode != ModeDispatch {
				req.Ticket, req.TicketBody = 0, ""
			}
			spec := modes[mode]
			base := buildLaunchPrompt(req, spec, testFacts("/tmp/wt", "agent/x/adhoc-1", true), nil, "")

			req.ExtraInstructions = "   \n"
			if got := buildLaunchPrompt(req, spec, testFacts("/tmp/wt", "agent/x/adhoc-1", true), nil, ""); got != base {
				t.Error("blank instructions changed the prompt")
			}
			if strings.Contains(base.Message, AdditionalInstructionsHeading) {
				t.Error("heading present with no instructions")
			}

			req.ExtraInstructions = "  Prefer small commits.\nNo emojis.\n"
			got := buildLaunchPrompt(req, spec, testFacts("/tmp/wt", "agent/x/adhoc-1", true), nil, "")
			if got.System != base.System {
				t.Error("instructions changed the system file")
			}
			section := AdditionalInstructionsHeading + "\n\nPrefer small commits.\nNo emojis."
			i := strings.Index(got.Message, section)
			if i < 0 {
				t.Fatalf("message missing the section:\n%s", got.Message)
			}
			if f := strings.Index(got.Message, "## Launch facts"); f >= 0 && f < i {
				t.Error("instructions come after the launch facts")
			}
		})
	}
}
