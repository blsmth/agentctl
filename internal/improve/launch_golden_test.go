package improve

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/sessions"
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
	if w := rec.Watches(); !slices.Equal(w, []string{"@9001"}) {
		t.Errorf("launch watcher started for %v, want [@9001]", w)
	}
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

// TestRunUnfocusedModelOverrideGolden pins `agentctl improve --model sonnet`
// as the CLI runs it: the window opens unfocused (no focus switch) and the
// launch passes the override as `--model` after `--agent`, replacing the
// agent's configured "opus". Compared with improve.txt (the popup, focused),
// the only differences are the new-window focus flag and the model.
func TestRunUnfocusedModelOverrideGolden(t *testing.T) {
	env := launchtest.Setup(t, "", map[string]string{
		"golden": "---\nname: golden\ndescription: golden test agent\nmodel: haiku\n---\nYou are the golden test agent.\n",
	})
	launchtest.WriteConfig(t, `{"agents": {"golden": {"model": "opus", "effort_level": "high", "dir": "`+env.Project+`"}}}`)
	rec := launchtest.Capture(t)
	if err := Run(Options{Agent: "golden", Model: "sonnet"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	l := rec.Only()
	got := env.Normalize(l.Render()+launchtest.WindowRecord("@9001"), launchtest.BinPair(l)...)
	launchtest.CheckGolden(t, filepath.Join("testdata", "golden", "improve_cli.txt"), got)
	if !strings.Contains(l.AgentCommand(), "--model 'sonnet'") || strings.Contains(l.AgentCommand(), "opus") {
		t.Errorf("command = %s, want --model sonnet only", l.AgentCommand())
	}
}

func TestRunDryRunLaunchesNothing(t *testing.T) {
	env := launchtest.Setup(t, "", map[string]string{
		"golden": "---\nname: golden\ndescription: golden test agent\n---\nYou are the golden test agent.\n",
	})
	launchtest.WriteConfig(t, `{"agents": {"golden": {"dir": "`+env.Project+`"}}}`)
	rec := launchtest.Capture(t)
	var out strings.Builder
	if err := Run(Options{Agent: "golden", Theme: "terse replies", DryRun: true, Out: &out}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := len(rec.Launches()); n != 0 {
		t.Errorf("dry run launched %d window(s)", n)
	}
	for _, want := range []string{"EVIDENCE (agent golden", "--- PROMPT ---", sessions.ImprovePromptHeader, "terse replies"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run output missing %q", want)
		}
	}
}

func TestRunFleetDryRunUsesFleetHeader(t *testing.T) {
	env := launchtest.Setup(t, "", map[string]string{
		"brandon": "---\nname: brandon\ndescription: orchestrator\n---\nYou orchestrate.\n",
	})
	launchtest.WriteConfig(t, `{"agents": {"brandon": {"dir": "`+env.Project+`"}}}`)
	var out strings.Builder
	if err := Run(Options{Fleet: true, DryRun: true, Out: &out}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), sessions.FleetImprovePromptHeader) || !strings.Contains(out.String(), "every agent") {
		t.Errorf("fleet output:\n%s", out.String())
	}
}
