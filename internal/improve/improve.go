package improve

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/hooks"
	"github.com/blsmth/agentctl/internal/ids"
	"github.com/blsmth/agentctl/internal/launchwatch"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/prompttemplate"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/shellquote"
	"github.com/blsmth/agentctl/internal/tmux"
)

// claudePermissions is a minimal struct for parsing Claude settings files.
type claudePermissions struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
}

// buildConfigContext produces a human-readable configuration block for the
// improvement prompt. It includes the agentctl AgentConfig, project-level
// Claude permissions (from <workDir>/.claude/settings.local.json), and global
// Claude permissions (from ~/.claude/settings.json). Missing files are
// silently skipped.
func buildConfigContext(agentName string, ac config.AgentConfig, workDir string) string {
	var b strings.Builder

	b.WriteString("CURRENT CONFIGURATION:\n")

	// --- agentctl AgentConfig ---
	b.WriteString(fmt.Sprintf("  agentctl config (%s):\n", agentName))

	model := ac.Model
	if model == "" {
		model = "(default)"
	}
	b.WriteString(fmt.Sprintf("    model:           %s\n", model))

	effortLevel := ac.EffortLevel
	if effortLevel == "" {
		effortLevel = "(default)"
	}
	b.WriteString(fmt.Sprintf("    effort_level:    %s\n", effortLevel))

	b.WriteString(fmt.Sprintf("    cross_context:   %v\n", ac.CrossContext))
	b.WriteString(fmt.Sprintf("    auto_accept:     %v\n", ac.AutoAccept))

	if len(ac.BlockedCommands) == 0 {
		b.WriteString("    blocked_commands: (none)\n")
	} else {
		b.WriteString(fmt.Sprintf("    blocked_commands: %s\n", strings.Join(ac.BlockedCommands, ", ")))
	}

	if len(ac.BlockedPatterns) == 0 {
		b.WriteString("    blocked_patterns: (none)\n")
	} else {
		b.WriteString(fmt.Sprintf("    blocked_patterns: %s\n", strings.Join(ac.BlockedPatterns, ", ")))
	}

	if len(ac.AllowedPermissions) == 0 {
		b.WriteString("    allowed_permissions: (none)\n")
	} else {
		b.WriteString("    allowed_permissions:\n")
		for _, p := range ac.AllowedPermissions {
			b.WriteString(fmt.Sprintf("      - %s\n", p))
		}
	}

	b.WriteString(fmt.Sprintf("    alerts:          blocked=%v  idle=%v  gone=%v\n",
		ac.AlertConfig.Blocked, ac.AlertConfig.Idle, ac.AlertConfig.Gone))

	// --- Project-level permissions ---
	if workDir != "" {
		localSettingsPath := filepath.Join(workDir, ".claude", "settings.local.json")
		if data, err := os.ReadFile(localSettingsPath); err == nil {
			var ps claudePermissions
			if json.Unmarshal(data, &ps) == nil {
				if len(ps.Permissions.Allow) > 0 || len(ps.Permissions.Deny) > 0 {
					b.WriteString(fmt.Sprintf("\n  project permissions (%s/.claude/settings.local.json):\n", workDir))
					if len(ps.Permissions.Allow) > 0 {
						b.WriteString("    allow:\n")
						for _, p := range ps.Permissions.Allow {
							b.WriteString(fmt.Sprintf("      - %s\n", p))
						}
					}
					if len(ps.Permissions.Deny) > 0 {
						b.WriteString("    deny:\n")
						for _, p := range ps.Permissions.Deny {
							b.WriteString(fmt.Sprintf("      - %s\n", p))
						}
					}
				}
			}
		}
	}

	// --- Global Claude permissions ---
	if home := claudehome.HomeDir(); home != "" {
		globalSettingsPath := filepath.Join(home, "settings.json")
		if data, err := os.ReadFile(globalSettingsPath); err == nil {
			var gs claudePermissions
			if json.Unmarshal(data, &gs) == nil {
				if len(gs.Permissions.Allow) > 0 || len(gs.Permissions.Deny) > 0 {
					b.WriteString(fmt.Sprintf("\n  global permissions (%s):\n", globalSettingsPath))
					if len(gs.Permissions.Allow) > 0 {
						b.WriteString("    allow:\n")
						for _, p := range gs.Permissions.Allow {
							b.WriteString(fmt.Sprintf("      - %s\n", p))
						}
					}
					if len(gs.Permissions.Deny) > 0 {
						b.WriteString("    deny:\n")
						for _, p := range gs.Permissions.Deny {
							b.WriteString(fmt.Sprintf("      - %s\n", p))
						}
					}
				}
			}
		}
	}

	return b.String()
}

