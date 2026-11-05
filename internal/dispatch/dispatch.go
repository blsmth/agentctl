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
	"strings"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/config"
	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/gittracked"
	"github.com/blsmth/agentctl/internal/hooks"
	"github.com/blsmth/agentctl/internal/ids"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/safefs"
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
	Model       string // overrides agent config and mode default when set
	Background  bool   // when true, skip switching to the new window after launch
}

// Result contains the output of a successful Launch.
type Result struct {
	WindowID  string
	AgentName string
	SessionID string
	WorkDir   string
	Branch    string
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
//  9. Generate session ID, base flags, settings file
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

	// Resolve useWorktree ONCE, immediately after the mode lookup -- this is
	// the single chokepoint for the mode-default-with-explicit-override
	// pattern. Every other site in this file that needs to know whether the
	// launch is worktree-isolated reads this local, never req.UseWorktree
	// directly (see Request.UseWorktree's doc comment).
	useWorktree := spec.DefaultWorktree
	if req.UseWorktree != nil {
		useWorktree = *req.UseWorktree
	}

	// 2. Resolve working directory.
	workDir := req.WorkDir
	if workDir == "" {
		return Result{}, fmt.Errorf("WorkDir is required")
	}

	// 3. Create worktree if requested.
	var branch string
	repoDir := workDir // remember original repo dir so we can detect an actual worktree below
	if useWorktree {
		branch = buildBranch(req)
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

	// 4. Load agent config.
	cfg := config.Load()
	ac := cfg.ForAgent(req.AgentName)

	// Apply model: priority order is Request.Model > AgentConfig.Model > modeSpec.DefaultModel.
	if req.Model != "" {
		ac.Model = req.Model
	} else if ac.Model == "" && spec.DefaultModel != "" {
		ac.Model = spec.DefaultModel
	}

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
	var sharedConventions string
	if body := resolveAgentBody(cfg, req.AgentName, workDir, repoDir); body != "" {
		expanded, missing := agent.ExpandSharedRefs(body)
		sharedConventions = expanded
		for _, ref := range missing {
			logging.Warn("dispatch: agent %q references unresolved shared include %q", req.AgentName, ref)
		}
	}

	// 7. Build system prompt.
	systemPrompt := buildSystemPrompt(req, spec, workDir, branch, useWorktree, contextEntries, sharedConventions)

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

	// Write project-local settings into the worktree so hooks fire there, and
	// do it BEFORE WriteAgentSettings below. Needed because Claude Code ignores
	// --settings when --agent is also passed (bug #37730). Project-local
	// resolution honours the worktree directory and is NOT affected by the
	// bug. Best-effort — a failure here is non-fatal.
	//
	// Ordering matters for the single-hooks-source invariant: WriteAgentSettings
	// checks workDir and its ancestors up to repoDir (via
	// ancestorHasAgentctlHooks) and omits hooks from the --settings file when it
	// finds agentctl hooks already registered anywhere in that chain. Writing the
	// worktree settings first means that check sees them and correctly excludes
	// hooks from the --settings file for this dispatch, so hooks fire from exactly
	// one source instead of two (see BuildAgentSettingsJSON's doc comment).
	//
	// repoDir is passed as the ancestor-walk boundary in both calls below: an
	// agentctl worktree lives at <repoDir>/.worktrees/<slug>/, nested INSIDE the
	// repo, and Claude Code's project-local settings resolution walks up the
	// directory tree from cwd rather than stopping at the immediate directory --
	// so a hand-maintained <repoDir>/.claude/settings.local.json that already
	// registers agentctl hooks gets loaded IN ADDITION TO whatever this dispatch
	// writes into the worktree, unless both writers here account for it. See
	// ancestorHasAgentctlHooks's doc comment for the empirical verification.
	//
	// A failure here aborts the dispatch rather than launching anyway: the
	// usual cause is a branch that commits .claude/ (or its
	// settings.local.json) as a symlink, and launching regardless would run
	// the worker -- often in auto permission mode -- without agentctl's
	// hooks, including hook-validate-bash.
	if useWorktree && workDir != repoDir {
		if err := WriteWorktreeSettings(workDir, repoDir, ac); err != nil {
			logging.Warn("dispatch: WriteWorktreeSettings: %v", err)
			return Result{}, fmt.Errorf("writing worktree settings in %s (refusing to launch without agentctl hooks): %w", workDir, err)
		}
	}

	// 9. Generate session ID, base flags, settings.
	sessionID := ids.NewUUID()
	settingsPath, err := writeAgentSettings(req.AgentName, ac, workDir, repoDir)
	if err != nil && paths.RuntimeDir() != "" {
		// Same reasoning as the worktree settings above: a refused write
		// (symlinked settings file or dir) must not launch a hookless agent.
		return Result{}, fmt.Errorf("writing agent settings (refusing to launch without agentctl hooks): %w", err)
	}
	baseFlags := BuildBaseFlags(ClaudeInvocation{
		Agent:          req.AgentName,
		SessionID:      sessionID,
		PermissionMode: resolvePermissionMode(req.Mode, ac),
		SettingsPath:   settingsPath,
	}, ac)

	// 10. Write system prompt to temp file.
	sysFile, err := writeTempPrompt(string(req.Mode)+"-sys", systemPrompt)
	if err != nil {
		return Result{}, fmt.Errorf("writing system prompt: %w", err)
	}
	logging.Debug("dispatch: full system prompt %s (%d bytes):\n%s", sysFile, len(systemPrompt), systemPrompt)

	// 11. Build shell command.
	// For interactive (non-autonomous) modes with no user message, omit the
	// message argument so claude starts in interactive mode rather than sending
	// an empty first message.
	// logCmd is shellCmd with the user prompt elided: the prompt carries issue
	// and spec text, which does not belong in the normal-level log.
	logCmd := fmt.Sprintf(
		"%s --append-system-prompt-file %s",
		baseFlags, shellquote.Quote(sysFile),
	)
	shellCmd := logCmd
	if userMessage != "" {
		shellCmd = logCmd + " " + shellquote.Quote(userMessage)
		logCmd = fmt.Sprintf("%s <prompt elided, %d bytes>", logCmd, len(userMessage))
	}

	// 12. Launch tmux window.
	// Ensure shell-prefix.sh is on disk so audit logging works from the first command.
	if err := hooks.EnsureShellPrefixScript(); err != nil {
		logging.Log("dispatch: EnsureShellPrefixScript: %v", err)
		// Non-fatal — continue launch without shell-prefix if the write failed.
	}
	windowName := tmux.NextInstanceName(req.AgentName)
	logPath := claude.LogPath(workDir, sessionID)
	extraEnv := buildDispatchEnv(req, ac, workDir, repoDir, branch, useWorktree)
	applySpawnedByEnv(extraEnv)

	logging.Log("dispatch: shellCmd len=%d: %s", len(shellCmd), textutil.Truncate(logCmd, 500, "..."))
	logging.Debug("dispatch: full shellCmd: %s", shellCmd)
	wid, err := tmux.LaunchAgent(windowName, workDir, shellCmd, logPath, req.AgentName, sessionID, extraEnv)
	if err != nil {
		return Result{}, fmt.Errorf("launching agent: %w", err)
	}

	// 13. Update window state + dispatch log.
	logLaunchBanner(req.AgentName, windowName, string(req.Mode), req.Ticket, req.TicketTitle, workDir, sessionID)

	if wid != "" {
		windowstate.Update(wid, func(ws *windowstate.WindowState) { //nolint:errcheck
			if req.Ticket > 0 {
				ws.Ticket = fmt.Sprintf("%d", req.Ticket)
				ws.TicketTitle = req.TicketTitle
			}
			ws.Mode = string(req.Mode)
			ws.Branch = branch
			// Only record WorktreeDir when a worktree was actually created for
			// this dispatch -- workDir still equals repoDir when useWorktree was
			// requested but worktree.Create fell back to the shared repo dir, and
			// recording that as a "worktree" would make `agentctl cleanup` treat
			// the main checkout itself as a reclaimable dispatch worktree.
			if useWorktree && workDir != repoDir {
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
		Branch:    branch,
	}, nil
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
// useWorktree is the already-resolved mode-default-or-override value from
// Launch; this function never dereferences req.UseWorktree (a *bool).
func buildDispatchEnv(req Request, ac config.AgentConfig, workDir, repoDir, branch string, useWorktree bool) map[string]string {
	extraEnv := AgentEnvOverrides(ac)
	if req.RepoSlug != "" {
		extraEnv["AGENTCTL_REPO"] = req.RepoSlug
	}
	if rd := paths.RuntimeDir(); rd != "" {
		extraEnv["AGENTCTL_RUNTIME_DIR"] = rd
	} else {
		extraEnv["AGENTCTL_RUNTIME_DIR"] = filepath.Join(worktree.MainRepoRoot(repoDir), ".agentctl")
	}
	if useWorktree && branch != "" {
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
	if spawnedBySession := os.Getenv("AGENTCTL_SESSION_ID"); spawnedBySession != "" {
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
	if req.Mode == ModeBare {
		return fmt.Sprintf("agent/%s/session-%s", req.AgentName, slug)
	}
	return fmt.Sprintf("agent/%s/adhoc-%s", req.AgentName, slug)
}

func modeLogPrefix(req Request) string {
	if req.Ticket > 0 {
		return fmt.Sprintf("#%d", req.Ticket)
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

// defaultDispatchPermissionMode is the claude --permission-mode used for
// worker dispatches (issue + adhoc) when the agent config doesn't set one.
const defaultDispatchPermissionMode = "auto"

// validPermissionModes is the set of values `claude --permission-mode` accepts.
var validPermissionModes = map[string]bool{
	"acceptEdits":       true,
	"auto":              true,
	"bypassPermissions": true,
	"manual":            true,
	"dontAsk":           true,
	"plan":              true,
}

// resolvePermissionMode returns the permission mode to pass to claude, or ""
// to pass none. An explicit valid config value wins in every mode; an invalid
// one is logged and ignored. Otherwise the default applies only to worker
// dispatch modes (ModeDispatch, ModeAdhoc) -- interactive modes (bare,
// discuss, triage, review, custom) have a human at the keyboard and keep
// Claude's own default.
func resolvePermissionMode(mode Mode, ac config.AgentConfig) string {
	if pm := ac.PermissionMode; pm != "" {
		if validPermissionModes[pm] {
			return pm
		}
		logging.Warn("dispatch: invalid permission_mode %q for agent; falling back to default", pm)
	}
	if mode == ModeDispatch || mode == ModeAdhoc {
		return defaultDispatchPermissionMode
	}
	return ""
}

// ClaudeInvocation names the per-launch parts of a `claude` command line.
// Empty fields are omitted from the command.
type ClaudeInvocation struct {
	Agent          string // --agent (required)
	SessionID      string // --session-id: start a new session with this ID
	Resume         string // --resume: continue this existing session
	ForkSession    bool   // --fork-session: resume into a new session ID
	PermissionMode string // --permission-mode (see resolvePermissionMode)
	SettingsPath   string // --settings
}

// BuildBaseFlags returns the `claude` invocation prefix shared by every
// launch path (dispatch.Launch, Restore, improve's launchImprovement, and the
// sessions browser's resume): agent, session/resume flags, and -- when set
// -- model and effort level from ac, and the settings file.
//
// The result is a shell command line (it is embedded in a launch script), so
// every interpolated value is quoted with shellquote.Quote. Values here come
// from CLI flags, config files, and $HOME-derived paths; none of them may be
// trusted to be free of spaces or shell metacharacters. Using CLI flags
// rather than env vars for model/effort keeps them visible in process
// listings and lets them take precedence over ambient environment settings.
func BuildBaseFlags(inv ClaudeInvocation, ac config.AgentConfig) string {
	var b strings.Builder
	b.WriteString("claude --agent " + shellquote.Quote(inv.Agent))
	if inv.SessionID != "" {
		b.WriteString(" --session-id " + shellquote.Quote(inv.SessionID))
	}
	if inv.Resume != "" {
		b.WriteString(" --resume " + shellquote.Quote(inv.Resume))
	}
	if inv.ForkSession {
		b.WriteString(" --fork-session")
	}
	if ac.Model != "" {
		b.WriteString(" --model " + shellquote.Quote(ac.Model))
	}
	if ac.EffortLevel != "" {
		b.WriteString(" --effort " + shellquote.Quote(ac.EffortLevel))
	}
	if inv.PermissionMode != "" {
		b.WriteString(" --permission-mode " + shellquote.Quote(inv.PermissionMode))
	}
	if inv.SettingsPath != "" {
		b.WriteString(" --settings " + shellquote.Quote(inv.SettingsPath))
	}
	return b.String()
}

// execPath resolves the path of the running binary, used to prefix hook
// commands in BuildAgentSettingsJSON. It is a package-level function
// variable -- mirroring the writeWorktreeSettingsFn / randomSuffixFn seams
// elsewhere in this codebase -- purely so tests can pin a deterministic,
// "agentctl"-named path instead of the test binary's own path (which would
// never satisfy projectHasAgentctlHooks's "agentctl hook-" substring check,
// since `go test` binaries aren't named "agentctl").
var execPath = os.Executable

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
		Hooks       map[string]interface{} `json:"hooks,omitempty"`
		Permissions *settingsPerms         `json:"permissions,omitempty"`
	}

	var s agentSettings
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
func WriteAgentSettings(name string, ac config.AgentConfig, workDir string) string {
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
func writeAgentSettings(name string, ac config.AgentConfig, workDir, hooksBoundary string) (string, error) {
	includeHooks := !ancestorHasAgentctlHooks(workDir, hooksBoundary)
	data, err := BuildAgentSettingsJSON(ac, includeHooks)
	if err != nil {
		logging.Log("WriteAgentSettings: marshal: %v", err)
		return "", fmt.Errorf("marshaling settings: %w", err)
	}

	path, err := writeSettingsFile(paths.RuntimeDir(), paths.SettingsDir(), name, data)
	if err != nil {
		logging.Warn("WriteAgentSettings: %v", err)
		return "", err
	}

	logging.Log("WriteAgentSettings: wrote %s for agent %s", path, name)
	return path, nil
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
	path := filepath.Join(worktreeDir, ".claude", "settings.local.json")
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
// Note: CLAUDE_CODE_SHELL_PREFIX is wired unconditionally by tmux.LaunchAgent
// and is therefore not set here.
func AgentEnvOverrides(ac config.AgentConfig) map[string]string {
	env := make(map[string]string)
	if ac.MaxThinkingTokens > 0 {
		env["MAX_THINKING_TOKENS"] = fmt.Sprintf("%d", ac.MaxThinkingTokens)
	}
	return env
}
