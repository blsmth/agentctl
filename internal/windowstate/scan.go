package windowstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// AllFrom is the directory-scoped counterpart to All: it scans an arbitrary
// windows directory instead of the package-level one set by Init.
//
// This exists for cross-session queries. All() can only ever see the calling
// process's own project, because windowsDir is derived from paths.RuntimeDir()
// at Init time. A command that aggregates every live agentctl session (see
// internal/herd) needs to read each session's windows directory in turn, and
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
