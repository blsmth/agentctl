package dispatch

import (
	"fmt"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/launchwatch"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
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

	// Account is the configured Claude account the window ran under ("" =
	// none recorded). It is re-resolved from config; if it no longer exists
	// Restore returns an error rather than relaunching under another login.
	// With none recorded, the account whose projects dir holds LogPath is
	// used so --resume finds the session.
	Account string

	// Launch metadata re-stamped on the relaunched window's windowstate record
	// so it keeps its dispatched/bare classification. Empty values are left
	// empty (an empty Mode counts as bare). SpawnedBy is deliberately absent:
	// window IDs change on restore, so the caller remaps it after launch.
	Mode        string
	Ticket      string
	TicketTitle string
	Task        string
	FirstPrompt string
	Prompt      string
	Branch      string
	WorktreeDir string
	PRNumber    int
	PRURL       string
	// SessionName is the original claude -n name, reused so the restored
	// session keeps its identity. Empty omits -n.
	SessionName string
	// Harness is the recorded harness ("" = claude). An unknown name fails
	// the restore rather than relaunching the window on another harness.
	Harness string
}

// Restore re-launches a saved agent window using the shared settings helpers
// from the dispatch package. It resumes the saved session when possible so the
// agent can continue the conversation that was interrupted by the agentctl
// restart, falling back to a fresh session if the session ID is unknown or
// duplicated. The window comes back on the harness it was launched with.
//
// Unlike Launch, Restore does not inject a system prompt or user message — the
// existing session already carries the conversation history.
func Restore(req RestoreRequest) (string, error) {
	if req.WorkDir == "" {
		return "", fmt.Errorf("dispatch.Restore: WorkDir is required")
	}
	if req.AgentName == "" {
		return "", fmt.Errorf("dispatch.Restore: AgentName is required")
	}
	h, err := harness.Lookup(req.Harness)
	if err != nil {
		return "", fmt.Errorf("dispatch.Restore: %s: %w", req.InstanceName, err)
	}

	cfg := config.Load()
	ac, extraEnv, hb, err := prepareRestore(cfg, h, req)
	if err != nil {
		return "", fmt.Errorf("dispatch.Restore: %s: %w", req.InstanceName, err)
	}

	prep, err := h.PrepareWorkspace(harness.Workspace{
		Dir:           launchDirFn(),
		WriteSettings: func() (string, error) { return WriteAgentSettings(req.AgentName, ac, req.WorkDir), nil },
		Backend:       hb,
	})
	if err != nil {
		return "", fmt.Errorf("dispatch.Restore: %s: %w", req.InstanceName, err)
	}
	resume := req.SessionID != "" && !req.DuplicateSession
	if resume {
		logging.Log("dispatch.Restore: resuming %s (session=%s)", req.InstanceName, req.SessionID)
	} else if req.SessionID != "" {
		logging.Log("dispatch.Restore: duplicate sessionID %s for %s, starting fresh", req.SessionID, req.InstanceName)
	}
	shellCmd, err := buildRestoreCmd(h, baseInvocation(req.AgentName, req.SessionName, ac, prep), req.SessionID, resume)
	if err != nil {
		return "", fmt.Errorf("dispatch.Restore: %s: %w", req.InstanceName, err)
	}

	instanceName := req.InstanceName
	if instanceName == "" {
		instanceName = tmux.NextInstanceName(req.AgentName)
	}

	logPath := req.LogPath
	if logPath == "" {
		// Generate a new log path if the saved one is unavailable.
		logPath = h.LaunchLogPath(accountDirFromEnv(extraEnv), req.WorkDir, "restore")
	}

	logging.Log("dispatch.Restore: launching %s (base=%s, harness=%s) workDir=%s logPath=%s", instanceName, req.AgentName, h.Name(), req.WorkDir, logPath)
	if extraEnv == nil {
		extraEnv = map[string]string{}
	}
	if req.SessionName != "" {
		extraEnv[EnvSessionName] = req.SessionName
	}
	ApplyHarnessEnv(extraEnv, h, "", prep)
	wid, err := tmux.LaunchAgent(instanceName, req.WorkDir, shellCmd, logPath, req.AgentName, "", extraEnv, false)
	if err != nil {
		return "", fmt.Errorf("dispatch.Restore: launching %s: %w", instanceName, err)
	}
	launchwatch.Start(wid, h.Caps())

	if wid != "" {
		windowstate.Update(wid, func(ws *windowstate.WindowState) { //nolint:errcheck
			applyRestoreMeta(ws, req)
		})
	}

	return wid, nil
}

// applyRestoreMeta stamps the saved launch metadata onto a window record. It
// only sets fields, so a later SessionStart (which merges via Update) keeps them.
func applyRestoreMeta(ws *windowstate.WindowState, req RestoreRequest) {
	ws.Mode = req.Mode
	ws.Ticket = req.Ticket
	ws.TicketTitle = req.TicketTitle
	ws.Task = req.Task
	ws.FirstPrompt = req.FirstPrompt
	ws.Prompt = req.Prompt
	ws.Branch = req.Branch
	ws.WorktreeDir = req.WorktreeDir
	ws.PRNumber = req.PRNumber
	ws.PRURL = req.PRURL
}

// buildRestoreCmd returns the shell command that re-launches a restored
// agent (see harness.Harness.RestoreCommand: Claude tries --resume and
// falls back to a fresh session; pi reopens the session by ID).
func buildRestoreCmd(h harness.Harness, inv harness.Invocation, sessionID string, resume bool) (string, error) {
	return h.RestoreCommand(inv, sessionID, resume)
}

// prepareRestore returns the agent config and extra launch env for a restored
// window. The backend comes from the persisted request, not the current agent
// config: a window launched with --backend none must come back on Anthropic,
// and one launched on a backend must never come back on Anthropic. Harnesses
// without accounts or backends skip both.
func prepareRestore(cfg *config.Config, h harness.Harness, req RestoreRequest) (config.AgentConfig, map[string]string, *harness.Backend, error) {
	ac := cfg.ForAgent(req.AgentName)
	ac.Backend = req.Backend
	env := map[string]string{}
	caps := h.Caps()
	if caps.Accounts {
		acct, err := restoreAccount(cfg, req.Account, req.LogPath)
		if err != nil {
			return ac, nil, nil, err
		}
		ApplyAccountEnv(env, acct)
	}
	var hb *harness.Backend
	switch {
	case caps.RegistersBackends:
		var err error
		if hb, err = PrepareBackendFor(cfg, h, &ac, "", "", req.WorkDir, env); err != nil {
			return ac, nil, nil, err
		}
	case caps.Backends:
		if _, err := PrepareBackend(cfg, &ac, "", "", req.WorkDir, env); err != nil {
			return ac, nil, nil, err
		}
	default:
		ac.Backend = ""
	}
	if len(env) == 0 {
		env = nil
	}
	return ac, env, hb, nil
}
