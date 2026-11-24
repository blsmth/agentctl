package sessions

import "testing"

func TestProjectName(t *testing.T) {
	tests := []struct {
		name, cwd, slug, want string
	}{
		{"main checkout cwd", "/Users/b/code/personal/agentctl", "", "agentctl"},
		{"worktree cwd", "/Users/b/code/personal/agentctl/.worktrees/agent-x-issue-1", "", "agentctl"},
		{"hyphenated via cwd", "/Users/b/code/personal/local-infra/.worktrees/foo", "-Users-b-code-personal-local-infra--worktrees-foo", "local-infra"},
		{"non-repo dir cwd", "/Users/b/code/personal", "", "personal"},
		{"brim cwd", "/Users/b/code/ll/brim", "", "brim"},
		{"worktree slug", "", "-Users-b-code-personal-agentctl--worktrees-agent-code-reviewer-review-pr-319-7042f83a", "agentctl"},
		{"dot worktree slug", "", "-Users-b-code-personal-agentctl-.worktrees-x", "agentctl"},
		{"main slug", "", "-Users-b-code-personal-agentctl", "agentctl"},
		{"non-repo slug", "", "-Users-b-code-personal", "personal"},
		{"decoded main slug", "", decodeDirName("-Users-b-code-personal-agentctl"), "agentctl"},
		{"decoded worktree slug", "", decodeDirName("-Users-b-code-personal-agentctl--worktrees-x"), "agentctl"},
		{"real path as project", "", "/tmp/project", "project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProjectName(tt.cwd, tt.slug); got != tt.want {
				t.Errorf("ProjectName(%q,%q) = %q, want %q", tt.cwd, tt.slug, got, tt.want)
			}
		})
	}
}
