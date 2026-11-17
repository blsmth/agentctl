package dispatch

import (
	"fmt"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/tmux"
)

// resolveHarness picks the harness for launching agentName: override
// (dispatch --harness) > agents.<name>.harness > the agent's frontmatter
// harness > harness.Default. An unknown name is an error.
func resolveHarness(cfg *config.Config, agentName, override, workDir string) (harness.Harness, error) {
	fm := ""
	if override == "" && cfg.ForAgent(agentName).Harness == "" {
		fm = frontmatterHarnessFn(cfg, agentName, workDir)
	}
	h, err := harness.Resolve(override, cfg.ForAgent(agentName).Harness, fm)
	if err != nil {
		return nil, fmt.Errorf("agent %s: %w", agentName, err)
	}
	return h, nil
}

// frontmatterHarnessFn returns the harness named in agentName's definition
// frontmatter ("" when none or not found). A var for tests.
var frontmatterHarnessFn = frontmatterHarness

func frontmatterHarness(cfg *config.Config, agentName, workDir string) string {
	agents, err := agent.DiscoverAll(cfg, projectDirsForBodyResolve(cfg, workDir))
	if err != nil {
		return ""
	}
	for _, a := range agents {
		if a.Name == agentName {
			return a.Harness
		}
	}
	return ""
}

// ResolveHarness is resolveHarness for launch paths outside this package
// (improvement sessions, the sessions browser's resume).
func ResolveHarness(cfg *config.Config, agentName, override, workDir string) (harness.Harness, error) {
	return resolveHarness(cfg, agentName, override, workDir)
}

// modeDefaultModel is the mode's default model for h. Mode defaults are
// Claude model aliases ("sonnet"), which mean nothing to another harness's
// model resolution, so other harnesses keep their own default.
func modeDefaultModel(h harness.Harness, spec modeSpec) string {
	if h.Name() != harness.Claude {
		return ""
	}
	return spec.DefaultModel
}

// launchAccount is the account a launch runs under, plus the dispatching
// window's account it was resolved against.
type launchAccount struct {
	claudehome.Resolution
	parent string
}

// prepareAccountAndBackend resolves the Claude account and the backend for a
// launch and returns the env they add. Account comes first: a nous
// preflight checks the account's own settings.json. It never falls back to
// another account on error.
//
// Harnesses without accounts or backends (pi) skip both. An explicit
// --account or --backend for such a harness is an error (the user asked for
// something that cannot happen); a value from the agent's config is ignored
// with a warning, so one agent config can serve several harnesses.
func prepareAccountAndBackend(cfg *config.Config, h harness.Harness, ac *config.AgentConfig, req Request, explicitModel, workDir string) (launchAccount, map[string]string, error) {
	env := map[string]string{}
	caps := h.Caps()
	var acct launchAccount
	if caps.Accounts {
		if !req.Detached && cfg.HasAccounts() {
			acct.parent = parentAccountFn()
		}
		res, err := resolveAccountFn(cfg, req.Account, req.AgentName, workDir, acct.parent)
		if err != nil {
			return acct, nil, fmt.Errorf("resolving account: %w", err)
		}
		acct.Resolution = res
		ApplyAccountEnv(env, res)
	} else if req.Account != "" {
		return acct, nil, fmt.Errorf("--account %q: the %s harness has no accounts", req.Account, h.Name())
	}
	if caps.Backends {
		if _, err := PrepareBackend(cfg, ac, req.Backend, explicitModel, workDir, env); err != nil {
			return acct, nil, fmt.Errorf("resolving backend: %w", err)
		}
		return acct, env, nil
	}
	if req.Backend != "" && req.Backend != BackendNone {
		return acct, nil, fmt.Errorf("--backend %q: backends do not apply to the %s harness", req.Backend, h.Name())
	}
	if ac.Backend != "" {
		logging.Warn("dispatch: agent %s: backend %q ignored on the %s harness", req.AgentName, ac.Backend, h.Name())
		ac.Backend = ""
	}
	return acct, env, nil
}

