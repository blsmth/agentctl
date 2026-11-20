// Package dispatch implements the unified launch pipeline for dispatching
// agents to work on issues or ad-hoc prompts. It consolidates prompt
// composition, worktree creation, settings generation, env var injection,
// and tmux launch into a single Launch() entry point.
package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/dispatchplan"
	"github.com/blsmth/agentctl/internal/gittracked"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/hooks"
	"github.com/blsmth/agentctl/internal/ids"
	"github.com/blsmth/agentctl/internal/launchopts"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/safefs"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/shellquote"
	"github.com/blsmth/agentctl/internal/signal"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/worktree"
)

// Request describes what to launch. Callers populate the fields relevant to
// their mode; Launch() fills in defaults for anything left empty.
type Request struct {
	// Orchestrator is the dispatching session, filled in by Launch for worker
	// roles so the preamble can name it. Zero means none.
	Orchestrator Orchestrator
	AgentName    string // required
	Mode         Mode   // required
	Ticket       int    // 0 for ad-hoc
	TicketTitle  string
	TicketBody   string
	UserMessage  string // override default user message
	CustomPrompt string // ad-hoc prompt text
	// PromptKey is the context store key the ad-hoc prompt came from, if any
	// (set only when the caller used --prompt-key rather than --prompt). It is
	// display-only metadata: it exists to make the first user message
	// recognizable to a human scanning panes, since the prompt text itself has
	// already been resolved into CustomPrompt before Launch runs. Nothing in
	// Launch re-reads the store using this field.
	PromptKey string
	WorkDir   string // empty = resolve from agent config
	// UseWorktree overrides the mode's DefaultWorktree when non-nil. nil means
	// "use whatever the mode spec says" -- see Launch's step 3 for the single
	// resolution site. Every mode currently defaults to true (see modes.go),
	// so nil effectively means "isolate this launch in a worktree." Pass an
	// explicit pointer (true or false) only when the caller has a specific
	// reason to override that default, e.g. the CLI's --no-worktree flag.
	UseWorktree *bool
	RepoSlug    string // e.g. "owner/repo"
	Model       string // overrides agent config and mode default when set; Inherit clears the configured model
	// Effort overrides the agent's effort_level when set (see ValidEfforts);
	// Inherit clears the configured level so claude uses its own default.
	Effort string
	// PermissionMode overrides the agent's permission_mode and the mode
	// default when set; it must pass ValidatePermissionMode.
	PermissionMode string
	// Backend overrides the agent's configured backend: "" = use the agent
	// config, BackendNone = force Anthropic, else a name from config backends.
	Backend string
	// Account is the --account override: a name from config accounts. ""
	// resolves through claudehome.Resolve (agent, rules, parent, default).
	Account string
	// CrossAccount is set by Launch when the worker runs under a different
	// Claude account than its dispatcher: the worker is then told of no
	// orchestrator to SendMessage (accounts are isolated on purpose).
	CrossAccount bool
	Background   bool // when true, skip switching to the new window after launch
	// Detached marks a launch with no dispatching window (agentctl serve,
	// acting for a phone): spawned_by stays empty and the worker is told of no
	// orchestrator, whatever the launching process's own environment says
	// (serve may have been started from a tmux pane, and TMUX_PANE would
	// otherwise name an unrelated window as the parent).
	Detached bool
	// Branch overrides the generated worktree branch name (buildBranch).
	// cron uses it for its cron/<job>-<YYYYMMDD-HHMM> worktrees.
	Branch string
	// ScheduledBy is the cron job launching this window, recorded in
	// windowstate (with the launch time) for overlap and timeout checks.
	ScheduledBy string
	// Harness overrides the agent's configured harness (dispatch --harness);
	// "" resolves through config, frontmatter, then harness.Default.
	Harness string
	// ReviewPR is the pull request a ModeReviewPR launch reviews.
	ReviewPR *PullRequest
	// VerifyAgent, when set, runs once the worktree exists and before
	// anything is launched, with the directories the agent definition is
	// resolved from (the launch dir, then the repo dir). An error aborts the
	// launch and removes a worktree this launch created. The --review-pr
	// approval check uses it to see the reviewer the launch will really run.
	VerifyAgent func(dirs ...string) error

	// Set by Launch from the resolved harness, for buildSystemPrompt.
	persona          string // full agent body, for harnesses without --agent
	noMessaging      bool   // harness has no SendMessage/ListAgents
	reportViaMailbox bool   // a dispatcher exists but cannot be messaged
	// reviewSharedCheckout is set for a ModeReviewPR launch that runs in the
	// shared checkout (no worktree, or worktree creation failed).
	reviewSharedCheckout bool
}

// applyOverrides applies the request's model and effort to ac and returns the
// explicit model to hand to PrepareBackend (empty when the request did not
// name one). Model priority: Request.Model > AgentConfig.Model > defaultModel.
func applyOverrides(ac *config.AgentConfig, req Request, defaultModel string) (string, error) {
	explicitModel := req.Model
	if req.Model == Inherit {
		explicitModel = ""
	}
	ac.Model = dispatchplan.Model(req.Model, ac.Model, defaultModel)
	if req.Effort != "" {
		if err := ValidateEffort(req.Effort); err != nil {
			return "", err
		}
		if req.Effort == Inherit {
			ac.EffortLevel = ""
		} else {
			ac.EffortLevel = req.Effort
		}
	}
	return explicitModel, nil
}

// Inherit is the Request.Model / Request.Effort value that clears the agent's
// configured value instead of overriding it.
const Inherit = launchopts.Inherit

// ValidEfforts are the --effort levels Claude Code accepts.
var ValidEfforts = launchopts.Efforts

// ValidateEffort rejects an effort that is neither Inherit nor a known level.
func ValidateEffort(e string) error { return launchopts.ValidateEffort(e) }

// ValidatePermissionMode rejects a value `claude --permission-mode` does not
// accept. The empty string (no override) is valid.
func ValidatePermissionMode(pm string) error {
	if pm == "" || validPermissionModes[pm] {
		return nil
	}
	modes := make([]string, 0, len(validPermissionModes))
	for m := range validPermissionModes {
		modes = append(modes, m)
	}
	sort.Strings(modes)
	return fmt.Errorf("invalid permission mode %q (valid: %s)", pm, strings.Join(modes, ", "))
}

