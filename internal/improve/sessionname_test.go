package improve

import "testing"

func TestImproveSessionName(t *testing.T) {
	tests := []struct{ name, project, work, want string }{
		{"uses project root, not .claude/agents dir", "/r/proj", "/r/proj/.claude/agents", "go-engineer-improve-proj"},
		{"falls back to workDir", "", "/r/other", "go-engineer-improve-other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := improveSessionName("go-engineer", tt.project, tt.work); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
