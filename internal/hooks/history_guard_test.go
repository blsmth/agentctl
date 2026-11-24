package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func pinHistoryDir(t *testing.T) (state string) {
	t.Helper()
	state = t.TempDir()
	if r, err := filepath.EvalSymlinks(state); err == nil {
		state = r
	}
	if err := os.MkdirAll(filepath.Join(state, "agent-history.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := historyStateDir
	historyStateDir = func() string { return state }
	t.Cleanup(func() { historyStateDir = orig })
	return state
}

func TestCheckHistoryTamper(t *testing.T) {
	state := pinHistoryDir(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(state, "agent-history.git"), link); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	store := filepath.Join(state, "agent-history.git")
	tests := []struct {
		name     string
		command  string
		cwd      string
		wantDeny bool
	}{
		{"git-dir checkout", `git --git-dir=` + store + ` --work-tree=~/.config/agentctl checkout abc -- config.json`, cwd, true},
		{"git-dir separate word", `git --git-dir ` + store + ` log`, cwd, true},
		{"config store name", `git --git-dir=/x/state/agentctl/agent-history-config.git log`, cwd, true},
		{"GIT_DIR env", `GIT_DIR=` + store + ` git log`, cwd, true},
		{"export GIT_DIR", `export GIT_DIR=` + store + `; git log`, cwd, true},
		{"-C", `git -C ` + store + ` log`, cwd, true},
		{"project store", `git --git-dir=` + state + `/agent-history/ab12-repo.git log`, cwd, true},
		{"cd into", `cd ` + store + ` && git log`, cwd, true},
		{"relative after cd", `cd ` + state + ` && git --git-dir=agent-history.git log`, cwd, true},
		{"relative dot after cd", `cd ` + store + ` && git --git-dir=. log`, cwd, true},
		{"relative from cwd", `git --git-dir=agent-history.git log`, state, true},
		{"rm", `rm -rf ` + store, cwd, true},
		{"mv", `mv /tmp/x ` + store + `/HEAD`, cwd, true},
		{"cp", `cp /tmp/x ` + store + `/objects`, cwd, true},
		{"redirect", `echo x > ` + store + `/HEAD`, cwd, true},
		{"symlink", `git --git-dir=` + link + ` log`, cwd, true},
		{"cd symlink then dot", `cd ` + link + ` && git --git-dir=. log`, cwd, true},

		{"unrelated git", `git log --oneline`, cwd, false},
		{"other state dir file", `cat ` + state + `/cron/jobs.json`, cwd, false},
		{"agents history", `agentctl agents history`, cwd, false},
		{"agents diff", `agentctl agents diff agents/x.md`, cwd, false},
		{"agents revert", `agentctl agents revert agents/x.md abc123`, cwd, false},
		{"agentctl arg mentions store", `agentctl context set k '` + store + `'`, cwd, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkHistoryTamper(tt.command, tt.cwd)
			if (got != "") != tt.wantDeny {
				t.Errorf("checkHistoryTamper(%q) = %q, want deny=%v", tt.command, got, tt.wantDeny)
			}
			if got != "" && checkGlobalDenyListIn(tt.command, tt.cwd) == "" {
				t.Error("checkGlobalDenyListIn does not apply the rule")
			}
		})
	}
}

func TestHistoryEditTarget(t *testing.T) {
	state := pinHistoryDir(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(state, "agent-history.git"), link); err != nil {
		t.Fatal(err)
	}
	in := func(p string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"file_path": p})
		return b
	}
	tests := []struct {
		name string
		tool string
		path string
		deny bool
	}{
		{"write HEAD", "Write", filepath.Join(state, "agent-history.git", "HEAD"), true},
		{"edit config", "Edit", filepath.Join(state, "agent-history-config.git", "config"), true},
		{"multiedit project store", "MultiEdit", filepath.Join(state, "agent-history", "a-b.git", "HEAD"), true},
		{"via symlink", "Write", filepath.Join(link, "HEAD"), true},
		{"other state file", "Write", filepath.Join(state, "other", "x"), false},
		{"outside", "Write", "/tmp/x", false},
		{"read tool", "Read", filepath.Join(state, "agent-history.git", "HEAD"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := historyEditTarget(tt.tool, in(tt.path))
			if (got != "") != tt.deny {
				t.Errorf("historyEditTarget = %q, want deny=%v", got, tt.deny)
			}
		})
	}
}
