package dispatch

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/tui"
)

// buildSystemPrompt composes the full system prompt for a dispatch session.
//
// Composition order:
//  1. Frontmatter
//  2. Task content (issue body or long custom prompt)
//  3. "---" separator
//  4. Preamble (agentctl capabilities, role-selected -- see preamble.go)
//  5. Shared Conventions (expanded from the launching agent's own @shared
//     includes, if any -- see agent.ExpandSharedRefs)
//  6. Worktree orientation note (branch name only, if applicable)
//  7. Mode instructions (autonomy/behavior boilerplate)
//  8. Context store entries
//  9. Finishing steps (verify -> commit -> push -> PR, worktree-aware) --
//     only for modes with IncludeFinishingSteps set (dispatch, adhoc)
//
// Finishing steps are deliberately last: ordered instructions closest to the
// end of the prompt are followed most reliably, and this is the sequence we
// most need the agent to get right (it's what actually turns work into a
// reviewable PR).
// useWorktree is the already-resolved mode-default-or-override value from
// Launch's single resolution site (see Request.UseWorktree's doc comment) --
// this function never reads req.UseWorktree directly.
func buildSystemPrompt(req Request, spec modeSpec, workDir, branch string, useWorktree bool, contextEntries []ctxstore.Entry, sharedConventions string) string {
	var parts []string

	// 1. Frontmatter
	parts = append(parts, buildFrontmatter(req, workDir))

	// 2. Task content (issue body or custom prompt)
	if spec.IncludeTicketBody && req.Ticket > 0 {
		parts = append(parts, fmt.Sprintf("## Issue #%d: %s\n\n%s", req.Ticket, req.TicketTitle, req.TicketBody))
	}

	// For adhoc with long prompts, embed the custom prompt as task content.
	if req.Mode == ModeAdhoc && req.CustomPrompt != "" && len(req.CustomPrompt) > longPromptThreshold {
		parts = append(parts, fmt.Sprintf("## Task\n\n%s", req.CustomPrompt))
	}

	// 3. "---" separator
	parts = append(parts, "---")

	// 4. Preamble
	parts = append(parts, preambleFor(spec.Role))

	// 5. Shared Conventions -- expanded from the launching agent's own
	// @shared/*.md includes. The raw "@shared/foo.md" line still lives in the
	// .md body Claude Code loads directly, but Claude Code does not expand it
	// (see agent.ExpandSharedRefs doc comment), so this expanded copy is the
	// one the model can actually rely on; the heading says so explicitly.
	if sharedConventions != "" {
		parts = append(parts, "## Shared Conventions\n\n"+
			"_The following was expanded from this agent's `@shared` includes, because Claude Code does not expand `@` imports inside agent definition files. This expanded copy is authoritative over any raw `@shared/...` reference you may also see in your own instructions._\n\n"+
			sharedConventions)
	}

	// 6. Worktree orientation note
	if useWorktree && branch != "" {
		parts = append(parts, buildWorktreeOrientation(spec, workDir, branch))
	}

	// 7. Mode instructions
	parts = append(parts, spec.SystemInstructions(req))

	// 8. Context store entries
	if len(contextEntries) > 0 {
		var contextParts []string
		totalBytes := 0
		for _, e := range contextEntries {
			formatted := ctxstore.Format(e)
			contextParts = append(contextParts, formatted)
			totalBytes += len(formatted)
		}
		contextSection := "## Shared Context\n\n" + strings.Join(contextParts, "\n\n---\n\n")
		parts = append(parts, contextSection)
		// Byte count, not just entry count, is the point: a count alone
		// cannot reveal a 276KB payload -- that's exactly how the
		// unscoped-broadcast bug went unnoticed for months.
		logging.Log("dispatch: injected %d context store entries (%d bytes) for %s", len(contextEntries), totalBytes, req.AgentName)
	}

	// 9. Finishing steps — kept last on purpose (see doc comment above).
	// Only appended for fully autonomous, code-producing modes (dispatch,
	// adhoc); modes like triage/review explicitly forbid implementing, and
	// discuss/custom/bare leave "done" to the human.
	if spec.IncludeFinishingSteps {
		parts = append(parts, buildFinishingSteps(req, branch, useWorktree))
	}

	return strings.Join(parts, "\n\n")
}

