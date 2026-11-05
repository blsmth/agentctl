package dispatch

import (
	"strings"
	"testing"

	ctxstore "github.com/blsmth/agentctl/internal/context"
)

func TestBuildSystemPrompt_IssueDispatch(t *testing.T) {
	req := Request{
		AgentName:   "go-engineer",
		Mode:        ModeDispatch,
		Ticket:      42,
		TicketTitle: "Fix the widget",
		TicketBody:  "The widget is broken.\n\nPlease fix it.",
		RepoSlug:    "blsmth/agentctl",
	}
	spec := modes[ModeDispatch]
	workDir := "/tmp/test-worktree"
	branch := "agent/go-engineer/issue-42"

	got := buildSystemPrompt(req, spec, workDir, branch, true, nil, "")

	checks := []struct {
		desc string
		want string
	}{
		{"has frontmatter start", "---"},
		{"has ticket in frontmatter", `ticket: "#42"`},
		{"has title in frontmatter", `title: "Fix the widget"`},
		{"has agent in frontmatter", "agent: go-engineer"},
		{"has mode in frontmatter", "mode: dispatch"},
		{"has preamble", "## agentctl Runtime Environment"},
		{"has context store capability", "agentctl context"},
		{"has issue heading", "## Issue #42: Fix the widget"},
		{"has issue body", "The widget is broken."},
		{"has exec instructions", "Execute this ticket autonomously"},
		{"has closes reference", "Closes #42"},
		{"has worktree note", "git worktree on branch agent/go-engineer/issue-42"},
		{"has branch note", "agent/go-engineer/issue-42"},
		{"has gh pr create", "gh pr create"},
		{"has git push", "git push -u origin agent/go-engineer/issue-42"},
	}

	for _, tc := range checks {
		t.Run(tc.desc, func(t *testing.T) {
			if !strings.Contains(got, tc.want) {
				t.Errorf("system prompt missing %q\n\ngot (first 2000 chars):\n%s", tc.want, truncateStr(got, 2000))
			}
		})
	}

	// Verify ordering: preamble comes after frontmatter, exec instructions come after preamble.
	t.Run("order: frontmatter before preamble", func(t *testing.T) {
		fmIdx := strings.Index(got, `ticket: "#42"`)
		pIdx := strings.Index(got, "## agentctl Runtime Environment")
		if fmIdx >= pIdx {
			t.Error("frontmatter should appear before preamble")
		}
	})
	t.Run("order: preamble before exec instructions", func(t *testing.T) {
		pIdx := strings.Index(got, "## agentctl Runtime Environment")
		eIdx := strings.Index(got, "Execute this ticket autonomously")
		if pIdx >= eIdx {
			t.Error("preamble should appear before exec instructions")
		}
	})
}

func TestBuildSystemPrompt_AdHocDispatch(t *testing.T) {
	req := Request{
		AgentName:    "go-engineer",
		Mode:         ModeAdhoc,
		CustomPrompt: strings.Repeat("x", 600), // long prompt goes into system prompt
		RepoSlug:     "blsmth/agentctl",
	}
	spec := modes[ModeAdhoc]
	workDir := "/tmp/test-dir"

	got := buildSystemPrompt(req, spec, workDir, "", false, nil, "")

	t.Run("no issue zero", func(t *testing.T) {
		if strings.Contains(got, "Issue #0") {
			t.Error("ad-hoc prompt should not contain 'Issue #0'")
		}
	})
	t.Run("no ticket in frontmatter", func(t *testing.T) {
		if strings.Contains(got, "ticket:") {
			t.Error("ad-hoc frontmatter should not contain 'ticket:'")
		}
	})
	t.Run("has preamble", func(t *testing.T) {
		if !strings.Contains(got, "## agentctl Runtime Environment") {
			t.Error("ad-hoc prompt should contain preamble")
		}
	})
	t.Run("has exec instructions", func(t *testing.T) {
		if !strings.Contains(got, "Execute this t") {
			t.Error("ad-hoc prompt should contain exec instructions")
		}
	})
	t.Run("has task section for long prompt", func(t *testing.T) {
		if !strings.Contains(got, "## Task") {
			t.Error("long ad-hoc prompt should be embedded as ## Task section")
		}
	})
	t.Run("commit step has no Closes ref", func(t *testing.T) {
		if strings.Contains(got, "Closes #") {
			t.Error("ad-hoc prompt should not reference Closes #N")
		}
	})
}