// buildImprovePrompt composes the self-improvement prompt. State is
// hook-driven: the agent's harness hooks write per-window state to
// .agentctl/windows/<windowID>.json; an optional background monitor may read
// it, but nothing depends on it, and no transition handler launches improve
// sessions: they only start when a human asks (agentctl improve, prefix+i,
// the hub). See CLAUDE.md's "Key data flow" section.
//
// The only genuinely conditional content is the focus block, which depends
// on whether a theme was supplied -- see buildFocusBlock.
func buildImprovePrompt(agentPath, agentName, harnessName, configContext, theme, sessionContext string) string {
	return fmt.Sprintf(
		sessions.ImprovePromptHeader+" Be better than you were yesterday.\n\n"+
			"YOUR AGENT FILE: %s\n"+
			"Read it first — this is the file you will be updating.\n\n"+
			"%s\n"+
			"HOW THIS WORKS: your .md file IS your identity — agentctl launches the %s harness with `--agent %s`. "+
			"agentctl is a tmux-native command center: each agent runs in a tmux window, and the harness's hooks "+
			"write per-window state (working/idle/blocked) to disk. An optional background monitor (off by default) "+
			"may also read that state; there is no plugin system. This improvement session was started manually "+
			"(`agentctl improve`, prefix+i or the hub); nothing triggers it automatically.\n\n"+
			"%s"+
			"%s\n"+
			"INSTRUCTIONS:\n"+
			"1. Read your agent file\n"+
			"2. Make targeted improvements related to the focus area above\n"+
			"3. Every edit should make you measurably better at your job — clearer instructions, fewer mistakes, better defaults\n"+
			"4. Don't rewrite the whole file. Surgical, high-value changes only.\n\n"+
			"You exist to get better. Ship the improvement.",
		agentPath, configContext, harnessName, agentName, buildFocusBlock(theme), sessionContext,
	)
}

// buildFocusBlock returns the improvement-focus fragment: a themed directive
// when the caller specified one, otherwise generic guidance.
func buildFocusBlock(theme string) string {
	if theme != "" {
		return fmt.Sprintf(
			"IMPROVEMENT FOCUS: %s\n"+
				"Focus specifically on this area. Review the evidence for examples "+
				"related to this theme and update the agent definition to address it.\n\n",
			theme,
		)
	}
	return "Focus on:\n" +
		"- Patterns that should be documented as guidelines\n" +
		"- Common tasks that could benefit from better instructions\n" +
		"- Delegation rules that need updating\n" +
		"- Knowledge gaps that came up during sessions\n\n"
}

// DefaultFleetAgent runs a fleet review when no agent is named.
const DefaultFleetAgent = "brandon"

// DefaultSince is the evidence window when none is given.
const DefaultSince = 7 * 24 * time.Hour

// Options configures one improve run.
type Options struct {
	// Agent is the agent whose definition is improved (single mode) or that
	// runs the review (fleet mode; DefaultFleetAgent when empty).
	Agent string
	Fleet bool
	// SessionIDs (full IDs or prefixes) restrict the evidence to those sessions.
	SessionIDs []string
	// Since is the evidence window (DefaultSince when zero).
	Since time.Duration
	Theme string
	// Template names a prompt template whose body is added to the prompt; its
	// model fills Model when that is unset.
	Template string
	// Model overrides the agent's frontmatter and configured model.
	Model   string
	Backend string
	Account string
	// DryRun prints the evidence and prompt to Out and launches nothing.
	DryRun bool
	// Focus selects the new window. Only a human at the popup or hub wants it.
	Focus bool
	// CallerDir is the directory of the window the request came from.
	CallerDir string
	// Out receives the dry-run output (os.Stdout when nil).
	Out io.Writer
}

// LaunchSession launches a focused improvement session for the named agent
// from the popup or hub, where a human is waiting for the window. theme sets
// an optional improvement focus area. callerDir is the directory of the window
// the request came from; it is the working directory unless the agent has its
// own configured dir.
func LaunchSession(agentName string, theme string, callerDir string) error {
	return Run(Options{Agent: agentName, Theme: theme, CallerDir: callerDir, Focus: true})
}

