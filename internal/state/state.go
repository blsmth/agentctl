package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/safefs"
)

// AgentState tracks a running agent's resumable session.
type AgentState struct {
	Name                string `json:"name"`                 // Instance name (e.g. "go-engineer~2")
	AgentName           string `json:"agent_name,omitempty"` // Base agent name (e.g. "go-engineer"); empty means same as Name
	SessionID           string `json:"session_id"`
	LogPath             string `json:"log_path,omitempty"`
	WorkDir             string `json:"work_dir,omitempty"`
	WindowID            string `json:"window_id,omitempty"`              // tmux window ID (e.g. "@3")
	WindowIndex         int    `json:"window_index,omitempty"`           // tmux display order (0-based position in list-windows output)
	State               string `json:"state,omitempty"`                  // "working", "idle", "blocked"
	ContextPct          int    `json:"context_pct,omitempty"`            // 0–100
	ContextTokens       int    `json:"context_tokens,omitempty"`         // input tokens in the live context (last turn)
	ContextWindow       int    `json:"context_window,omitempty"`         // usable window (model-aware, reserve-aware) for the active model
	WorktreePath        string `json:"worktree_path,omitempty"`          // if launched in a worktree
	Done                bool   `json:"done,omitempty"`                   // true when agent signaled done
	InputTokens         int    `json:"input_tokens,omitempty"`           // cumulative input tokens
	OutputTokens        int    `json:"output_tokens,omitempty"`          // cumulative output tokens
	CacheReadTokens     int    `json:"cache_read_tokens,omitempty"`      // cumulative cache read tokens
	CacheCreateTokens   int    `json:"cache_create_tokens,omitempty"`    // cumulative cache create tokens
	ModelName           string `json:"model_name,omitempty"`             // e.g. "claude-sonnet-4-20250514"
	TurnCount           int    `json:"turn_count,omitempty"`             // number of assistant turns
	LastTurnInput       int    `json:"last_turn_input,omitempty"`        // last turn input tokens
	LastTurnOutput      int    `json:"last_turn_output,omitempty"`       // last turn output tokens
	LastTurnCacheRead   int    `json:"last_turn_cache_read,omitempty"`   // last turn cache-read tokens
	LastTurnCacheCreate int    `json:"last_turn_cache_create,omitempty"` // last turn cache-create tokens
	Ticket              int    `json:"ticket,omitempty"`                 // GitHub issue number (0 if not a dispatch)
	StartTime           int64  `json:"start_time,omitempty"`             // Unix timestamp when window was first seen
	HeaderPane          string `json:"header_pane,omitempty"`            // tmux pane ID for header chrome (e.g. "%5")
	FooterPane          string `json:"footer_pane,omitempty"`            // tmux pane ID for footer chrome (e.g. "%6")
	MainPane            string `json:"main_pane,omitempty"`              // tmux pane ID of the agent content pane
	LastToolDetail      string `json:"last_tool_detail,omitempty"`       // e.g. "monitor.go" or "go build ./..."
	LastText            string `json:"last_text,omitempty"`              // last assistant text block (already truncated to ~100 chars upstream)
	MailboxPending      int    `json:"mailbox_pending,omitempty"`        // count-only mailbox.PendingCount, refreshed on a throttled monitor tick; see internal/cmd/wake.go for delivery
}

// RateLimitState holds account-level rate limit information for the chrome bar.
type RateLimitState struct {
	ResetsAt      int64  `json:"resets_at,omitempty"`       // Unix timestamp from rate_limit_event
	RateLimitType string `json:"rate_limit_type,omitempty"` // "five_hour"
	Status        string `json:"status,omitempty"`          // "allowed" or "blocked"
	WindowTokens  int64  `json:"window_tokens,omitempty"`   // input+output tokens consumed in current window
	TokenCap      int    `json:"token_cap,omitempty"`       // from config, for percentage calc
	UsagePct      int    `json:"usage_pct,omitempty"`       // 0-100, computed

	// Source records which path produced UsagePct: "statusline" when the
	// pushed rate_limits.five_hour.used_percentage was used directly, or
	// "derived" when computed from WindowTokens/TokenCap. Empty when neither
	// path has produced anything yet (e.g. the monitor hasn't ticked, or
	// no pushed statusLine data has arrived). When Source
	// is "statusline", WindowTokens/TokenCap are meaningless and left zero
	// rather than reporting a cap that did not produce UsagePct.
	Source string `json:"source,omitempty"`

	// SevenDayPct/SevenDayResetsAt surface rate_limits.seven_day from a
	// pushed statusLine payload -- these stay nil/zero whenever Source != "statusline".
	SevenDayPct      *float64 `json:"seven_day_pct,omitempty"`
	SevenDayResetsAt int64    `json:"seven_day_resets_at,omitempty"`
}

// State is the snapshot of running agents, written by the monitor.
type State struct {
	Agents         []AgentState   `json:"agents"`
	Timestamp      time.Time      `json:"timestamp"`
	ActiveWindowID string         `json:"active_window_id,omitempty"` // tmux window ID of the currently active window
	PrefixActive   bool           `json:"prefix_active,omitempty"`    // true when the tmux prefix key (ctrl+b) is active
	RateLimit      RateLimitState `json:"rate_limit,omitempty"`       // account-level rate limit state
}

