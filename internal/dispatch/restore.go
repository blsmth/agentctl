package dispatch

import (
	"fmt"

	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/tmux"
)

// RestoreRequest describes a previously-saved agent window to re-launch.
// It is typically built from a state.AgentState record that was persisted when
// agentctl last exited.
type RestoreRequest struct {
	// AgentName is the base agent name (e.g. "go-engineer"), without ~N suffix.
	AgentName string

	// InstanceName is the full window name to restore (e.g. "go-engineer~2").
	// If empty, a new instance name is allocated.
	InstanceName string

	// SessionID is the Claude session to resume via --resume. If empty (or
	// DuplicateSession is true) a fresh session is started instead.
	SessionID string

	// DuplicateSession must be true when this SessionID has already been used
	// to restore another window in the same batch. Duplicate resumes cause both
	// windows to race over the same conversation, so the duplicate gets a fresh
	// session instead.
	DuplicateSession bool

	// WorkDir is the working directory for the agent process. Required.
	WorkDir string

	// LogPath is the path of the existing JSONL log file so the monitor can
	// track this window immediately (without a filesystem scan). May be empty
	// if the log path is not known.
	LogPath string

	// Backend is the config backend name the window was launched against
	// ("" = Anthropic), as persisted in windowstate/state.json. It is
	// re-resolved from config; if it no longer exists Restore returns an
	// error rather than relaunching on Anthropic.
	Backend string
}

// Restore re-launches a saved agent window using the shared settings helpers
// from the dispatch package. It uses --resume <sessionID> when possible so the
// agent can continue the conversation that was interrupted by the agentctl
// restart, falling back to a fresh session if the session ID is unknown or
// duplicated.
//
// Unlike Launch, Restore does not inject a system prompt or user message — the
// existing Claude session already carries the conversation history.
func Restore(req RestoreRequest) (string, error) {
	if req.WorkDir == "" {
		return "", fmt.Errorf("dispatch.Restore: WorkDir is required")
	}
	if req.AgentName == "" {
		return "", fmt.Errorf("dispatch.Restore: AgentName is required")
	}

	cfg := config.Load()
	ac, extraEnv, err := prepareRestore(cfg, req)
	if err != nil {
		return "", fmt.Errorf("dispatch.Restore: %s: %w", req.InstanceName, err)
	}

	settingsPath := WriteAgentSettings(req.AgentName, ac, req.WorkDir)
	resume := req.SessionID != "" && !req.DuplicateSession
	if resume {
		logging.Log("dispatch.Restore: resuming %s (session=%s)", req.InstanceName, req.SessionID)
	} else if req.SessionID != "" {
		logging.Log("dispatch.Restore: duplicate sessionID %s for %s, starting fresh", req.SessionID, req.InstanceName)
	}
	shellCmd := buildRestoreCmd(req.AgentName, req.SessionID, settingsPath, resume, ac)

	instanceName := req.InstanceName
	if instanceName == "" {
		instanceName = tmux.NextInstanceName(req.AgentName)
	}

	logPath := req.LogPath
	if logPath == "" {
		// Generate a new log path if the saved one is unavailable.
		logPath = claude.LogPath(req.WorkDir, "restore")
	}

	logging.Log("dispatch.Restore: launching %s (base=%s) workDir=%s logPath=%s", instanceName, req.AgentName, req.WorkDir, logPath)
	wid, err := tmux.LaunchAgent(instanceName, req.WorkDir, shellCmd, logPath, req.AgentName, "", extraEnv, false)
	if err != nil {
		return "", fmt.Errorf("dispatch.Restore: launching %s: %w", instanceName, err)
	}

	return wid, nil
}

// buildRestoreCmd returns the shell command that re-launches a restored
// agent. With resume it tries --resume <sessionID> first and falls back to a
// fresh session if that fails (stale or invalid session ID), so the window
// still opens. --continue is intentionally avoided: when multiple windows
// share the same base agent name it grabs the most-recent session and causes
// races.
func buildRestoreCmd(agentName, sessionID, settingsPath string, resume bool, ac config.AgentConfig) string {
	fresh := BuildBaseFlags(ClaudeInvocation{Agent: agentName, SettingsPath: settingsPath}, ac)
	if !resume {
		return fresh
	}
	resumed := BuildBaseFlags(ClaudeInvocation{Agent: agentName, Resume: sessionID, SettingsPath: settingsPath}, ac)
	return resumed + " || " + fresh
}

// prepareRestore returns the agent config and extra launch env for a restored
// window. The backend comes from the persisted request, not the current agent
// config: a window launched with --backend none must come back on Anthropic,
// and one launched on a backend must never come back on Anthropic.
func prepareRestore(cfg *config.Config, req RestoreRequest) (config.AgentConfig, map[string]string, error) {
	ac := cfg.ForAgent(req.AgentName)
	ac.Backend = req.Backend
	env := map[string]string{}
	if _, err := PrepareBackend(cfg, &ac, "", "", env); err != nil {
		return ac, nil, err
	}
	if len(env) == 0 {
		env = nil
	}
	return ac, env, nil
}