// Result contains the output of a successful Launch.
type Result struct {
	WindowID  string
	AgentName string
	SessionID string
	WorkDir   string
	Branch    string
	// Account is the configured account the window runs under ("" when no
	// accounts are configured or the default dir has no account name).
	Account string
	// Harness is the agent program the window runs (see internal/harness).
	Harness string
	// MessagesAuthor is true for a ModeReviewPR launch whose prompt names the
	// PR author's window as reachable with SendMessage (author known, same
	// account, messaging harness). The caller introduces the reviewer to the
	// author only then.
	MessagesAuthor bool
}

// Launch executes the unified dispatch pipeline:
//  1. Resolve working directory
//  2. Create worktree (if requested)
//  3. Look up mode spec
//  4. Load agent config
//  5. Gather context store entries
//  6. Resolve the launching agent's body and expand its @shared includes
//  7. Build system prompt
//  8. Determine user message
//  9. Prepare the harness workspace (settings or bridge extension), session ID
//  10. Write system prompt to temp file
//  11. Build shell command
//  12. Launch tmux window
//  13. Update window state + dispatch log
//  14. Log banner
func Launch(req Request) (Result, error) {
	// 0. Validate the agent name. It becomes a tmux window name (which tmux
	// format-expands), part of the shell command line, and a path component
	// (settings/<name>.json, env.d/<name>.env), and can originate in a cloned
	// repo's frontmatter or config.
	if !agent.ValidLaunchName(req.AgentName) {
		return Result{}, fmt.Errorf("invalid agent name %q", req.AgentName)
	}

	// 1. Validate mode.
	spec, ok := modes[req.Mode]
	if !ok {
		return Result{}, fmt.Errorf("unknown dispatch mode: %q", req.Mode)
	}

	// 2. Resolve working directory.
	workDir := req.WorkDir
	if workDir == "" {
		return Result{}, fmt.Errorf("WorkDir is required")
	}

	// The agent's frontmatter role adapts the mode (an orchestrator never
	// gets the worker tail) before anything reads the mode or its spec.
	cfg := config.Load()
	if role := frontmatterRoleFn(cfg, req.AgentName, workDir); role != "" {
		req, spec = applyAgentRole(req, spec, role, humanLaunch(req))
	}

	// Resolve useWorktree ONCE, immediately after the mode lookup -- this is
	// the single chokepoint for the mode-default-with-explicit-override
	// pattern. Every other site in this file that needs to know whether the
	// launch is worktree-isolated reads this local, never req.UseWorktree
	// directly (see Request.UseWorktree's doc comment).
	useWorktree := spec.DefaultWorktree
	if req.UseWorktree != nil {
		useWorktree = *req.UseWorktree
	}

	// 4. Load agent config. Done before creating the worktree so a bad or
	// unreachable backend fails the dispatch without leaving a worktree behind.
	ac := cfg.ForAgent(req.AgentName)
	h, err := resolveHarness(cfg, req.AgentName, req.Harness, workDir)
	if err != nil {
		return Result{}, err
	}
	caps := h.Caps()

	// Apply model: priority order is Request.Model > backend model >
	// AgentConfig.Model > modeSpec.DefaultModel.
	explicitModel, err := applyOverrides(&ac, req, modeDefaultModel(h, spec))
	if err != nil {
		return Result{}, err
	}
	acct, backendEnv, hb, err := prepareAccountAndBackend(cfg, h, &ac, req, explicitModel, workDir)
	if err != nil {
		return Result{}, err
	}

	// 3. Create worktree if requested.
	var branch string
	repoDir := workDir // remember original repo dir so we can detect an actual worktree below
	if useWorktree {
		branch = req.Branch
		if branch == "" {
			branch = buildBranch(req)
		}
		wtPath, err := worktree.Create(workDir, branch)
		if err != nil {
			// Visible, not just logged: the agent now runs in the shared
			// checkout instead of an isolated worktree.
			logging.Warn("dispatch: worktree failed (branch=%s), falling back to shared dir: %v", branch, err)
			fmt.Fprintf(os.Stderr, "agentctl: warning: worktree for %s not created (%v); launching in the shared checkout %s\n", branch, err, workDir)
		} else {
			workDir = wtPath
			logging.Log("dispatch: created worktree at %s (branch %s)", wtPath, branch)
		}
	}
	// inWorktree is whether a worktree actually exists for this launch. When
	// worktree.Create fell back to the shared dir (e.g. workDir is not a git
	// repo) the generated branch was never created, so the prompt, env,
	// windowstate and Result carry no branch (ownBranch) rather than one the
	// window does not have: the agent gets the shared-dir finishing steps,
	// matching what dispatch completion will look the PR up by.
	inWorktree := useWorktree && workDir != repoDir
	ownBranch := ""
	if inWorktree {
		ownBranch = branch
	}

	if req.VerifyAgent != nil {
		if err := req.VerifyAgent(workDir, repoDir); err != nil {
			if workDir != repoDir {
				if rmErr := worktree.Remove(workDir); rmErr != nil {
					logging.Warn("dispatch: removing worktree %s after a refused launch: %v", workDir, rmErr)
				}
			}
			return Result{}, err
		}
	}

	// 4. (see above) agent config is loaded before the worktree is created.

	// 5. Gather context store entries.
	var contextEntries []ctxstore.Entry
	if ac.CrossContext {
		entries, err := ctxstore.ForAgent(req.AgentName, ac.ContextKeys)
		if err == nil {
			contextEntries = entries
		}
	}

	// 6. Resolve the launching agent's own .md body and expand its
	// @shared/*.md includes -- Claude Code does not expand @ imports inside
	// agent definition files, so this is the only channel that actually gets
	// their content in front of the model (see agent.ExpandSharedRefs).
	// Never fails the dispatch: a resolution failure just means no expansion.
	if caps.NativeMessaging {
		warnMissingMessagingTools(cfg, req.AgentName, workDir, repoDir)
	}
	var sharedConventions string
	if body := resolveAgentBody(cfg, req.AgentName, workDir, repoDir); body != "" {
		expanded, missing := agent.ExpandSharedRefs(body)
		sharedConventions = expanded
		for _, ref := range missing {
			logging.Warn("dispatch: agent %q references unresolved shared include %q", req.AgentName, ref)
		}
		if !caps.AgentFlag {
			// No --agent: the persona itself must travel in the prompt.
			req.persona = body
		}
	}

	// 7. Build system prompt.
	req.Orchestrator = launchOrchestrator(req, spec)
	if req.Orchestrator.Known() && cfg.HasAccounts() && caps.Accounts {
		dropCrossAccountOrchestrator(&req, acct.Dir, claudehome.ParentDir(acct.parent))
	}
	if !caps.NativeMessaging {
		// No SendMessage: the worker is not told of an orchestrator to
		// message. agentctl's done notice still reaches the dispatcher's
		// mailbox (spawned_by is set from the environment, not from this).
		req.reportViaMailbox = req.Orchestrator.Known()
		req.Orchestrator = Orchestrator{}
		req.noMessaging = true
	}
	messagesAuthor := prepareReview(&req, cfg, caps.Accounts, acct.Dir, inWorktree)
	systemPrompt := buildSystemPrompt(req, spec, workDir, ownBranch, inWorktree, contextEntries, sharedConventions)

	// 8. Determine user message.
	userMessage := req.UserMessage
	if userMessage == "" {
		// For adhoc with short custom prompts, use the prompt as the user message.
		if req.Mode == ModeAdhoc && req.CustomPrompt != "" && len(req.CustomPrompt) <= longPromptThreshold {
			userMessage = req.CustomPrompt
		} else {
			userMessage = spec.DefaultUserMessage(req)
		}
	}

	// 9. Prepare the harness workspace: for Claude, agentctl's settings
	// (hooks, permissions, backend routing); for pi, the bridge extension.
	prep, err := h.PrepareWorkspace(harness.Workspace{
		Dir:           launchDirFn(),
		WriteSettings: func() (string, error) { return writeLaunchSettings(req.AgentName, ac, workDir, repoDir, useWorktree) },
		Backend:       hb,
	})
	if err != nil {
		return Result{}, err
	}
	sessionID := ids.NewUUID()
	windowName := tmux.NextInstanceName(req.AgentName)
	sessionName := SessionNameFor(req.AgentName, windowName, workDir, repoDir, useWorktree)
	permissionMode := launchPermissionMode(req, spec, ac)
	inv := baseInvocation(req.AgentName, sessionName, ac, prep)
	inv.SessionID = sessionID
	if caps.PermissionModes {
		inv.PermissionMode = permissionMode
	}

	// 10. Write system prompt to temp file.
	sysFile, err := writeTempPrompt(string(req.Mode)+"-sys", systemPrompt)
	if err != nil {
		return Result{}, fmt.Errorf("writing system prompt: %w", err)
	}
	logging.Debug("dispatch: full system prompt %s (%d bytes):\n%s", sysFile, len(systemPrompt), systemPrompt)
	inv.SystemPromptFile = sysFile

	// 11. Build shell command.
	// For interactive (non-autonomous) modes with no user message, omit the
	// message argument so the agent starts in interactive mode rather than
	// sending an empty first message.
	// logCmd is shellCmd with the user prompt elided: the prompt carries issue
	// and spec text, which does not belong in the normal-level log.
	logCmd, err := h.Command(inv)
	if err != nil {
		return Result{}, fmt.Errorf("building %s command: %w", h.Name(), err)
	}
	shellCmd := logCmd
	if userMessage != "" {
		inv.Message = userMessage
		if shellCmd, err = h.Command(inv); err != nil {
			return Result{}, fmt.Errorf("building %s command: %w", h.Name(), err)
		}
		logCmd = fmt.Sprintf("%s <prompt elided, %d bytes>", logCmd, len(userMessage))
	}

	// 12. Launch tmux window.
	// Ensure shell-prefix.sh is on disk so audit logging works from the first command.
	if err := hooks.EnsureShellPrefixScript(); err != nil {
		logging.Log("dispatch: EnsureShellPrefixScript: %v", err)
		// Non-fatal — continue launch without shell-prefix if the write failed.
	}
	logPath := h.LaunchLogPath(acct.Dir, workDir, sessionID)
	extraEnv := buildDispatchEnv(req, ac, workDir, repoDir, ownBranch, inWorktree)
	for k, v := range backendEnv {
		extraEnv[k] = v
	}
	applyParentEnv(req, extraEnv)
	extraEnv[EnvSessionName] = sessionName
	ApplyHarnessEnv(extraEnv, h, permissionMode, prep)

	logging.Log("dispatch: shellCmd len=%d: %s", len(shellCmd), textutil.Truncate(logCmd, 500, "..."))
	logging.Debug("dispatch: full shellCmd: %s", shellCmd)
	wid, err := tmux.LaunchAgent(windowName, workDir, shellCmd, logPath, req.AgentName, sessionID, extraEnv, !req.Background)
	if err != nil {
		return Result{}, fmt.Errorf("launching agent: %w", err)
	}

	// 13. Update window state + dispatch log.
	logLaunchBanner(req.AgentName, windowName, string(req.Mode), req.Ticket, req.TicketTitle, workDir, sessionID)

	if wid != "" {
		windowstate.Update(wid, func(ws *windowstate.WindowState) { //nolint:errcheck
			ws.Instance = windowName
			ws.AgentName = req.AgentName
			if req.Ticket > 0 {
				ws.Ticket = fmt.Sprintf("%d", req.Ticket)
				ws.TicketTitle = req.TicketTitle
			} else if task := TaskFor(req); task != "" {
				ws.Task = task
			}
			ws.Mode = string(req.Mode)
			if req.ScheduledBy != "" {
				ws.ScheduledBy = req.ScheduledBy
				ws.ScheduledAt = time.Now()
			}
			// The name is unique per window by construction (worktree
			// slug or instance number), so it is safe to show others.
			ws.SessionNameVerified = true
			ws.Branch = ownBranch
			// Only record WorktreeDir when a worktree was actually created for
			// this dispatch -- workDir still equals repoDir when useWorktree was
			// requested but worktree.Create fell back to the shared repo dir, and
			// recording that as a "worktree" would make `agentctl cleanup` treat
			// the main checkout itself as a reclaimable dispatch worktree.
			if inWorktree {
				ws.WorktreeDir = workDir
			}
		})
		if !req.Background {
			if err := tmux.SwitchToWindow(wid); err != nil {
				logging.Warn("dispatch: switch to window %s: %v", wid, err)
			}
		}
	}

	logging.Log("dispatch: %s → %s (window %s, dir %s)", modeLogPrefix(req), req.AgentName, wid, workDir)

	return Result{
		WindowID:  wid,
		AgentName: req.AgentName,
		SessionID: sessionID,
		WorkDir:   workDir,
		Branch:    ownBranch,
		Account:   acct.Name,
		Harness:   h.Name(),

		MessagesAuthor: messagesAuthor,
	}, nil
}

