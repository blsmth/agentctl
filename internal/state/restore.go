package state

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/safefs"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// RestoreFileName is the runtime-dir file holding the session-restore input.
// Unlike state.json it is not rewritten by the monitor: it is written only at
// the points that are about to destroy windows (session start, quit, restart,
// relaunch) from the per-window hook state.
const RestoreFileName = "restore.json"

// LiveWindow describes a window that still exists in tmux when the restore
// file is built.
type LiveWindow struct {
	Index int    // position in tmux list-windows order
	Path  string // active pane's current path
}

// utilityWindows are never restored as agents.
var utilityWindows = map[string]bool{
	"shell": true, "hub": true, "tickets": true, "sessions": true, "agents": true, "context": true,
}

// BuildRestoreAgents produces the restore entries from per-window hook state.
// live maps window IDs that still exist in tmux to their order and cwd (nil
// when tmux is gone, e.g. after a server crash). The order of the result is
// the saved tab order: the live tmux index when the window is alive,
// otherwise the WindowIndex recorded by SessionStart (possibly slightly stale
// after left-insertion; ties and unknowns fall back to window ID order).
func BuildRestoreAgents(records map[string]windowstate.WindowState, live map[string]LiveWindow) []AgentState {
	var out []AgentState
	for id, ws := range records {
		name := ws.Instance
		if name == "" || utilityWindows[name] || strings.HasPrefix(name, "improve~") ||
			utilityWindows[ws.AgentName] || strings.HasPrefix(ws.AgentName, "improve~") {
			continue
		}
		idx := ws.WindowIndex
		workDir := ws.WorkDir
		if lw, ok := live[id]; ok {
			idx = lw.Index
			if workDir == "" {
				workDir = lw.Path
			}
		}
		sessionID := ws.SessionID
		if sessionID == "" && ws.LogPath != "" && ws.Harness == "" {
			sessionID = strings.TrimSuffix(filepath.Base(ws.LogPath), ".jsonl")
		}
		out = append(out, AgentState{
			Name:        name,
			AgentName:   ws.AgentName,
			SessionID:   sessionID,
			LogPath:     ws.LogPath,
			WorkDir:     workDir,
			WindowID:    id,
			WindowIndex: idx,
			Backend:     ws.Backend,
			Account:     ws.Account,
			Harness:     ws.Harness,

			Mode:             ws.Mode,
			TicketRef:        ws.Ticket,
			TicketTitle:      ws.TicketTitle,
			Task:             ws.Task,
			FirstPrompt:      ws.FirstPrompt,
			Branch:           ws.Branch,
			WorktreeDir:      ws.WorktreeDir,
			SpawnedBy:        ws.SpawnedBy,
			SpawnedBySession: ws.SpawnedBySession,
			SessionName:      ws.SessionName,
			PRNumber:         ws.PRNumber,
			PRURL:            ws.PRURL,
		})
	}
	slices.SortFunc(out, func(a, b AgentState) int {
		if c := cmp.Compare(a.WindowIndex, b.WindowIndex); c != 0 {
			return c
		}
		return cmp.Compare(windowNum(a.WindowID), windowNum(b.WindowID))
	})
	return out
}

func windowNum(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "@"))
	if err != nil {
		return 0
	}
	return n
}

// restorePath returns the default restore file path (next to state.json).
func restorePath() string {
	mu.Lock()
	p := stateFilePath
	mu.Unlock()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), RestoreFileName)
}

// SaveRestore writes the restore file next to state.json.
func SaveRestore(s *State) error {
	p := restorePath()
	if p == "" {
		return fmt.Errorf("state: no state file path")
	}
	return SaveRestoreAt(filepath.Dir(p), s)
}

// SaveRestoreAt atomically writes <runtimeDir>/restore.json with mode 0600,
// refusing symlinks (see safefs).
func SaveRestoreAt(runtimeDir string, s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling restore state: %w", err)
	}
	if err := safefs.WriteFile(runtimeDir, filepath.Join(runtimeDir, RestoreFileName), data, 0o600); err != nil {
		return fmt.Errorf("writing restore file: %w", err)
	}
	return nil
}

// SaveRestoreAgents stamps agents with the current time and writes them.
func SaveRestoreAgents(runtimeDir string, agents []AgentState) error {
	return SaveRestoreAt(runtimeDir, &State{Agents: agents, Timestamp: time.Now()})
}

// LoadRestore reads restore.json. Returns nil, nil if it does not exist.
func LoadRestore() (*State, error) {
	p := restorePath()
	if p == "" {
		return nil, nil
	}
	s, err := LoadFrom(p)
	if err != nil {
		return nil, fmt.Errorf("loading restore file: %w", err)
	}
	return s, nil
}

// ClearRestore removes restore.json. Returns nil if it does not exist.
func ClearRestore() error {
	p := restorePath()
	if p == "" {
		return nil
	}
	dir := filepath.Dir(p)
	if err := safefs.CheckDir(dir, dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
