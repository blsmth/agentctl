package dispatch

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/tui"
	"github.com/blsmth/agentctl/internal/worktree"
)

// launchFacts are the per-launch facts of one launch: where it runs and who
// dispatched it. They go in the first user message, never in the system
// prompt file.
type launchFacts struct {
	WorkDir    string // where the agent runs: the worktree when one exists
	RepoDir    string // the checkout the launch targets (repo slug fallback)
	Branch     string // the window's own branch; "" without a worktree
	InWorktree bool   // a worktree was actually created for this launch
	// Checkout is what WorkDir is when no worktree was created for this
	// launch; the zero value is the repository's main checkout.
	Checkout checkoutInfo
	Window   string // tmux window name (instance)
	Started  time.Time
}

// worktree reports whether the launch runs in its own worktree and branch.
func (f launchFacts) worktree() bool { return f.InWorktree && f.Branch != "" }

// sharedWorktree reports whether the launch runs in a linked worktree it did
// not create (e.g. --no-worktree from a session that runs in one).
func (f launchFacts) sharedWorktree() bool { return !f.worktree() && f.Checkout.Kind == checkoutLinked }

// notRepo reports whether the launch runs outside any git repository.
func (f launchFacts) notRepo() bool { return !f.worktree() && f.Checkout.Kind == checkoutNotRepo }

// checkoutKind is the kind of directory a launch without its own worktree
// runs in.
type checkoutKind int

const (
	checkoutMain    checkoutKind = iota // the repository's main checkout
	checkoutLinked                      // a linked worktree that already existed
	checkoutNotRepo                     // not inside a git repository
)

// checkoutInfo describes the directory a launch without its own worktree
// runs in.
type checkoutInfo struct {
	Kind   checkoutKind
	Branch string // checked-out branch of a linked worktree; "" when detached
}

// inspectCheckoutFn inspects a working directory. A var for tests.
var inspectCheckoutFn = worktree.Inspect

// inspectCheckout describes dir for the launch facts.
func inspectCheckout(dir string) checkoutInfo {
	c := inspectCheckoutFn(dir)
	switch {
	case !c.Repo:
		return checkoutInfo{Kind: checkoutNotRepo}
	case c.Linked:
		return checkoutInfo{Kind: checkoutLinked, Branch: c.Branch}
	default:
		return checkoutInfo{Kind: checkoutMain}
	}
}

// launchPrompt is what a launch hands the agent: the file passed with
// --append-system-prompt-file and the first user message.
type launchPrompt struct {
	// System is identical across launches of the same agent, mode, repo,
	// harness, account and config: nothing per launch goes in it.
	System string
	// Message is the first user message; "" starts the agent interactively.
	Message string
	// TaskInSystem is set when the task block was too large for the message
	// (maxInlineMessage) and was appended to System instead.
	TaskInSystem bool
}

// taskMaterialHeading heads a task block moved to the system prompt file
// because it was too large for the first message.
const taskMaterialHeading = "## Task material (too large for the first message)"