// prepareReview finishes a ModeReviewPR request once the account and harness
// are known: it copies ReviewPR (Launch must not mutate the caller's), marks
// a cross-account author, and records whether the launch runs in the shared
// checkout. It returns whether the prompt names the author as reachable.
func prepareReview(req *Request, cfg *config.Config, accounts bool, reviewerDir string, inWorktree bool) bool {
	if req.ReviewPR == nil {
		return false
	}
	pr := *req.ReviewPR
	req.ReviewPR = &pr
	req.reviewSharedCheckout = !inWorktree
	if !pr.Author.Known() || req.noMessaging {
		return false
	}
	if cfg.HasAccounts() && accounts && crossAccount(reviewerDir, claudehome.ParentDir(pr.Author.Account)) {
		logging.Log("dispatch: PR #%d author %s runs under a different account; the reviewer reports to its orchestrator only", pr.Number, pr.Author.WindowID)
		pr.Author.crossAccount = true
		return false
	}
	return true
}

// buildDispatchEnv builds the extraEnv map passed to tmux.LaunchAgent for a
// dispatched agent's window. It is factored out of Launch() so the resulting
// map -- in particular AGENTCTL_RUNTIME_DIR -- is testable without a live
// tmux/worktree environment.
//
// AGENTCTL_RUNTIME_DIR follows session ownership, not project ownership: the
// window lives in (and is monitored and rendered by) the dispatcher's tmux
// session, so it inherits the dispatcher's paths.RuntimeDir() verbatim, even
// for a cross-project --dir dispatch. Re-deriving it from the target repo
// would create a second .agentctl tree that the session's monitor and chrome
// bar never read.
//
// Only when paths.RuntimeDir() is empty (dispatch invoked before paths.Init)
// does it fall back to worktree.MainRepoRoot(repoDir) joined with
// ".agentctl"; anchoring on the main repo root keeps a dispatcher that has
// cd-ed into a subdirectory from producing a bogus runtime dir.
//
// inWorktree reports whether Launch actually created a worktree (not just
// requested one); this function never dereferences req.UseWorktree (a *bool).
// AGENTCTL_WORKTREE and AGENTCTL_BRANCH are set only then: tmux.LaunchAgent
// unsets identity variables a launch does not set, so they are never
// inherited from the launcher either.
func buildDispatchEnv(req Request, ac config.AgentConfig, workDir, repoDir, branch string, inWorktree bool) map[string]string {
	extraEnv := AgentEnvOverrides(ac)
	if req.RepoSlug != "" {
		extraEnv["AGENTCTL_REPO"] = req.RepoSlug
	}
	if rd := paths.RuntimeDir(); rd != "" {
		extraEnv["AGENTCTL_RUNTIME_DIR"] = rd
	} else {
		extraEnv["AGENTCTL_RUNTIME_DIR"] = filepath.Join(worktree.MainRepoRoot(repoDir), ".agentctl")
	}
	if inWorktree && branch != "" {
		extraEnv["AGENTCTL_WORKTREE"] = workDir
		extraEnv["AGENTCTL_BRANCH"] = branch
	}
	if req.Ticket > 0 {
		extraEnv["AGENTCTL_TICKET"] = fmt.Sprintf("%d", req.Ticket)
		extraEnv["AGENTCTL_TICKET_TITLE"] = req.TicketTitle
	}
	extraEnv["AGENTCTL_MODE"] = string(req.Mode)
	return extraEnv
}

