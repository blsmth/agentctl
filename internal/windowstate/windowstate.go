// Package windowstate provides read/write helpers for per-window JSON state files.
//
// Hooks write directly to these files instead of setting tmux window options,
// eliminating the "hooks → tmux options → monitor → state.json" chain and the
// stale-cache bug that came with it.
//
// File layout:
//
//	.agentctl/windows/<windowID>.json      (state)
//	.agentctl/windows/<windowID>.json.lock (advisory flock)
//
// All writes are atomic (tmp+rename). Concurrent writers use flock on the
// .lock file so partial or interleaved writes cannot occur.
package windowstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/safefs"
)

// WindowState is the per-window authoritative state written by hooks and read
// by the monitor. It replaces tmux window options as the IPC mechanism.
type WindowState struct {
	State         string `json:"state"`
	LogPath       string `json:"log_path"`
	SessionID     string `json:"session_id"`
	Done          bool   `json:"done"`
	LastToolTime  int64  `json:"last_tool_time,omitempty"`
	Ticket        string `json:"ticket,omitempty"`
	TicketTitle   string `json:"ticket_title,omitempty"`
	Mode          string `json:"mode,omitempty"`
	SubagentCount int    `json:"subagent_count,omitempty"`

	// Branch and WorktreeDir record the dispatch worktree linkage: which git
	// branch this window is working on, and (only when a worktree was
	// actually created for it, never when dispatch fell back to the shared
	// repo dir) the absolute path to that worktree. Populated once at
	// dispatch time (internal/dispatch.Launch); nothing else writes them.
	// This is what lets `agentctl cleanup` map a finished window back to the
	// worktree it left behind.
	Branch      string    `json:"branch,omitempty"`
	WorktreeDir string    `json:"worktree_dir,omitempty"`
	Error       string    `json:"error,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`

	// SpawnedBy and SpawnedBySession record the dispatcher's reply-to address:
	// the tmux window ID and session UUID of the process that dispatched this
	// agent. Both are the window ID/session pair, not just the window ID,
	// because tmux reuses "@N" window IDs after a window closes -- a bare
	// window ID can silently resolve to an unrelated later agent. Neither
	// mailbox nor delivery consume these fields yet (see
	// docs/agent-messaging-design.md); they may
	// legitimately be empty when a window has no dispatching parent.
	SpawnedBy        string `json:"spawned_by,omitempty"`         // tmux window ID of the dispatcher
	SpawnedBySession string `json:"spawned_by_session,omitempty"` // dispatcher session UUID

	// Pane layout metadata (replaces @agentctl_chrome_bar, @agentctl_hud_pane,
	// @agentctl_hud_collapsed tmux window options).
	ChromeBarPane string `json:"chrome_bar_pane,omitempty"`
	HudPane       string `json:"hud_pane,omitempty"`
	HudCollapsed  bool   `json:"hud_collapsed,omitempty"`

	// MainPane is the tmux pane ID of the agent content pane (e.g. "%12") --
	// the pane that is neither the chrome bar nor the HUD pane. It is the
	// authoritative answer to "which pane holds the agent" for callers (e.g.
	// the native macapp) that would otherwise have to re-derive it with a
	// heuristic. Written once by tmux.LaunchChromeBar (or backfilled by the
	// monitor for windows that already had a chrome bar before this field
	// existed); never changes for the lifetime of a window.
	MainPane string `json:"main_pane,omitempty"`

	// Raw context window usage percentage (0–100). Read by chrome_bar.go.
	// Replaces the rendered tmux-format CtxDisplay string.
	//
	// This field has two writers that do not step on each other: `agentctl statusline` (internal/statusline) writes it
	// from the pushed context_window.used_percentage whenever a value is
	// present, and the monitor writes its own tokens.CtxPct-derived fallback
	// ONLY when the existing value did not come from statusline, or the
	// statusline value has gone stale (see CtxPctSource, CtxPushedAt,
	// CtxFresh). The monitor must not overwrite a fresh pushed value.
	CtxPct int `json:"ctx_pct,omitempty"`

	// CtxPctSource records who most recently won the right to write CtxPct:
	// "statusline" or "derived". See CtxFresh.
	CtxPctSource string `json:"ctx_pct_source,omitempty"`

	// CtxPushedAt is when `agentctl statusline` last successfully wrote
	// CtxPct from a pushed used_percentage. Deliberately separate from
	// UpdatedAt, which every writer (including the monitor's own derived
	// write) touches on every Write call -- UpdatedAt alone can't tell "the
	// statusline push landed 30s ago" from "the statusline push landed 3
	// hours ago and something else has written this record since".
	CtxPushedAt time.Time `json:"ctx_pushed_at,omitempty"`

	// ContextWindowSize and ContextTokens mirror
	// context_window.context_window_size and current_usage's input-only
	// total (input + cache_creation + cache_read -- the same numerator
	// used_percentage itself uses, see internal/statusline.Payload.
	// ContextWindow.UsedPercentage) pushed by `agentctl statusline`. Zero
	// when no push has landed or CtxPctSource != "statusline"; the monitor's
	// persistState falls back to tokens.UsableWindow / the last-turn input
	// token count in that case.
	ContextWindowSize int `json:"context_window_size,omitempty"`
	ContextTokens     int `json:"context_tokens,omitempty"`

	// Model is the model ID (e.g. "claude-sonnet-4-20250514") pushed by
	// Claude Code's statusLine payload (model.id). Written only by
	// `agentctl statusline` (internal/statusline).
	Model string `json:"model,omitempty"`

	// CostUSD is the cumulative session cost pushed by Claude Code's
	// statusLine payload (cost.total_cost_usd). Written only by
	// `agentctl statusline` (internal/statusline).
	CostUSD float64 `json:"cost_usd,omitempty"`

	// PendingTool / PendingToolDetail / PendingToolAt record the tool call
	// that is currently in flight: written by hooks.PreTool, cleared by
	// hooks.PostTool / hooks.PostToolFailure. A non-empty PendingTool means a
	// tool started and has not completed -- which, at the moment the
	// Notification hook fires, is exactly the tool the permission prompt is
	// asking about (PreToolUse fires before Claude Code prompts the user).
	PendingTool       string    `json:"pending_tool,omitempty"`
	PendingToolDetail string    `json:"pending_tool_detail,omitempty"`
	PendingToolAt     time.Time `json:"pending_tool_at,omitempty"`

	// BlockedSince is when this window most recently *entered* the blocked
	// state (not refreshed while it stays blocked), so a repeat notification
	// for a still-pending prompt can report how long it has been waiting.
	BlockedSince time.Time `json:"blocked_since,omitempty"`
}

