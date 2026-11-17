package cmd

import (
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/tmux/launchtest"
)

// TestLaunchWithResumeGolden pins the claude command line and window env of
// the sessions browser's resume (--resume --fork-session). It exists to
// prove the harness refactor changed nothing for Claude; regenerate with
// AGENTCTL_UPDATE_GOLDEN=1 only for an intended change.
func TestLaunchWithResumeGolden(t *testing.T) {
	env := launchtest.Setup(t, `{"agents": {"golden": {"model": "opus", "effort_level": "high"}}}`, map[string]string{
		"golden": "---\nname: golden\ndescription: golden test agent\n---\nYou are the golden test agent.\n",
	})
	rec := launchtest.Capture(t)
	LaunchWithResume("golden", "11111111-2222-3333-4444-555555555555", env.Project, dispatch.WriteAgentSettings)
	l := rec.Only()
	got := env.Normalize(l.Render()+launchtest.WindowRecord("@9001"), launchtest.BinPair(l)...)
	launchtest.CheckGolden(t, filepath.Join("testdata", "golden", "resume.txt"), got)
}