// applyParentEnv records the dispatcher on extraEnv unless the launch is
// Detached, which has no parent window by definition.
func applyParentEnv(req Request, extraEnv map[string]string) {
	if !req.Detached {
		applySpawnedByEnv(extraEnv)
	}
}

// applySpawnedByEnv sets AGENTCTL_SPAWNED_BY and AGENTCTL_SPAWNED_BY_SESSION
// on extraEnv from the dispatching process's own tmux window ID and Claude
// session UUID, giving the dispatched agent a reply-to address (Phase 1 of
// docs/agent-messaging-design.md — nothing consumes these yet).
//
// Both may legitimately be empty (e.g. dispatch run from a plain shell
// outside tmux). Empty means "no parent": the keys are left unset entirely
// rather than written as empty strings, and callers must not error on their
// absence.
func applySpawnedByEnv(extraEnv map[string]string) {
	if spawnedBy := signal.ResolveWindowID(); spawnedBy != "" {
		extraEnv["AGENTCTL_SPAWNED_BY"] = spawnedBy
	}
	// AGENTCTL_SESSION_ID reaches a Bash tool via CLAUDE_ENV_FILE and can be
	// missing on the first command after a restore, resume or /clear; Claude
	// Code itself sets CLAUDE_CODE_SESSION_ID in every Bash tool env.
	spawnedBySession := os.Getenv("AGENTCTL_SESSION_ID")
	if spawnedBySession == "" {
		spawnedBySession = os.Getenv("CLAUDE_CODE_SESSION_ID")
	}
	if spawnedBySession != "" {
		extraEnv["AGENTCTL_SPAWNED_BY_SESSION"] = spawnedBySession
	}
}

// buildBranch returns the git branch name for the dispatch worktree. Ticket
// wins over mode when both are present -- an issue number is the more
// specific, more useful identifier. ModeBare (interactive orchestrator
// sessions with no ticket) gets a "session-" prefix so a human can tell it
// apart from real autonomous work at a glance; every other ticketless mode
// keeps the historical "adhoc-" prefix.
func buildBranch(req Request) string {
	if req.Ticket > 0 {
		return fmt.Sprintf("agent/%s/issue-%d", req.AgentName, req.Ticket)
	}
	slug := ids.NewUUID()[:8]
	if req.ReviewPR != nil {
		// Suffixed so a second review of the same PR never shares a live
		// reviewer's worktree.
		return fmt.Sprintf("agent/%s/review-pr-%d-%s", req.AgentName, req.ReviewPR.Number, slug)
	}
	if req.Mode == ModeBare {
		return fmt.Sprintf("agent/%s/session-%s", req.AgentName, slug)
	}
	return fmt.Sprintf("agent/%s/adhoc-%s", req.AgentName, slug)
}