// CtxPctFreshness is how recent a statusline-pushed CtxPct value must be
// (CtxPushedAt) before the monitor treats it as stale and reclaims CtxPct
// for its own tokens.CtxPct-derived fallback. A parked window stops
// producing assistant messages -- and therefore stops pushing -- so a frozen
// ctx% from hours ago is worse than a freshly recomputed one.
const CtxPctFreshness = 5 * time.Minute

// CtxFresh reports whether ws's CtxPct was written by `agentctl statusline`
// and is still within CtxPctFreshness of now. Callers (the monitor's
// applyVisuals) use this to decide whether to leave CtxPct (and the paired
// ContextWindowSize/ContextTokens) alone or overwrite them with a derived
// value.
func (ws WindowState) CtxFresh(now time.Time) bool {
	if ws.CtxPctSource != "statusline" || ws.CtxPushedAt.IsZero() {
		return false
	}
	return now.Sub(ws.CtxPushedAt) <= CtxPctFreshness
}

// windowsDir is the directory holding per-window state files, and rootDir
// the directory above it from which no path component may be a symlink (the
// runtime dir). Set once by Init; never changed after that.
var (
	windowsDir string
	rootDir    string

	tightenOnce sync.Once
)

// Init sets the windows state directory. Must be called once at startup before
// any other function in this package. paths.Init must be called first.
//
// Without a runtime dir, state goes to a per-user 0700 directory under
// os.TempDir (safefs.PrivateTempDir) rather than a fixed, shared /tmp path.
func Init() {
	if dir := paths.WindowsDir(); dir != "" {
		setDirs(paths.RuntimeDir(), dir)
		return
	}
	tmp, err := safefs.PrivateTempDir()
	if err != nil {
		logging.Log("windowstate: no runtime dir and no private temp dir: %v", err)
		setDirs("", "")
		return
	}
	setDirs(tmp, filepath.Join(tmp, "windows"))
}

// setDirs records the windows dir and its no-symlink root.
func setDirs(root, dir string) {
	rootDir, windowsDir = root, dir
	tightenOnce = sync.Once{}
}

// tightenWindowsDir chmods an existing windows dir (created 0755 by older
// versions) to 0700 once per process: its files carry pending tool command
// text. A symlinked dir is left alone; writes refuse it anyway.
func tightenWindowsDir() {
	tightenOnce.Do(func() {
		if fi, err := os.Lstat(windowsDir); err == nil && fi.IsDir() && fi.Mode().Perm() != 0o700 {
			_ = os.Chmod(windowsDir, 0o700)
		}
	})
}

