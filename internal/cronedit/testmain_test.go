package cronedit

import (
	"testing"

	"github.com/blsmth/agentctl/internal/testenv"
)

// TestMain isolates the tests from the live home dir, Claude config dirs,
// agentctl runtime dir and tmux server.
func TestMain(m *testing.M) {
	testenv.Main(m)
}
