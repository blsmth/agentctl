package cmd

import "testing"

func TestResolveRelaunchRoot(t *testing.T) {
	tests := []struct {
		name, cwd, env, want string
	}{
		{"plain repo", "/home/u/proj", "", "/home/u/proj"},
		{"worktree cwd", "/home/u/proj/.worktrees/agent-x-issue-1", "", "/home/u/proj"},
		{"nested in worktree", "/home/u/proj/.worktrees/agent-x/internal/cmd", "", "/home/u/proj"},
		{"worktrees dir itself", "/home/u/proj/.worktrees", "", "/home/u/proj"},
		{"env wins over worktree cwd", "/home/u/proj/.worktrees/w", "/home/u/proj/.agentctl", "/home/u/proj"},
		{"env trailing slash", "/elsewhere", "/home/u/proj/.agentctl/", "/home/u/proj"},
		{"lookalike dir", "/home/u/.worktreesx/p", "", "/home/u/.worktreesx/p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveRelaunchRoot(tt.cwd, tt.env); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
