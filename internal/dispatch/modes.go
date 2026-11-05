package dispatch

import (
	"fmt"
	"strings"
)

// Mode identifies the dispatch launch mode.
type Mode string

const (
	ModeDispatch Mode = "dispatch"
	ModeAdhoc    Mode = "adhoc"
	ModeDiscuss  Mode = "discuss"
	ModeTriage   Mode = "triage"
	ModeReview   Mode = "review"
	ModeCustom   Mode = "custom"
	// ModeBare is used when launching an agent interactively with no ticket, no
	// autonomous execution instructions, and no initial user message. The agent
	// receives the preamble so it knows about agentctl capabilities, but is
	// otherwise started fresh and waits for the user.
	ModeBare Mode = "bare"
)

// modeSpec defines per-mode prompt composition and behavior.
type modeSpec struct {
	// SystemInstructions returns the mode-specific instructions block that
	// appears after the preamble in the system prompt.
	SystemInstructions func(req Request) string

	// DefaultUserMessage returns the first user message sent to the agent.
	DefaultUserMessage func(req Request) string

	// Autonomous indicates whether the agent runs without human interaction.
	Autonomous bool

	// IncludeTicketBody controls whether the issue body is embedded in the
	// system prompt (true for dispatch, false for adhoc).
	IncludeTicketBody bool

	// IncludeFinishingSteps controls whether buildFinishingSteps' ordered
	// verify -> commit -> push -> PR sequence is appended to the system
	// prompt. True only for the fully autonomous, code-producing modes
	// (dispatch, adhoc) -- modes like triage and review explicitly forbid
	// implementing/committing, and discuss/custom/bare expect a human to
	// decide when work is "done", so appending finishing steps there would
	// contradict their own instructions.
	IncludeFinishingSteps bool

	// DefaultModel is the model this mode prefers when the agent config
	// doesn't specify one. Empty means inherit from agent config.
	DefaultModel string

	// Role selects which preamble variant (RoleWorker or RoleOrchestrator)
	// the mode's system prompt gets. See preamble.go. Zero value (empty
	// string) resolves to RoleWorker via preambleFor.
	Role Role

	// DefaultWorktree is the mode's default answer to "should this launch get
	// an isolated git worktree?" Launch() resolves the effective useWorktree
	// value from this default, overridden by an explicit Request.UseWorktree
	// pointer -- see Launch's doc comment. Every mode defaults to true: a
	// worktree protects every launch (including interactive ones) from
	// concurrent-session collisions (git pull/stash/dirty-tree stomping) in
	// the shared checkout. Callers that genuinely need the main checkout
	// (e.g. internal/improve editing an agent definition in place) opt out
	// explicitly via a non-nil Request.UseWorktree pointer, not by relying on
	// a mode default.
	DefaultWorktree bool
}

