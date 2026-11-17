package hooks

import (
	"testing"

	"github.com/blsmth/agentctl/internal/detach"
	"github.com/blsmth/agentctl/internal/logging"
)

// spawnMetaSyncFn starts the detached `agentctl sessions sync --session <id>`;
// a seam for tests.
var spawnMetaSyncFn = func(sessionID string) error {
	if testing.Testing() {
		return nil // never re-exec a test binary
	}
	return detach.StartSelf([]string{"sessions", "sync", "--session", sessionID}, nil)
}

// syncSessionMeta refreshes the session's .meta.json sidecar out of process.
// The Stop and SessionEnd hooks call it now that no monitor writes sidecars.
// It never blocks or fails the hook.
func syncSessionMeta(sessionID string) {
	if sessionID == "" {
		return
	}
	if err := spawnMetaSyncFn(sessionID); err != nil {
		logging.Debug("hooks: spawn session meta sync %s: %v", sessionID, err)
	}
}
