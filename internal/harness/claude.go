package harness

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/panestate"
	"github.com/blsmth/agentctl/internal/shellquote"
)

// claudeHarness is Claude Code: hooks registered through settings files,
// transcripts under <config dir>/projects/<slug>/<session>.jsonl, and the
// account (CLAUDE_CONFIG_DIR) and backend machinery.
type claudeHarness struct{}

func (claudeHarness) Name() string   { return Claude }
func (claudeHarness) Binary() string { return "claude" }

func (claudeHarness) Caps() Caps {
	return Caps{
		PreassignSessionID: true,
		ResumeByID:         true,
		BlockingPreTool:    true,
		BlockedSignal:      true,
		ContextInjection:   true,
		NativeMessaging:    true,
		Statusline:         true,
		AgentFlag:          true,
		PermissionModes:    true,
		Accounts:           true,
		Backends:           true,
		EnvFile:            true,
		PaneClassifier:     true,
	}
}

// Command returns the claude command line: agent, session/resume flags, and
// -- when set -- model, effort, permission mode, settings file, system
// prompt file and first message, in that order.
//
// The result is embedded in a launch script, so every interpolated value is
// quoted with shellquote.Quote: values come from CLI flags, config files and
// $HOME-derived paths, none of which may be trusted to be free of spaces or
// shell metacharacters. Model and effort are flags rather than env vars so
// they show in process listings and outrank ambient settings.
func (claudeHarness) Command(inv Invocation) (string, error) {
	var b strings.Builder
	b.WriteString("claude --agent " + shellquote.Quote(inv.Agent))
	if inv.Name != "" {
		b.WriteString(" -n " + shellquote.Quote(inv.Name))
	}
	if inv.SessionID != "" {
		b.WriteString(" --session-id " + shellquote.Quote(inv.SessionID))
	}
	if inv.Resume != "" {
		b.WriteString(" --resume " + shellquote.Quote(inv.Resume))
	}
	if inv.ForkSession {
		b.WriteString(" --fork-session")
	}
	if inv.Model != "" {
		b.WriteString(" --model " + shellquote.Quote(inv.Model))
	}
	if inv.Effort != "" {
		b.WriteString(" --effort " + shellquote.Quote(inv.Effort))
	}
	if inv.PermissionMode != "" {
		b.WriteString(" --permission-mode " + shellquote.Quote(inv.PermissionMode))
	}
	if inv.SettingsPath != "" {
		b.WriteString(" --settings " + shellquote.Quote(inv.SettingsPath))
	}
	if inv.SystemPromptFile != "" {
		b.WriteString(" --append-system-prompt-file " + shellquote.Quote(inv.SystemPromptFile))
	}
	if inv.Message != "" {
		b.WriteString(" " + shellquote.Quote(inv.Message))
	}
	return b.String(), nil
}

// RestoreCommand tries --resume <sessionID> first and falls back to a fresh
// session if that fails (stale or invalid ID), so the window still opens.
// --continue is avoided: with several windows of one agent it grabs the most
// recent session and races.
func (h claudeHarness) RestoreCommand(inv Invocation, sessionID string, resume bool) (string, error) {
	inv.SessionID, inv.Resume = "", ""
	fresh, err := h.Command(inv)
	if err != nil || !resume {
		return fresh, err
	}
	inv.Resume = sessionID
	resumed, err := h.Command(inv)
	if err != nil {
		return "", err
	}
	return resumed + " || " + fresh, nil
}

// LaunchEnv returns the Claude Code env every agent window gets.
func (claudeHarness) LaunchEnv(p EnvParams) map[string]string {
	return map[string]string{
		"DISABLE_AUTOUPDATER":                      "1",
		"CLAUDE_CODE_DISABLE_TERMINAL_TITLE":       "1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB":         subprocessEnvScrub(p.PermissionMode),
		"CLAUDE_CODE_SHELL_PREFIX":                 p.ShellPrefixScript,
	}
}

// subprocessEnvScrub returns the CLAUDE_CODE_SUBPROCESS_ENV_SCRUB value for a
// launch with the given --permission-mode.
//
// Do NOT set this to 1 unconditionally. Claude Code (v2.1.311+) overrides the
// permission mode when it is set, even with --allowedTools:
//
//	⚠ Permission mode forced to default — CLAUDE_CODE_SUBPROCESS_ENV_SCRUB is
//	set (allowed_non_write_users hardening). Declare allowedTools explicitly,
//	or set CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=0 to opt out.
//
// For auto mode we inject an explicit 0 (not just omit it) so a value
// inherited from the parent environment (agentctl itself may run in a window
// with SCRUB=1) cannot re-enable it.
func subprocessEnvScrub(permissionMode string) string {
	if permissionMode == "auto" {
		return "0"
	}
	return "1"
}

// PrepareWorkspace writes the Claude Code settings (hooks, permissions,
// backend routing) through the launcher's writer.
func (claudeHarness) PrepareWorkspace(ws Workspace) (Prepared, error) {
	if ws.WriteSettings == nil {
		return Prepared{}, nil
	}
	path, err := ws.WriteSettings()
	return Prepared{SettingsPath: path}, err
}

// DecodeHook passes the payload through: Claude Code's hook shape is the
// canonical one.
func (claudeHarness) DecodeHook(_ string, raw []byte) (Event, error) {
	var ev Event
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &ev); err != nil {
			return Event{Raw: raw}, err
		}
	}
	ev.Raw = raw
	return ev, nil
}

// EncodeDecision renders Claude Code's hook output contract.
// hookEventName is required as of Claude Code 2.1.231: without it Claude
// Code shows a generic "hook error" instead of the reason.
func (claudeHarness) EncodeDecision(d Decision) []byte {
	switch d.Kind {
	case Allow, Deny:
		decision := "allow"
		if d.Kind == Deny {
			decision = "deny"
		}
		return marshalLine(map[string]any{
			"hookSpecificOutput": map[string]string{
				"hookEventName":            d.HookEvent,
				"permissionDecision":       decision,
				"permissionDecisionReason": d.Reason,
			},
		})
	case Context:
		return marshalLine(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":     d.HookEvent,
				"additionalContext": d.Text,
			},
		})
	case SessionContext:
		return marshalLine(map[string]string{"additionalContext": d.Text})
	}
	return nil
}

// SessionLogPath returns the payload's transcript_path when it is named for
// the session (it already lives under the right account's config dir), else
// the path derived from cwd under this process's config dir
// ($CLAUDE_CONFIG_DIR, inherited from claude). "" when neither is usable.
func (claudeHarness) SessionLogPath(sessionID, cwd, transcriptPath string) string {
	if transcriptPath != "" && filepath.IsAbs(transcriptPath) && filepath.Base(transcriptPath) == sessionID+".jsonl" {
		return transcriptPath
	}
	if cwd == "" {
		return ""
	}
	return claude.LogPath(cwd, sessionID)
}

// LaunchLogPath is <config dir>/projects/<slug>/<session>.jsonl.
func (claudeHarness) LaunchLogPath(configDir, workDir, sessionID string) string {
	if configDir != "" {
		return claude.LogPathIn(configDir, workDir, sessionID)
	}
	return claude.LogPath(workDir, sessionID)
}

func (claudeHarness) ParseTranscript(r io.Reader) ([]logstream.Entry, error) {
	return logstream.ReadAll(r)
}

func (claudeHarness) ClassifyPane(text string) panestate.Verdict {
	return panestate.Classify(text)
}