// buildWorktreeOrientation returns the worktree orientation note included in
// the system prompt when a launch was worktree-isolated. It splits on
// spec.IncludeFinishingSteps -- the same flag that governs whether
// buildFinishingSteps' verify->commit->push->PR sequence gets appended --
// because the two are two halves of the same contract:
//
//   - IncludeFinishingSteps == true (dispatch, adhoc): these are the fully
//     autonomous, code-producing modes whose job ends at "open a PR, do not
//     merge it yourself." The original terse warning is exactly right for
//     them, verbatim.
//   - IncludeFinishingSteps == false (bare and the conversational modes):
//     these sessions are driven by a human, and for ModeBare in particular
//     the human's job explicitly includes merging other agents' pull
//     requests (`gh pr merge`) -- so "do not merge to main" is actively
//     wrong guidance here. These modes get a longer orientation instead:
//     what the worktree is, how to see the current default branch without
//     merging into this throwaway branch, that PR merges are unaffected, and
//     that uncommitted edits here are worktree-local and will be discarded
//     unless committed and opened as a PR.
//
// The bare branch of this split is no longer reachable by default: ModeBare's
// DefaultWorktree is now false, and buildSystemPrompt only calls this at all
// when useWorktree is true (see its "6. Worktree orientation note" gate). It
// remains reachable only via an explicit Request.UseWorktree override that
// forces a worktree for a bare launch. The code stays in place for that case
// rather than being deleted.
func buildWorktreeOrientation(spec modeSpec, workDir, branch string) string {
	if spec.IncludeFinishingSteps {
		return fmt.Sprintf("You are working in a git worktree on branch %s. Do not merge to main.", branch)
	}

	return fmt.Sprintf(`You are in a git worktree at %s on branch %s, cut from the remote default branch at launch. The main checkout stays on its own branch, and other sessions cannot see your uncommitted edits.

To see the current default branch after another session merges something, run `+"`git fetch origin`"+` and read through `+"`origin/main:<path>`"+`. Do not run `+"`git pull origin main`"+`, which merges into this throwaway branch.

Merging pull requests is unaffected. `+"`gh pr merge`"+` is a remote operation and works normally from a worktree.

Any file you edit here is worktree-local. If an edit needs to land (agent definitions, docs, specs), commit it and open a PR, otherwise it is discarded when the worktree is reclaimed.`, workDir, branch)
}

// buildFinishingSteps returns the ordered "when you are finished" sequence
// for a dispatched agent. It owns the WORKFLOW (verify -> commit -> push ->
// PR) because only the dispatch prompt knows whether there is a worktree,
// what the branch is, and whether there is a ticket number to close. It
// deliberately never names a language-specific build/test tool or a
// repo-specific deploy/restart step (e.g. "go build", "just restart") --
// that's the agent's own .md file's job, since only it knows the toolchain.
//
// useWorktree is the already-resolved mode-default-or-override value from
// Launch's single resolution site -- this function never reads
// req.UseWorktree directly.
func buildFinishingSteps(req Request, branch string, useWorktree bool) string {
	if useWorktree && branch != "" {
		closesLine := ""
		if req.Ticket > 0 {
			closesLine = fmt.Sprintf(" Include \"Closes #%d\" in the commit body so GitHub auto-closes the issue.", req.Ticket)
		}
		prClosesNote := ""
		if req.Ticket > 0 {
			prClosesNote = fmt.Sprintf(" Mention \"Closes #%d\" in the PR body too.", req.Ticket)
		}

		return fmt.Sprintf(`When you are finished — follow these steps IN ORDER, do not skip any:
1. Verify the change builds and tests pass, using the project's own build/test commands.
2. Stage ONLY the files you changed — explicit paths, never "git add -A" or "git add .".
3. Commit your changes with a descriptive message.%s
4. Push your branch: git push -u origin %s
5. Open a pull request: gh pr create --base main --title "<title>" --body-file <file>.%s Write the body to a temp file and use --body-file rather than an inline --body — inline bodies with non-trivial content trigger interactive prompts.
6. Print the PR URL.

The PR is the deliverable, not the commit — do not stop after committing, and do NOT merge to main yourself.`, closesLine, branch, prClosesNote)
	}

	return `When you are finished — follow these steps IN ORDER, do not skip any:
1. Verify the change builds and tests pass, using the project's own build/test commands.
2. Stage ONLY the files you changed — explicit paths, never "git add -A" or "git add .".
3. Commit your changes with a descriptive message.

Do NOT push to main and do NOT open a pull request — you are working directly in the shared repo directory, not an isolated worktree. Stop after committing and report back what you did.`
}