func modeLogPrefix(req Request) string {
	if req.Ticket > 0 {
		return fmt.Sprintf("#%d", req.Ticket)
	}
	if req.ReviewPR != nil {
		return fmt.Sprintf("review PR #%d", req.ReviewPR.Number)
	}
	return "adhoc"
}

// --- Helpers ported from main.go ---

// logLaunchBanner writes agent identity info to the agentctl runtime log.
func logLaunchBanner(agentName, windowName, mode string, ticket int, ticketTitle, workDir, sessionID string) {
	logging.Log("=== Agent Launched ===")
	logging.Log("  agent:    %s", agentName)
	logging.Log("  window:   %s", windowName)
	logging.Log("  mode:     %s", mode)
	if ticket > 0 {
		logging.Log("  ticket:   #%d — %s", ticket, ticketTitle)
	}
	logging.Log("  dir:      %s", workDir)
	logging.Log("  session:  %s", sessionID)
	logging.Log("======================")
}

// validPermissionModes is the set of values `claude --permission-mode` accepts.
var validPermissionModes = dispatchplan.ValidPermissionModes

// resolvePermissionMode returns the permission mode to pass to claude, or ""
// to pass none. A valid per-launch override wins over everything. An explicit valid config value wins in every mode; an invalid
// one is logged and ignored. Otherwise the default applies only to worker
// dispatch modes (ModeDispatch, ModeAdhoc, ModeReviewPR) -- interactive modes (bare,
// discuss, triage, review, custom) have a human at the keyboard and keep
// Claude's own default. The one exception is a human ad-hoc launch of an
// orchestrator converted to ModeBare: the caller passes ModeAdhoc for it
// (modeSpec.WorkerPermissionMode) so it gets the worker default too. The
// precedence lives in dispatchplan.PermissionMode, which the approval
// preview shares.
func resolvePermissionMode(mode Mode, ac config.AgentConfig, override string) string {
	if pm := ac.PermissionMode; pm != "" && !validPermissionModes[pm] && (override == "" || !validPermissionModes[override]) {
		logging.Warn("dispatch: invalid permission_mode %q for agent; falling back to default", pm)
	}
	return dispatchplan.PermissionMode(mode == ModeDispatch || mode == ModeAdhoc || mode == ModeReviewPR, ac.PermissionMode, override)
}

// launchPermissionMode resolves the permission mode for a launch. An ad-hoc
// orchestrator launch converted to ModeBare (spec.WorkerPermissionMode)
// resolves like the ad-hoc launch it started as.
func launchPermissionMode(req Request, spec modeSpec, ac config.AgentConfig) string {
	mode := req.Mode
	if spec.WorkerPermissionMode {
		mode = ModeAdhoc
	}
	return resolvePermissionMode(mode, ac, req.PermissionMode)
}

// execPath resolves the path of the running binary, used to prefix hook
// commands in BuildAgentSettingsJSON. It is a package-level function
// variable -- mirroring the writeWorktreeSettingsFn / randomSuffixFn seams
// elsewhere in this codebase -- purely so tests can pin a deterministic,
// "agentctl"-named path instead of the test binary's own path (which would
// never satisfy projectHasAgentctlHooks's "agentctl hook-" substring check,
// since `go test` binaries aren't named "agentctl").
var execPath = selfpath.Lookup

// BuildAgentSettingsJSON returns the marshaled Claude Code settings JSON for an
// agent (hooks + permissions). Exported so it can be written to multiple paths —
// the per-agent settings file AND a worktree's project-local settings file (the
// latter is needed because --settings is ignored when --agent is passed, see
// Claude Code bug #37730).
//
// includeHooks controls whether the "hooks" key is rendered at all. Claude
// Code merges hooks from every settings source it loads for a launch rather
// than letting one override another, so registering the full hook map in two
// files at once makes every hook fire twice (duplicate subprocess spawns,
// duplicate notifications, duplicate windowstate writes). The invariant this
// package maintains is: exactly ONE settings source registers hooks per
// launch. When includeHooks is false the "hooks" key is omitted from the
// JSON entirely (not rendered as an empty map, which Claude Code would still
// treat as a hooks source) — see WriteAgentSettings and WriteWorktreeSettings
// for which one wins and why.
func BuildAgentSettingsJSON(ac config.AgentConfig, includeHooks bool) ([]byte, error) {
	exe, _ := execPath()
	if exe == "" {
		home, _ := os.UserHomeDir()
		exe = filepath.Join(home, ".local", "bin", "agentctl")
	}

	type settingsPerms struct {
		Allow []string `json:"allow,omitempty"`
		Deny  []string `json:"deny,omitempty"`
	}
	type agentSettings struct {
		Hooks        map[string]interface{} `json:"hooks,omitempty"`
		Permissions  *settingsPerms         `json:"permissions,omitempty"`
		APIKeyHelper string                 `json:"apiKeyHelper,omitempty"`
		Env          map[string]string      `json:"env,omitempty"`
	}

	var s agentSettings
	nous, err := nousBackendFor(ac)
	if err != nil {
		return nil, err
	}
	if nous != nil {
		// The base URL and the credential helper always travel together in
		// the same file (see BackendEnv). The empty API key / auth token
		// blank any lower settings layer so a stray credential cannot
		// outrank the helper and reach Nous.
		s.APIKeyHelper = shellquote.Quote(exe) + " backend-token " + shellquote.Quote(nous.Name)
		s.Env = map[string]string{
			"ANTHROPIC_BASE_URL":   nous.BaseURL,
			"ANTHROPIC_API_KEY":    "",
			"ANTHROPIC_AUTH_TOKEN": "",
		}
	}
	if includeHooks {
		// Render the full canonical hook event set from internal/hooks — the
		// single source of truth for agentctl's hook map — using the resolved
		// binary path as the command prefix (rather than the literal
		// "~/.local/bin/agentctl" used by the global settings.json). No events
		// are excluded: dispatched agents get the same hook coverage as
		// interactive sessions.
		s.Hooks = hooks.BuildHooksConfig(exe)
	}

	if len(ac.BlockedCommands) > 0 {
		if s.Permissions == nil {
			s.Permissions = &settingsPerms{}
		}
		deny := make([]string, 0, len(ac.BlockedCommands))
		for _, bc := range ac.BlockedCommands {
			deny = append(deny, fmt.Sprintf("Bash(*%s*)", bc))
		}
		s.Permissions.Deny = deny
	}

	if nous != nil {
		// Defence in depth only: the agent runs as the same user and can
		// read its key by other means (see docs/configuration.md).
		if s.Permissions == nil {
			s.Permissions = &settingsPerms{}
		}
		s.Permissions.Deny = append(s.Permissions.Deny, nousDenyRules...)
	}

	if ac.AutoAccept {
		if s.Permissions == nil {
			s.Permissions = &settingsPerms{}
		}
		s.Permissions.Allow = append(s.Permissions.Allow,
			"Bash(*)", "Write(*)", "Edit(*)", "Read(*)",
			"Glob(*)", "Grep(*)", "Agent(*)", "WebFetch(*)",
			"WebSearch(*)", "NotebookEdit(*)",
		)
	}

	if len(ac.AllowedPermissions) > 0 {
		if s.Permissions == nil {
			s.Permissions = &settingsPerms{}
		}
		s.Permissions.Allow = append(s.Permissions.Allow, ac.AllowedPermissions...)
	}

	return json.MarshalIndent(s, "", "  ")
}

