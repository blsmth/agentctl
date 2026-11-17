package harness

import (
	"slices"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/shell"
)

// fields tokenizes cmd the way a POSIX shell would. Every $VAR expands to a
// sentinel so an unquoted expansion shows up as a mismatch.
func fields(t *testing.T, cmd string) []string {
	t.Helper()
	got, err := shell.Fields(cmd, func(string) string { return "EXPANDED" })
	if err != nil {
		t.Fatalf("shell.Fields(%q): %v", cmd, err)
	}
	return got
}

func TestClaudeCommand(t *testing.T) {
	tests := []struct {
		name string
		inv  Invocation
		want []string
	}{
		{
			name: "no model or effort",
			inv:  Invocation{Agent: "go-engineer", SessionID: "sess-001"},
			want: []string{"claude", "--agent", "go-engineer", "--session-id", "sess-001"},
		},
		{
			name: "model only",
			inv:  Invocation{Agent: "go-engineer", SessionID: "sess-002", Model: "opus"},
			want: []string{"claude", "--agent", "go-engineer", "--session-id", "sess-002", "--model", "opus"},
		},
		{
			name: "effort only",
			inv:  Invocation{Agent: "go-engineer", SessionID: "sess-003", Effort: "high"},
			want: []string{"claude", "--agent", "go-engineer", "--session-id", "sess-003", "--effort", "high"},
		},
		{
			name: "everything, settings path in a HOME with spaces",
			inv: Invocation{
				Agent: "python-engineer", SessionID: "sess-004", Model: "claude-sonnet-4-20250514", Effort: "max",
				SettingsPath: "/Users/Jane Doe/proj/.agentctl/settings/python-engineer.json",
			},
			want: []string{
				"claude", "--agent", "python-engineer", "--session-id", "sess-004",
				"--model", "claude-sonnet-4-20250514", "--effort", "max",
				"--settings", "/Users/Jane Doe/proj/.agentctl/settings/python-engineer.json",
			},
		},
		{
			name: "resume with fork (sessions browser)",
			inv: Invocation{
				Agent: "go-engineer", Resume: "abc", ForkSession: true, Model: "opus", Effort: "high",
				SettingsPath: "/Users/Jane Doe/s.json",
			},
			want: []string{
				"claude", "--agent", "go-engineer", "--resume", "abc", "--fork-session",
				"--model", "opus", "--effort", "high", "--settings", "/Users/Jane Doe/s.json",
			},
		},
		{
			name: "permission mode, system prompt file and message come last",
			inv: Invocation{
				Agent: "a", SessionID: "s", PermissionMode: "auto", SettingsPath: "/s.json",
				SystemPromptFile: "/p/sys.txt", Message: "say 'hi'",
			},
			want: []string{
				"claude", "--agent", "a", "--session-id", "s", "--permission-mode", "auto",
				"--settings", "/s.json", "--append-system-prompt-file", "/p/sys.txt", "say 'hi'",
			},
		},
		{
			name: "session name with spaces is one argument",
			inv:  Invocation{Agent: "go-engineer", Name: "my project; x"},
			want: []string{"claude", "--agent", "go-engineer", "-n", "my project; x"},
		},
		{
			name: "pi-only fields are ignored",
			inv:  Invocation{Agent: "a", ExtensionPath: "/x.ts"},
			want: []string{"claude", "--agent", "a"},
		},
		{
			name: "hostile agent name",
			inv:  Invocation{Agent: "x; touch /tmp/pwned", SessionID: "s"},
			want: []string{"claude", "--agent", "x; touch /tmp/pwned", "--session-id", "s"},
		},
		{
			name: "hostile model and effort",
			inv:  Invocation{Agent: "a", SessionID: "s", Model: "$(touch /tmp/pwned)", Effort: "`id`"},
			want: []string{"claude", "--agent", "a", "--session-id", "s", "--model", "$(touch /tmp/pwned)", "--effort", "`id`"},
		},
		{
			name: "quotes, newlines, and variables",
			inv: Invocation{
				Agent: `it's "quoted"`, SessionID: "line1\nline2", Resume: "r;m",
				SettingsPath: "$HOME/${USER}/s.json",
			},
			want: []string{
				"claude", "--agent", `it's "quoted"`, "--session-id", "line1\nline2",
				"--resume", "r;m", "--settings", "$HOME/${USER}/s.json",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := For(Claude).Command(tc.inv)
			if err != nil {
				t.Fatal(err)
			}
			if argv := fields(t, got); !slices.Equal(argv, tc.want) {
				t.Errorf("Command() = %s\n tokenizes to %q\n want         %q", got, argv, tc.want)
			}
		})
	}
}

