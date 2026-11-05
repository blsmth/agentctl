package tmux

import (
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
)

// legacyCleanupMarker is created in the launch dir once the legacy shared
// temp dir has been swept, so the sweep runs once per launch dir rather than
// on every launch.
const legacyCleanupMarker = ".legacy-tmp-cleaned"

// legacyMinAge protects files a still-running older agentctl may be about to
// use (a launch script tmux has not started yet, a prompt file claude has
// not read yet).
const legacyMinAge = 10 * time.Minute

// legacyLaunchDir is where launch scripts, prompt files, and 0644 launch
// logs (containing full prompts) used to be written, shared by every user.
func legacyLaunchDir() string { return filepath.Join(os.TempDir(), "agentctl") }

// cleanupLegacyLaunchDirOnce sweeps legacyLaunchDir unless launchDir already
// carries the marker. Best-effort: every error is ignored.
func cleanupLegacyLaunchDirOnce(launchDir string) {
	marker := filepath.Join(launchDir, legacyCleanupMarker)
	if _, err := os.Lstat(marker); err == nil {
		return
	}
	n := cleanupLegacyLaunchDir(legacyLaunchDir(), os.Getuid(), time.Now())
	if n > 0 {
		logging.Log("LaunchAgent: removed %d legacy launch files from %s", n, legacyLaunchDir())
	}
	if f, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600); err == nil {
		f.Close()
	}
}

// cleanupLegacyLaunchDir removes the non-directory entries directly inside
// dir that are owned by uid and older than legacyMinAge, then removes dir
// itself if it ends up empty and is owned by uid. Entries owned by anyone
// else are never touched, and symlinks are removed (not followed). Returns
// the number of files removed; errors are ignored.
func cleanupLegacyLaunchDir(dir string, uid int, now time.Time) int {
	dirInfo, err := os.Lstat(dir)
	if err != nil || !dirInfo.IsDir() {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(p)
		if err != nil || fi.IsDir() || !ownedBy(fi, uid) || now.Sub(fi.ModTime()) < legacyMinAge {
			continue
		}
		if os.Remove(p) == nil {
			removed++
		}
	}
	if ownedBy(dirInfo, uid) {
		_ = os.Remove(dir) // only succeeds if empty
	}
	return removed
}

func ownedBy(fi os.FileInfo, uid int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}