// Dir returns the windows state directory (set by Init).
func Dir() string {
	return windowsDir
}

// filePath returns the path to the state file for windowID (e.g. "@97" → "<dir>/@97.json").
func filePath(windowID string) string {
	// windowID characters like "@" are valid in filenames on Unix.
	// Sanitize path separators just in case.
	safe := strings.ReplaceAll(windowID, string(filepath.Separator), "_")
	return filepath.Join(windowsDir, safe+".json")
}

// Read loads the state file for windowID. Returns a zero-value WindowState
// (not an error) if the file does not exist — callers treat missing state as
// "not yet written by hooks".
//
// It also defensively guards against a corrupt on-disk record where LogPath
// and SessionID have diverged (see checkLogPathConsistency): if the two
// disagree, LogPath is ignored (returned as "") rather than propagated to
// callers. This makes the monitor self-heal from stale records without
// rewriting the file on every read (which would be write-amplification on
// the 500ms monitor tick).
//
// The windows dir sits in the repo-controlled runtime dir, so Read refuses a
// symlinked dir chain or state file (see checkDir) rather than trusting
// whatever a planted link points at.
func Read(windowID string) (WindowState, error) {
	if windowsDir == "" {
		return WindowState{}, nil
	}
	if err := checkDir(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WindowState{}, nil
		}
		return WindowState{}, fmt.Errorf("reading window state %s: %w", windowID, err)
	}
	data, err := readNoFollow(filePath(windowID))
	if err != nil {
		if os.IsNotExist(err) {
			return WindowState{}, nil
		}
		return WindowState{}, fmt.Errorf("reading window state %s: %w", windowID, err)
	}
	var ws WindowState
	if err := json.Unmarshal(data, &ws); err != nil {
		return WindowState{}, fmt.Errorf("parsing window state %s: %w", windowID, err)
	}
	if !logPathConsistent(ws.LogPath, ws.SessionID) {
		warnInconsistentLogPath(windowID, ws.LogPath, ws.SessionID)
		ws.LogPath = ""
	}
	return ws, nil
}

// logPathConsistent reports whether logPath is plausibly the log for
// sessionID. claude.LogPath derives the filename directly from the session
// ID, so filepath.Base(logPath) must equal sessionID + ".jsonl" -- only the
// basename matters here, never the directory portion. A resumed session
// legitimately logs to the directory where it was originally created (not
// necessarily the window's current cwd), so directory mismatches are
// expected and must NOT be flagged.
//
// Empty LogPath or empty SessionID are not considered inconsistent -- there
// is nothing to compare, so we don't want a false positive.
func logPathConsistent(logPath, sessionID string) bool {
	if logPath == "" || sessionID == "" {
		return true
	}
	return filepath.Base(logPath) == sessionID+".jsonl"
}

// warnedLogPaths tracks (windowID, logPath) pairs we've already warned about
// so the 500ms monitor tick doesn't spam the log for a corrupt record that
// hasn't changed since the last read.
var (
	warnedLogPathsMu sync.Mutex
	warnedLogPaths   = map[string]string{}
)

func warnInconsistentLogPath(windowID, logPath, sessionID string) {
	warnedLogPathsMu.Lock()
	already := warnedLogPaths[windowID] == logPath
	warnedLogPaths[windowID] = logPath
	warnedLogPathsMu.Unlock()
	if already {
		return
	}
	logging.Log("windowstate: ignoring inconsistent log_path for window=%s session=%s log_path=%s (basename mismatch, likely stale)", windowID, sessionID, logPath)
}

// Write atomically writes ws as the state file for windowID (tmp+rename).
// It sets UpdatedAt to the current time before writing.
func Write(windowID string, ws WindowState) error {
	if windowsDir == "" {
		return fmt.Errorf("windowstate: Init not called")
	}
	ws.UpdatedAt = time.Now()

	data, err := json.Marshal(ws)
	if err != nil {
		return fmt.Errorf("marshaling window state %s: %w", windowID, err)
	}

	// The windows dir lives in the runtime dir inside the user's repository;
	// safefs refuses to write through any symlink a cloned repo planted.
	if err := safefs.WriteFile(rootDir, filePath(windowID), data, 0o600); err != nil {
		return fmt.Errorf("writing window state %s: %w", windowID, err)
	}
	tightenWindowsDir()
	return nil
}

