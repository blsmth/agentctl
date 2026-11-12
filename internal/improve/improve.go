package improve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/hooks"
	"github.com/blsmth/agentctl/internal/ids"
	"github.com/blsmth/agentctl/internal/logging"
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

	b.WriteString(fmt.Sprintf("    auto_improve:    %v\n", ac.AutoImprove))
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
	if home, err := os.UserHomeDir(); err == nil {
		globalSettingsPath := filepath.Join(home, ".claude", "settings.json")
		if data, err := os.ReadFile(globalSettingsPath); err == nil {
			var gs claudePermissions
			if json.Unmarshal(data, &gs) == nil {
				if len(gs.Permissions.Allow) > 0 || len(gs.Permissions.Deny) > 0 {
					b.WriteString("\n  global permissions (~/.claude/settings.json):\n")
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

// buildImprovePrompt composes the self-improvement prompt. It used to be
// duplicated near-verbatim across the skipEditor and interactive launch
// paths -- once as an "auto-improve" flavor and once as a "full review"
// flavor -- which is exactly how a stale claim about agentctl's own
// architecture (that a background monitor detects state by watching JSONL
// session logs, and that "plugins" react to transitions) had to be fixed in
// two places and drifted back out of sync. State is hook-driven: Claude Code
// hooks write per-window state to .agentctl/windows/<windowID>.json, and the
// monitor reads that every tick; JSONL logs are parsed only for metrics.
// There is no plugin registry -- transition handlers are called directly
// from detectAndFireTransitions() in monitor.go. See CLAUDE.md's "Key data
// flow" section.
//
// The only genuinely conditional content is the focus block, which depends
// on whether a theme was supplied -- see buildFocusBlock.
func buildImprovePrompt(agentPath, agentName, configContext, theme, sessionContext string) string {
	return fmt.Sprintf(
		"You are running a self-improvement review of your own agent definition. Be better than you were yesterday.\n\n"+
			"YOUR AGENT FILE: %s\n"+
			"Read it first — this is the file you will be updating.\n\n"+
			"%s\n"+
			"HOW THIS WORKS: your .md file IS your identity — agentctl launches you via `claude --agent %s`. "+
			"agentctl is a tmux-native command center: each agent runs in a tmux window; Claude Code hooks write "+
			"per-window state (working/idle/blocked) to disk, and a background monitor reads it every tick and "+
			"reacts to transitions directly (no plugin system). Improvement sessions like this one are triggered "+
			"manually or auto-triggered when a session ends, via the `auto_improve` config flag.\n\n"+
			"%s"+
			"RECENT SESSIONS:\n%s\n"+
			"INSTRUCTIONS:\n"+
			"1. Read your agent file\n"+
			"2. Make targeted improvements related to the focus area above\n"+
			"3. Every edit should make you measurably better at your job — clearer instructions, fewer mistakes, better defaults\n"+
			"4. Don't rewrite the whole file. Surgical, high-value changes only.\n\n"+
			"You exist to get better. Ship the improvement.",
		agentPath, configContext, agentName, buildFocusBlock(theme), sessionContext,
	)
}

// buildFocusBlock returns the improvement-focus fragment: a themed directive
// when the caller specified one, otherwise generic guidance.
func buildFocusBlock(theme string) string {
	if theme != "" {
		return fmt.Sprintf(
			"IMPROVEMENT FOCUS: %s\n"+
				"Focus specifically on this area. Review recent sessions for examples "+
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

// LaunchSession launches an improvement session for the named agent. theme
// sets an optional improvement focus area; skipEditor bypasses the $EDITOR
// review step for fully-automated (auto-improve) invocations.
func LaunchSession(agentName string, theme string, skipEditor bool) error {
	logging.Log("launchImprovement: start agent=%q theme=%q skipEditor=%v", agentName, theme, skipEditor)

	done := logging.Timing("launchImprovement: agent.DiscoverAll")
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
		logging.Log("launchImprovement: agent %q not found in discover results", agentName)
		return fmt.Errorf("unknown agent: %s", agentName)
	}

	workDir := target.Dir
	if workDir == "" {
		workDir = filepath.Dir(target.FilePath)
	}
	logging.Log("launchImprovement: workDir=%q filePath=%q", workDir, target.FilePath)

	// Build the configuration context block.
	ac := cfg.ForAgent(agentName)
	configContext := buildConfigContext(agentName, ac, workDir)

	// Build context from recent sessions
	done = logging.Timing("launchImprovement: FindSessionsForAgent")
	recentSessions, _ := sessions.FindSessionsForAgent(agentName, 3)
	done()
	logging.Log("launchImprovement: found %d recent sessions", len(recentSessions))
	logging.Debug("improve: building context from %d sessions", len(recentSessions))

	var sessionContext strings.Builder
	for _, s := range recentSessions {
		sum, err := sessions.ParseSummary(s)
		if err != nil {
			continue
		}

		sessionContext.WriteString(fmt.Sprintf("\nSession %s (%s):\n", s.ID[:8], s.Duration()))

		if len(sum.UserMsgs) > 0 {
			sessionContext.WriteString("  User asked: " + sum.UserMsgs[0] + "\n")
		}
		if len(sum.FilesEdited) > 0 {
			sessionContext.WriteString("  Files edited: " + strings.Join(sum.FilesEdited, ", ") + "\n")
		}
		if len(sum.ToolCalls) > 0 {
			tools := make(map[string]int)
			for _, tc := range sum.ToolCalls {
				tools[tc.Name]++
			}
			var toolSummary []string
			for name, count := range tools {
				toolSummary = append(toolSummary, fmt.Sprintf("%s(%d)", name, count))
			}
			sessionContext.WriteString("  Tools: " + strings.Join(toolSummary, ", ") + "\n")
		}
	}

	// skipEditor only changes how the prompt is delivered below (direct
	// launch vs. an $EDITOR review step) -- the prompt content itself is
	// identical either way, so it lives in exactly one place. See
	// buildImprovePrompt's doc comment for why this used to be two
	// near-duplicate prompts.
	prompt := buildImprovePrompt(target.FilePath, agentName, configContext, theme, sessionContext.String())

	// Write prompt to a temp file to avoid shell escaping nightmares.
	promptFile, err := writePromptFile(agentName, prompt)
	if err != nil {
		return fmt.Errorf("writing prompt file: %w", err)
	}
	logging.Log("launchImprovement: prompt written to %s (%d bytes)", promptFile, len(prompt))
	logging.Debug("improve: wrote prompt %s (%d bytes)", promptFile, len(prompt))

	sessionID := ids.NewUUID()

	// Same backend as the agent itself: a local-model agent's improvement
	// session must not quietly run on Anthropic (or vice versa).
	extraEnv := dispatch.AgentEnvOverrides(ac)
	if _, err := dispatch.PrepareBackend(cfg, &ac, "", "", extraEnv); err != nil {
		return fmt.Errorf("resolving backend: %w", err)
	}

	// Build base flags with per-session hook configuration.
	settingsPath := dispatch.WriteAgentSettings(agentName, ac, workDir)
	baseFlags := dispatch.BuildBaseFlags(dispatch.ClaudeInvocation{Agent: agentName, SessionID: sessionID, SettingsPath: settingsPath}, ac)
	shellCmd := buildImproveShellCmd(baseFlags, promptFile, skipEditor)

	// Improvement sessions run in the agent's work dir; compute the log path so
	// the monitor can track this window without scanning.
	logPath := claude.LogPath(workDir, sessionID)

	// Ensure shell-prefix.sh is on disk so audit logging works from the first command.
	if err := hooks.EnsureShellPrefixScript(); err != nil {
		logging.Log("launchImprovement: EnsureShellPrefixScript: %v", err)
		// Non-fatal — continue launch without shell-prefix if the write failed.
	}

	extraEnv["AGENTCTL_MODE"] = "improve"

	windowName := "improve~" + agentName
	logging.Log("launchImprovement: launching window %q cmd=%q session=%s", windowName, shellCmd, sessionID)
	var launchErr error
	_, launchErr = tmux.LaunchAgent(windowName, workDir, shellCmd, logPath, agentName, sessionID, extraEnv, true)
	if launchErr != nil {
		logging.Log("launchImprovement: LaunchAgent error: %v", launchErr)
	} else {
		logging.Log("launchImprovement: success")
	}
	return launchErr
}

// buildImproveShellCmd returns the shell command line for an improvement
// session. baseFlags must already be shell-quoted (dispatch.BuildBaseFlags);
// promptFile is quoted here. With skipEditor the prompt is sent straight to
// claude; otherwise $EDITOR opens it first so the user can review or tweak
// it, and an emptied file aborts the launch (like `git commit`).
func buildImproveShellCmd(baseFlags, promptFile string, skipEditor bool) string {
	pf := shellquote.Quote(promptFile)
	if skipEditor {
		return fmt.Sprintf(`%s "$(cat %s)"; rm -f %s`, baseFlags, pf, pf)
	}
	return fmt.Sprintf(
		`${EDITOR:-vi} %s && `+
			`if [ ! -s %s ]; then echo "Empty prompt — aborting."; rm -f %s; exit 0; fi && `+
			`%s "$(cat %s)"; rm -f %s`,
		pf,
		pf, pf,
		baseFlags, pf, pf,
	)
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