// buildLaunchPrompt composes a launch's system prompt file and first user
// message.
//
// Claude Code sends a request as tools, then the system prompt (its base,
// the agent body, then this file), then the messages (CLAUDE.md and memory
// as reminders, then the first user message). The prompt cache reuses the
// longest unchanged prefix, so anything that differs per launch inside this
// file would make every later block, the repo's CLAUDE.md included, a fresh
// cache write. The file therefore holds only stable rules, most stable first:
//
//  1. Preamble (agentctl capabilities, role-selected, see preamble.go), with
//     the orchestrator, cross-account or mailbox reporting section. The
//     orchestrator's location is a launch fact, not part of it.
//     1b. Agent definition body, for harnesses without --agent (pi)
//  2. Shared Conventions (the agent's own @shared includes, expanded)
//  3. Mode instructions (autonomy boilerplate, review rules, ...)
//  4. Worktree note (that there is one, not its path or branch)
//  5. Session (agent, repo, mode)
//  6. Context store entries for the agent (not the ticket's spec)
//
// The first message carries everything per launch, in this order:
//
//  1. Task: the issue body and its ticket spec, the ad-hoc prompt, custom
//     instructions, or the PR block with its author; then the user's
//     additional instructions
//  2. Launch facts: started time, window, working directory, branch,
//     orchestrator location (and for review-pr, how to check out the head)
//  3. The mode's directive, then the finishing steps (verify -> commit ->
//     push -> PR) for dispatch/adhoc. They stay last on purpose: ordered
//     instructions closest to the end are followed most reliably, and this
//     is the sequence that turns work into a reviewable PR.
//
// A message over maxInlineMessage keeps its launch facts and closing steps,
// and its task block moves to the end of the system prompt file, so that
// launch writes the cache from the task onwards but still launches.
//
// ModeBare is different: its first message is the human's own prompt (see
// bareLaunchPrompt).
func buildLaunchPrompt(req Request, spec modeSpec, facts launchFacts, contextEntries []ctxstore.Entry, sharedConventions string) launchPrompt {
	stable, perLaunch := splitContextEntries(req, contextEntries)
	system := buildSystemFile(req, spec, facts, stable, sharedConventions)
	if req.Mode == ModeBare {
		return bareLaunchPrompt(req, facts, system)
	}

	task := taskBlock(req, spec, perLaunch)
	tail := joinParts(launchFactsBlock(req, facts), closingBlock(req, spec, facts))
	msg := joinParts(task, tail)
	if len(msg) <= maxInlineMessage || task == "" {
		return launchPrompt{System: system, Message: msg}
	}
	logging.Log("dispatch: first message for %s is %d bytes (over %d); moving its task block to the system prompt", req.AgentName, len(msg), maxInlineMessage)
	return launchPrompt{
		System:       joinParts(system, taskMaterialHeading, task),
		Message:      joinParts(`Your task was too large to send as this message. It is at the end of your system prompt, under "`+strings.TrimPrefix(taskMaterialHeading, "## ")+`".`, tail),
		TaskInSystem: true,
	}
}

// bareLaunchPrompt finishes a ModeBare launch, whose first message is the
// human's prompt (an orchestrator launched on an ad-hoc prompt) or nothing.
// Bare launches have no orchestrator and no finishing steps, so the only
// per-launch fact is the worktree, when there is one: a one-line prefix on
// the message, or, with no message to carry it, the system file itself.
func bareLaunchPrompt(req Request, facts launchFacts, system string) launchPrompt {
	extra := extraSection(req)
	if req.CustomPrompt == "" {
		return launchPrompt{System: joinParts(system, extra)}
	}
	prefix := ""
	if facts.worktree() {
		prefix = fmt.Sprintf("[agentctl launch facts: git worktree %s, branch %s]", facts.WorkDir, facts.Branch)
	}
	msg := joinParts(prefix, req.CustomPrompt, extra)
	if len(msg) <= maxInlineMessage {
		return launchPrompt{System: system, Message: msg}
	}
	return launchPrompt{
		System:       joinParts(system, "## Task\n\n"+req.CustomPrompt, extra),
		Message:      joinParts(prefix, `Your task is in the system prompt under "## Task".`),
		TaskInSystem: true,
	}
}

