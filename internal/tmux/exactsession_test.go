package tmux

import (
	"os/exec"
	"strings"
	"testing"
)

func TestExactSessionHelpers(t *testing.T) {
	if got := ExactSession("agentctl-foo"); got != "=agentctl-foo" {
		t.Errorf("ExactSession = %q", got)
	}
	if got := ExactSessionTarget("agentctl-foo"); got != "=agentctl-foo:" {
		t.Errorf("ExactSessionTarget = %q", got)
	}
}

// TestSessionTargetsAreExact runs the helper forms against a private tmux
// server (own -L socket, never the user's) holding only "agentctl-foobar" and
// checks that targets for the nonexistent "agentctl-foo" do not prefix-match.
func TestSessionTargetsAreExact(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := "acexact" + strings.ReplaceAll(t.Name(), "/", "")
	tm := func(args ...string) error {
		return exec.Command("tmux", append([]string{"-L", sock, "-f", "/dev/null"}, args...)...).Run()
	}
	if err := tm("new-session", "-d", "-s", "agentctl-foobar", "sleep", "60"); err != nil {
		t.Skipf("cannot start scratch tmux server: %v", err)
	}
	t.Cleanup(func() { _ = tm("kill-server") })

	// Sanity: the bare name does prefix-match, which is the bug.
	if err := tm("has-session", "-t", "agentctl-foo"); err != nil {
		t.Skip("this tmux does not prefix-match; nothing to verify")
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"has-session", []string{"has-session", "-t", ExactSession("agentctl-foo")}},
		{"list-windows", []string{"list-windows", "-t", ExactSession("agentctl-foo")}},
		{"set-option", []string{"set-option", "-t", ExactSessionTarget("agentctl-foo"), "status", "off"}},
		{"new-window", []string{"new-window", "-d", "-t", ExactSessionTarget("agentctl-foo")}},
		{"select-window", []string{"select-window", "-t", sessionWindowTarget("agentctl-foo", "@0")}},
		{"kill-session", []string{"kill-session", "-t", ExactSession("agentctl-foo")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tm(tc.args...); err == nil {
				t.Errorf("tmux %v matched agentctl-foobar", tc.args)
			}
		})
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"has-session", []string{"has-session", "-t", ExactSession("agentctl-foobar")}},
		{"set-option", []string{"set-option", "-t", ExactSessionTarget("agentctl-foobar"), "status", "off"}},
		{"new-window", []string{"new-window", "-d", "-t", ExactSessionTarget("agentctl-foobar")}},
		{"new-window-left", []string{"new-window", "-d", "-b", "-t", ExactSessionTarget("agentctl-foobar") + "^"}},
		{"select-window", []string{"select-window", "-t", sessionWindowTarget("agentctl-foobar", "@0")}},
		{"list-panes-s", []string{"list-panes", "-s", "-t", ExactSessionTarget("agentctl-foobar")}},
	} {
		t.Run("exact/"+tc.name, func(t *testing.T) {
			if err := tm(tc.args...); err != nil {
				t.Errorf("tmux %v rejected: %v", tc.args, err)
			}
		})
	}
}
