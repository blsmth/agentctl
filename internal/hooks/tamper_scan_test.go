package hooks

import (
	"strings"
	"testing"
	"time"
)

// fpPrompt is a realistic multi-line single-quoted context set value, the
// shape that was denied as serve tampering: backticked commands (including
// `agentctl serve pair`), a PWA route with #, serve.log, the '\'' apostrophe
// idiom, $(...) and `...` inside the quotes, and many newlines.
const fpPrompt = `# Task

Fix the hub card. The human pairs a phone with ` + "`agentctl serve pair`" + `, then opens
/#/artifacts/<id> to see the report. If it fails, check serve.log
(` + "`agentctl serve logs`" + `) rather than stopping the server.

Brendan'\''s notes:
- do not run ` + "`agentctl serve stop`" + ` or ` + "`launchctl bootout gui/501/com.blsmth.agentctl.serve`" + `
- the old fix used $(date) and ` + "`kill agentctl serve`" + ` in a script; drop that
- never write to ~/.local/state/agentctl/serve/control.sock, schedules.json or .agentctl/templates

## Checks
go test ./internal/hooks/... > /tmp/out
`

func pinAllTamperDirs(t *testing.T) {
	t.Helper()
	pinCronPaths(t)
	pinArtifactsDir(t)
	pinApprovalDir(t)
	pinTemplateDir(t)
	pinGlobalConfig(t, nil)
	orig := serveStateDir
	t.Cleanup(func() { serveStateDir = orig })
	serveStateDir = func() string { return "/Users/u/.local/state/agentctl/serve" }
}

// TestTamperRules_QuotedAgentctlArgument is the regression test for the
// false positive: the serve-tamper rule matched words inside a quoted
// agentctl argument, because it ran on the raw line and never on the
// masked one.
func TestTamperRules_QuotedAgentctlArgument(t *testing.T) {
	pinAllTamperDirs(t)
	tests := []struct {
		name, command string
	}{
		{"full prompt", "agentctl context set tamper-fp-prompt '" + fpPrompt + "'"},
		{"serve pair in backticks", "agentctl context set k 'run `agentctl serve pair` first'"},
		{"serve.log", "agentctl context set k 'see serve.log'"},
		{"PWA route", "agentctl context set k 'open /#/artifacts/abc'"},
		{"apostrophe idiom", `agentctl context set k 'Brendan'\''s phone'`},
		{"substitution inside single quotes", "agentctl context set k 'x $(agentctl serve stop) y'"},
		{"multi-line kill", "agentctl context set k 'kill the\nagentctl serve\nprocess'"},
		{"dispatch prompt", "agentctl dispatch --agent go-engineer --prompt '" + fpPrompt + "'"},
		{"followed by a read", "agentctl context set k '" + fpPrompt + "' && agentctl context get k"},
		{"comment after", "agentctl context set k 'serve pair' # note"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if reason := checkGlobalDenyList(tt.command); reason != "" {
				t.Fatalf("checkGlobalDenyList denied: %q", reason)
			}
		})
	}
}

func TestMaskAgentctlArgs_QuotedConstructs(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"newlines in single quotes", "agentctl context set k 'a\nb\nc'", "agentctl _ _ _ _"},
		{"apostrophe idiom", `agentctl context set k 'a'\''b'`, "agentctl _ _ _ _"},
		{"backticks in single quotes", "agentctl context set k 'run `x`'", "agentctl _ _ _ _"},
		{"hash in single quotes", "agentctl context set k '/#/a'", "agentctl _ _ _ _"},
		{"hash after a space in quotes", "agentctl context set k 'a\n# b'", "agentctl _ _ _ _"},
		{"full prompt", "agentctl context set k '" + fpPrompt + "'", "agentctl _ _ _ _"},
		{"line continuation", "agentctl context set k \\\n  v", "agentctl _ _ _ \\\n  _"},
		{"pipeline over a newline", "agentctl status |\n tee x", "agentctl _ |\n tee x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maskAgentctlArgs(tt.in); got != tt.want {
				t.Errorf("maskAgentctlArgs(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestCheckServeTamper_RealInvocationsAroundMaskedArgs: masking must not
// hide a serve stop that the shell really runs.
func TestCheckServeTamper_RealInvocationsAroundMaskedArgs(t *testing.T) {
	pinAllTamperDirs(t)
	tests := []struct {
		name, command string
	}{
		{"unquoted substitution", "agentctl context set k $(agentctl serve stop)"},
		{"double-quoted substitution", `agentctl context set k "$(agentctl serve stop)"`},
		{"backticks", "agentctl context set k `agentctl serve stop`"},
		{"backticks in double quotes", "agentctl context set k \"`agentctl serve toggle`\""},
		{"substitution in another command", "echo $(agentctl serve stop)"},
		{"nested substitution", "agentctl context set k $(echo $(agentctl serve start))"},
		{"chained after context set", "agentctl context set k 'serve stop' && agentctl serve stop"},
		{"after a newline", "agentctl context set k 'a\nb'\nagentctl serve stop"},
		{"after a comment", "agentctl context set k v # x\nagentctl serve pair"},
		{"leading option", "agentctl --debug serve stop"},
		{"launchctl chained", "agentctl context set k 'x'; launchctl bootout gui/501/com.blsmth.agentctl.serve"},
		{"launchctl in substitution", "agentctl context set k \"$(launchctl bootout gui/501/com.blsmth.agentctl.serve)\""},
		{"state dir write chained", "agentctl context set k 'x' && rm ~/.local/state/agentctl/serve/devices.json"},
		{"state dir as an agentctl path argument", "agentctl artifact add ~/.local/state/agentctl/serve/devices.json"},
		{"socket as an agentctl path argument", "agentctl artifact add /tmp/control.sock"},
		{"state dir through cd", "cd ~/.local/state/agentctl && cat serve/devices.json"},
		{"state dir through a variable", "D=~/.local/state/agentctl; cat $D/serve/devices.json"},
		{"pkill chained", "agentctl context set k v && pkill -f 'agentctl serve'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if reason := checkGlobalDenyList(tt.command); !strings.Contains(reason, "agentctl serve logs") {
				t.Fatalf("checkGlobalDenyList(%q) = %q, want the serve deny", tt.command, reason)
			}
		})
	}
}

