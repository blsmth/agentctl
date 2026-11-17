package improve

import (
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/tmux/launchtest"
)

// TestLaunchSessionGolden pins the claude command line and window env of an
// improvement session (skipEditor, the auto-improve path). It exists to
// prove the harness refactor changed nothing for Claude; regenerate with
// AGENTCTL_UPDATE_GOLDEN=1 only for an intended change.
func TestLaunchSessionGolden(t *testing.T) {
	env := launchtest.Setup(t, "", map[string]string{
		"golden": "---\nname: golden\ndescription: golden test agent\n---\nYou are the golden test agent.\n",
	})
	launchtest.WriteConfig(t, `{"agents": {"golden": {"model": "opus", "effort_level": "high", "dir": "`+env.Project+`"}}}`)
	rec := launchtest.Capture(t)
	if err := LaunchSession("golden", "", true); err != nil {
		t.Fatalf("LaunchSession: %v", err)
	}
	l := rec.Only()
	got := env.Normalize(l.Render()+launchtest.WindowRecord("@9001"), launchtest.BinPair(l)...)
	launchtest.CheckGolden(t, filepath.Join("testdata", "golden", "improve.txt"), got)
}