// WriteAgentSettings generates a per-session Claude Code settings file (the
// one passed to `claude --settings`) and returns its path. Exported because
// it is also used by the launch call sites in main.go, internal/cmd,
// internal/improve, and dispatch.Restore.
//
// It includes the "hooks" key ONLY when workDir's own project-local
// .claude/settings.local.json does not already register agentctl hooks (see
// projectHasAgentctlHooks). workDir must reflect the actual directory the
// agent will run in for this check to be meaningful.
//
// This exported form checks only workDir itself, matching every caller
// except dispatch.Launch's worktree path, which needs to check ancestors too
// (see writeAgentSettings and ancestorHasAgentctlHooks) because a worktree
// dispatch's ancestor directory is a second settings source Claude Code
// actually loads. Every other caller here runs directly in its own working
// directory with no such ancestor concern, so checking workDir alone is
// correct for them.
//
// When workDir is an agentctl worktree it also rewrites the worktree's
// settings.local.json from ac, so a restore, resume or relaunch never
// inherits backend routing (or loses it) from an earlier launch. A failure
// there is logged and the --settings file is still written. Outside a
// worktree nothing is written to the checkout's settings.local.json: the
// --settings file alone carries backend routing.
func WriteAgentSettings(name string, ac config.AgentConfig, workDir string) string {
	if root, ok := agentctlWorktreeRoot(workDir); ok {
		if err := WriteWorktreeSettings(workDir, root, ac); err != nil {
			logging.Warn("WriteAgentSettings: refreshing worktree settings in %s: %v", workDir, err)
		}
	}
	path, _ := writeAgentSettings(name, ac, workDir, workDir)
	return path
}

// writeAgentSettings is WriteAgentSettings's implementation, parameterized by
// hooksBoundary -- the upper bound (inclusive) of the ancestor-hooks scan
// (see ancestorHasAgentctlHooks). dispatch.Launch calls this directly for a
// worktree dispatch, passing the pre-worktree repo root as hooksBoundary so
// the --settings file's hooks decision considers the same ancestor
// .claude/settings.local.json that WriteWorktreeSettings just checked --
// otherwise a worktree whose own settings.local.json was written WITHOUT
// hooks (because the ancestor already has them) would still get a third
// hooks source from this file, since checking workDir alone would come up
// empty. WriteAgentSettings passes workDir as its own boundary, i.e. no
// ancestor walk, preserving its documented single-directory behavior for
// every other caller.
//
// When workDir is itself an agentctl worktree, the boundary always extends to
// the worktree's root (the main checkout), whatever the caller passed: Claude
// Code loads <root>/.claude/settings.local.json from the parent directory, so
// a --no-worktree launch or a restore inside a worktree would otherwise
// register the hooks twice.
func writeAgentSettings(name string, ac config.AgentConfig, workDir, hooksBoundary string) (string, error) {
	if root, ok := agentctlWorktreeRoot(workDir); ok {
		hooksBoundary = root
	}
	includeHooks := !ancestorHasAgentctlHooks(workDir, hooksBoundary)
	data, err := BuildAgentSettingsJSON(ac, includeHooks)
	if err != nil {
		logging.Log("WriteAgentSettings: marshal: %v", err)
		return "", fmt.Errorf("marshaling settings: %w", err)
	}

	path, err := writeSettingsFile(paths.RuntimeDir(), paths.SettingsDir(), settingsFileStem(name, ac.Backend), data)
	if err != nil {
		logging.Warn("WriteAgentSettings: %v", err)
		return "", err
	}

	logging.Log("WriteAgentSettings: wrote %s for agent %s", path, name)
	return path, nil
}

// agentctlWorktreeRoot reports whether dir is an agentctl worktree and
// returns the repository root. The layout must be <root>/.worktrees/<slug>
// AND dir must be a linked worktree of the repository at <root>: its .git
// file has to point into <root>/.git/worktrees/. A look-alike directory (for
// example one a cloned repo committed under .worktrees/) does not match.
func agentctlWorktreeRoot(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	dir = filepath.Clean(dir)
	parent := filepath.Dir(dir)
	if filepath.Base(parent) != ".worktrees" {
		return "", false
	}
	root := filepath.Dir(parent)
	fi, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return "", false
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return "", false
	}
	gitdir = filepath.Clean(strings.TrimSpace(gitdir))
	wtParent := filepath.Dir(gitdir) // <common>/worktrees
	if filepath.Base(wtParent) != "worktrees" {
		return "", false
	}
	if !sameDir(filepath.Dir(wtParent), filepath.Join(root, ".git")) {
		return "", false
	}
	return root, true
}