// Run builds the evidence and prompt and launches the improvement session (or
// prints them for DryRun).
func Run(opts Options) error {
	agentName := opts.Agent
	if agentName == "" && opts.Fleet {
		agentName = DefaultFleetAgent
	}
	logging.Log("improve: start agent=%q fleet=%v theme=%q callerDir=%q focus=%v dry=%v", agentName, opts.Fleet, opts.Theme, opts.CallerDir, opts.Focus, opts.DryRun)
	if agentName == "" {
		return fmt.Errorf("no agent given")
	}

	done := logging.Timing("improve: agent.DiscoverAll")
	cfg := config.Load()
	agents, err := agent.DiscoverFromCwd(cfg)
	done()
	if err != nil {
		return fmt.Errorf("discovering agents: %w", err)
	}

	var target *agent.Agent
	for _, a := range agents {
		if a.Name == agentName {
			target = &a
			break
		}
	}
	if target == nil {
		logging.Log("improve: agent %q not found in discover results", agentName)
		return fmt.Errorf("unknown agent: %s", agentName)
	}

	ac := cfg.ForAgent(agentName)
	workDir, err := resolveWorkDir(target.Dir, ac.Dir, opts.CallerDir, paths.ProjectDir())
	if err != nil {
		return err
	}
	logging.Log("improve: workDir=%q filePath=%q", workDir, target.FilePath)

	var tmplBody string
	if opts.Template != "" {
		t, err := prompttemplate.Load(opts.Template, workDir)
		if err != nil {
			return fmt.Errorf("loading template %q: %w", opts.Template, err)
		}
		tmplBody = t.Prompt
		if opts.Model == "" {
			opts.Model = t.Model
		}
	}
	if opts.Model != "" {
		// An explicit model beats the agent's frontmatter: the launch passes
		// it as `--model`, which Claude Code applies over `--agent`.
		ac.Model = opts.Model
	}

	since := opts.Since
	if since <= 0 {
		since = DefaultSince
	}
	pack, err := Evidence(opts.Agent, opts.Fleet, since, opts.SessionIDs)
	if err != nil {
		return err
	}
	evidence := pack.Render(0)

	// Same harness as the agent itself.
	h, err := dispatch.ResolveHarness(cfg, agentName, "", workDir)
	if err != nil {
		return err
	}
	caps := h.Caps()

	var prompt string
	if opts.Fleet {
		prompt = buildFleetPrompt(agents, agentName, opts.Theme, evidence)
	} else {
		prompt = buildImprovePrompt(target.FilePath, agentName, h.Name(), buildConfigContext(agentName, ac, workDir), opts.Theme, evidence)
	}
	if tmplBody != "" {
		prompt += "\n\nTEMPLATE INSTRUCTIONS (" + opts.Template + "):\n" + strings.TrimRight(tmplBody, "\n")
	}

	if opts.DryRun {
		out := opts.Out
		if out == nil {
			out = os.Stdout
		}
		_, err := fmt.Fprintf(out, "%s\n\n--- PROMPT ---\n%s\n", evidence, prompt)
		return err
	}

	// Write prompt to a temp file to avoid shell escaping nightmares.
	promptFile, err := writePromptFile(agentName, prompt)
	if err != nil {
		return fmt.Errorf("writing prompt file: %w", err)
	}
	logging.Log("improve: prompt written to %s (%d bytes)", promptFile, len(prompt))

	sessionID := ids.NewUUID()

	// Same backend as the agent itself unless overridden: a local-model
	// agent's improvement session must not quietly run on Anthropic.
	extraEnv := dispatch.AgentEnvOverrides(ac)
	// Same account resolution as a launch of the agent itself (flag, agent
	// setting, rules on the repo, default); never another account on error.
	acctDir := ""
	if caps.Accounts {
		acct, err := claudehome.ForLaunch(cfg, opts.Account, agentName, workDir, "")
		if err != nil {
			return fmt.Errorf("resolving account: %w", err)
		}
		dispatch.ApplyAccountEnv(extraEnv, acct)
		acctDir = acct.Dir
	}
	hb, err := dispatch.PrepareBackendFor(cfg, h, &ac, opts.Backend, opts.Model, workDir, extraEnv)
	if err != nil {
		return err
	}

	// Build the command with per-session hook configuration.
	prep, err := h.PrepareWorkspace(harness.Workspace{
		Dir:           dispatch.LaunchDir(),
		WriteSettings: func() (string, error) { return dispatch.WriteAgentSettings(agentName, ac, workDir), nil },
		Backend:       hb,
	})
	if err != nil {
		return err
	}
	sessionName := improveSessionName(agentName, paths.ProjectDir(), workDir)
	extraEnv[dispatch.EnvSessionName] = sessionName
	inv := dispatch.BaseInvocation(agentName, sessionName, ac, prep)
	inv.SessionID = sessionID
	baseFlags, err := h.Command(inv)
	if err != nil {
		return fmt.Errorf("building %s command: %w", h.Name(), err)
	}
	shellCmd := buildImproveShellCmd(baseFlags, promptFile)

	// Improvement sessions run in the agent's work dir; compute the log path so
	// the monitor can track this window without scanning.
	logPath := h.LaunchLogPath(acctDir, workDir, sessionID)

	// Ensure shell-prefix.sh is on disk so audit logging works from the first command.
	if err := hooks.EnsureShellPrefixScript(); err != nil {
		logging.Log("improve: EnsureShellPrefixScript: %v", err)
		// Non-fatal — continue launch without shell-prefix if the write failed.
	}

	extraEnv["AGENTCTL_MODE"] = "improve"
	// Record who asked, so the window shows as a child row and closes with it.
	dispatch.ApplySpawnedBy(extraEnv)
	dispatch.ApplyHarnessEnv(extraEnv, h, "", prep)

	windowName := "improve~" + agentName
	logging.Log("improve: launching window %q focus=%v session=%s", windowName, opts.Focus, sessionID)
	wid, launchErr := tmux.LaunchAgent(windowName, workDir, shellCmd, logPath, agentName, sessionID, extraEnv, opts.Focus)
	if launchErr != nil {
		logging.Log("improve: LaunchAgent error: %v", launchErr)
	} else {
		logging.Log("improve: success")
		launchwatch.Start(wid, caps)
	}
	return launchErr
}