var modes = map[Mode]modeSpec{
	ModeDispatch: {
		SystemInstructions: dispatchInstructions,
		DefaultUserMessage: func(req Request) string {
			if req.TicketTitle != "" {
				return fmt.Sprintf("Implement #%d: %s. The spec is in your system prompt — read it and open a PR when done.", req.Ticket, req.TicketTitle)
			}
			return fmt.Sprintf("Implement #%d. The spec is in your system prompt — read it and open a PR when done.", req.Ticket)
		},
		Autonomous:            true,
		IncludeTicketBody:     true,
		IncludeFinishingSteps: true,
		DefaultModel:          "sonnet",
		Role:                  RoleWorker,
		DefaultWorktree:       true,
	},
	ModeAdhoc: {
		SystemInstructions: adhocInstructions,
		DefaultUserMessage: func(req Request) string {
			synopsis := taskSynopsis(req.CustomPrompt)
			if synopsis == "" && req.PromptKey == "" {
				return "Your task is in the system prompt. Read it, execute it."
			}

			var lines []string
			if synopsis != "" {
				lines = append(lines, "Task: "+synopsis)
			}
			if req.PromptKey != "" {
				lines = append(lines, fmt.Sprintf("Source: context key `%s`", req.PromptKey))
			}
			lines = append(lines, `Full instructions are in your system prompt under "## Task". Read it and execute.`)
			return strings.Join(lines, "\n")
		},
		Autonomous:            true,
		IncludeTicketBody:     false,
		IncludeFinishingSteps: true,
		DefaultModel:          "",
		Role:                  RoleWorker,
		DefaultWorktree:       true,
	},
	ModeDiscuss: {
		SystemInstructions: func(_ Request) string {
			return `You have been given a GitHub issue to discuss and scope. Do NOT start implementing it yet.

Help the user understand this ticket, ask clarifying questions, suggest an implementation approach, and help refine the scope. Think of yourself as a technical collaborator — not an executor.

When the user is satisfied with the plan and says to proceed, THEN you can start implementing.`
		},
		DefaultUserMessage: func(req Request) string {
			return fmt.Sprintf("Discuss issue #%d: %s. The full issue is in your system prompt.", req.Ticket, req.TicketTitle)
		},
		Autonomous:        false,
		IncludeTicketBody: true,
		DefaultModel:      "sonnet",
		// Discuss is a scoping conversation with the human, not a delegation
		// hub -- it does not itself dispatch other agents.
		Role:            RoleWorker,
		DefaultWorktree: true,
	},
	ModeTriage: {
		SystemInstructions: func(req Request) string {
			repo := req.RepoSlug
			if repo == "" {
				repo = "this repository"
			}
			return fmt.Sprintf(`You are triaging this ticket for %s.

Your job: make this ticket ready to hand to an engineer. Do NOT implement anything.

## Process

1. Read the source files the issue references. Understand current state and constraints.
2. Assess scope: quick fix (1 file, <30 min) or real feature (multi-file, design needed)?
3. Fill gaps: add exact file paths, function signatures, edge cases, test requirements.
4. Update the issue using "gh issue edit %d --body-file <file>". Use the format below. Apply the best-fit labels from those that already exist in the repo — run `+"`gh label list`"+` to see them; do not invent new ones.

## Issue format

Structure the updated issue body like this:

## Context
<1-2 sentences: what problem this solves and why it matters>

## What to build
<bullet list of concrete deliverables>

## Implementation notes
<relevant files, functions, or patterns from the codebase>
- `+"`file.go`"+` — relevant because ...

## Files to change
<exact file paths and what changes in each>

## Testing
<specific test cases to write, existing test files to reference>

## Acceptance criteria
- [ ] ...
- [ ] ...

Do NOT implement. Do NOT ask questions. Read code, update the issue.`, repo, req.Ticket)
		},
		DefaultUserMessage: func(req Request) string {
			return fmt.Sprintf(
				"Triage #%d. The ticket is in your system prompt. Read the relevant code, flesh out the spec, update the issue on GitHub.",
				req.Ticket)
		},
		Autonomous:        true,
		IncludeTicketBody: true,
		DefaultModel:      "sonnet",
		// Triage rewrites a single issue's body; it does not delegate.
		Role:            RoleWorker,
		DefaultWorktree: true,
	},
	ModeReview: {
		SystemInstructions: func(_ Request) string {
			return `You have been asked to perform a code review. Do NOT start implementing.

Please conduct a thorough code review:
1. Identify any open pull requests or recent commits related to this issue
2. Examine the implementation for correctness, edge cases, and potential bugs
3. Check for adherence to the project's code style and patterns
4. Evaluate test coverage — are the right cases tested?
5. Look for performance or security concerns
6. Suggest specific, actionable improvements with code examples where helpful

Be direct and constructive. Point out what is done well, and be explicit about what needs to change before this can be merged.`
		},
		DefaultUserMessage: func(req Request) string {
			return fmt.Sprintf("Review issue #%d: %s. The full issue is in your system prompt.", req.Ticket, req.TicketTitle)
		},
		Autonomous:        false,
		IncludeTicketBody: true,
		DefaultModel:      "opus",
		// Review reads and comments; it does not delegate.
		Role:            RoleWorker,
		DefaultWorktree: true,
	},
	ModeCustom: {
		SystemInstructions: func(req Request) string {
			return req.CustomPrompt
		},
		DefaultUserMessage: func(req Request) string {
			if req.Ticket > 0 {
				return fmt.Sprintf("Issue #%d: %s. Your instructions are in the system prompt.", req.Ticket, req.TicketTitle)
			}
			if synopsis := taskSynopsis(req.CustomPrompt); synopsis != "" {
				return fmt.Sprintf("Task: %s\nYour instructions are in the system prompt.", synopsis)
			}
			return "Your instructions are in the system prompt."
		},
		Autonomous:        false,
		IncludeTicketBody: true,
		DefaultModel:      "",
		// Custom is caller-supplied instructions for a single agent, not a
		// delegation hub; default to the more restrictive role.
		Role:            RoleWorker,
		DefaultWorktree: true,
	},
	// ModeBare: no exec boilerplate, no ticket, no initial user message.
	// The agent receives the preamble (so it knows about agentctl capabilities)
	// and then starts interactively waiting for the user.
	ModeBare: {
		SystemInstructions: func(_ Request) string { return "" },
		DefaultUserMessage: func(_ Request) string { return "" },
		Autonomous:         false,
		IncludeTicketBody:  false,
		DefaultModel:       "",
		// Bare is how the human-facing lead session is launched -- its job
		// is coordinating other agents, so it gets the delegation surface.
		Role: RoleOrchestrator,
		// DefaultWorktree is false here -- deliberately the one exception to
		// the "every mode defaults to true" rule above. Two production
		// incidents drove this:
		//
		//  1. The after-new-window hook runs via tmux run-shell, which
		//     inherits the ACTIVE PANE's cwd. When bare sessions lived in a
		//     worktree, that cwd was always a worktree, so paths.Init(cwd)
		//     anchored the runtime dir to the worktree, the tmux session name
		//     resolved to a session that doesn't exist, and list-panes
		//     failed -- breaking the chrome bar on every dispatched agent for
		//     days. We separately fixed this class of bug by pinning the
		//     runtime dir explicitly, but bare-in-a-worktree was the root
		//     cause.
		//  2. The orchestrator's actual job is editing repo files in place
		//     (.claude/agents/, docs/, spec files). In a worktree those edits
		//     are worktree-local and get silently discarded on reclaim unless
		//     separately committed and PR-ed -- exactly backwards for the
		//     role.
		//
		// Do not flip this back to true without addressing both.
		DefaultWorktree: false,
	},
}

