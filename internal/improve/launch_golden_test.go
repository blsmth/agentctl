package improve

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/blsmth/agentctl/internal/tmux/launchtest"
)

// TestLaunchSessionGolden pins the claude command line and window env of an
// improvement session . It exists to
// prove the harness refactor changed nothing for Claude; regenerate with
// AGENTCTL_UPDATE_GOLDEN=1 only for an intended change.
func TestLaunchSessionGolden(t *testing.T) {
	env := launchtest.Setup(t, "", map[string]string{
		"golden": "---\nname: golden\ndescription: golden test agent\n---\nYou are the golden test agent.\n",
	})
	launchtest.WriteConfig(t, `{"agents": {"golden": {"model": "opus", "effort_level": "high", "dir": "`+env.Project+`"}}}`)
	rec := launchtest.Capture(t)
	if err := LaunchSession("golden", "", ""); err != nil {
		t.Fatalf("LaunchSession: %v", err)
	}
	l := rec.Only()
	got := env.Normalize(l.Render()+launchtest.WindowRecord("@9001"), launchtest.BinPair(l)...)
	launchtest.CheckGolden(t, filepath.Join("testdata", "golden", "improve.txt"), got)
}

// TestLaunchSessionWorkDir pins where the session starts when the agent has no
// dir of its own: the caller's directory, never the agent file's folder.
func TestLaunchSessionWorkDir(t *testing.T) {
	env := launchtest.Setup(t, "", map[string]string{
		"golden": "---\nname: golden\ndescription: golden test agent\n---\nYou are the golden test agent.\n",
	})
	launchtest.WriteConfig(t, `{"agents": {"golden": {"model": "opus"}}}`)
	caller := t.TempDir()
	rec := launchtest.Capture(t)
	if err := LaunchSession("golden", "", caller); err != nil {
		t.Fatalf("LaunchSession: %v", err)
	}
	l := rec.Only()
	i := slices.Index(l.Argv, "-c")
	if i < 0 || i+1 >= len(l.Argv) || l.Argv[i+1] != caller {
		t.Fatalf("new-window -c = %v, want %q", l.Argv, caller)
	}
	if got := l.Env["AGENTCTL_WORKING_DIR"]; got != caller {
		t.Errorf("AGENTCTL_WORKING_DIR = %q, want %q", got, caller)
	}
	_ = env
}