// TestWriteTemplates_Evasions covers writes whose target is reached through
// a cd, a variable or find, for every rule on the shared matcher.
func TestWriteTemplates_Evasions(t *testing.T) {
	pinAllTamperDirs(t)
	tests := []struct {
		name, command, want string
		wantDeny            bool
	}{
		// cd into the protected dir, relative write.
		{"cd cron dir redirect", "cd ~/.local/state/agentctl/cron && echo x > state.json", "cron", true},
		{"cd cron dir rel subdir", "cd /Users/u/.local/state/agentctl/cron && echo x > rel/file", "cron", true},
		{"cd artifacts dir rm dot", "cd ~/.local/state/agentctl/artifacts && rm -rf .", "artifact store", true},
		{"cd templates dir", "cd ~/.config/agentctl/templates; echo x > t.md", "prompt templates", true},
		{"cd approvals dir", "cd ~/.local/state/agentctl/dispatch-approvals && touch m", "approval", true},
		// cd into the state dir (a parent), relative write.
		{"cd state dir rm artifacts", "cd ~/.local/state/agentctl && rm -rf artifacts", "artifact store", true},
		{"cd state dir rm cron", "cd $HOME/.local/state/agentctl && rm -rf cron", "cron", true},
		{"cd grandparent", "cd ~/.local/state && rm -rf agentctl/artifacts", "artifact store", true},
		{"cd in steps", "cd ~ && cd .local/state && cd agentctl && rm -r artifacts", "artifact store", true},
		{"cd dotdot", "cd ~/.local/state/agentctl/cron/.. && rm -rf artifacts", "artifact store", true},
		{"cd semicolon", "cd ~/.local/state/agentctl; mv x artifacts/y", "artifact store", true},
		{"cd newline", "cd ~/.local/state/agentctl\nrm -rf cron", "cron", true},
		{"cd tee", "cd ~/.local/state/agentctl && echo x | tee cron/state.json", "cron", true},
		{"cd interpreter", "cd ~/.local/state/agentctl/cron && python3 -c 'open(\"state.json\",\"w\")'", "cron", true},
		{"cd global config dir", "cd ~/.config/agentctl && printf x > config.json", "human-only", true},
		// An uncertain cd counts toward the deny side.
		{"uncertain cd then away", "cd ~/.local/state/agentctl || cd /tmp; rm -rf artifacts", "artifact store", true},
		{"cd after &&", "true && cd ~/.local/state/agentctl; rm -rf cron", "cron", true},
		{"cd inside bash -c", "bash -c 'cd ~/.local/state/agentctl && rm -rf artifacts'", "artifact store", true},
		{"cd inside eval", "eval 'cd ~/.local/state/agentctl; rm -rf artifacts'", "artifact store", true},
		// A variable holding the protected dir.
		{"var redirect", "D=~/.local/state/agentctl/cron; echo x > $D/a", "cron", true},
		{"var braces", "D=/Users/u/.local/state/agentctl/artifacts; echo x > ${D}/a", "artifact store", true},
		{"var quoted", `D="$HOME/.local/state/agentctl/cron"; echo x > "$D/state.json"`, "cron", true},
		{"var export", "export D=~/.local/state/agentctl; rm -rf $D/artifacts", "artifact store", true},
		{"var cd", "D=~/.local/state/agentctl; cd $D && rm -rf cron", "cron", true},
		{"var from substitution", "D=$(echo ~/.local/state/agentctl/cron); echo x > $D/a", "cron", true},
		{"var concatenated", "B=~/.local/state/agentctl; D=$B/cron; touch $D/x", "cron", true},
		// find.
		{"find delete", "find ~/.local/state/agentctl/artifacts -delete", "artifact store", true},
		{"find name delete", "find ~/.local/state/agentctl/cron -name '*.json' -delete", "cron", true},
		{"find exec rm", "find ~/.local/state/agentctl/artifacts -type f -exec rm {} \\;", "artifact store", true},
		{"find execdir", "find ~/.local/state/agentctl/dispatch-approvals -execdir rm {} +", "approval", true},
		{"find xargs rm", "find ~/.local/state/agentctl/artifacts -print0 | xargs -0 rm -f", "artifact store", true},
		{"cd find delete", "cd ~/.local/state/agentctl && find artifacts -delete", "artifact store", true},
		{"cd find dot", "cd ~/.local/state/agentctl/cron && find . -delete", "cron", true},

		// Allowed.
		{"cd subshell undone", "(cd ~/.local/state/agentctl && ls) && rm -rf artifacts", "", false},
		{"cd and read", "cd ~/.local/state/agentctl && ls artifacts", "", false},
		{"cd elsewhere", "cd /repo && rm -rf build && echo x > out.txt", "", false},
		{"cd certain away", "cd ~/.local/state/agentctl && cd /tmp && rm -rf artifacts", "", false},
		{"var elsewhere", "D=/tmp/x; echo x > $D/artifacts", "", false},
		{"find read", "find ~/.local/state/agentctl/artifacts -name meta.json", "", false},
		{"find delete elsewhere", "find /tmp/build -delete", "", false},
		{"go test", "cd /repo && go test ./internal/artifacts/... > /tmp/out", "", false},
		{"agentctl after cd", "cd ~/.local/state/agentctl/artifacts && agentctl artifact list", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := checkGlobalDenyList(tt.command)
			if got := reason != ""; got != tt.wantDeny {
				t.Fatalf("checkGlobalDenyList(%q) = %q, wantDeny %v", tt.command, reason, tt.wantDeny)
			}
			if tt.wantDeny && !strings.Contains(reason, tt.want) {
				t.Fatalf("reason %q, want one mentioning %q", reason, tt.want)
			}
		})
	}
}

func TestCleanViewPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"~/.local/state/agentctl/serve/..", "~/.local/state/agentctl"},
		{"$HOME/..", "$HOME/.."},
		{"~/.", "~"},
		{"/a/b/../c", "/a/c"},
		{"rel/./x", "rel/x"},
		{"~", "~"},
	}
	for _, tt := range tests {
		if got := cleanViewPath(tt.in); got != tt.want {
			t.Errorf("cleanViewPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestWriteViews_Bounded: variable values that double per assignment, and
// long cd chains, must not keep the hook past its timeout (which would fail
// open). Past the caps the rules fail closed.
func TestWriteViews_Bounded(t *testing.T) {
	pinAllTamperDirs(t)
	tests := []struct {
		name, command string
		wantDeny      bool
	}{
		{"doubling variable", strings.Repeat("D=x$D$D; ", 40) + "echo x > $D; rm -rf ~/.local/state/agentctl/cron", true},
		{"doubling variable, target only mentioned", strings.Repeat("D=x$D$D; ", 40) + "echo ~/.local/state/agentctl/cron", true},
		{"doubling variable, no target", strings.Repeat("D=x$D$D; ", 40) + "echo x > $D", false},
		{"long certain cd chain", strings.Repeat("cd a && echo x > b; ", 1000) + "cron", false},
		{"long uncertain cd chain", strings.Repeat("cd a || cd b; echo x > c; ", 1000) + "cron", false},
		{"long chain then a write", strings.Repeat("cd a || cd b; echo x > c; ", 1000) + "rm -rf ~/.local/state/agentctl/cron", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			reason := checkGlobalDenyList(tt.command)
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("took %v", d)
			}
			if got := reason != ""; got != tt.wantDeny {
				t.Errorf("denied %v (%q), want %v", got, reason, tt.wantDeny)
			}
		})
	}
}

func TestTamperRules_ReviewFollowups(t *testing.T) {
	pinAllTamperDirs(t)
	for _, command := range []string{
		"D=~/.local/state/agentctl E=$D/cron; rm -rf $E",
		"S=stop; agentctl serve $S",
		`S=stop; agentctl serve "$S"`,
		"agentctl serve {stop,}",
		"go run . serve $S",
	} {
		if checkGlobalDenyList(command) == "" {
			t.Errorf("checkGlobalDenyList(%q) allowed it, want a deny", command)
		}
	}
}
