package dispatch

import (
	"slices"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/shell"
	"mvdan.cc/sh/v3/syntax"

	"github.com/blsmth/agentctl/internal/config"
)

// fields tokenizes cmd the way a POSIX shell would, failing the test on a
// parse error. Every $VAR expands to a sentinel so an unquoted expansion
// shows up as a mismatch rather than silently matching an empty env.
func fields(t *testing.T, cmd string) []string {
	t.Helper()
	got, err := shell.Fields(cmd, func(string) string { return "EXPANDED" })
	if err != nil {
		t.Fatalf("shell.Fields(%q): %v", cmd, err)
	}
	return got
}

func TestBuildBaseFlags(t *testing.T) {
	tests := []struct {
		name string
		inv  ClaudeInvocation
		ac   config.AgentConfig
		want []string
	}{
		{
			name: "no model or effort",
			inv:  ClaudeInvocation{Agent: "go-engineer", SessionID: "sess-001"},
			want: []string{"claude", "--agent", "go-engineer", "--session-id", "sess-001"},
		},
		{
			name: "model only",
			inv:  ClaudeInvocation{Agent: "go-engineer", SessionID: "sess-002"},
			ac:   config.AgentConfig{Model: "opus"},
			want: []string{"claude", "--agent", "go-engineer", "--session-id", "sess-002", "--model", "opus"},
		},
		{
			name: "effort only",
			inv:  ClaudeInvocation{Agent: "go-engineer", SessionID: "sess-003"},
			ac:   config.AgentConfig{EffortLevel: "high"},
			want: []string{"claude", "--agent", "go-engineer", "--session-id", "sess-003", "--effort", "high"},
		},
		{
			name: "everything, settings path in a HOME with spaces",
			inv: ClaudeInvocation{
				Agent: "python-engineer", SessionID: "sess-004",
				SettingsPath: "/Users/Jane Doe/proj/.agentctl/settings/python-engineer.json",
			},
			ac: config.AgentConfig{Model: "claude-sonnet-4-20250514", EffortLevel: "max"},
			want: []string{
				"claude", "--agent", "python-engineer", "--session-id", "sess-004",
				"--model", "claude-sonnet-4-20250514", "--effort", "max",
				"--settings", "/Users/Jane Doe/proj/.agentctl/settings/python-engineer.json",
			},
		},
		{
			name: "resume with fork (sessions browser)",
			inv: ClaudeInvocation{
				Agent: "go-engineer", Resume: "abc", ForkSession: true,
				SettingsPath: "/Users/Jane Doe/s.json",
			},
			ac: config.AgentConfig{Model: "opus", EffortLevel: "high"},
			want: []string{
				"claude", "--agent", "go-engineer", "--resume", "abc", "--fork-session",
				"--model", "opus", "--effort", "high", "--settings", "/Users/Jane Doe/s.json",
			},
		},
		{
			name: "hostile agent name",
			inv:  ClaudeInvocation{Agent: "x; touch /tmp/pwned", SessionID: "s"},
			want: []string{"claude", "--agent", "x; touch /tmp/pwned", "--session-id", "s"},
		},
		{
			name: "hostile model and effort",
			inv:  ClaudeInvocation{Agent: "a", SessionID: "s"},
			ac:   config.AgentConfig{Model: "$(touch /tmp/pwned)", EffortLevel: "`id`"},
			want: []string{"claude", "--agent", "a", "--session-id", "s", "--model", "$(touch /tmp/pwned)", "--effort", "`id`"},
		},
		{
			name: "quotes, newlines, and variables",
			inv: ClaudeInvocation{
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
			got := BuildBaseFlags(tc.inv, tc.ac)
			if argv := fields(t, got); !slices.Equal(argv, tc.want) {
				t.Errorf("BuildBaseFlags() = %s\n tokenizes to %q\n want         %q", got, argv, tc.want)
			}
		})
	}
}

// TestBuildBaseFlagsLiteralFlagsUnquoted keeps the command readable in logs
// and process listings: only interpolated values are quoted.
func TestBuildBaseFlagsLiteralFlagsUnquoted(t *testing.T) {
	got := BuildBaseFlags(ClaudeInvocation{Agent: "go-engineer", SessionID: "sid"}, config.AgentConfig{Model: "opus"})
	if !strings.HasPrefix(got, "claude --agent 'go-engineer' --session-id 'sid' --model 'opus'") {
		t.Errorf("BuildBaseFlags() = %q", got)
	}
}

// TestBuildRestoreCmd parses the restore command as a POSIX `a || b` list
// and checks each side's argv: a HOME with spaces used to split the
// --settings path, and --model/--effort used to be dropped on restore.
func TestBuildRestoreCmd(t *testing.T) {
	const settings = "/Users/Jane Doe/.agentctl/settings/go-engineer.json"
	ac := config.AgentConfig{Model: "opus", EffortLevel: "high"}
	fresh := []string{"claude", "--agent", "go-engineer", "--model", "opus", "--effort", "high", "--settings", settings}
	resumed := []string{"claude", "--agent", "go-engineer", "--resume", "sid $(x)", "--model", "opus", "--effort", "high", "--settings", settings}

	tests := []struct {
		name   string
		resume bool
		want   [][]string // one argv per side of ||
	}{
		{"resume falls back to fresh", true, [][]string{resumed, fresh}},
		{"fresh only", false, [][]string{fresh}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := buildRestoreCmd("go-engineer", "sid $(x)", settings, tt.resume, ac)
			f, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(cmd), "")
			if err != nil || len(f.Stmts) != 1 {
				t.Fatalf("parsing %q: %v (stmts=%d)", cmd, err, len(f.Stmts))
			}
			var sides []*syntax.Stmt
			if bin, ok := f.Stmts[0].Cmd.(*syntax.BinaryCmd); ok {
				if bin.Op != syntax.OrStmt {
					t.Fatalf("operator = %v, want ||", bin.Op)
				}
				sides = []*syntax.Stmt{bin.X, bin.Y}
			} else {
				sides = []*syntax.Stmt{f.Stmts[0]}
			}
			if len(sides) != len(tt.want) {
				t.Fatalf("%q has %d commands, want %d", cmd, len(sides), len(tt.want))
			}
			for i, s := range sides {
				var b strings.Builder
				if err := syntax.NewPrinter().Print(&b, s); err != nil {
					t.Fatal(err)
				}
				if got := fields(t, b.String()); !slices.Equal(got, tt.want[i]) {
					t.Errorf("command %d tokenizes to %q, want %q", i, got, tt.want[i])
				}
			}
		})
	}
}
