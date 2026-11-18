package fleet

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// EnrichmentMaxAge is how old state.json may be before its monitor-only
// fields (cumulative/last-turn token counts, StartTime) are ignored. The
// monitor writes every 500ms, so anything older means it is off or wedged.
const EnrichmentMaxAge = 3 * time.Second

// listWindowsFn is the tmux seam for LoadAgents; tests replace it.
var listWindowsFn = tmux.ListWindowsInErr

// LoadAgents builds the per-window view for one session: `tmux list-windows`
// joined with the session's windowstate records, enriched with state.json
// when it is fresh. It never requires state.json. A failed list-windows is
// returned as an error with a nil slice.
func LoadAgents(session, runtimeDir string) ([]state.AgentState, error) {
	wins, err := listWindowsFn(session)
	if err != nil {
		return nil, fmt.Errorf("listing windows: %w", err)
	}
	records := windowstate.AllFrom(windowstate.WindowsDirFor(runtimeDir))
	var snap *state.Snapshot
	if runtimeDir != "" {
		snap, _ = state.LoadSnapshotFrom(runtimeDir + "/state.json")
	}
	return BuildAgents(wins, records, snap, time.Now()), nil
}

// loadAgentsAndShells is LoadAgents plus the session's "shell" windows,
// from a single list-windows call. Only the hub wants the shells; every other
// caller keeps using LoadAgents, which skips them.
func loadAgentsAndShells(session, runtimeDir string) (agents, shells []state.AgentState, err error) {
	wins, err := listWindowsFn(session)
	if err != nil {
		return nil, nil, fmt.Errorf("listing windows: %w", err)
	}
	records := windowstate.AllFrom(windowstate.WindowsDirFor(runtimeDir))
	var snap *state.Snapshot
	if runtimeDir != "" {
		snap, _ = state.LoadSnapshotFrom(runtimeDir + "/state.json")
	}
	return BuildAgents(wins, records, snap, time.Now()), ShellWindows(wins), nil
}

// ShellWindows returns one state per window named "shell", with the same
// WindowIndex BuildAgents would assign. They carry no state, so they are
// neutral rather than idle agents.
func ShellWindows(wins []tmux.WindowInfo) []state.AgentState {
	var out []state.AgentState
	for i, w := range wins {
		if w.ID == "" || tmux.StripAgentPrefix(w.Name) != "shell" {
			continue
		}
		out = append(out, state.AgentState{Name: "shell", WindowID: w.ID, WindowIndex: i, WorkDir: w.Path})
	}
	return out
}

// BuildAgents is the pure core of LoadAgents. wins is tmux's window list for
// the session in tmux order; records maps window ID to windowstate. tmux is
// the source of truth for which windows exist: records without a window are
// ignored. The persistent "shell" and "improve~*" windows are skipped.
// Windows without a record are listed as untracked (idle, named from tmux).
//
// snap is optional enrichment: when non-nil and younger than
// EnrichmentMaxAge, the monitor-only token and start-time fields are merged
// in by window ID. Nothing else ever depends on it.
func BuildAgents(wins []tmux.WindowInfo, records map[string]windowstate.WindowState, snap *state.Snapshot, now time.Time) []state.AgentState {
	enrich := snap != nil && snap.State != nil && now.Sub(snap.Timestamp) <= EnrichmentMaxAge
	out := make([]state.AgentState, 0, len(wins))
	for i, w := range wins {
		if w.ID == "" {
			continue
		}
		name := tmux.StripAgentPrefix(w.Name)
		if IsNonAgentWindow(name) {
			continue
		}
		a := agentFromWindow(i, w, name, records)
		if enrich {
			if m := snap.ByWindowID(w.ID); m != nil {
				a.InputTokens = m.InputTokens
				a.OutputTokens = m.OutputTokens
				a.CacheReadTokens = m.CacheReadTokens
				a.CacheCreateTokens = m.CacheCreateTokens
				a.LastTurnInput = m.LastTurnInput
				a.LastTurnOutput = m.LastTurnOutput
				a.LastTurnCacheRead = m.LastTurnCacheRead
				a.LastTurnCacheCreate = m.LastTurnCacheCreate
				a.StartTime = m.StartTime
				a.MailboxPending = m.MailboxPending
			}
		}
		out = append(out, a)
	}
	return out
}