func TestBuildSystemPrompt_NoWorktree(t *testing.T) {
	req := Request{
		AgentName:   "go-engineer",
		Mode:        ModeDispatch,
		Ticket:      10,
		TicketTitle: "Test",
		TicketBody:  "Body",
	}
	spec := modes[ModeDispatch]

	got := buildSystemPrompt(req, spec, "/tmp/dir", "", false, nil, "")

	if strings.Contains(got, "git worktree at") {
		t.Error("prompt should not contain worktree note when useWorktree is false")
	}
	if strings.Contains(got, "git worktree on branch") {
		t.Error("prompt should not contain worktree note when useWorktree is false")
	}
}

func TestBuildSystemPrompt_PreamblePresent(t *testing.T) {
	req := Request{
		AgentName:   "go-engineer",
		Mode:        ModeDispatch,
		Ticket:      1,
		TicketTitle: "T",
		TicketBody:  "B",
	}
	spec := modes[ModeDispatch]

	got := buildSystemPrompt(req, spec, "/tmp/dir", "", false, nil, "")

	t.Run("contains agentctl context", func(t *testing.T) {
		if !strings.Contains(got, "agentctl context") {
			t.Error("preamble should mention 'agentctl context'")
		}
	})
	t.Run("does not mention agentctl signal done", func(t *testing.T) {
		if strings.Contains(got, "agentctl signal done") {
			t.Error("preamble should no longer mention 'agentctl signal done' -- done is derived from the Stop hook")
		}
	})
}

func TestBuildSystemPrompt_WithContextEntries(t *testing.T) {
	req := Request{
		AgentName:   "go-engineer",
		Mode:        ModeDispatch,
		Ticket:      5,
		TicketTitle: "Test",
		TicketBody:  "Body",
	}
	spec := modes[ModeDispatch]

	entries := []ctxstore.Entry{
		{Key: "test-key", Content: "test-value"},
	}

	got := buildSystemPrompt(req, spec, "/tmp/dir", "", false, entries, "")

	if !strings.Contains(got, "## Shared Context") {
		t.Error("prompt should contain '## Shared Context' when context entries are provided")
	}
	if !strings.Contains(got, "test-key") {
		t.Error("prompt should contain the context entry key")
	}
}

func TestBuildFrontmatter_Issue(t *testing.T) {
	req := Request{
		AgentName:   "go-engineer",
		Mode:        ModeDispatch,
		Ticket:      42,
		TicketTitle: "Fix the widget",
		RepoSlug:    "blsmth/agentctl",
	}

	got := buildFrontmatter(req, "/tmp/dir")

	for _, want := range []string{
		"---",
		`ticket: "#42"`,
		`title: "Fix the widget"`,
		"agent: go-engineer",
		"repo: blsmth/agentctl",
		"started:",
		"mode: dispatch",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("frontmatter missing %q, got:\n%s", want, got)
		}
	}
}

