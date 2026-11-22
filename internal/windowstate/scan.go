package windowstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// AllFrom is the directory-scoped counterpart to All: it scans an arbitrary
// windows directory instead of the package-level one set by Init.
//
// This exists for cross-session queries. All() can only ever see the calling
// process's own project, because windowsDir is derived from paths.RuntimeDir()
// at Init time. A command that aggregates every live agentctl session (see
// internal/fleet) needs to read each session's windows directory in turn, and
// hijacking the package global via SetDirForTest would be both racy and a lie
// about that function's contract.
//
// Unlike Read, this deliberately does NOT apply the log-path consistency
// guard: that guard exists to make the monitor self-heal its own project's
// records, and warning about another project's on-disk state from a read-only
// query would be noise attributed to the wrong session. Callers that care
// about LogPath for a foreign session should treat it as advisory.
//
// A corrupt or unreadable file is skipped rather than failing the whole scan,
// mirroring All. An empty or unreadable dir yields an empty (non-nil) map.
func AllFrom(dir string) map[string]WindowState {
	out := make(map[string]WindowState)
	if dir == "" {
		return out
	}
	// dir is another project's repo-controlled runtime dir: skip it if it is
	// a symlink, and skip symlinked entries, rather than reading whatever
	// they point at.
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return out
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		// Only .json — the directory also holds .lock and .tmp siblings.
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := readNoFollow(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var ws WindowState
		if err := json.Unmarshal(data, &ws); err != nil {
			continue
		}
		out[strings.TrimSuffix(name, ".json")] = ws
	}
	return out
}

// WindowsDirFor returns the windows directory for a runtime dir (the
// "<project>/.agentctl" path recorded in a registry entry). It mirrors
// paths.WindowsDir, which can only answer for the current process.
func WindowsDirFor(runtimeDir string) string {
	if runtimeDir == "" {
		return ""
	}
	return filepath.Join(runtimeDir, "windows")
}

// DirCache reads records from one windows directory for a set of window IDs,
// re-parsing a file only when its size, mtime or inode changed. It serves
// long-lived readers that rebuild on every windowstate write in the project
// (the chrome bar): they need only their session's live windows, while the
// directory also holds the records of every other session. Like AllFrom it
// skips a symlinked dir or entry and a corrupt file, and applies no log-path
// guard. Not safe for concurrent use.
type DirCache struct {
	dir     string
	entries map[string]dirCacheEntry
}

type dirCacheEntry struct {
	size  int64
	mtime int64
	ino   uint64
	ws    WindowState
}

// NewDirCache returns a cache over dir (a WindowsDirFor path).
func NewDirCache(dir string) *DirCache {
	return &DirCache{dir: dir, entries: map[string]dirCacheEntry{}}
}

// Records returns the records of ids that exist, keyed by window ID. Entries
// for IDs not asked for are dropped.
func (c *DirCache) Records(ids []string) map[string]WindowState {
	out := make(map[string]WindowState, len(ids))
	if c.dir == "" {
		return out
	}
	if fi, err := os.Lstat(c.dir); err != nil || !fi.IsDir() {
		clear(c.entries)
		return out
	}
	next := make(map[string]dirCacheEntry, len(ids))
	for _, id := range ids {
		if id == "" || strings.ContainsRune(id, filepath.Separator) {
			continue
		}
		e, ok := c.load(id)
		if !ok {
			continue
		}
		next[id] = e
		out[id] = e.ws
	}
	c.entries = next
	return out
}

func (c *DirCache) load(id string) (dirCacheEntry, bool) {
	path := filepath.Join(c.dir, id+".json")
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return dirCacheEntry{}, false
	}
	e := dirCacheEntry{size: fi.Size(), mtime: fi.ModTime().UnixNano()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		e.ino = uint64(st.Ino) //nolint:unconvert // differs per OS
	}
	if old, ok := c.entries[id]; ok && old.size == e.size && old.mtime == e.mtime && old.ino == e.ino {
		return old, true
	}
	data, err := readNoFollow(path)
	if err != nil {
		return dirCacheEntry{}, false
	}
	if err := json.Unmarshal(data, &e.ws); err != nil {
		return dirCacheEntry{}, false
	}
	return e, true
}