// sameDir reports whether a and b are the same path after resolving symlinks.
func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// unsafeStemChars matches characters not allowed in a settings file name.
var unsafeStemChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// settingsFileStem names the --settings file: <agent>@<backend> when a
// backend applies, so launches of one agent on different backends never
// overwrite each other's file.
func settingsFileStem(agentName, backend string) string {
	if backend == "" {
		return agentName
	}
	return agentName + "@" + unsafeStemChars.ReplaceAllString(backend, "_")
}

// loadConfigFn loads the config for backend resolution; tests replace it.
var loadConfigFn = config.Load

// nousBackendFor returns the nous backend that ac.Backend (set by
// PrepareBackend to the effective backend) names, or nil when the launch is
// not on a nous backend. A name that no longer resolves is an error: settings
// must never silently drop the routing for a backend the launch was prepared
// against.
func nousBackendFor(ac config.AgentConfig) (*Backend, error) {
	if ac.Backend == "" {
		return nil, nil
	}
	b, err := loadConfigFn().ResolveBackend(ac.Backend)
	if err != nil {
		return nil, fmt.Errorf("resolving backend for settings: %w", err)
	}
	if !b.IsNous() {
		return nil, nil
	}
	return &Backend{Name: ac.Backend, BackendConfig: b}, nil
}

// projectHasAgentctlHooks reports whether dir has a project-local
// .claude/settings.local.json that already registers at least one agentctl
// hook command (identified by the substring "agentctl hook-", matching the
// "<exe> hook-<name>" command shape from hooks.BuildHooksConfig). It checks
// exactly dir -- see ancestorHasAgentctlHooks for the ancestor-walking form
// used to detect a worktree dispatch's ancestor settings source.
//
// A missing file, an unreadable file, malformed JSON, or a file with no
// "hooks" key all return false — the failure mode here must be "hooks get
// registered from the --settings file too" (a harmless duplicate at worst),
// never "hooks silently stop firing because we assumed a file we couldn't
// actually read had them."
func projectHasAgentctlHooks(dir string) bool {
	path := filepath.Join(dir, ".claude", "settings.local.json")
	if !isUserOwnedSettings(dir) {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}

	var parsed struct {
		Hooks json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false
	}
	if len(parsed.Hooks) == 0 {
		return false
	}

	return strings.Contains(string(parsed.Hooks), "agentctl hook-")
}

// gitRunner is the git invocation isUserOwnedSettings uses; tests replace it.
var gitRunner gittracked.Runner = gittracked.ExecGit

// isUserOwnedSettings reports whether dir/.claude/settings.local.json may be
// trusted as the user's own hooks source: neither .claude nor the file is a
// symlink, the file is a regular file, and git does not track it.
//
// A repository can commit that file, and a committed file that merely
// mentions "agentctl hook-" (or registers look-alike commands that are not
// agentctl at all) would otherwise make agentctl drop its own hooks --
// including hook-validate-bash, which enforces blocked_commands. Parsing the
// entries more strictly does not close that, because a committed file can
// contain exactly the entries agentctl would write; only the file's
// provenance separates a user-maintained file from a repo-supplied one. A
// file git cannot classify (no git, not a repo) is treated as untracked:
// there is no repository to have supplied it.
func isUserOwnedSettings(dir string) bool {
	claudeDir := filepath.Join(dir, ".claude")
	if fi, err := os.Lstat(claudeDir); err != nil || !fi.IsDir() {
		return false
	}
	if fi, err := os.Lstat(filepath.Join(claudeDir, "settings.local.json")); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	// icase: on a case-insensitive filesystem a committed
	// ".CLAUDE/Settings.Local.json" is this very file.
	tracked, err := gittracked.Files(gitRunner, dir, ":(icase).claude/settings.local.json")
	if errors.Is(err, gittracked.ErrUnavailable) {
		return true // no repository could have supplied it
	}
	if err != nil {
		logging.Warn("dispatch: cannot tell whether %s/.claude/settings.local.json is tracked: %v", dir, err)
		return false // unknown provenance: do not trust it
	}
	return len(tracked) == 0
}

