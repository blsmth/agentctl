package hooks

import (
	"errors"
	"strings"
	"testing"
)

// stubAgentCheck installs a validator that knows the given names (plus
// plugin:name) and records the dir it was asked about.
func stubAgentCheck(t *testing.T, known ...string) *string {
	t.Helper()
	gotDir := new(string)
	orig := agentNameCheck
	t.Cleanup(func() { agentNameCheck = orig })
	SetAgentNameCheck(func(name, dir string) error {
		*gotDir = dir
		for _, k := range known {
			if k == name || strings.Contains(name, ":") {
				return nil
			}
		}
		return errors.New(`unknown agent "` + name + `"; available agents: ` + strings.Join(known, ", "))
	})
	return gotDir
}

func TestUnknownAgentDeny(t *testing.T) {
	tests := []struct {
		name    string
		command string
		cron    bool
		deny    bool
		wantDir string
	}{
		{"dispatch unknown", "agentctl dispatch --issue 1 --agent claude", false, true, "/work"},
		{"dispatch unknown short", "agentctl dispatch -a claude -p hi", false, true, "/work"},
		{"dispatch unknown eq", "agentctl dispatch --agent=claude -p hi", false, true, "/work"},
		{"dispatch known", "agentctl dispatch --issue 1 --agent go-engineer", false, false, ""},
		{"dispatch plugin", "agentctl dispatch --issue 1 --agent p:x", false, false, ""},
		{"dispatch no agent", "agentctl dispatch --issue 1", false, false, ""},
		{"dispatch review-pr no agent", "agentctl dispatch --review-pr 3", false, false, ""},
		{"dispatch prompt mentions agent", `agentctl dispatch -p "use --agent claude"`, false, false, ""},
		{"dispatch mode improve", "agentctl dispatch --mode improve --agent claude", false, false, ""},
		{"dispatch expanded", "agentctl dispatch --agent $A -p hi", false, false, ""},
		{"dispatch dir", "agentctl dispatch --agent claude --dir /other -p hi", false, true, "/other"},
		{"dispatch relative dir", "agentctl dispatch --agent claude --dir sub -p hi", false, true, "/work/sub"},
		{"dispatch dir changes", "cd /x && agentctl dispatch --agent claude -p hi", false, false, ""},
		{"cron add unknown", "agentctl cron add j --schedule '@daily' --agent claude --prompt hi", true, true, "/work"},
		{"cron add known", "agentctl cron add j --schedule '@daily' --agent go-engineer --prompt hi", true, false, ""},
		{"cron add template", "agentctl cron add j --schedule '@daily' --template t", true, false, ""},
		{"cron add improve", "agentctl cron add j --schedule '@daily' --improve-fleet --agent claude", true, false, ""},
		{"cron add dir", "agentctl cron add j --schedule '@daily' --agent claude --dir /other --prompt hi", true, true, "/other"},
		{"cron edit unknown with dir", "agentctl cron edit j --agent claude --dir /other", true, true, "/other"},
		{"cron edit unknown no dir", "agentctl cron edit j --agent claude", true, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDir := stubAgentCheck(t, "go-engineer", "general-purpose")
			var got string
			if tt.cron {
				got = cronUnknownAgentDeny(tt.command, "/work")
			} else {
				got = dispatchUnknownAgentDeny(tt.command, "/work")
			}
			if (got != "") != tt.deny {
				t.Fatalf("deny = %q, want deny=%v", got, tt.deny)
			}
			if tt.deny {
				if !strings.Contains(got, "available agents:") {
					t.Errorf("reason %q lacks the available agents", got)
				}
				if *gotDir != tt.wantDir {
					t.Errorf("checked dir %q, want %q", *gotDir, tt.wantDir)
				}
			}
		})
	}
}

func TestUnknownAgentDeny_NoCheckInstalled(t *testing.T) {
	orig := agentNameCheck
	t.Cleanup(func() { agentNameCheck = orig })
	agentNameCheck = nil
	if got := dispatchUnknownAgentDeny("agentctl dispatch --agent claude", "/work"); got != "" {
		t.Errorf("deny = %q, want none without a check", got)
	}
}

func TestGates_UnknownAgentDeniedWithoutPopup(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-unknown-agent")
	stubAgentCheck(t, "go-engineer")
	failIfDispatchPopupRunnerCalled(t)

	for _, tt := range []struct {
		name string
		gate func(string, string, string) (bool, bool, string)
		cmd  string
	}{
		{"dispatch", dispatchPreviewGate, "agentctl dispatch --issue 1 --agent claude"},
		{"cron add", cronPreviewGate, "agentctl cron add j --schedule '@daily' --agent claude --prompt hi"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			approved, decided, reason := tt.gate("go-engineer", tt.cmd, "/work")
			if approved || decided || !strings.Contains(reason, `unknown agent "claude"`) {
				t.Errorf("got approved=%v decided=%v reason=%q, want a deny naming the agent", approved, decided, reason)
			}
		})
	}
}

func TestDispatchGate_KnownAgentReachesPopup(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-known-agent")
	stubAgentCheck(t, "go-engineer")
	command := "agentctl dispatch --issue 1 --agent go-engineer"
	removeDispatchLockForTest(t, command)
	called := withFakeDispatchPopupRunner(t, func(int, int, string) error { return nil })

	approved, decided, _ := dispatchPreviewGate("go-engineer", command, "/work")
	if !*called || !decided || !approved {
		t.Errorf("called=%v decided=%v approved=%v, want the popup shown and approved", *called, decided, approved)
	}
}
