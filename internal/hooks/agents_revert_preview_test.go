package hooks

import (
	"strconv"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func TestIsAgentctlAgentsRevert(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want bool
	}{
		{"agentctl agents revert reviewer", true},
		{"/usr/local/bin/agentctl agents revert reviewer abc123", true},
		{"agentctl agent-manager revert reviewer", true},
		{"cd /tmp && agentctl agents revert reviewer; echo ok", true},
		{"agentctl agents revert;", true},
		{"agentctl agents --json revert rev", true},
		{"agentctl agents --tui revert rev", true},
		{"agentctl agent-manager --json=false revert rev abc", true},
		{`agentctl agents "revert" rev`, true},
		{`agentctl agents 'revert' rev`, true},
		{`agentctl agents re""vert rev`, true},
		{"agentctl agents revert --help", false},
		{"agentctl agents history reviewer", false},
		{"agentctl agents diff reviewer", false},
		{"agentctl agents", false},
		{"agentctl agents reviewer", false},
		{"echo 'agentctl agents revert reviewer'", false},
		{"git revert HEAD", false},
	} {
		if got := isAgentctlAgentsRevert(tc.cmd); got != tc.want {
			t.Errorf("isAgentctlAgentsRevert(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

// runRevertValidate runs ValidateBash with the revert popup stubbed and
// reports whether it was shown and the exit codes the hook produced.
func runRevertValidate(t *testing.T, command string, approved bool) (gated bool, exits []int) {
	t.Helper()
	d := DefaultDeps()
	d.LoadConfig = func() *config.Config { return &config.Config{} }
	d.LoadProjectConfig = func(string) *config.ProjectConfig { return nil }
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	orig := agentsRevertGate
	agentsRevertGate = func(_, _, _ string) (bool, bool, string) { gated = true; return approved, true, "" }
	t.Cleanup(func() { agentsRevertGate = orig })
	stdin := `{"tool_name":"Bash","cwd":"","tool_input":{"command":` + strconv.Quote(command) + `}}`
	_, exits = runValidateBashCapturingExit(t, map[string]string{
		"AGENTCTL": "1", "AGENTCTL_AGENT_NAME": "lead", "AGENTCTL_WINDOW_ID": "w99",
		"AGENTCTL_SESSION_ID": "", "TMUX_PANE": "",
	}, stdin)
	return gated, exits
}

func TestValidateBash_AgentsRevertAlwaysGated(t *testing.T) {
	if gated, exits := runRevertValidate(t, "agentctl agents revert reviewer", true); !gated || len(exits) != 0 {
		t.Errorf("approved: gated=%v exits=%v, want popup shown and no deny", gated, exits)
	}
	if gated, exits := runRevertValidate(t, "agentctl agents revert reviewer", false); !gated || len(exits) != 1 || exits[0] != 2 {
		t.Errorf("rejected: gated=%v exits=%v, want popup shown and exit 2", gated, exits)
	}
	if gated, _ := runRevertValidate(t, "agentctl agents history reviewer", true); gated {
		t.Error("history must not be gated")
	}
	if gated, exits := runRevertValidate(t, "agentctl agents revert a && agentctl template rm b", true); gated || len(exits) != 1 {
		t.Errorf("mixed gates: gated=%v exits=%v, want a deny without a popup", gated, exits)
	}
}