// buildSystemFile composes the stable system prompt file (see
// buildLaunchPrompt for the order and why).
func buildSystemFile(req Request, spec modeSpec, facts launchFacts, contextEntries []ctxstore.Entry, sharedConventions string) string {
	var parts []string

	// 1. Preamble
	reviewer := req.Mode == ModeReviewPR
	switch {
	case req.noMessaging && reviewer:
		parts = append(parts, reviewerPreamble(false))
		if req.reportViaMailbox {
			parts = append(parts, reviewerMailboxReportingSection)
		}
	case req.noMessaging:
		parts = append(parts, preambleForHarnessBackends(spec.Role, false, req.prReview, req.backendLines))
		if req.reportViaMailbox && spec.Role != RoleOrchestrator {
			parts = append(parts, mailboxReportingSection)
		}
	case reviewer:
		parts = append(parts, reviewerPreamble(true))
		if req.Orchestrator.Known() {
			parts = append(parts, reviewerOrchestratorSection())
		}
	default:
		parts = append(parts, preambleWithOrchestratorBackends(spec.Role, req.Orchestrator, req.prReview, req.backendLines))
	}
	if req.CrossAccount && spec.Role != RoleOrchestrator {
		if reviewer {
			parts = append(parts, reviewerCrossAccountSection)
		} else {
			parts = append(parts, crossAccountSection)
		}
	}

	// 1b. Persona -- for harnesses with no --agent flag (pi), the agent's
	// own definition body has no other way in front of the model.
	if req.persona != "" {
		parts = append(parts, "## Agent definition: "+req.AgentName+"\n\n"+strings.TrimSpace(req.persona))
	}

	// 2. Shared Conventions -- expanded from the launching agent's own
	// @shared/*.md includes. The raw "@shared/foo.md" line still lives in the
	// .md body Claude Code loads directly, but Claude Code does not expand it
	// (see agent.ExpandSharedRefs doc comment), so this expanded copy is the
	// one the model can actually rely on; the heading says so explicitly.
	if sharedConventions != "" {
		note := "_The following was expanded from this agent's `@shared` includes, because Claude Code does not expand `@` imports inside agent definition files. This expanded copy is authoritative over any raw `@shared/...` reference you may also see in your own instructions._\n\n"
		if req.persona != "" {
			note = "_The following was expanded from the `@shared` includes in the agent definition above. It is authoritative over the raw `@shared/...` references there._\n\n"
		}
		parts = append(parts, "## Shared Conventions\n\n"+note+sharedConventions)
	}

	// 3. Mode instructions
	if s := spec.SystemInstructions(req); s != "" {
		parts = append(parts, s)
	}

	// 4. Worktree note. A review-pr launch's worktree is all per-launch
	// (the PR head to check out), so it is in its launch facts only.
	if facts.worktree() && req.Mode != ModeReviewPR {
		// A bare launch with no message has no launch facts to point to.
		withPath := req.Mode == ModeBare && req.CustomPrompt == ""
		parts = append(parts, worktreeNote(spec, facts, withPath))
	}

	// 5. Session
	parts = append(parts, sessionBlock(req, facts))

	// 6. Context store entries
	if len(contextEntries) > 0 {
		parts = append(parts, contextSection(req, contextEntries))
	}

	return strings.Join(parts, "\n\n")
}

// splitContextEntries separates the context entries that are per launch
// (the ticket's own spec, which goes in the first message) from the
// agent's standing entries (system file). The --prompt-key entry is the task
// itself, already in the message, so it is dropped.
func splitContextEntries(req Request, entries []ctxstore.Entry) (stable, perLaunch []ctxstore.Entry) {
	for _, e := range entries {
		switch {
		case req.PromptKey != "" && e.Key == req.PromptKey:
		case isTicketSpec(e.Key, req.Ticket):
			perLaunch = append(perLaunch, e)
		default:
			stable = append(stable, e)
		}
	}
	return stable, perLaunch
}

// isTicketSpec reports whether key is ticket's ticket-<N>-spec entry.
func isTicketSpec(key string, ticket int) bool {
	n, ok := ctxstore.TicketSpecNumber(key)
	return ok && ticket > 0 && n == ticket
}

