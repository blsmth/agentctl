package dispatch

import (
	"time"

	ctxstore "github.com/blsmth/agentctl/internal/context"
)

// testFacts are launch facts for a launch in workDir on branch.
func testFacts(workDir, branch string, useWorktree bool) launchFacts {
	if !useWorktree {
		branch = ""
	}
	return launchFacts{
		WorkDir: workDir, RepoDir: workDir, Branch: branch, InWorktree: useWorktree,
		Window: "golden", Started: time.Date(2026, 11, 24, 21, 5, 43, 0, time.UTC),
	}
}

// buildSystemPrompt is buildLaunchPrompt's system prompt file.
func buildSystemPrompt(req Request, spec modeSpec, workDir, branch string, useWorktree bool, entries []ctxstore.Entry, shared string) string {
	return buildLaunchPrompt(req, spec, testFacts(workDir, branch, useWorktree), entries, shared).System
}

// buildFirstMessage is buildLaunchPrompt's first user message.
func buildFirstMessage(req Request, spec modeSpec, workDir, branch string, useWorktree bool, entries []ctxstore.Entry, shared string) string {
	return buildLaunchPrompt(req, spec, testFacts(workDir, branch, useWorktree), entries, shared).Message
}

// buildBoth is buildLaunchPrompt's system file and first message joined, for
// assertions that only care that the agent sees something.
func buildBoth(req Request, spec modeSpec, workDir, branch string, useWorktree bool, entries []ctxstore.Entry, shared string) string {
	lp := buildLaunchPrompt(req, spec, testFacts(workDir, branch, useWorktree), entries, shared)
	return lp.System + "\n\n" + lp.Message
}