func TestBuildFrontmatter_AdHoc(t *testing.T) {
	req := Request{
		AgentName: "go-engineer",
		Mode:      ModeAdhoc,
		RepoSlug:  "blsmth/agentctl",
	}

	got := buildFrontmatter(req, "/tmp/dir")

	if strings.Contains(got, "ticket:") {
		t.Errorf("ad-hoc frontmatter should not contain 'ticket:', got:\n%s", got)
	}
	if strings.Contains(got, "title:") {
		t.Errorf("ad-hoc frontmatter should not contain 'title:', got:\n%s", got)
	}
	for _, want := range []string{"agent: go-engineer", "started:", "mode: adhoc", "---"} {
		if !strings.Contains(got, want) {
			t.Errorf("ad-hoc frontmatter missing %q, got:\n%s", want, got)
		}
	}
}

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
// (dispatch/wake), and the worker variant must NOT.
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

	t.Run("orchestrator preamble contains dispatch and wake", func(t *testing.T) {
		if !strings.Contains(OrchestratorPreamble, "agentctl dispatch") {
			t.Error("OrchestratorPreamble missing 'agentctl dispatch'")
		}
		if !strings.Contains(OrchestratorPreamble, "agentctl wake") {
			t.Error("OrchestratorPreamble missing 'agentctl wake'")
		}
	})

	t.Run("worker preamble omits orchestrator-only dispatch guidance", func(t *testing.T) {
		if strings.Contains(WorkerPreamble, "agentctl dispatch") {
			t.Error("WorkerPreamble should not contain 'agentctl dispatch' delegation guidance")
		}
		if strings.Contains(WorkerPreamble, "### Delegation") {
			t.Error("WorkerPreamble should not contain the Delegation section")
		}
		// wake is part of the shared messaging section, not delegation --
		// workers should still be able to wake another agent's mailbox.
		if !strings.Contains(WorkerPreamble, "agentctl wake") {
			t.Error("WorkerPreamble should still contain 'agentctl wake' (shared messaging, not delegation)")
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

	got := buildSystemPrompt(req, spec, workDir, "", false, nil, "")

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

	got := buildSystemPrompt(req, spec, workDir, "", false, nil, "")

	checks := []struct {
		desc string
		want string
	}{
		{"has preamble", "## agentctl Runtime Environment"},
		{"has Do NOT implement", "Do NOT implement"},
		{"has ticket body in system prompt", "We want feature X to do Y."},
		{"has issue heading", "## Issue #15: Add feature X"},
		{"has gh issue edit instruction", "gh issue edit 15"},
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

	got := buildSystemPrompt(req, spec, workDir, "", false, nil, "")

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

	got := buildSystemPrompt(req, spec, workDir, "", false, nil, "")

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

func TestDiscussMode_DefaultUserMessage(t *testing.T) {
	req := Request{
		Ticket:      7,
		TicketTitle: "Scope this feature",
		TicketBody:  "Body content here.",
	}
	spec := modes[ModeDiscuss]
	msg := spec.DefaultUserMessage(req)

	if !strings.Contains(msg, "Discuss issue #7") {
		t.Errorf("discuss user message should reference the issue number, got: %s", msg)
	}
	// The user message is passed as an inline shell argument -- the body must
	// stay in the system prompt file (see docs/prompt-architecture.md).
	if strings.Contains(msg, "Body content here.") {
		t.Errorf("discuss user message should NOT embed the ticket body, got: %s", msg)
	}
}

func TestTriageMode_DefaultUserMessage(t *testing.T) {
	req := Request{Ticket: 15}
	spec := modes[ModeTriage]
	msg := spec.DefaultUserMessage(req)

	if !strings.Contains(msg, "Triage #15") {
		t.Errorf("triage user message should reference ticket number, got: %s", msg)
	}
}

func TestReviewMode_DefaultUserMessage(t *testing.T) {
	req := Request{
		Ticket:      22,
		TicketTitle: "Fix widget",
		TicketBody:  "Widget body.",
	}
	spec := modes[ModeReview]
	msg := spec.DefaultUserMessage(req)

	if !strings.Contains(msg, "Review issue #22") {
		t.Errorf("review user message should reference issue number, got: %s", msg)
	}
	// The user message is passed as an inline shell argument -- the body must
	// stay in the system prompt file (see docs/prompt-architecture.md).
	if strings.Contains(msg, "Widget body.") {
		t.Errorf("review user message should NOT embed the ticket body, got: %s", msg)
	}
}

// TestDefaultUserMessage_NeverEmbedsTicketBody is a regression test across
// every mode: DefaultUserMessage is delivered as an inline CLI argument (see
// dispatch.go Launch, step 10), so embedding a large ticket body in it risks
// "shell argument too long, claude never launches" -- the exact failure
// docs/prompt-architecture.md documents as already fixed once. It was still
// live for ModeDiscuss and ModeReview.
func TestDefaultUserMessage_NeverEmbedsTicketBody(t *testing.T) {
	const sentinelBody = "SENTINEL-TICKET-BODY-MUST-NEVER-APPEAR-IN-A-SHELL-ARGUMENT"

	for mode, spec := range modes {
		t.Run(string(mode), func(t *testing.T) {
			req := Request{
				Ticket:      99,
				TicketTitle: "Sentinel ticket",
				TicketBody:  sentinelBody,
			}
			msg := spec.DefaultUserMessage(req)
			if strings.Contains(msg, sentinelBody) {
				t.Errorf("mode %s: DefaultUserMessage embeds the full ticket body, got: %s", mode, msg)
			}
		})
	}
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
		if strings.Contains(got, "ticket:") {
			t.Error("bare prompt frontmatter should NOT contain ticket field")
		}
	})
	t.Run("no closes reference", func(t *testing.T) {
		if strings.Contains(got, "Closes #") {
			t.Error("bare prompt should NOT contain Closes #N reference")
		}
	})
	t.Run("has agent in frontmatter", func(t *testing.T) {
		if !strings.Contains(got, "agent: go-engineer") {
			t.Error("bare prompt frontmatter should contain agent name")
		}
	})
	t.Run("has mode in frontmatter", func(t *testing.T) {
		if !strings.Contains(got, "mode: bare") {
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
		got := buildSystemPrompt(req, spec, "/tmp/wt", branch, true, nil, "")

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
		got := buildSystemPrompt(req, modes[ModeDispatch], "/tmp/wt", "agent/go-engineer/issue-42", true, nil, "")
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
		got := buildSystemPrompt(req, modes[ModeAdhoc], "/tmp/wt", "agent/go-engineer/adhoc-xyz", true, nil, "")
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
		got := buildSystemPrompt(req, modes[ModeDispatch], "/tmp/dir", "", false, nil, "")
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
		got := buildSystemPrompt(tc.req, spec, "/tmp/dir", "agent/go-engineer/issue-1", tc.useWorktree, nil, "")
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

func TestTaskSynopsis(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{
			name:   "skips ## Problem heading for following prose",
			prompt: "## Problem\n\nThe monitor never detects idle agents correctly.\n\nMore detail here.",
			want:   "The monitor never detects idle agents correctly.",
		},
		{
			name:   "falls back to heading when nothing else usable",
			prompt: "## Task",
			want:   "Task",
		},
		{
			name:   "skips leading blank lines and a rule",
			prompt: "\n\n---\n\nDo the thing well.",
			want:   "Do the thing well.",
		},
		{
			name:   "strips bullet marker",
			prompt: "- Fix the widget before it breaks.",
			want:   "Fix the widget before it breaks.",
		},
		{
			name:   "strips bold wrapper",
			prompt: "**Fix the widget before it breaks.**",
			want:   "Fix the widget before it breaks.",
		},
		{
			name:   "collapses internal whitespace",
			prompt: "Fix   the    widget\tbefore  it breaks.",
			want:   "Fix the widget before it breaks.",
		},
		{
			name:   "empty input returns empty",
			prompt: "",
			want:   "",
		},
		{
			name:   "whitespace-only input returns empty",
			prompt: "   \n\t\n   ",
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := taskSynopsis(tc.prompt)
			if got != tc.want {
				t.Errorf("taskSynopsis(%q) = %q, want %q", tc.prompt, got, tc.want)
			}
		})
	}

	t.Run("truncates long first line with ellipsis", func(t *testing.T) {
		long := strings.Repeat("word ", 40) // way over synopsisWidth
		got := taskSynopsis(long)
		if len([]rune(got)) > synopsisWidth {
			t.Errorf("taskSynopsis result exceeds synopsisWidth: %q (%d runes)", got, len([]rune(got)))
		}
		runes := []rune(got)
		if len(runes) == 0 || runes[len(runes)-1] != '…' {
			t.Errorf("taskSynopsis result should end with ellipsis rune, got %q", got)
		}
	})
}

func TestAdhocMode_DefaultUserMessage(t *testing.T) {
	spec := modes[ModeAdhoc]

	t.Run("long prompt: has synopsis and Task pointer, no old boilerplate", func(t *testing.T) {
		req := Request{
			Mode:         ModeAdhoc,
			CustomPrompt: "## Problem\n\nFix the monitor's idle detection logic.\n\nMore context follows in the rest of this very long prompt that exceeds the long prompt threshold by quite a lot of padding text to push it well past five hundred characters in total length, which is required for this test scenario to exercise the long-prompt code path in DefaultUserMessage as intended by the original ticket description above.",
		}
		msg := spec.DefaultUserMessage(req)

		if !strings.Contains(msg, "Fix the monitor's idle detection logic.") {
			t.Errorf("message missing synopsis, got: %q", msg)
		}
		if !strings.Contains(msg, `## Task`) {
			t.Errorf("message missing '## Task' pointer, got: %q", msg)
		}
		if strings.Contains(msg, "Read it, execute it.") {
			t.Errorf("message should not contain the old boilerplate sentence, got: %q", msg)
		}
	})

	t.Run("prompt key set: message contains the key", func(t *testing.T) {
		req := Request{
			Mode:         ModeAdhoc,
			CustomPrompt: "Do the thing.",
			PromptKey:    "my-spec-key",
		}
		msg := spec.DefaultUserMessage(req)
		if !strings.Contains(msg, "my-spec-key") {
			t.Errorf("message missing prompt key, got: %q", msg)
		}
	})

	t.Run("empty custom prompt falls back to non-empty message", func(t *testing.T) {
		req := Request{Mode: ModeAdhoc}
		msg := spec.DefaultUserMessage(req)
		if strings.TrimSpace(msg) == "" {
			t.Error("message should never be empty")
		}
	})
}

func TestDispatchMode_DefaultUserMessage(t *testing.T) {
	spec := modes[ModeDispatch]

	t.Run("with ticket title", func(t *testing.T) {
		req := Request{Mode: ModeDispatch, Ticket: 42, TicketTitle: "Fix monitor idle detection"}
		msg := spec.DefaultUserMessage(req)
		if !strings.Contains(msg, "Implement #42: Fix monitor idle detection.") {
			t.Errorf("message missing title, got: %q", msg)
		}
	})

	t.Run("without ticket title", func(t *testing.T) {
		req := Request{Mode: ModeDispatch, Ticket: 42}
		msg := spec.DefaultUserMessage(req)
		if !strings.Contains(msg, "Implement #42.") {
			t.Errorf("message missing bare form, got: %q", msg)
		}
	})
}

func TestCustomMode_DefaultUserMessage(t *testing.T) {
	spec := modes[ModeCustom]

	t.Run("with ticket", func(t *testing.T) {
		req := Request{Mode: ModeCustom, Ticket: 7, TicketTitle: "Migrate database"}
		msg := spec.DefaultUserMessage(req)
		if !strings.Contains(msg, "Issue #7: Migrate database") {
			t.Errorf("message missing ticket reference, got: %q", msg)
		}
	})

	t.Run("without ticket, with custom prompt synopsis", func(t *testing.T) {
		req := Request{Mode: ModeCustom, CustomPrompt: "## Problem\n\nRun the migration runbook exactly."}
		msg := spec.DefaultUserMessage(req)
		if !strings.Contains(msg, "Run the migration runbook exactly.") {
			t.Errorf("message missing synopsis, got: %q", msg)
		}
	})

	t.Run("without ticket and empty prompt falls back", func(t *testing.T) {
		req := Request{Mode: ModeCustom}
		msg := spec.DefaultUserMessage(req)
		if msg != "Your instructions are in the system prompt." {
			t.Errorf("message should fall back to the plain sentence, got: %q", msg)
		}
	})
}