// contextSection renders context entries under "## Shared Context".
func contextSection(req Request, entries []ctxstore.Entry) string {
	formatted := make([]string, 0, len(entries))
	totalBytes := 0
	for _, e := range entries {
		f := ctxstore.Format(e)
		formatted = append(formatted, f)
		totalBytes += len(f)
	}
	// Byte count, not just entry count, is the point: a count alone cannot
	// reveal a 276KB payload -- that's exactly how the unscoped-broadcast
	// bug went unnoticed for months.
	logging.Log("dispatch: injected %d context store entries (%d bytes) for %s", len(entries), totalBytes, req.AgentName)
	return "## Shared Context\n\n" + strings.Join(formatted, "\n\n---\n\n")
}

// taskBlock is the first message's task: the issue (with its ticket spec),
// the ad-hoc prompt, custom instructions or the PR, then the user's
// additional instructions.
func taskBlock(req Request, spec modeSpec, perLaunch []ctxstore.Entry) string {
	var parts []string
	if spec.IncludeTicketBody && req.Ticket > 0 {
		parts = append(parts, fmt.Sprintf("## Issue #%d: %s\n\n%s", req.Ticket, req.TicketTitle, req.TicketBody))
	}
	if len(perLaunch) > 0 {
		parts = append(parts, contextSection(req, perLaunch))
	}
	switch {
	case req.Mode == ModeAdhoc && req.CustomPrompt != "":
		parts = append(parts, "## Task\n\n"+taskSource(req)+req.CustomPrompt)
	case req.Mode == ModeCustom && req.CustomPrompt != "":
		parts = append(parts, "## Instructions\n\n"+req.CustomPrompt)
	case req.Mode == ModeReviewPR:
		parts = append(parts, reviewPRTask(req))
	}
	parts = append(parts, extraSection(req))
	return joinParts(parts...)
}

