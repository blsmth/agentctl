package phoneview

import (
	"os"
	"testing"

	"github.com/blsmth/agentctl/internal/testenv"
)

func TestMain(m *testing.M) {
	cleanup := testenv.Setup()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