// buildFrontmatter returns a YAML-style frontmatter block with session metadata.
func buildFrontmatter(req Request, workDir string) string {
	repoSlug := req.RepoSlug
	if repoSlug == "" {
		repoSlug = tui.TLRepoSlug(workDir)
		if repoSlug == "" {
			repoSlug = filepath.Base(workDir)
		}
	}
	started := time.Now().UTC().Format(time.RFC3339)

	lines := []string{"---"}
	if req.Ticket > 0 {
		lines = append(lines,
			fmt.Sprintf("ticket: \"#%d\"", req.Ticket),
			fmt.Sprintf("title: %q", req.TicketTitle),
		)
	}
	lines = append(lines,
		fmt.Sprintf("agent: %s", req.AgentName),
		fmt.Sprintf("repo: %s", repoSlug),
		fmt.Sprintf("started: %s", started),
		fmt.Sprintf("mode: %s", string(req.Mode)),
	)
	lines = append(lines, "---")
	return strings.Join(lines, "\n")
}

const longPromptThreshold = 500

// synopsisWidth is the maximum rendered width of a taskSynopsis result,
// chosen to fit comfortably in a tmux input box on a single line.
const synopsisWidth = 100

// genericSectionHeadings are section headings common enough in our own task
// prompts (e.g. "## Problem") that using one verbatim as the synopsis would
// tell a human nothing. taskSynopsis skips over them in favor of the next
// meaningful line, falling back to the heading only if nothing else is found.
var genericSectionHeadings = map[string]bool{
	"problem":    true,
	"task":       true,
	"context":    true,
	"summary":    true,
	"changes":    true,
	"goal":       true,
	"background": true,
}

// taskSynopsis reduces a multi-line task prompt to a single short line
// suitable for the pane input box: the first meaningful line, stripped of
// markdown decoration, whitespace-collapsed, truncated to synopsisWidth.
// Returns "" if nothing usable was found; callers must handle that case.
func taskSynopsis(prompt string) string {
	var fallback string

	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isMarkdownRule(line) {
			continue
		}

		cleaned := cleanSynopsisLine(line)
		if cleaned == "" {
			continue
		}

		if genericSectionHeadings[strings.ToLower(cleaned)] {
			if fallback == "" {
				fallback = cleaned
			}
			continue
		}

		return ansi.Truncate(cleaned, synopsisWidth, "…")
	}

	if fallback != "" {
		return ansi.Truncate(fallback, synopsisWidth, "…")
	}
	return ""
}

// isMarkdownRule reports whether line is a bare markdown horizontal rule
// (---, ***, or ===), which carries no synopsis-worthy content.
func isMarkdownRule(line string) bool {
	switch line {
	case "---", "***", "===":
		return true
	default:
		return false
	}
}

// cleanSynopsisLine strips leading markdown decoration (heading '#' runs,
// blockquote '>', list markers, and surrounding bold/code markers) from a
// single line and collapses internal whitespace to single spaces.
func cleanSynopsisLine(line string) string {
	line = strings.TrimLeft(line, "#")
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, ">")
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "- ")
	line = strings.TrimPrefix(line, "* ")
	line = strings.TrimPrefix(line, "+ ")
	line = strings.Trim(line, "*")
	// Strip ALL backticks rather than just the two outermost characters.
	// strings.Trim(line, "`") only removes backticks that sit at the very
	// start/end of the whole line -- if the line contains more than one
	// inline code span (e.g. "`foo.go` breaks `bar`"), the leading backtick
	// of the first span gets trimmed away but its matching closing backtick,
	// sitting mid-string, does not, leaving an ODD number of backticks in
	// the result. An odd backtick count is exactly what makes a downstream
	// double-quoted shell string (e.g. a diagnostics echo) unparseable: the
	// shell treats a lone backtick as the start of unterminated command
	// substitution and hangs waiting for its close. This is a defense in
	// depth measure -- the real fix is that no generated shell string should
	// ever interpolate raw synopsis/command text into a double-quoted
	// context in the first place (see tmux.LaunchAgent) -- but removing
	// backticks here as well costs nothing and closes this class of bug at
	// the source too.
	line = strings.ReplaceAll(line, "`", "")
	line = strings.TrimSpace(line)

	fields := strings.Fields(line)
	return strings.Join(fields, " ")
}