// Update reads current state, applies fn, and writes back atomically.
// It holds an exclusive flock on a per-window lock file for the duration,
// preventing concurrent hook writes from clobbering each other.
// This is the primary API for hooks.
func Update(windowID string, fn func(*WindowState)) error {
	if windowsDir == "" {
		return fmt.Errorf("windowstate: Init not called")
	}
	// Acquire exclusive lock before read-modify-write. O_NOFOLLOW: a
	// symlinked lock file must not create or open its target.
	lockPath := filePath(windowID) + ".lock"
	lockFile, err := safefs.OpenNoFollow(rootDir, lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening lock file for %s: %w", windowID, err)
	}
	defer lockFile.Close()
	_ = lockFile.Chmod(0o600) // tighten a lock file created 0644 by an older version

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring lock for %s: %w", windowID, err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) //nolint:errcheck

	ws, err := Read(windowID)
	if err != nil {
		return err
	}

	fn(&ws)

	return Write(windowID, ws)
}

// Remove deletes the state file and lock file for windowID.
// It is called when a tmux window is closed to avoid unbounded file accumulation.
//
// Nothing is removed when the windows dir chain contains a symlink: deleting
// through one would remove files wherever it points.
func Remove(windowID string) {
	if windowsDir == "" || checkDir() != nil {
		return
	}
	fp := filePath(windowID)
	os.Remove(fp)           //nolint:errcheck
	os.Remove(fp + ".lock") //nolint:errcheck
	os.Remove(fp + ".tmp")  //nolint:errcheck // legacy fixed temp name
	// safefs.WriteFile's temp files: ".<id>.json.<random>.tmp".
	if leftovers, err := filepath.Glob(filepath.Join(windowsDir, "."+globEscape(filepath.Base(fp))+".*.tmp")); err == nil {
		for _, p := range leftovers {
			os.Remove(p) //nolint:errcheck
		}
	}
}

// globEscape escapes filepath.Match metacharacters in s.
func globEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)
	return r.Replace(s)
}

// checkDir verifies the windows dir and every directory from rootDir down
// to it are real directories (not symlinks). A missing dir wraps
// os.ErrNotExist.
func checkDir() error {
	root := rootDir
	if root == "" {
		root = windowsDir
	}
	return safefs.CheckDir(root, windowsDir)
}

// readNoFollow reads path, refusing (ELOOP) when it is a symlink.
func readNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	return io.ReadAll(f)
}

// RemoveAll removes all window state files. Called at session startup to
// clear stale state from a previous session (window/pane IDs are not stable
// across tmux session restarts).
func RemoveAll() error {
	if windowsDir == "" {
		return nil
	}
	// A symlinked windows dir (or runtime dir) would make this delete every
	// file in the link's target.
	if err := checkDir(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("reading windows dir: %w", err)
	}
	entries, err := os.ReadDir(windowsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading windows dir: %w", err)
	}
	for _, e := range entries {
		os.Remove(filepath.Join(windowsDir, e.Name())) //nolint:errcheck
	}
	return nil
}

// SetDirForTest overrides the windows state directory (which also becomes
// the no-symlink root). Only for use in tests.
func SetDirForTest(dir string) {
	setDirs(dir, dir)
}

// All reads every window state file and returns a map of windowID to
// WindowState. Used by callers that need to enumerate every window's state
// rather than looking one up by a single known ID (e.g. `agentctl cleanup`
// matching worktrees back to the window that dispatched into them). A
// corrupt or unreadable file is skipped rather than failing the whole scan,
// mirroring FindBySessionID.
func All() map[string]WindowState {
	out := make(map[string]WindowState)
	if windowsDir == "" {
		return out
	}
	entries, err := os.ReadDir(windowsDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		windowID := strings.TrimSuffix(name, ".json")
		ws, err := Read(windowID)
		if err != nil {
			continue
		}
		out[windowID] = ws
	}
	return out
}

// FindBySessionID scans all window state files and returns the windowID
// whose SessionID field matches sessionID. Returns "" if not found.
// Used by hooks that receive a session_id from their payload but not a window_id
// (e.g. hook-session-end, which runs async and may lack TMUX_PANE).
func FindBySessionID(sessionID string) string {
	if sessionID == "" || windowsDir == "" {
		return ""
	}
	entries, err := os.ReadDir(windowsDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Only consider state files; skip .lock, .tmp files.
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		// Derive windowID from filename by stripping the .json suffix.
		windowID := strings.TrimSuffix(name, ".json")
		ws, err := Read(windowID)
		if err != nil {
			continue
		}
		if ws.SessionID == sessionID {
			return windowID
		}
	}
	return ""
}
