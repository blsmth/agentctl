package main

import (
	"testing"

	"github.com/alecthomas/kong"

	"github.com/blsmth/agentctl/internal/audit"
)

// TestAuditCmd_LimitDefault pins two properties of `agentctl audit --limit`:
// no --limit flag at all falls back to the documented default of 100 records,
// and an explicit `--limit 0` means "unlimited" rather than "show nothing" --
// see AuditCmd.Limit's help text and audit.Filter.Limit's doc comment.
func TestAuditCmd_LimitDefault(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no flag uses default 100", nil, 100},
		{"explicit limit overrides default", []string{"--limit", "5"}, 5},
		{"--limit 0 means unlimited", []string{"--limit", "0"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cmd AuditCmd
			parser, err := kong.New(&cmd)
			if err != nil {
				t.Fatalf("kong.New: %v", err)
			}
			if _, err := parser.Parse(tt.args); err != nil {
				t.Fatalf("Parse(%v): %v", tt.args, err)
			}
			if cmd.Limit != tt.want {
				t.Errorf("Limit = %d, want %d", cmd.Limit, tt.want)
			}
		})
	}
}

// TestAuditCmd_DeniedAndEventFlags pins that --denied and --event each reach
// audit.Filter.DeniedOnly / audit.Filter.Event (the fields existed but
// the flags were never wired the flags -- this is the regression guard for that gap), and
// that both compose with the pre-existing --failed, --tool, and --agent
// flags rather than clobbering them.
func TestAuditCmd_DeniedAndEventFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want audit.Filter
	}{
		{
			name: "no flags impose no denied/event filter",
			args: nil,
			want: audit.Filter{Limit: 100},
		},
		{
			name: "--denied sets DeniedOnly",
			args: []string{"--denied"},
			want: audit.Filter{DeniedOnly: true, Limit: 100},
		},
		{
			name: "--event sets Event",
			args: []string{"--event", "tool_denied"},
			want: audit.Filter{Event: "tool_denied", Limit: 100},
		},
		{
			name: "--denied composes with --failed, --tool, --agent",
			args: []string{"--denied", "--failed", "--tool", "Bash", "--agent", "go-engineer"},
			want: audit.Filter{
				DeniedOnly: true,
				FailedOnly: true,
				Tool:       "Bash",
				Agent:      "go-engineer",
				Limit:      100,
			},
		},
		{
			name: "--event composes with --failed, --tool, --agent",
			args: []string{"--event", "tool_failure", "--failed", "--tool", "Bash", "--agent", "go-engineer"},
			want: audit.Filter{
				Event:      "tool_failure",
				FailedOnly: true,
				Tool:       "Bash",
				Agent:      "go-engineer",
				Limit:      100,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cmd AuditCmd
			parser, err := kong.New(&cmd)
			if err != nil {
				t.Fatalf("kong.New: %v", err)
			}
			if _, err := parser.Parse(tt.args); err != nil {
				t.Fatalf("Parse(%v): %v", tt.args, err)
			}
			got, err := cmd.buildFilter()
			if err != nil {
				t.Fatalf("buildFilter: %v", err)
			}
			if got != tt.want {
				t.Errorf("buildFilter() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
