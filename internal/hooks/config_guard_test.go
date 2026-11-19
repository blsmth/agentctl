package hooks

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
)

const testGlobalConfig = "/Users/u/.config/agentctl/config.json"

// pinGlobalConfig points the config guard at testGlobalConfig under a fake
// home, with current as the file's content (nil = no file).
func pinGlobalConfig(t *testing.T, current []byte) {
	t.Helper()
	t.Setenv("HOME", "/Users/u")
	origFile, origRead := globalConfigFile, readGlobalConfig
	t.Cleanup(func() { globalConfigFile, readGlobalConfig = origFile, origRead })
	globalConfigFile = func() (string, error) { return testGlobalConfig, nil }
	readGlobalConfig = func(p string) ([]byte, error) {
		if p != testGlobalConfig {
			t.Errorf("read %q, want %q", p, testGlobalConfig)
		}
		if current == nil {
			return nil, fs.ErrNotExist
		}
		return current, nil
	}
}

func TestCheckConfigTamper(t *testing.T) {
	pinGlobalConfig(t, nil)
	pinCronPaths(t)
	tests := []struct {
		name     string
		command  string
		wantDeny bool
	}{
		// Denied: shell writes to the global config.
		{"redirect", `echo '{}' > ~/.config/agentctl/config.json`, true},
		{"redirect absolute", `echo '{}' > /Users/u/.config/agentctl/config.json`, true},
		{"redirect via HOME", `printf x > "$HOME/.config/agentctl/config.json"`, true},
		{"redirect via XDG", `printf x > "$XDG_CONFIG_HOME/agentctl/config.json"`, true},
		{"jq redirect onto it", `jq '.auto_poll=true' ~/.config/agentctl/config.json > ~/.config/agentctl/config.json`, true},
		{"jq via temp then mv", `jq . ~/.config/agentctl/config.json > /tmp/c && mv /tmp/c ~/.config/agentctl/config.json`, true},
		{"tee", `jq . x.json | tee ~/.config/agentctl/config.json`, true},
		{"sed in place", `sed -i '' 's/all/off/' ~/.config/agentctl/config.json`, true},
		{"cp onto it", `cp /tmp/c.json ~/.config/agentctl/config.json`, true},
		{"python", `python3 -c "open('/Users/u/.config/agentctl/config.json','w')"`, true},
		{"dispatch_approval with the dir", `cd ~/.config/agentctl && jq '.dispatch_approval="off"' config.json > t && mv t config.json`, true},
		{"redirect after agentctl args", `agentctl status --json > ~/.config/agentctl/config.json`, true},
		{"agentctl with a command substitution", `agentctl context set k "$(tee ~/.config/agentctl/config.json </tmp/x)"`, true},

		// Allowed: reads, the project config, and agentctl argument text.
		{"cat", `cat ~/.config/agentctl/config.json`, false},
		{"jq read", `jq '.agents | keys' ~/.config/agentctl/config.json`, false},
		{"jq read piped", `cat ~/.config/agentctl/config.json | jq .`, false},
		{"grep", `grep -n auto_poll ~/.config/agentctl/config.json`, false},
		{"project config write", `echo '{"dispatch_approval":"all"}' > .agentctl/config.json`, false},
		{"jq redirect elsewhere", `jq . ~/.config/agentctl/config.json > /tmp/backup.json`, false},
		{"unrelated config.json", `echo '{}' > testdata/config.json`, false},
		{"context set text", `agentctl context set notes 'never tee ~/.config/agentctl/config.json or set dispatch_approval'`, false},
		{"dispatch prompt text", `agentctl dispatch --agent a --prompt "do not mv /tmp/x ~/.config/agentctl/config.json"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := checkGlobalDenyList(tt.command)
			if got := reason == configTamperReason; got != tt.wantDeny {
				t.Errorf("checkGlobalDenyList(%q) = %q, want config deny %v", tt.command, reason, tt.wantDeny)
			}
		})
	}
}

func TestGlobalConfigEditTarget(t *testing.T) {
	edit := func(oldS, newS string, all bool) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"file_path": testGlobalConfig, "old_string": oldS, "new_string": newS, "replace_all": all})
		return b
	}
	write := func(path, content string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"file_path": path, "content": content})
		return b
	}
	const (
		unset  = `{"auto_poll": false}`
		nonRev = `{"auto_poll": false, "dispatch_approval": "non-review"}`
		all    = `{"auto_poll": false, "dispatch_approval": "all"}`
		off    = `{"auto_poll": false, "dispatch_approval": "off"}`
	)
	tests := []struct {
		name     string
		current  string // "" = no file
		tool     string
		input    json.RawMessage
		wantDeny bool
	}{
		{"edit an unrelated key", all, "Edit", edit(`"auto_poll": false`, `"auto_poll": true`, false), false},
		{"edit non-review to off", nonRev, "Edit", edit(`"non-review"`, `"off"`, false), true},
		{"edit unset to off", unset, "Edit", edit(`false}`, `false, "dispatch_approval": "off"}`, false), true},
		{"edit non-review to all", nonRev, "Edit", edit(`"non-review"`, `"all"`, false), false},
		{"edit off to non-review", off, "Edit", edit(`"off"`, `"non-review"`, false), false},
		{"edit all to an unknown value", all, "Edit", edit(`"all"`, `"bogus"`, false), false},
		{"edit an unknown value to off", `{"dispatch_approval": "bogus"}`, "Edit", edit(`"bogus"`, `"off"`, false), true},
		{"edit removing the key from all", all, "Edit", edit(`, "dispatch_approval": "all"`, ``, false), true},
		{"edit old_string not found", all, "Edit", edit(`"nope"`, `"off"`, false), false},
		{"edit replace_all loosens", `{"dispatch_approval": "all", "x": "all"}`, "Edit", edit(`"all"`, `"off"`, true), true},
		{"edit first occurrence only", `{"x": "all", "dispatch_approval": "all"}`, "Edit", edit(`"all"`, `"off"`, false), false},
		{"write dropping the key when current is all", all, "Write", write(testGlobalConfig, unset), true},
		{"write keeping all", all, "Write", write(testGlobalConfig, `{"dispatch_approval": "all", "auto_poll": true}`), false},
		{"write tightening a new file", "", "Write", write(testGlobalConfig, all), false},
		{"write off into a new file", "", "Write", write(testGlobalConfig, off), true},
		{"write an unparseable result mentioning the key", nonRev, "Write", write(testGlobalConfig, `{"dispatch_approval": "all",`), true},
		{"write an unparseable result without the key", unset, "Write", write(testGlobalConfig, `{"auto_poll": `), false},
		{"write an unparseable result over all", all, "Write", write(testGlobalConfig, `{"auto_poll": `), true},
		{"write via tilde", nonRev, "Write", write("~/.config/agentctl/config.json", off), true},
		{"write the project config", nonRev, "Write", write("/src/app/.agentctl/config.json", off), false},
		{"read is not an edit", nonRev, "Read", write(testGlobalConfig, off), false},
		{"multiedit loosening", nonRev, "MultiEdit", json.RawMessage(`{"file_path":"` + testGlobalConfig + `","edits":[{"old_string":"auto_poll","new_string":"auto_poll"},{"old_string":"\"non-review\"","new_string":"\"off\""}]}`), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cur []byte
			if tt.current != "" {
				cur = []byte(tt.current)
			}
			pinGlobalConfig(t, cur)
			got := globalConfigEditTarget(tt.tool, tt.input)
			if (got != "") != tt.wantDeny {
				t.Errorf("globalConfigEditTarget = %q, want deny %v", got, tt.wantDeny)
			}
		})
	}
}

func TestPreTool_DeniesLooseningDispatchApproval(t *testing.T) {
	pinGlobalConfig(t, []byte(`{"dispatch_approval": "all"}`))
	stdin := `{"tool_name":"Edit","tool_input":{"file_path":"` + testGlobalConfig + `","old_string":"\"all\"","new_string":"\"off\""}}`
	var exits []int
	origExit := exitFunc
	t.Cleanup(func() { exitFunc = origExit })
	out, stderr := runHookCaptured(t, map[string]string{"AGENTCTL": "1", "AGENTCTL_WINDOW_ID": "", "TMUX_PANE": ""}, stdin, func() {
		exitFunc = func(code int) { exits = append(exits, code) }
		PreTool()
	})
	decision, reason := decisionOf(t, out)
	if decision != "deny" || !strings.Contains(reason, "human-only") {
		t.Errorf("decision %q reason %q, want a human-only deny", decision, reason)
	}
	if len(exits) != 1 || exits[0] != 2 {
		t.Errorf("exit codes = %v, want [2]", exits)
	}
	if !strings.Contains(stderr, "dispatch_approval") {
		t.Errorf("stderr %q should name dispatch_approval", stderr)
	}
}

func TestMaskAgentctlArgs(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"context set", `agentctl context set k 'tee schedules.json'`, `agentctl _ _ _ _`},
		{"path to agentctl", `/usr/local/bin/agentctl status x`, `/usr/local/bin/agentctl _ _`},
		{"redirect target kept", `agentctl status --json > out.json`, `agentctl _ _ > out.json`},
		{"stderr redirect", `agentctl status 2>&1`, `agentctl _ _>&1`},
		{"chained other command untouched", `agentctl context set k 'v' && tee x`, `agentctl _ _ _ _ && tee x`},
		{"env assignment prefix", `FOO=1 agentctl context set k v`, `FOO=1 agentctl _ _ _ _`},
		{"double-quoted substitution kept", `agentctl context set k "$(tee x)"`, `agentctl context set k "$(tee x)"`},
		{"backticks kept", "agentctl context set k `tee x`", "agentctl context set k `tee x`"},
		{"single-quoted substitution is text", `agentctl context set k '$(tee x)'`, `agentctl _ _ _ _`},
		{"not agentctl", `tee 'agentctl x'`, `tee 'agentctl x'`},
		{"newline ends the command", "agentctl context set k v\ntee x", "agentctl _ _ _ _\ntee x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maskAgentctlArgs(tt.in); got != tt.want {
				t.Errorf("maskAgentctlArgs(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Regression: the cron tamper rule matched the text of an agentctl context
// set value that mentioned schedules.json next to tee or mv.
func TestCheckCronTamper_IgnoresAgentctlArgumentText(t *testing.T) {
	pinCronPaths(t)
	pinGlobalConfig(t, nil)
	allowed := []string{
		`agentctl context set notes 'agents must not tee or mv ~/.config/agentctl/schedules.json'`,
		`agentctl context set notes "never cp anything onto schedules.json"`,
		`agentctl dispatch --agent a --prompt 'do not rm schedules.json'`,
	}
	for _, cmd := range allowed {
		if reason := checkGlobalDenyList(cmd); reason != "" {
			t.Errorf("checkGlobalDenyList(%q) = %q, want allowed", cmd, reason)
		}
	}
	denied := []string{
		`agentctl context set k v && tee ~/.config/agentctl/schedules.json </tmp/x`,
		`agentctl cron list > ~/.config/agentctl/schedules.json`,
		`agentctl context set k "$(cp /tmp/x ~/.config/agentctl/schedules.json)"`,
	}
	for _, cmd := range denied {
		if reason := checkGlobalDenyList(cmd); reason != cronTamperReason {
			t.Errorf("checkGlobalDenyList(%q) = %q, want the cron deny", cmd, reason)
		}
	}
}