func agentFromWindow(idx int, w tmux.WindowInfo, name string, records map[string]windowstate.WindowState) state.AgentState {
	ws, ok := records[w.ID]
	if !ok {
		return state.AgentState{
			Name:        name,
			AgentName:   tmux.BaseAgentName(name),
			WindowID:    w.ID,
			WindowIndex: idx,
			State:       "idle",
			WorkDir:     w.Path,
		}
	}
	if ws.Instance != "" {
		name = ws.Instance
	}
	base := ws.AgentName
	if base == "" {
		base = tmux.BaseAgentName(name)
	}
	done := ws.Done && !ws.Seen
	st := ws.State
	// Never report "working" for a finished agent: the Stop hook may have set
	// Done before State settles.
	if st == "" || ws.Done {
		st = "idle"
	}
	workDir := ws.WorkDir
	if workDir == "" {
		workDir = w.Path
	}
	worktree := ws.WorktreeDir
	if worktree == "" && strings.Contains(workDir, "/.worktrees/") {
		worktree = workDir
	}
	ticket, _ := strconv.Atoi(strings.TrimPrefix(ws.Ticket, "#"))
	return state.AgentState{
		Name:             name,
		AgentName:        base,
		SessionID:        ws.SessionID,
		LogPath:          ws.LogPath,
		WorkDir:          workDir,
		WindowID:         w.ID,
		WindowIndex:      idx,
		State:            st,
		ContextPct:       ws.CtxPct,
		ContextTokens:    ws.ContextTokens,
		ContextWindow:    ws.ContextWindowSize,
		WorktreePath:     worktree,
		Done:             done,
		ModelName:        ws.Model,
		CostUSD:          ws.CostUSD,
		TurnCount:        ws.TurnCount,
		Ticket:           ticket,
		MainPane:         ws.MainPane,
		LastToolDetail:   ws.LastToolDetail,
		LastText:         ws.LastText,
		Backend:          ws.Backend,
		Account:          ws.Account,
		Harness:          ws.Harness,
		LastActivity:     lastActivity(ws),
		Mode:             ws.Mode,
		TicketRef:        ws.Ticket,
		TicketTitle:      ws.TicketTitle,
		Task:             ws.Task,
		Branch:           ws.Branch,
		WorktreeDir:      ws.WorktreeDir,
		SpawnedBy:        ws.SpawnedBy,
		SpawnedBySession: ws.SpawnedBySession,
		SessionName:      ws.SessionName,
		ScheduledBy:      ws.ScheduledBy,
		ScheduledAt:      ws.ScheduledAt,
	}
}

// LocalWindowNames resolves a window of the current project to its instance
// name ("go-engineer:2") and base agent name ("go-engineer") from windowstate.
// Both are empty when the window has no record naming it.
func LocalWindowNames(windowID string) (instance, base string) {
	ws, err := windowstate.Read(windowID)
	if err != nil || ws.Instance == "" {
		return "", ""
	}
	base = ws.AgentName
	if base == "" {
		base = tmux.BaseAgentName(ws.Instance)
	}
	return ws.Instance, base
}

// LocalAgents is LoadAgents for the current process's session and runtime
// dir, for the popups and menus that act on their own project.
func LocalAgents() ([]state.AgentState, error) {
	return LoadAgents(tmux.SessionName, paths.RuntimeDir())
}

// lastActivity is the unix time of the window's latest real activity: the
// later of its last tool call (LastToolTime, unix seconds) and its last state
// change (StateSince, which covers prompt submit and Stop). UpdatedAt is
// deliberately not used: every write stamps it, including statusline context
// pushes, so idle windows would look active. 0 when neither is recorded.
func lastActivity(ws windowstate.WindowState) int64 {
	t := ws.LastToolTime
	if !ws.StateSince.IsZero() {
		if s := ws.StateSince.Unix(); s > t {
			t = s
		}
	}
	if t < 0 {
		return 0
	}
	return t
}