// Snapshot wraps State with pre-built indexes for fast lookups.
type Snapshot struct {
	*State
	byWindowID map[string]*AgentState
	byName     map[string]*AgentState
}

// LoadSnapshot reads state.json (from the path set by Init) and builds lookup
// indexes. Returns nil, nil if the file doesn't exist.
func LoadSnapshot() (*Snapshot, error) {
	return LoadSnapshotFrom(stateFilePath)
}

// LoadSnapshotFrom reads a state.json from an arbitrary path and builds lookup
// indexes. Returns nil, nil if the file doesn't exist. Used by the hub to read
// N snapshots from N project directories.
func LoadSnapshotFrom(path string) (*Snapshot, error) {
	s, err := LoadFrom(path)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	snap := &Snapshot{
		State:      s,
		byWindowID: make(map[string]*AgentState, len(s.Agents)),
		byName:     make(map[string]*AgentState, len(s.Agents)),
	}
	for i := range s.Agents {
		a := &s.Agents[i]
		if a.WindowID != "" {
			snap.byWindowID[a.WindowID] = a
		}
		snap.byName[a.Name] = a
	}
	return snap, nil
}

// ByWindowID returns the AgentState for a given tmux window ID (e.g. "@3").
// Returns nil if not found.
func (s *Snapshot) ByWindowID(id string) *AgentState {
	if s == nil {
		return nil
	}
	return s.byWindowID[id]
}

// ByName returns the AgentState for a given instance name (e.g. "go-engineer~2").
// Returns nil if not found.
func (s *Snapshot) ByName(name string) *AgentState {
	if s == nil {
		return nil
	}
	return s.byName[name]
}

// ActiveWindowID returns the tmux window ID of the currently active window,
// or an empty string if the snapshot is nil or the field is unset.
func (s *Snapshot) ActiveWindowID() string {
	if s == nil {
		return ""
	}
	return s.State.ActiveWindowID
}

// PrefixActive returns true when the tmux prefix key (ctrl+b) was active at
// the time the snapshot was written, or false if the snapshot is nil.
func (s *Snapshot) PrefixActive() bool {
	if s == nil {
		return false
	}
	return s.State.PrefixActive
}

// RateLimit returns the rate limit state from the snapshot, or a zero value
// if the snapshot is nil.
func (s *Snapshot) RateLimit() RateLimitState {
	if s == nil {
		return RateLimitState{}
	}
	return s.State.RateLimit
}

// IsStale returns true if the snapshot's timestamp is older than maxAge,
// indicating the monitor may have stopped writing.
func (s *Snapshot) IsStale(maxAge time.Duration) bool {
	if s == nil {
		return true
	}
	return time.Since(s.Timestamp) > maxAge
}

var (
	stateFilePath string
	mu            sync.Mutex
)

// Init sets the state file path. Must be called once at startup before Load or
// Save are used. paths.Init must be called first.
//
// Without a runtime dir the snapshot goes to a per-user 0700 directory under
// os.TempDir (see safefs.PrivateTempDir) rather than a fixed, shared /tmp
// path another local user could pre-create or read.
func Init() {
	dir := paths.RuntimeDir()
	if dir == "" {
		tmp, err := safefs.PrivateTempDir()
		if err != nil {
			stateFilePath = ""
			return
		}
		dir = tmp
	}
	stateFilePath = filepath.Join(dir, "state.json")
}

// FilePath returns the path to the state file (for debugging).
func FilePath() string {
	return stateFilePath
}

// Save atomically writes the state to disk (temp file + rename) with mode
// 0600: it carries pending tool command text. The state file's directory is
// the runtime dir inside the user's repository, so the write refuses a
// symlinked directory or state.json rather than following it (see safefs).
func Save(s *State) error {
	mu.Lock()
	defer mu.Unlock()

	if stateFilePath == "" {
		return fmt.Errorf("state: no state file path")
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling state: %w", err)
	}

	dir := filepath.Dir(stateFilePath)
	if err := safefs.WriteFile(dir, stateFilePath, data, 0o600); err != nil {
		return fmt.Errorf("writing state file: %w", err)
	}
	return nil
}

// Load reads the state from disk using the path set by Init (or
// SetStatePathForTest). Returns nil, nil if the file doesn't exist.
func Load() (*State, error) {
	mu.Lock()
	path := stateFilePath
	mu.Unlock()
	return LoadFrom(path)
}

// LoadFrom reads a state.json from an arbitrary path. Returns nil, nil if the
// file doesn't exist. This lets callers (e.g. the herd hub) read N snapshots
// from N project directories without going through the package-global path.
func LoadFrom(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading state file: %w", err)
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing state file: %w", err)
	}

	return &s, nil
}

// SetStatePathForTest overrides the state file path. Only for use in tests.
func SetStatePathForTest(path string) {
	mu.Lock()
	defer mu.Unlock()
	stateFilePath = path
}

// Clear removes the state file. Returns nil if the file doesn't exist.
func Clear() error {
	mu.Lock()
	defer mu.Unlock()

	// Removing through a symlinked runtime dir would delete a state.json
	// elsewhere.
	dir := filepath.Dir(stateFilePath)
	if err := safefs.CheckDir(dir, dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	err := os.Remove(stateFilePath)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}
