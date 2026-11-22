package prompttemplate

import (
	"testing"

	"github.com/blsmth/agentctl/internal/testenv"
)

// TestMain isolates the tests from the live home dir and config dirs.
func TestMain(m *testing.M) {
	testenv.Main(m)
}
