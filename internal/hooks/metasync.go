package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/detach"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/stamp"
)

// metaSyncInterval is the minimum gap between Stop-hook sidecar syncs for one
// session. SessionEnd always syncs, so the final state is never lost.
const metaSyncInterval = 2 * time.Minute

// spawnMetaSyncFn starts the detached `agentctl sessions sync --session <id>`
// (with --transcript when the hook payload named the log, so the child does
// not scan ~/.claude/projects); a seam for tests.
var spawnMetaSyncFn = func(sessionID, transcriptPath string) error {
	args := []string{"sessions", "sync", "--session", sessionID}
	if transcriptPath != "" {
		args = append(args, "--transcript", transcriptPath)
	}
	return detach.StartSelf(args, nil)
}

// syncSessionMeta refreshes the session's .meta.json sidecar out of process,
// unconditionally. SessionEnd uses it. It never blocks or fails the hook.
func syncSessionMeta(sessionID, transcriptPath string) {
	if sessionID == "" {
		return
	}
	if err := spawnMetaSyncFn(sessionID, transcriptPath); err != nil {
		logging.Debug("hooks: spawn session meta sync %s: %v", sessionID, err)
	}
}

// syncSessionMetaThrottled is syncSessionMeta for the Stop hook, which fires
// every turn: at most one sync per metaSyncInterval per session, enforced with
// a stamp file in the runtime dir so concurrent hooks agree.
func syncSessionMetaThrottled(sessionID, transcriptPath string, now time.Time) {
	if sessionID == "" {
		return
	}
	if rt := paths.RuntimeDir(); rt != "" {
		dir := filepath.Join(rt, "meta-sync")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return
		}
		// Session ids are uuids; refuse anything that could escape the dir.
		if strings.ContainsAny(sessionID, `/\`) || sessionID == "." || sessionID == ".." {
			return
		}
		if !stamp.Try(filepath.Join(dir, sessionID+".stamp"), metaSyncInterval, now) {
			return
		}
	}
	syncSessionMeta(sessionID, transcriptPath)
}
