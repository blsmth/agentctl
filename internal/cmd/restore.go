package cmd

import (
	"os"
	"path/filepath"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
)

const maxStateAge = 5 * time.Minute

// RunRestore reads the persisted agent state and relaunches any agents
// that were running when agentctl last exited.
func RunRestore() {
	saved, err := state.Load()
	if err != nil {
		logging.Log("restoreAgents: failed to load state: %v", err)
		return
	}
	if saved == nil || len(saved.Agents) == 0 {
		logging.Log("restoreAgents: no saved state")
		return
	}
	if time.Since(saved.Timestamp) > maxStateAge {
		logging.Log("restoreAgents: state is stale (age=%s), skipping", time.Since(saved.Timestamp).Round(time.Second))
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
	projectsRoot := filepath.Join(home, ".claude", "projects")

	// Dedup session IDs: only the first window with a given session ID gets
	// --resume. Subsequent duplicates start a fresh session to avoid two windows
	// resuming the same conversation.
	usedSessions := make(map[string]bool)

	for _, as := range saved.Agents {
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

		// Use the saved working directory (captured from the pane at save time),
		// or fall back to the agent's configured dir. We NEVER fall back to $HOME
		// because `claude --continue` there will trigger a directory trust prompt.
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
		// rather than --continue. Using --continue here is unsafe: when multiple
		// windows share the same base agent, they race and both grab the same
		// most-recent session.
		// Guard against duplicate session IDs in state.json: only the first window
		// with a given session ID gets --resume; extras start fresh.

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

		// Restore using the saved instance name (e.g. "go-engineer~2").
		// Pass the persisted log path so the monitor can track this window
		// immediately without filesystem scanning.
		if _, err := dispatch.Restore(dispatch.RestoreRequest{
			AgentName:        baseName,
			InstanceName:     as.Name,
			SessionID:        sessionID,
			DuplicateSession: isDuplicate,
			WorkDir:          workDir,
			LogPath:          logPath,
		}); err != nil {
			logging.Log("restoreAgents: failed to launch %s: %v", as.Name, err)
		}
	}

	if err := state.Clear(); err != nil {
		logging.Log("restoreAgents: failed to clear state: %v", err)
	}
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
