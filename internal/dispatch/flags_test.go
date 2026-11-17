package dispatch

import (
	"slices"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/shell"
	"mvdan.cc/sh/v3/syntax"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/harness"
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

// restoreCmd builds the Claude restore command the way Restore does.
func restoreCmd(t *testing.T, agentName, sessionName, sessionID, settings string, resume bool, ac config.AgentConfig) string {
	t.Helper()
	inv := baseInvocation(agentName, sessionName, ac, harness.Prepared{SettingsPath: settings})
	cmd, err := buildRestoreCmd(harness.For(""), inv, sessionID, resume)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
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
			cmd := restoreCmd(t, "go-engineer", "", "sid $(x)", settings, tt.resume, ac)
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

// TestBuildRestoreCmd_ReusesSessionName verifies both sides of the restore
// fallback carry the original -n name.
func TestBuildRestoreCmd_ReusesSessionName(t *testing.T) {
	cmd := restoreCmd(t, "go-engineer", "agent-go-engineer-issue-42", "sid", "/s.json", true, config.AgentConfig{})
	if got := strings.Count(cmd, "-n 'agent-go-engineer-issue-42'"); got != 2 {
		t.Errorf("want -n on both resume and fresh sides, got %d in %q", got, cmd)
	}
}

// TestBuildRestoreCmd_Pi: pi reopens the saved session by ID (no fallback
// chain), and a duplicate gets a fresh ID.
func TestBuildRestoreCmd_Pi(t *testing.T) {
	inv := baseInvocation("go-engineer", "n", config.AgentConfig{}, harness.Prepared{ExtensionPath: "/x/agentctl-pi.ts"})
	pi := harness.For(harness.Pi)
	cmd, err := buildRestoreCmd(pi, inv, "sid-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"pi", "--session-id", "sid-1", "-n", "n", "-e", "/x/agentctl-pi.ts"}; !slices.Equal(fields(t, cmd), want) {
		t.Errorf("resume = %q, want %q", fields(t, cmd), want)
	}
	dup, err := buildRestoreCmd(pi, inv, "sid-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dup, "sid-1") || !strings.Contains(dup, "--session-id") {
		t.Errorf("duplicate restore = %q, want a fresh --session-id", dup)
	}
}
