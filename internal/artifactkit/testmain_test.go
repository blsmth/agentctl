package artifactkit

import (
	"testing"

	"github.com/blsmth/agentctl/internal/testenv"
)

// TestMain isolates the tests from the live home dir and state dir.
func TestMain(m *testing.M) {
	testenv.Main(m)
}
