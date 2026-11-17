package claude_test

import (
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/claude"
)

func TestLogPath(t *testing.T) {
	tests := []struct {
		name      string
		workDir   string
		sessionID string
		wantEmpty bool
		wantSeg   string // expected project dir segment in path
	}{
		{
			name:      "empty workDir",
			workDir:   "",
			sessionID: "abc-123",
			wantEmpty: true,
		},
		{
			name:      "empty sessionID",
			workDir:   "/Users/alice/src",
			sessionID: "",
			wantEmpty: true,
		},
		{
			name:      "typical path",
			workDir:   "/Users/alice/src/agentctl",
			sessionID: "abc-123",
			wantSeg:   "-Users-alice-src-agentctl",
		},
		{
			name:      "dots in path",
			workDir:   "/Users/alice/src/acme-ops",
			sessionID: "def-456",
			wantSeg:   "-Users-alice-src-acme-ops",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := claude.LogPath(tt.workDir, tt.sessionID)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("LogPath(%q, %q) = %q, want empty string", tt.workDir, tt.sessionID, got)
				}
				return
			}
			if got == "" {
				t.Fatalf("LogPath(%q, %q) returned empty string unexpectedly", tt.workDir, tt.sessionID)
			}
			if !strings.Contains(got, tt.wantSeg) {
				t.Errorf("LogPath(%q, %q) = %q, want path containing %q", tt.workDir, tt.sessionID, got, tt.wantSeg)
			}
			if !strings.HasSuffix(got, tt.sessionID+".jsonl") {
				t.Errorf("LogPath(%q, %q) = %q, want path ending with %q", tt.workDir, tt.sessionID, got, tt.sessionID+".jsonl")
			}
		})
	}
}

func TestProjectSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"/Users/x/code/agentctl", "-Users-x-code-agentctl"},
		{"/Users/x/src/acme/data_pipelines", "-Users-x-src-acme-data-pipelines"},
		{"/Users/x/.config/foo", "-Users-x--config-foo"},
		{"/Users/x/my project", "-Users-x-my-project"},
		{"/Users/x/a/.worktrees/agent-y-issue-1", "-Users-x-a--worktrees-agent-y-issue-1"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := claude.ProjectSlug(tt.in); got != tt.want {
				t.Errorf("ProjectSlug(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLogPathHonorsConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	want := dir + "/projects/-w/s.jsonl"
	if got := claude.LogPath("/w", "s"); got != want {
		t.Errorf("LogPath = %q, want %q", got, want)
	}
	if got := claude.LogPathIn("/other", "/w", "s"); got != "/other/projects/-w/s.jsonl" {
		t.Errorf("LogPathIn = %q", got)
	}
	if got := claude.LogPathIn("", "/w", "s"); got != "" {
		t.Errorf("LogPathIn with no dir = %q, want empty", got)
	}
}
