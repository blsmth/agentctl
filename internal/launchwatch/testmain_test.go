package launchwatch

import (
	"testing"

	"github.com/blsmth/agentctl/internal/testenv"
)

// TestMain isolates the tests from the live home dir, runtime dir and tmux
// server.
func TestMain(m *testing.M) {
	testenv.Main(m)
}
