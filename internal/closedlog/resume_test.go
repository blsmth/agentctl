package closedlog

import (
	"strings"
	"testing"
)

func TestResumeEnv(t *testing.T) {
	got := ResumeEnv("/x/p", []string{"A=1", "AGENTCTL_RUNTIME_DIR=/hub", "TMUX_PANE=%3", "PWD=/hub"})
	want := []string{"A=1", "PWD=/x/p", "AGENTCTL_RUNTIME_DIR=/x/p/.agentctl"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("env = %v", got)
	}
}

func TestResumeArgs(t *testing.T) {
	got := strings.Join(ResumeArgs(Record{ProjectDir: "/p", Agent: "a", SessionID: "s"}), " ")
	if want := "hub-resume /p --work-dir /p --agent a --session-id s"; got != want {
		t.Errorf("args = %q", got)
	}
	got = strings.Join(ResumeArgs(Record{ProjectDir: "/p", WorkDir: "/p/w/", Agent: "a", SessionID: "s"}), " ")
	if want := "hub-resume /p --work-dir /p/w --agent a --session-id s"; got != want {
		t.Errorf("args = %q", got)
	}
}