// TestClaudeCommandLiteralFlagsUnquoted keeps the command readable in logs
// and process listings: only interpolated values are quoted.
func TestClaudeCommandLiteralFlagsUnquoted(t *testing.T) {
	got, _ := For(Claude).Command(Invocation{Agent: "go-engineer", SessionID: "sid", Model: "opus"})
	if !strings.HasPrefix(got, "claude --agent 'go-engineer' --session-id 'sid' --model 'opus'") {
		t.Errorf("Command() = %q", got)
	}
}

func TestClaudeLaunchEnv(t *testing.T) {
	const script = "/home/user/.agentctl/shell-prefix.sh"
	tests := []struct{ mode, scrub string }{
		{"auto", "0"}, {"", "1"}, {"default", "1"}, {"acceptEdits", "1"}, {"plan", "1"},
	}
	for _, tt := range tests {
		t.Run("mode="+tt.mode, func(t *testing.T) {
			env := For(Claude).LaunchEnv(EnvParams{PermissionMode: tt.mode, ShellPrefixScript: script})
			if env["CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"] != tt.scrub {
				t.Errorf("SCRUB = %q, want %q", env["CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"], tt.scrub)
			}
			// The shell prefix is exactly the script path: no "source ", no
			// trailing " &&" (either would corrupt argv[0] when Claude Code
			// execs it).
			if env["CLAUDE_CODE_SHELL_PREFIX"] != script {
				t.Errorf("CLAUDE_CODE_SHELL_PREFIX = %q, want %q", env["CLAUDE_CODE_SHELL_PREFIX"], script)
			}
			for _, k := range []string{"DISABLE_AUTOUPDATER", "CLAUDE_CODE_DISABLE_TERMINAL_TITLE", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"} {
				if env[k] != "1" {
					t.Errorf("%s = %q, want 1", k, env[k])
				}
			}
		})
	}
}

func TestClaudeEncodeDecision(t *testing.T) {
	h := For(Claude)
	tests := []struct {
		name string
		d    Decision
		want string
	}{
		{"deny", Decision{Kind: Deny, HookEvent: "PreToolUse", Reason: "no <way>"},
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"no \u003cway\u003e"}}` + "\n"}, // HTML-escaped, as json.Encoder writes it
		{"allow", Decision{Kind: Allow, HookEvent: "PreToolUse", Reason: "ok"},
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":"ok"}}` + "\n"},
		{"context", Decision{Kind: Context, HookEvent: "PostToolUse", Text: "mail"},
			`{"hookSpecificOutput":{"additionalContext":"mail","hookEventName":"PostToolUse"}}` + "\n"},
		{"session context", Decision{Kind: SessionContext, Text: "hi"}, `{"additionalContext":"hi"}` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(h.EncodeDecision(tt.d)); got != tt.want {
				t.Errorf("EncodeDecision = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestClaudeDecodeHookPassesThrough(t *testing.T) {
	raw := []byte(`{"session_id":"s","tool_name":"Bash","tool_input":{"command":"ls"},"extra":1}`)
	ev, err := For(Claude).DecodeHook("hook-pre-tool", raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(ev.Raw) != string(raw) || ev.SessionID != "s" || ev.ToolName != "Bash" {
		t.Errorf("DecodeHook = %+v (raw %s)", ev, ev.Raw)
	}
}

func TestClaudeSessionLogPath(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/cfg")
	h := For(Claude)
	if got := h.SessionLogPath("sid", "/w", "/acct/projects/x/sid.jsonl"); got != "/acct/projects/x/sid.jsonl" {
		t.Errorf("named transcript = %q", got)
	}
	if got := h.SessionLogPath("sid", "/w/p", "/acct/other.jsonl"); got != "/cfg/projects/-w-p/sid.jsonl" {
		t.Errorf("derived = %q", got)
	}
	if got := h.SessionLogPath("sid", "", ""); got != "" {
		t.Errorf("unknown = %q", got)
	}
}
