package cmd

import (
	"testing"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/sessionname"
)

func TestResumeSessionName(t *testing.T) {
	paths.Init(t.TempDir())
	if err := sessionname.Record("sid-worktree", "agent-go-engineer-issue-42"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		sessionID string
		want      string
	}{
		{"recorded name is reused verbatim", "sid-worktree", "agent-go-engineer-issue-42"},
		{"legacy session derives agent-project", "sid-legacy", "go-engineer-proj"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resumeSessionName("go-engineer", tt.sessionID, "/r/proj"); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
