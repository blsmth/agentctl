package launchwatch

import (
	"errors"
	"slices"
	"testing"

	"github.com/blsmth/agentctl/internal/harness"
)

func TestStart(t *testing.T) {
	tests := []struct {
		name     string
		windowID string
		harness  string
		want     []string
	}{
		{name: "claude window", windowID: "@7", harness: harness.Claude, want: []string{"@7"}},
		{name: "harness without a trust dialog", windowID: "@7", harness: harness.Pi},
		{name: "no window (no tmux)", windowID: "", harness: harness.Claude},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			restore := SetSpawnForTest(func(windowID string) error {
				got = append(got, windowID)
				return nil
			})
			defer restore()
			Start(tt.windowID, harness.For(tt.harness).Caps())
			if !slices.Equal(got, tt.want) {
				t.Errorf("spawned %v, want %v", got, tt.want)
			}
		})
	}
}

// A spawn failure is logged, never returned or fatal.
func TestStartSpawnFailure(t *testing.T) {
	defer SetSpawnForTest(func(string) error { return errors.New("boom") })()
	Start("@7", harness.Caps{TrustDialog: true})
}

// The default spawn refuses to fork inside a test binary, so a launch test
// that forgot to stub it cannot start watchers (or re-run the suite).
func TestDefaultSpawnRefusesInTests(t *testing.T) {
	if err := spawnFn("@7"); !errors.Is(err, errTestSpawn) {
		t.Fatalf("default spawn in a test binary = %v, want errTestSpawn", err)
	}
}
