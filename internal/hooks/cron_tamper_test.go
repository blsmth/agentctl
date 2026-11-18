package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

// pinCronPaths points the cron tamper and edit guards at fixed paths under a
// fake home.
func pinCronPaths(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", "/Users/u")
	origDir, origJobs := cronStateDir, cronJobsPath
	t.Cleanup(func() { cronStateDir, cronJobsPath = origDir, origJobs })
	cronStateDir = func() string { return "/Users/u/.local/state/agentctl/cron" }
	cronJobsPath = func() (string, error) { return "/Users/u/.config/agentctl/schedules.json", nil }
}

func TestCheckCronTamper(t *testing.T) {
	pinCronPaths(t)
	tests := []struct {
		name     string
		command  string
		wantDeny bool
	}{
		// Denied: writes to the jobs file.
		{"echo redirect", `echo {} > ~/.config/agentctl/schedules.json`, true},
		{"append redirect", `echo x >> /Users/u/.config/agentctl/schedules.json`, true},
		{"quoted redirect target", `printf '{}' > "$HOME/.config/agentctl/schedules.json"`, true},
		{"jq into the file", `jq '.jobs[0].enabled=true' ~/.config/agentctl/schedules.json > schedules.json`, true},
		{"jq via temp then mv", `jq . ~/.config/agentctl/schedules.json > /tmp/s && mv /tmp/s ~/.config/agentctl/schedules.json`, true},
		{"tee", `jq . x.json | tee ~/.config/agentctl/schedules.json`, true},
		{"sponge", `jq . ~/.config/agentctl/schedules.json | sponge ~/.config/agentctl/schedules.json`, true},
		{"sed in place", `sed -i '' 's/false/true/' ~/.config/agentctl/schedules.json`, true},
		{"sed --in-place", `sed --in-place 's/a/b/' schedules.json`, true},
		{"perl in place", `perl -pi -e 's/false/true/' ~/.config/agentctl/schedules.json`, true},
		{"cp over", `cp /tmp/evil.json ~/.config/agentctl/schedules.json`, true},
		{"rm", `rm ~/.config/agentctl/schedules.json`, true},
		{"python write", `python3 -c "open('/Users/u/.config/agentctl/schedules.json','w').write('{}')"`, true},
		{"heredoc into the file", "cat > ~/.config/agentctl/schedules.json <<'EOF'\n{\"jobs\":[]}\nEOF", true},
		// Denied: writes to the cron state dir.
		{"state.json redirect", `echo '{}' > ~/.local/state/agentctl/cron/state.json`, true},
		{"state.json via HOME", `echo '{}' > "$HOME/.local/state/agentctl/cron/state.json"`, true},
		{"state rm", `rm -rf /Users/u/.local/state/agentctl/cron`, true},
		{"xdg state", `rm $XDG_STATE_HOME/agentctl/cron/tick.lock`, true},
		{"chained", `go build ./... && echo x > ~/.local/state/agentctl/cron/state.json`, true},

		// Allowed: reading, and ordinary commands that mention cron.
		{"cat", `cat ~/.config/agentctl/schedules.json`, false},
		{"jq read", `jq '.jobs[].name' ~/.config/agentctl/schedules.json`, false},
		{"jq read piped", `cat ~/.config/agentctl/schedules.json | jq .`, false},
		{"read and save elsewhere", `jq . ~/.config/agentctl/schedules.json > /tmp/backup.json`, false},
		{"read with stderr redirect", `cat ~/.config/agentctl/schedules.json 2>&1`, false},
		{"tail cron log", `tail -n 50 ~/.local/state/agentctl/cron/cron.log`, false},
		{"cat state", `cat ~/.local/state/agentctl/cron/state.json`, false},
		{"go test cron", `go test ./internal/cron/...`, false},
		{"git add cron state code", `git add internal/cron/state.go internal/cron/jobs.go`, false},
		{"cron list", `agentctl cron list`, false},
		{"cron rm", `agentctl cron rm nightly`, false},
		{"heredoc body mentions it", "cat > notes.md <<'EOF'\nnever echo > schedules.json\nEOF", false},
		{"crontab", `crontab -l > /tmp/crontab.txt`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := checkGlobalDenyList(tt.command)
			if got := reason == cronTamperReason; got != tt.wantDeny {
				t.Errorf("checkGlobalDenyList(%q) = %q, want cron deny %v", tt.command, reason, tt.wantDeny)
			}
		})
	}
}

func TestCronEditTarget(t *testing.T) {
	pinCronPaths(t)
	input := func(field, path string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{field: path})
		return b
	}
	tests := []struct {
		name  string
		tool  string
		input json.RawMessage
		want  string
	}{
		{"write jobs file", "Write", input("file_path", "/Users/u/.config/agentctl/schedules.json"), "/Users/u/.config/agentctl/schedules.json"},
		{"edit jobs file via tilde", "Edit", input("file_path", "~/.config/agentctl/schedules.json"), "/Users/u/.config/agentctl/schedules.json"},
		{"multiedit unclean path", "MultiEdit", input("file_path", "/Users/u/.config/agentctl/../agentctl/schedules.json"), "/Users/u/.config/agentctl/schedules.json"},
		{"edit state", "Edit", input("file_path", "/Users/u/.local/state/agentctl/cron/state.json"), "/Users/u/.local/state/agentctl/cron/state.json"},
		{"notebook in state dir", "NotebookEdit", input("notebook_path", "/Users/u/.local/state/agentctl/cron/x.ipynb"), "/Users/u/.local/state/agentctl/cron/x.ipynb"},
		{"read jobs file", "Read", input("file_path", "/Users/u/.config/agentctl/schedules.json"), ""},
		{"write a fixture elsewhere", "Write", input("file_path", "/src/app/testdata/schedules.json"), ""},
		{"write sibling dir", "Write", input("file_path", "/Users/u/.local/state/agentctl/cronish/x"), ""},
		{"write config", "Write", input("file_path", "/Users/u/.config/agentctl/config.json"), ""},
		{"bad input", "Write", json.RawMessage(`not json`), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cronEditTarget(tt.tool, tt.input); got != tt.want {
				t.Errorf("cronEditTarget = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPreTool_DeniesCronJobsEdit(t *testing.T) {
	pinCronPaths(t)
	stdin := `{"tool_name":"Write","tool_input":{"file_path":"/Users/u/.config/agentctl/schedules.json","content":"{}"}}`
	var exits []int
	origExit := exitFunc
	t.Cleanup(func() { exitFunc = origExit })
	out, stderr := runHookCaptured(t, map[string]string{"AGENTCTL": "1", "AGENTCTL_WINDOW_ID": "", "TMUX_PANE": ""}, stdin, func() {
		exitFunc = func(code int) { exits = append(exits, code) }
		PreTool()
	})
	decision, reason := decisionOf(t, out)
	if decision != "deny" || !strings.Contains(reason, "schedules.json") {
		t.Errorf("decision %q reason %q, want a schedules.json deny", decision, reason)
	}
	if len(exits) != 1 || exits[0] != 2 {
		t.Errorf("exit codes = %v, want [2]", exits)
	}
	if !strings.Contains(stderr, "cron add") {
		t.Errorf("stderr %q should point at cron add", stderr)
	}
}