// IsAutonomous reports whether mode runs without human interaction, i.e.
// whether it is safe for the monitor to auto-deliver an unsolicited mailbox
// message into the window (see docs/agent-messaging-design.md §3's "Risks"
// section: the Stop hook writes "idle" unconditionally between every turn,
// including in interactive sessions, so gating delivery on state alone would
// inject into a window where a human is mid-conversation).
//
// True for dispatch, adhoc, and triage — the fully autonomous modes with no
// expected human turn-taking. False for discuss, review, custom, and bare,
// and for any unrecognised/empty mode string (e.g. a window launched before
// hooks recorded a mode) — those default to the safe answer, not delivery.
func IsAutonomous(mode string) bool {
	spec, ok := modes[Mode(mode)]
	return ok && spec.Autonomous
}

func dispatchInstructions(_ Request) string {
	return execInstructions()
}

func adhocInstructions(_ Request) string {
	return execInstructions()
}

// execInstructions returns the shared autonomy/behavior boilerplate: execute
// without asking clarifying questions, work silently, only stop if truly
// blocked. It intentionally contains no git/build steps or language-specific
// commands -- those are owned by buildFinishingSteps (worktree-aware) and by
// the agent's own .md (toolchain-aware). See buildSystemPrompt's doc comment
// for the full composition order.
func execInstructions() string {
	return `Execute this ticket autonomously from start to finish. Do not ask clarifying questions — if something is ambiguous, make a reasonable decision and move on. Only stop and report to the user if you are truly blocked (missing permissions, broken dependencies, etc.).

Work silently: no progress updates, no "here's my plan" preambles. Just read the code and implement the changes.`
}
