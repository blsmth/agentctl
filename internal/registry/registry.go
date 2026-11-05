// Package registry tracks live agentctl sessions on the machine so the herd
// hub (a future central aggregator) can discover every project's state.json
// without a shared, contended file.
//
// Each session's monitor owns one JSON file at
// ~/.local/state/agentctl/registry/<session-name>.json, written atomically
// (tmp+rename), mirroring how internal/windowstate avoids write contention
// across concurrent writers — N monitors registering/heartbeating concurrently
// must never corrupt each other's entries or block on a lock.
package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

// StaleAfter is the maximum time a registry entry may go without a heartbeat
// before it's considered stale. The monitor heartbeats well within this
// window (see monitor.go's heartbeat throttle), so the hub can safely prune
// entries whose LastSeen is older than this.
const StaleAfter = 30 * time.Second

// Entry describes one live agentctl session.
type Entry struct {
	SessionName string `json:"session_name"` // tmux session, e.g. "agentctl-myproject"
	ProjectDir  string `json:"project_dir"`  // project root (parent of .agentctl/)
	RuntimeDir  string `json:"runtime_dir"`  // where state.json lives
	Host        string `json:"host"`         // "local" for now; Phase 3 remote seam
	PID         int    `json:"pid"`          // monitor pid, for diagnostics
	StartedAt   int64  `json:"started_at"`   // unix seconds
	LastSeen    int64  `json:"last_seen"`    // unix seconds, heartbeat
}

// dirOverride lets tests (and, in principle, an env-based override) redirect
// the registry directory away from the real $HOME.
var dirOverride string

// SetDirForTest overrides the registry directory. Only for use in tests.
func SetDirForTest(path string) {
	dirOverride = path
}

// Dir returns the registry directory. It does not create it, so read-only
// callers (status, hub) leave nothing behind; writeEntry creates it on demand.
func Dir() string {
	return resolveDir()
}

func resolveDir() string {
	if dirOverride != "" {
		return dirOverride
	}
	if v := os.Getenv("AGENTCTL_REGISTRY_DIR"); v != "" {
		return v
	}
	dir, err := config.StateDir()
	if err != nil || dir == "" {
		return filepath.Join(os.TempDir(), "agentctl-registry")
	}
	newPath := filepath.Join(dir, "registry")
	if cfg, cerr := config.GlobalDir(); cerr == nil {
		return config.MigrateLegacy(filepath.Join(cfg, "registry"), newPath)
	}
	return newPath
}

// sessionNameSanitizer strips characters that aren't filename-safe from a
// session name before using it as a filename.
var sessionNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

func filePath(sessionName string) string {
	safe := sessionNameSanitizer.ReplaceAllString(sessionName, "_")
	return filepath.Join(Dir(), safe+".json")
}

// writeEntry atomically writes e to its session's file (tmp+rename).
func writeEntry(e Entry) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling registry entry: %w", err)
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return fmt.Errorf("creating registry dir: %w", err)
	}
	fp := filePath(e.SessionName)
	tmp := fp + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing registry entry: %w", err)
	}
	if err := os.Rename(tmp, fp); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("renaming registry entry: %w", err)
	}
	return nil
}

// readEntry reads and parses a single session's entry file.
func readEntry(sessionName string) (Entry, error) {
	data, err := os.ReadFile(filePath(sessionName))
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return Entry{}, fmt.Errorf("parsing registry entry: %w", err)
	}
	return e, nil
}

// Register writes/overwrites this session's entry file.
func Register(e Entry) error {
	if e.SessionName == "" {
		return fmt.Errorf("registry: SessionName is required")
	}
	return writeEntry(e)
}

// Heartbeat refreshes LastSeen on an existing entry, leaving every other
// field unchanged. Returns an error if the entry doesn't exist (the caller
// should Register first).
func Heartbeat(sessionName string) error {
	e, err := readEntry(sessionName)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("registry: no entry for session %q", sessionName)
		}
		return fmt.Errorf("reading registry entry for heartbeat: %w", err)
	}
	e.LastSeen = time.Now().Unix()
	return writeEntry(e)
}

// Deregister removes this session's entry file. Returns nil if it doesn't
// exist.
func Deregister(sessionName string) error {
	fp := filePath(sessionName)
	if err := os.Remove(fp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing registry entry: %w", err)
	}
	os.Remove(fp + ".tmp") //nolint:errcheck
	return nil
}

// List reads every entry in the registry directory, skipping any file that
// can't be read or parsed (a half-written or corrupt file must not blank the
// hub's whole view).
func List() ([]Entry, error) {
	dir := Dir()
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading registry dir: %w", err)
	}

	var out []Entry
	for _, de := range dirEntries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			continue // skip unreadable
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			continue // skip corrupt/truncated
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionName < out[j].SessionName })
	return out, nil
}

// Prune removes entries whose LastSeen is older than maxAge, or whose
// session name is absent from live, and returns the surviving entries. A nil
// live map skips the liveness check (age-only pruning). live is supplied by
// the caller so this package stays free of any tmux dependency.
func Prune(live map[string]bool, maxAge time.Duration) ([]Entry, error) {
	entries, err := List()
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	var surviving []Entry
	for _, e := range entries {
		if now-e.LastSeen > int64(maxAge.Seconds()) {
			Deregister(e.SessionName) //nolint:errcheck
			continue
		}
		if live != nil && !live[e.SessionName] {
			Deregister(e.SessionName) //nolint:errcheck
			continue
		}
		surviving = append(surviving, e)
	}
	return surviving, nil
}