// taskSource names where an ad-hoc prompt came from, so a human scanning
// the transcript recognizes it: "Source: context key `k`\n\n", or "".
func taskSource(req Request) string {
	var lines []string
	if req.PromptKey != "" {
		lines = append(lines, fmt.Sprintf("Source: context key `%s`", req.PromptKey))
	}
	if req.Template != "" {
		lines = append(lines, fmt.Sprintf("Source: template `%s`", req.Template))
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n\n"
}

// extraSection renders the user's additional instructions, or "".
func extraSection(req Request) string {
	extra := strings.TrimSpace(req.ExtraInstructions)
	if extra == "" {
		return ""
	}
	return AdditionalInstructionsHeading + "\n\n" + extra
}

// launchFactsBlock renders the first message's "## Launch facts".
func launchFactsBlock(req Request, facts launchFacts) string {
	lines := []string{"## Launch facts", ""}
	if !facts.Started.IsZero() {
		lines = append(lines, "- Started: "+facts.Started.UTC().Format(time.RFC3339))
	}
	if facts.Window != "" {
		lines = append(lines, "- Window: "+facts.Window+" (`agentctl status whoami` prints its tmux window ID)")
	}
	if facts.WorkDir != "" {
		lines = append(lines, "- Working directory: "+facts.WorkDir)
	}
	lines = append(lines, checkoutFact(facts))
	if req.Orchestrator.Known() {
		lines = append(lines, orchestratorFact(req.Orchestrator))
	}
	out := strings.Join(lines, "\n")
	if req.Mode == ModeReviewPR && facts.worktree() && !req.reviewSharedCheckout {
		out += "\n\n" + reviewWorktreeOrientation(req, facts.WorkDir, facts.Branch)
	}
	return out
}

// checkoutFact is the launch-facts line saying where the agent works: its
// own worktree, an existing worktree it shares, the main checkout, or a
// directory outside git.
func checkoutFact(facts launchFacts) string {
	switch {
	case facts.worktree():
		return "- Branch: " + facts.Branch + " (a git worktree)"
	case facts.sharedWorktree() && facts.Checkout.Branch != "":
		return "- Branch: " + facts.Checkout.Branch + " (an existing git worktree, not created for this launch; other sessions may also use it)"
	case facts.sharedWorktree():
		return "- Detached HEAD in an existing git worktree, not created for this launch; other sessions may also use it"
	case facts.notRepo():
		return "- No worktree: the working directory is not in a git repository"
	default:
		return "- No worktree: this is the repo's main checkout, which other sessions may also use"
	}
}

// closingBlock is the end of the first message: the mode's directive, then
// the finishing steps for the modes that have them.
func closingBlock(req Request, spec modeSpec, facts launchFacts) string {
	var parts []string
	if spec.Directive != nil {
		parts = append(parts, spec.Directive(req))
	}
	switch {
	case spec.IncludeFinishingSteps && spec.Role == RoleReporter:
		parts = append(parts, reporterFinishingSteps(req.Orchestrator))
	case spec.IncludeFinishingSteps:
		parts = append(parts, buildFinishingSteps(req, facts))
	}
	return joinParts(parts...)
}

// joinParts joins the non-empty parts with blank lines.
func joinParts(parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}

// AdditionalInstructionsHeading heads the user's extra guidance.
const AdditionalInstructionsHeading = "## Additional instructions from the user"

// worktreeNote returns the system file's worktree note. It splits on
// spec.IncludeFinishingSteps -- the same flag that governs whether
// buildFinishingSteps' verify->commit->push->PR sequence closes the first
// message -- because the two are two halves of the same contract:
//
//   - IncludeFinishingSteps == true (dispatch, adhoc): these are the fully
//     autonomous, code-producing modes whose job ends at "open a PR, do not
//     merge it yourself." The terse warning is exactly right for them.
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
// The path and branch are launch facts, so the note points at the first
// message for them. withPath names them inline instead, for a launch with no
// first message to carry them (a bare launch with no prompt, reachable only
// through an explicit Request.UseWorktree override, since ModeBare's
// DefaultWorktree is false).
func worktreeNote(spec modeSpec, facts launchFacts, withPath bool) string {
	if spec.IncludeFinishingSteps {
		return "You are working in a git worktree on its own branch (named in the launch facts of your first message). Do not merge to main."
	}
	where := "You are in a git worktree (its path and branch are in the launch facts of your first message)"
	if withPath {
		where = fmt.Sprintf("You are in a git worktree at %s on branch %s", facts.WorkDir, facts.Branch)
	}
	return where + `, cut from the remote default branch at launch. The main checkout stays on its own branch, and other sessions cannot see your uncommitted edits.

To see the current default branch after another session merges something, run ` + "`git fetch origin`" + ` and read through ` + "`origin/main:<path>`" + `. Do not run ` + "`git pull origin main`" + `, which merges into this throwaway branch.

Merging pull requests is unaffected. ` + "`gh pr merge`" + ` is a remote operation and works normally from a worktree.

Any file you edit here is worktree-local. If an edit needs to land (agent definitions, docs, specs), commit it and open a PR, otherwise it is discarded when the worktree is reclaimed.`
}

// buildFinishingSteps returns the ordered "when you are finished" sequence
// for a dispatched agent. It owns the WORKFLOW (verify -> commit -> push ->
// PR) because only the dispatch prompt knows whether there is a worktree,
// what the branch is, and whether there is a ticket number to close. It
// deliberately avoids language-specific build/test commands and
// repo-specific deploy/restart steps (e.g. "go build", "just restart") --
// that's the agent's own .md file's job, since only it knows the toolchain.
// The one tool it names, golangci-lint, is only an example of a linter.
//
// facts.InWorktree is the already-resolved mode-default-or-override value
// from Launch's single resolution site -- this function never reads
// req.UseWorktree directly.
func buildFinishingSteps(req Request, facts launchFacts) string {
	if branch := facts.Branch; facts.worktree() {
		closesLine := ""
		if req.Ticket > 0 {
			closesLine = fmt.Sprintf(" Include \"Closes #%d\" in the commit body so GitHub auto-closes the issue.", req.Ticket)
		}
		prClosesNote := ""
		if req.Ticket > 0 {
			prClosesNote = fmt.Sprintf(" Mention \"Closes #%d\" in the PR body too.", req.Ticket)
		}

		return fmt.Sprintf(`When you are finished — follow these steps IN ORDER, do not skip any:
1. Verify the change builds and tests pass, using the project's own build/test commands, and run the repo's configured linter (e.g. golangci-lint run when .golangci.yml exists) on what you changed; fix what it reports. If the task names checks (screen sizes, themes, browsers, commands, environments), do every one before the PR and list each in the PR message as done, or not done with the reason.
2. Stage ONLY the files you changed — explicit paths, never "git add -A" or "git add .".
3. Commit your changes with a descriptive message.%s
4. Push your branch: git push -u origin %s
5. Open a pull request: gh pr create --base main --title "<title>" --body-file <file>.%s Write the body to a temp file and use --body-file rather than an inline --body — inline bodies with non-trivial content trigger interactive prompts.
6. Print the PR URL.%s

The PR is the deliverable, not the commit — do not stop after committing, and do NOT merge to main yourself.`, closesLine, branch, prClosesNote, reportStep(req.Orchestrator, "7", "the PR URL"))
	}

	return `When you are finished — follow these steps IN ORDER, do not skip any:
1. Verify the change builds and tests pass, using the project's own build/test commands, and run the repo's configured linter (e.g. golangci-lint run when .golangci.yml exists) on what you changed; fix what it reports. If the task names checks (screen sizes, themes, browsers, commands, environments), do every one before you finish and list each in your final report as done, or not done with the reason.
2. Stage ONLY the files you changed — explicit paths, never "git add -A" or "git add .".
3. Commit your changes with a descriptive message.

Do NOT push to main and do NOT open a pull request — ` + sharedDirReason(facts) + ` Stop after committing and report back what you did.` + reportStep(req.Orchestrator, "", "a summary of what you did")
}

// sharedDirReason says why a launch without its own worktree must not push
// or open a PR, matching the checkout its launch facts name.
func sharedDirReason(facts launchFacts) string {
	switch {
	case facts.sharedWorktree():
		return "you are working in an existing git worktree that was not created for this launch and that other sessions may also use, not an isolated worktree of your own."
	case facts.notRepo():
		return "the working directory is not in a git repository, so there is no isolated worktree."
	default:
		return "you are working directly in the repo's main checkout, not an isolated worktree."
	}
}

// reporterFinishingSteps closes the first message for a role: reporter
// agent, which reports instead of changing code: no commit or PR unless the
// task asks for one, and the result goes to the orchestrator.
func reporterFinishingSteps(o Orchestrator) string {
	report := "put your result in your final message."
	if o.Known() {
		report = "send your result to your orchestrator (window " + o.WindowID + ") with SendMessage. If it is gone, put the result in your final message instead."
	}
	return "When you are finished: make no commits and open no pull request unless the task asks for one, then " + report
}

// reportStep is the final finishing step: report to the orchestrator with
// SendMessage. Empty when there is no orchestrator.
func reportStep(o Orchestrator, num, what string) string {
	if !o.Known() {
		return ""
	}
	prefix := "\n" + num + ". "
	if num == "" {
		prefix = "\n\nFinally: "
	}
	return prefix + "SendMessage to your orchestrator (window " + o.WindowID + ") with " + what + "."
}

// sessionBlock names the agent, repo and mode. No timestamp, ticket or other
// per-launch value: those are launch facts.
func sessionBlock(req Request, facts launchFacts) string {
	repoSlug := req.RepoSlug
	if repoSlug == "" {
		dir := facts.RepoDir
		if dir == "" {
			dir = facts.WorkDir
		}
		repoSlug = tui.TLRepoSlug(dir)
		if repoSlug == "" {
			repoSlug = filepath.Base(dir)
		}
	}
	return "## Session\n\n" +
		"- Agent: " + req.AgentName + "\n" +
		"- Repo: " + repoSlug + "\n" +
		"- Mode: " + string(req.Mode)
}