// buildFleetPrompt composes the prompt of a fleet-wide review: the evidence of
// every agent and the list of definitions the session may change.
func buildFleetPrompt(agents []agent.Agent, runner, theme, evidence string) string {
	var list strings.Builder
	for _, a := range agents {
		fmt.Fprintf(&list, "  - %s: %s\n", a.Name, a.FilePath)
	}
	return fmt.Sprintf(
		sessions.FleetImprovePromptHeader+" Be better than you were yesterday.\n\n"+
			"AGENT DEFINITIONS:\n%s\n"+
			"This review was started manually (`agentctl improve --fleet`); nothing triggers it automatically. "+
			"You run as the %s agent.\n\n"+
			"%s"+
			"%s\n"+
			"INSTRUCTIONS:\n"+
			"1. Read the evidence above and find problems that recur (two or more sessions) or that are a security issue\n"+
			"2. Read the definitions of the agents involved before changing them\n"+
			"3. Make targeted, high-value edits; don't rewrite whole files\n"+
			"4. Report what you changed and what you chose to leave alone",
		list.String(), runner, buildFocusBlock(theme), evidence,
	)
}

// buildImproveShellCmd returns the shell command line for an improvement
// session. baseFlags must already be shell-quoted (harness.Harness.Command);
// promptFile is quoted here. The prompt is sent straight to the harness.
func buildImproveShellCmd(baseFlags, promptFile string) string {
	pf := shellquote.Quote(promptFile)
	return fmt.Sprintf(`%s "$(cat %s)"; rm -f %s`, baseFlags, pf, pf)
}

// resolveWorkDir picks the improvement session's working directory: the
// agent's configured dir, then the caller's project root. It never falls back
// to the agent file's folder (e.g. ~/.claude/agents): account_rules would
// match the wrong repo and .agentctl runtime dirs would be created there.
func resolveWorkDir(agentDir, configuredDir, callerDir, projectDir string) (string, error) {
	for _, d := range []string{agentDir, paths.ExpandHome(configuredDir), callerDir, projectDir} {
		if d != "" {
			return d, nil
		}
	}
	return "", fmt.Errorf("cannot determine a working directory: agent has no dir configured and the caller's directory is unknown")
}

// writePromptFile writes the prompt to a 0600 file in the per-user launch
// directory (tmux.LaunchDir) and returns its path.
func writePromptFile(agentName string, prompt string) (string, error) {
	dir, err := tmux.LaunchDir()
	if err != nil {
		return "", err
	}

	f, err := os.CreateTemp(dir, fmt.Sprintf("improve-%s-*.txt", agentName)) // CreateTemp uses mode 0600
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := f.WriteString(prompt); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// improveSessionName names an improvement session <agent>-improve-<project>.
// The project is the project root's basename; workDir is only a fallback when
// the project root is unknown.
func improveSessionName(agentName, projectDir, workDir string) string {
	if projectDir == "" {
		projectDir = workDir
	}
	return dispatch.SessionNameFor(agentName+"-improve", "", projectDir, projectDir, false)
}
