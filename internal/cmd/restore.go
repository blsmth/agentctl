package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
)

// restoreStale reports whether a state snapshot saved at ts is older than
// maxAge. maxAge <= 0 disables the gate.
func restoreStale(ts, now time.Time, maxAge time.Duration) bool {
	return maxAge > 0 && now.Sub(ts) > maxAge
}

func agentNames(agents []state.AgentState) []string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name
	}
	return names
}

// restoreFunc relaunches one agent; dispatch.Restore in production.
type restoreFunc func(dispatch.RestoreRequest) (string, error)

// loadRestoreInput returns the saved agents to restore. It prefers
// restore.json (built from per-window hook state at the points that destroy
// windows) and falls back to the monitor-written state.json when restore.json
// is absent.
func loadRestoreInput() (*state.State, error) {
	saved, err := state.LoadRestore()
	if err != nil {
		logging.Log("restoreAgents: failed to load restore.json: %v", err)
	}
	if saved != nil {
		return saved, nil
	}
	// COMPAT: one-release fallback to the monitor snapshot. Goes away when the
	// monitor is deleted.
	return state.Load()
}

// loadFreshRestoreInput returns the agents to restore, or nil when there is
// nothing to do. A stale or empty input is cleared (restore.json and
// state.json) before returning: restore.json is consumed or discarded on
// every path, because a leftover file would make StartSession's keep-existing
// rule block every later crash-recovery save.
func loadFreshRestoreInput(now time.Time, maxAge time.Duration) *state.State {
	saved, err := loadRestoreInput()
	if err != nil {
		logging.Log("restoreAgents: failed to load state: %v", err)
		return nil
	}
	if saved == nil || len(saved.Agents) == 0 {
		logging.Log("restoreAgents: no saved state")
		clearRestoreInputs()
		return nil
	}
	if restoreStale(saved.Timestamp, now, maxAge) {
		logging.Log("restoreAgents: state is stale (age=%s, restore_max_age=%s), skipping agents %v; clearing",
			now.Sub(saved.Timestamp).Round(time.Second), maxAge, agentNames(saved.Agents))
		clearRestoreInputs()
		return nil
	}
	return saved
}

func clearRestoreInputs() {
	if err := state.Clear(); err != nil {
		logging.Log("restoreAgents: failed to clear state: %v", err)
	}
	if err := state.ClearRestore(); err != nil {
		logging.Log("restoreAgents: failed to clear restore file: %v", err)
	}
}

// RunRestore reads the persisted agent state and relaunches any agents
// that were running when agentctl last exited.
func RunRestore() {
	saved := loadFreshRestoreInput(time.Now(), config.Load().EffectiveRestoreMaxAge())
	if saved == nil {
		return
	}

	app := NewApp()
	agents, err := app.DiscoverAll()
	if err != nil {
		logging.Log("restoreAgents: agent.DiscoverAll failed: %v", err)
		return
	}

	byName := make(map[string]agent.Agent, len(agents))
	for _, a := range agents {
		byName[a.Name] = a
	}

	home, _ := os.UserHomeDir()
	restoreAgents(saved.Agents, byName, home, filepath.Join(home, ".claude", "projects"),
		config.Load().EffectiveNewWindowPosition(), dispatch.Restore)

	clearRestoreInputs()
}

// restoreAgents relaunches each saved agent through restore.
func restoreAgents(saved []state.AgentState, byName map[string]agent.Agent, home, projectsRoot, position string, restore restoreFunc) {
	// Dedup session IDs: only the first window with a given session ID gets
	// --resume. Subsequent duplicates start a fresh session to avoid two windows
	// resuming the same conversation.
	usedSessions := make(map[string]bool)

	for _, as := range restoreOrder(saved, position) {
		// Resolve base agent name from instance name or explicit AgentName field
		baseName := as.AgentName
		if baseName == "" {
			baseName = tmux.BaseAgentName(as.Name)
		}

		// Skip agents that don't exist in discovery (deleted .md files, etc.)
		a, ok := byName[baseName]
		if !ok {
			logging.Log("restoreAgents: skipping unknown agent %s (base=%s)", as.Name, baseName)
			continue
		}

		// Use the saved working directory, or fall back to the agent's
		// configured dir. We NEVER fall back to $HOME because `claude
		// --continue` there will trigger a directory trust prompt.
		workDir := as.WorkDir
		if workDir == "" {
			workDir = a.Dir
		}
		if workDir == "" || workDir == home {
			logging.Log("restoreAgents: skipping %s — no valid workDir (saved=%q, configured=%q)", as.Name, as.WorkDir, a.Dir)
			continue
		}

		// Use --resume <id> to restore the exact session this agent was running.
		// If --resume fails (stale/invalid session), fall back to a fresh session
		// rather than --continue, which is unsafe when multiple windows share the
		// same base agent. Duplicate session IDs: only the first window gets
		// --resume; extras start fresh.
		sessionID, logPath := validateRestoreSession(projectsRoot, workDir, as.SessionID, as.LogPath)
		if as.SessionID != "" && sessionID == "" {
			where := sessions.FindLogBySessionIDIn(projectsRoot, as.SessionID)
			logging.Log("restoreAgents: %s: session %s has no log under project %s (found at %q); starting fresh instead of resuming",
				as.Name, as.SessionID, claude.ProjectSlug(workDir), where)
		}

		isDuplicate := sessionID != "" && usedSessions[sessionID]
		if sessionID != "" && !isDuplicate {
			usedSessions[sessionID] = true
		}

		// Restore using the saved instance name (e.g. "go-engineer:2").
		if _, err := restore(dispatch.RestoreRequest{
			AgentName:        baseName,
			InstanceName:     as.Name,
			SessionID:        sessionID,
			DuplicateSession: isDuplicate,
			WorkDir:          workDir,
			LogPath:          logPath,
			Backend:          as.Backend,
		}); err != nil {
			logging.Log("restoreAgents: not restoring %s: %v", as.Name, err)
		}
	}
}

// restoreOrder returns agents in the order they should be relaunched so the
// resulting tmux window order matches the saved one. Each launch inserts at
// the leftmost position when new_window_position is "left", which would
// reverse the list, so launch last-to-first in that case. The input is not
// modified.
func restoreOrder(agents []state.AgentState, position string) []state.AgentState {
	out := slices.Clone(agents)
	if position == config.NewWindowLeft {
		slices.Reverse(out)
	}
	return out
}

// validateRestoreSession returns the sessionID and logPath that are safe to
// restore for workDir. A session is accepted only when its log exists at
// <projectsRoot>/<ProjectSlug(workDir)>/<sessionID>.jsonl; otherwise both
// results are empty (fresh session). A saved logPath that differs from that
// canonical path is replaced by it.
func validateRestoreSession(projectsRoot, workDir, sessionID, savedLogPath string) (string, string) {
	if sessionID == "" {
		return "", ""
	}
	want := filepath.Join(projectsRoot, claude.ProjectSlug(workDir), sessionID+".jsonl")
	if st, err := os.Stat(want); err != nil || st.IsDir() {
		return "", ""
	}
	return sessionID, want
}
