package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/claude"
)

func TestValidateRestoreSession(t *testing.T) {
	const sid = "17f983b2-aaaa"
	workDir := "/Users/x/src/myproj"
	wt := workDir + "/.worktrees/agent-go-engineer-issue-1"
	other := "/Users/x/src/acme-infra"

	tests := []struct {
		name      string
		logsIn    []string // workDirs whose project dir holds the log
		workDir   string
		sessionID string
		savedLog  string
		wantKept  bool
	}{
		{"right project", []string{workDir}, workDir, sid, "", true},
		{"only other project", []string{other}, workDir, sid, filepath.Join("/x", claude.ProjectSlug(other), sid+".jsonl"), false},
		{"missing", nil, workDir, sid, "/some/path.jsonl", false},
		{"empty session", []string{workDir}, workDir, "", "/some/path.jsonl", false},
		{"logpath mismatch replaced", []string{workDir}, workDir, sid, "/elsewhere/" + sid + ".jsonl", true},
		{"worktree workdir", []string{wt}, wt, sid, "", true},
		{"worktree but log in parent", []string{workDir}, wt, sid, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, d := range tt.logsIn {
				dir := filepath.Join(root, claude.ProjectSlug(d))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			gotSID, gotLog := validateRestoreSession(root, tt.workDir, tt.sessionID, tt.savedLog)
			if !tt.wantKept {
				if gotSID != "" || gotLog != "" {
					t.Fatalf("got (%q,%q), want empty", gotSID, gotLog)
				}
				return
			}
			want := filepath.Join(root, claude.ProjectSlug(tt.workDir), sid+".jsonl")
			if gotSID != sid || gotLog != want {
				t.Fatalf("got (%q,%q), want (%q,%q)", gotSID, gotLog, sid, want)
			}
		})
	}
}
