package cmd

import (
	"testing"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/launchwatch"
	"github.com/blsmth/agentctl/internal/testenv"
)

// TestMain isolates the tests from the live home dir, Claude config dirs,
// agentctl runtime dir and tmux server.
func TestMain(m *testing.M) {
	// Launch paths must never spawn a real launch watcher.
	launchwatch.SetSpawnForTest(func(string) error { return nil })
	// The audit ID column reads no tmux or ledger.
	auditIndex = func() *fleet.Index { return fleet.NewIndex(nil, nil) }
	testenv.Main(m)
}
