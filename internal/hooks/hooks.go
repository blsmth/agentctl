// Package hooks implements Claude Code hook subcommands.
//
// To install hooks, run: agentctl init (or agentctl hooks-install)
// This writes the hooks config to ~/.claude/settings.json automatically.
//
// See HooksConfig() for the canonical list of all registered hooks.
package hooks

//go:generate go test . -run TestPluginHooksJSONMatches -update

import (
	"os"
	"os/exec"

	"github.com/blsmth/agentctl/internal/selfpath"
)

// Dispatch routes a CLI subcommand to the appropriate hook handler.
// Returns true if the subcommand was handled, false otherwise.
func Dispatch(subcmd string) bool {
	switch subcmd {
	case "hook-session-start":
		SessionStart()
	case "hook-notify":
		Notify()
	case "hook-pre-tool":
		PreTool()
	case "hook-post-tool":
		PostTool()
	case "hook-stop-failure":
		StopFailure()
	case "hook-user-prompt":
		UserPrompt()
	case "hook-subagent-start":
		SubagentStart()
	case "hook-subagent-stop":
		SubagentStop()
	case "hook-post-compact":
		PostCompact()
	case "hook-post-tool-failure":
		PostToolFailure()
	case "hook-validate-bash":
		ValidateBash()
	case "hook-check-done":
		CheckDone()
	case "hook-session-end":
		SessionEnd()
	case "hook-pre-compact":
		Noop("pre-compact")
	case "hook-instructions-loaded":
		Noop("instructions-loaded")
	case "hook-config-change":
		Noop("config-change")
	case "hook-cwd-changed":
		Noop("cwd-changed")
	case "hook-file-changed":
		Noop("file-changed")
	case "hook-permission-request":
		PermissionRequest()
	case "hook-worktree-create":
		WorktreeCreate()
	case "hook-worktree-remove":
		WorktreeRemove()
	case "hook-elicitation":
		Noop("elicitation")
	case "hook-elicitation-result":
		Noop("elicitation-result")
	case "hook-task-created":
		Noop("task-created")
	case "hook-task-completed":
		Noop("task-completed")
	case "hook-teammate-idle":
		Noop("teammate-idle")
	case "hook-setup":
		Noop("setup")
	case "hooks-install":
		// Handled by caller — not a hook handler itself.
		return false
	default:
		return false
	}
	return true
}

// HooksConfig returns the full Claude Code hooks configuration for agentctl,
// with every command prefixed per PersistedPrefix (bare "agentctl" when it is
// on PATH, else the quoted path of the running binary). This is the canonical
// list of all hook events agentctl handles.
func HooksConfig() map[string]interface{} {
	return BuildHooksConfig(commandPrefix())
}

// PersistedPrefix returns the command prefix that is safe to write into
// long-lived settings files, or an error when the running binary is
// ephemeral (temp dir / go-build cache). See selfpath.Persisted.
func PersistedPrefix() (string, error) {
	return selfpath.Persisted(os.Executable, exec.LookPath, os.TempDir())
}

// commandPrefix is PersistedPrefix with a non-failing fallback to the quoted
// running binary, for callers that only compare or render (not persist).
func commandPrefix() string {
	if p, err := PersistedPrefix(); err == nil {
		return p
	}
	return selfpath.Quote(selfpath.Path())
}

// BuildHooksConfig returns the full Claude Code hooks configuration for
// agentctl, prefixing every hook command with exePrefix (e.g.
// "~/.local/bin/agentctl" for the global settings.json, or a resolved
// absolute binary path for per-agent/worktree settings files). This is the
// single canonical definition of agentctl's hook map — every place that
// writes a Claude Code settings.json (agentctl init, dispatch's per-agent and
// worktree settings) renders from this one function so the event set can
// never drift between them.
func BuildHooksConfig(exePrefix string) map[string]interface{} {
	// hook builds a single hook entry with type "command".
	hook := func(cmd string, async bool) map[string]interface{} {
		h := map[string]interface{}{
			"type":    "command",
			"command": exePrefix + " " + cmd,
		}
		if async {
			h["async"] = true
		}
		return h
	}

	// event builds an event entry without a matcher.
	event := func(cmd string, async bool) []interface{} {
		return []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{hook(cmd, async)},
			},
		}
	}

	// eventWithMatcher builds an event entry with a matcher string.
	eventWithMatcher := func(matcher, cmd string, async bool) map[string]interface{} {
		return map[string]interface{}{
			"matcher": matcher,
			"hooks":   []interface{}{hook(cmd, async)},
		}
	}

	return map[string]interface{}{
		"SessionStart": event("hook-session-start", false),
		"SessionEnd":   event("hook-session-end", true),
		"Notification": []interface{}{
			eventWithMatcher("idle_prompt|permission_prompt", "hook-notify", true),
		},
		"PreToolUse": []interface{}{
			eventWithMatcher("*", "hook-pre-tool", false),
			eventWithMatcher("Bash", "hook-validate-bash", false),
		},
		"PostToolUse": []interface{}{
			eventWithMatcher("*", "hook-post-tool", false),
		},
		"PostToolUseFailure": []interface{}{
			eventWithMatcher("*", "hook-post-tool-failure", false),
		},
		"Stop":               event("hook-check-done", false),
		"StopFailure":        event("hook-stop-failure", false),
		"UserPromptSubmit":   event("hook-user-prompt", false),
		"SubagentStart":      event("hook-subagent-start", true),
		"SubagentStop":       event("hook-subagent-stop", false),
		"PostCompact":        event("hook-post-compact", false),
		"PreCompact":         event("hook-pre-compact", false),
		"InstructionsLoaded": event("hook-instructions-loaded", true),
		"ConfigChange":       event("hook-config-change", false),
		"CwdChanged":         event("hook-cwd-changed", true),
		"FileChanged":        event("hook-file-changed", true),
		"PermissionRequest": []interface{}{
			eventWithMatcher("Write", "hook-permission-request", false),
			eventWithMatcher("Bash", "hook-permission-request", false),
		},
		"WorktreeCreate":    event("hook-worktree-create", false),
		"WorktreeRemove":    event("hook-worktree-remove", true),
		"Elicitation":       event("hook-elicitation", false),
		"ElicitationResult": event("hook-elicitation-result", false),
		"TaskCreated":       event("hook-task-created", false),
		"TaskCompleted":     event("hook-task-completed", false),
		"TeammateIdle":      event("hook-teammate-idle", false),
		"Setup":             event("hook-setup", false),
	}
}