// baseInvocation is the part of a harness invocation every launch path
// shares: agent, session name, model and effort from ac, and the files
// PrepareWorkspace wrote.
func baseInvocation(agentName, sessionName string, ac config.AgentConfig, prep harness.Prepared) harness.Invocation {
	return harness.Invocation{
		Agent:         agentName,
		Name:          sessionName,
		Model:         ac.Model,
		Effort:        ac.EffortLevel,
		SettingsPath:  prep.SettingsPath,
		ExtensionPath: prep.ExtensionPath,
	}
}

// ApplyHarnessEnv adds h's launch env (and, for a non-default harness, its
// name) to extraEnv. Keys the caller already set win, as they did when this
// env was built inside tmux.LaunchAgent.
func ApplyHarnessEnv(extraEnv map[string]string, h harness.Harness, permissionMode string) {
	for k, v := range h.LaunchEnv(harness.EnvParams{
		PermissionMode:    permissionMode,
		ShellPrefixScript: tmux.ShellPrefixScriptPath(),
	}) {
		if _, set := extraEnv[k]; !set {
			extraEnv[k] = v
		}
	}
	if name := harness.Canonical(h.Name()); name != "" {
		extraEnv[harness.EnvHarness] = name
	}
}

// launchDirFn is the private directory harness files are written to. A var
// for tests.
var launchDirFn = func() string {
	dir, err := tmux.LaunchDir()
	if err != nil {
		logging.Warn("dispatch: launch dir: %v", err)
		return ""
	}
	return dir
}

// writeLaunchSettings writes the Claude Code settings for a dispatch and
// returns the --settings path.
//
// For a worktree dispatch it first writes project-local settings into the
// worktree so hooks fire there, and does it BEFORE the --settings file.
// Needed because Claude Code ignores --settings when --agent is also passed
// (bug #37730). Project-local resolution honours the worktree directory and
// is NOT affected by the bug.
//
// Ordering matters for the single-hooks-source invariant: writeAgentSettings
// checks workDir and its ancestors up to repoDir (via
// ancestorHasAgentctlHooks) and omits hooks from the --settings file when it
// finds agentctl hooks already registered anywhere in that chain. Writing the
// worktree settings first means that check sees them and correctly excludes
// hooks from the --settings file for this dispatch, so hooks fire from exactly
// one source instead of two (see BuildAgentSettingsJSON's doc comment).
//
// repoDir is passed as the ancestor-walk boundary in both calls: an agentctl
// worktree lives at <repoDir>/.worktrees/<slug>/, nested INSIDE the repo, and
// Claude Code's project-local settings resolution walks up the directory tree
// from cwd rather than stopping at the immediate directory -- so a
// hand-maintained <repoDir>/.claude/settings.local.json that already
// registers agentctl hooks gets loaded IN ADDITION TO whatever this dispatch
// writes into the worktree, unless both writers here account for it. See
// ancestorHasAgentctlHooks's doc comment for the empirical verification.
//
// A failure aborts the dispatch rather than launching anyway: the usual
// cause is a branch that commits .claude/ (or its settings.local.json) as a
// symlink, and launching regardless would run the worker -- often in auto
// permission mode -- without agentctl's hooks, including hook-validate-bash.
// The --settings file is held to the same rule whenever a runtime dir exists.
func writeLaunchSettings(agentName string, ac config.AgentConfig, workDir, repoDir string, useWorktree bool) (string, error) {
	if useWorktree && workDir != repoDir {
		if err := WriteWorktreeSettings(workDir, repoDir, ac); err != nil {
			logging.Warn("dispatch: WriteWorktreeSettings: %v", err)
			return "", fmt.Errorf("writing worktree settings in %s (refusing to launch without agentctl hooks): %w", workDir, err)
		}
	}
	settingsPath, err := writeAgentSettings(agentName, ac, workDir, repoDir)
	if err != nil && paths.RuntimeDir() != "" {
		return "", fmt.Errorf("writing agent settings (refusing to launch without agentctl hooks): %w", err)
	}
	return settingsPath, nil
}

// BaseInvocation is baseInvocation for launch paths outside this package.
func BaseInvocation(agentName, sessionName string, ac config.AgentConfig, prep harness.Prepared) harness.Invocation {
	return baseInvocation(agentName, sessionName, ac, prep)
}

// LaunchDir is the private directory harness files (the pi bridge
// extension) are written to; "" when it cannot be created.
func LaunchDir() string { return launchDirFn() }
