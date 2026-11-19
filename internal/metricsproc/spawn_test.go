package metricsproc

import (
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/detach"
)

// TestSpawnStripsWindowAccount: the per-machine serve must not inherit the
// Claude account of the window whose hook started it.
func TestSpawnStripsWindowAccount(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/accounts/work")
	t.Setenv("AGENTCTL_ACCOUNT", "work")
	t.Setenv("CLAUDE_CODE_SUBPROCESS_ENV_SCRUB", "1")
	var gotArgs, gotEnv []string
	t.Cleanup(detach.SetStartSelfForTest(func(args, env []string) error {
		gotArgs, gotEnv = args, env
		return nil
	}))
	if err := spawn(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotArgs, []string{"metrics", "serve"}) {
		t.Fatalf("args = %q", gotArgs)
	}
	for _, e := range gotEnv {
		for _, name := range []string{"CLAUDE_CONFIG_DIR=", "AGENTCTL_ACCOUNT=", "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB="} {
			if strings.HasPrefix(e, name) {
				t.Errorf("child env keeps %s", e)
			}
		}
	}
}