// writeSettingsFile writes data to <settingsDir>/<name>.json without
// following symlinks anywhere from root (the runtime dir) down, and returns
// the path. The runtime dir sits inside the user's repository, so a cloned
// repo can pre-populate it; see safefs.
func writeSettingsFile(root, settingsDir, name string, data []byte) (string, error) {
	if root == "" || settingsDir == "" {
		return "", fmt.Errorf("runtime dir not initialised")
	}
	path := filepath.Join(settingsDir, name+".json")
	if err := safefs.WriteFile(root, path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// maxAncestorHopsForHooks bounds ancestorHasAgentctlHooks's upward directory
// walk. It only exists as a hard safety net against an unexpected
// configuration (e.g. boundary not actually being an ancestor of startDir);
// in the normal case the walk terminates in 1-3 hops (worktreeDir ->
// .worktrees -> repoDir) long before this limit matters.
const maxAncestorHopsForHooks = 64

// ancestorHasAgentctlHooks reports whether startDir, or any ancestor
// directory from startDir up to and including boundary, has a
// .claude/settings.local.json that already registers agentctl hooks (see
// projectHasAgentctlHooks).
//
// This exists because Claude Code's project-local settings resolution is not
// confined to a single directory: a Claude Code process launched with its
// cwd inside an agentctl worktree (<repoDir>/.worktrees/<slug>/, nested
// INSIDE the repo) also loads <repoDir>/.claude/settings.local.json from the
// main checkout, since that file sits between startDir and boundary. Claude
// Code MERGES the "hooks" key from every settings source it loads rather
// than letting one override another, so if both files register the full
// hook map, every hook fires twice per tool call -- this was verified
// empirically (not assumed) by running `claude --debug -p ... ` from inside
// a scratch `git worktree add`-created subdirectory with hook-registering
// settings.local.json files planted at both the worktree dir and its parent:
// both hooks fired for the same tool call. See the PR description for the
// full probe.
//
// boundary is normally the pre-worktree repo root (dispatch.Launch's
// repoDir): the walk never needs to go further than that, and doing so would
// risk picking up an unrelated ancestor .claude/settings.local.json
// somewhere above the repo on the user's machine. If boundary equals
// startDir (WriteAgentSettings's non-worktree callers), the walk checks
// exactly startDir and stops -- identical to the pre-ancestor-walk behavior.
func ancestorHasAgentctlHooks(startDir, boundary string) bool {
	dir := filepath.Clean(startDir)
	boundary = filepath.Clean(boundary)
	for i := 0; i < maxAncestorHopsForHooks; i++ {
		if projectHasAgentctlHooks(dir) {
			return true
		}
		if dir == boundary {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached filesystem root without hitting boundary
			return false
		}
		dir = parent
	}
	return false
}

// WriteWorktreeSettings writes Claude Code settings to
// <worktreeDir>/.claude/settings.local.json so that hooks fire for agents
// running in the worktree. This works around Claude Code's --settings + --agent
// bug (#37730) by relying on project-local settings resolution, which is
// honored regardless of --agent.
//
// It includes hooks (includeHooks=true) UNLESS an ancestor of worktreeDir, up
// to and including repoRoot, already registers agentctl hooks (see
// ancestorHasAgentctlHooks) -- most commonly a hand-maintained
// <repoRoot>/.claude/settings.local.json in the main checkout, which Claude
// Code loads in addition to worktreeDir's own settings file. When that
// ancestor source exists, this file is written WITHOUT hooks so the ancestor
// remains the sole source; callers are responsible for making sure the
// --settings file written alongside it (writeAgentSettings, called with the
// same repoRoot as its hooksBoundary) also excludes hooks in that case. See
// BuildAgentSettingsJSON's doc comment for the single-source invariant.
func WriteWorktreeSettings(worktreeDir, repoRoot string, ac config.AgentConfig) error {
	includeHooks := !ancestorHasAgentctlHooks(filepath.Dir(worktreeDir), repoRoot)
	data, err := BuildAgentSettingsJSON(ac, includeHooks)
	if err != nil {
		return fmt.Errorf("building settings JSON: %w", err)
	}
	// The worktree is a checkout of the repository, so .claude/ and the
	// settings file may be whatever the repo committed -- including
	// symlinks. safefs.WriteFile refuses both rather than writing through.
	//
	// The file may also hold permission rules Claude Code saved there, so
	// only the agentctl-owned keys are updated (mergeAgentctlSettings).
	path := filepath.Join(worktreeDir, ".claude", "settings.local.json")
	existing, err := readSettingsFile(worktreeDir, path)
	if err != nil {
		return fmt.Errorf("reading settings: %w", err)
	}
	data, err = mergeAgentctlSettings(existing, data)
	if err != nil {
		return fmt.Errorf("merging settings in %s: %w", path, err)
	}
	if err := safefs.WriteFile(worktreeDir, path, data, 0o600); err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}
	logging.Log("WriteWorktreeSettings: wrote %s (includeHooks=%v)", path, includeHooks)
	return nil
}

// writeTempPrompt writes content to a 0600 file in the per-user launch
// directory (tmux.LaunchDir). Prompts can carry issue bodies and private
// context, so they must never land in a shared, world-readable temp dir.
func writeTempPrompt(prefix, content string) (string, error) {
	dir, err := tmux.LaunchDir()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, prefix+"-*.txt") // CreateTemp uses mode 0600
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// AgentEnvOverrides returns Claude Code env var overrides from agent config.
// Exported so that internal/improve can reuse the same logic without duplication.
// Note: CLAUDE_CODE_SHELL_PREFIX comes from the Claude harness's launch env
// (ApplyHarnessEnv) and is therefore not set here.
func AgentEnvOverrides(ac config.AgentConfig) map[string]string {
	env := make(map[string]string)
	if ac.MaxThinkingTokens > 0 {
		env["MAX_THINKING_TOKENS"] = fmt.Sprintf("%d", ac.MaxThinkingTokens)
	}
	return env
}

// launchOrchestrator is the orchestrator a launch reports to: none for an
// orchestrator role, and none for a Detached launch (agentctl serve), whose
// dispatcher is a phone: resolving one there would pick up whatever window
// the serve process happens to inherit.
func launchOrchestrator(req Request, spec modeSpec) Orchestrator {
	if spec.Role == RoleOrchestrator || req.Detached {
		return Orchestrator{}
	}
	return resolveOrchestratorFn()
}

// CallerIsAgentSession reports whether this process was started from a
// tracked Claude session (an agent's Bash tool) rather than by a person in a
// plain shell: the same test that decides whether a launch has an
// orchestrator.
func CallerIsAgentSession() bool {
	return resolveOrchestratorFn().Known()
}

// resolveOrchestratorFn is the seam launchOrchestrator resolves through.
var resolveOrchestratorFn = resolveOrchestrator

// resolveOrchestrator identifies the dispatching Claude session. It returns
// the zero value unless the dispatcher is a tracked session, so a human
// dispatching from a plain shell window (or outside tmux) yields no
// orchestrator for the worker to report to.
func resolveOrchestrator() Orchestrator {
	return orchestratorFor(signal.ResolveWindowID(), os.Getenv("AGENTCTL_SESSION_ID"), windowstate.Read)
}

// orchestratorFor is resolveOrchestrator with its inputs injected. A window
// counts only when it has a Claude session: envSessionID (the same signal as
// spawned_by_session) or a windowstate record carrying a SessionID. A tracked
// session with no name still counts.
func orchestratorFor(windowID, envSessionID string, read func(string) (windowstate.WindowState, error)) Orchestrator {
	if windowID == "" {
		return Orchestrator{}
	}
	ws, err := read(windowID)
	if err != nil {
		ws = windowstate.WindowState{}
	}
	if envSessionID == "" && ws.SessionID == "" {
		return Orchestrator{}
	}
	o := Orchestrator{WindowID: windowID, Pane: ws.MainPane}
	if ws.SessionNameVerified {
		o.Name = ws.SessionName
	}
	return o
}

// taskLineMax is the rune budget for the recorded ad-hoc task line.
const taskLineMax = 80

// TaskFor returns the one-line task recorded in windowstate for an ad-hoc
// dispatch (the first prose line of its prompt), or "" for a ticket dispatch
// or an empty prompt.
func TaskFor(req Request) string {
	if req.Ticket > 0 {
		return ""
	}
	if pr := req.ReviewPR; pr != nil {
		return textutil.TaskLine(fmt.Sprintf("Review PR #%d: %s", pr.Number, pr.Title), taskLineMax)
	}
	return textutil.TaskLine(req.CustomPrompt, taskLineMax)
}
