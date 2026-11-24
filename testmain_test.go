package main

import (
	"testing"

	"github.com/blsmth/agentctl/internal/launchwatch"
	"github.com/blsmth/agentctl/internal/testenv"
)

// TestMain isolates the tests from the live home dir, Claude config dirs,
// agentctl runtime dir and tmux server.
func TestMain(m *testing.M) {
	// Launch paths must never spawn a real launch watcher.
	launchwatch.SetSpawnForTest(func(string) error { return nil })
	testenv.Main(m)
}
