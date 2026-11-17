package dispatch

import (
	"slices"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/harness"
)

func TestResolvePermissionMode(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
		ac   config.AgentConfig
		want string // "" means no --permission-mode flag
	}{
		{"dispatch unset defaults to auto", ModeDispatch, config.AgentConfig{}, "auto"},
		{"adhoc unset defaults to auto", ModeAdhoc, config.AgentConfig{}, "auto"},
		{"explicit manual", ModeDispatch, config.AgentConfig{PermissionMode: "manual"}, "manual"},
		{"explicit plan on adhoc", ModeAdhoc, config.AgentConfig{PermissionMode: "plan"}, "plan"},
		{"invalid falls back to auto", ModeDispatch, config.AgentConfig{PermissionMode: "yolo"}, "auto"},
		{"bare unset gets no flag", ModeBare, config.AgentConfig{}, ""},
		{"bare explicit honored", ModeBare, config.AgentConfig{PermissionMode: "acceptEdits"}, "acceptEdits"},
		{"bare invalid gets no flag", ModeBare, config.AgentConfig{PermissionMode: "yolo"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolvePermissionMode(tt.mode, tt.ac, ""); got != tt.want {
				t.Errorf("resolvePermissionMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClaudeCommand_PermissionModeWithModelAndEffort checks the full argv
// dispatch.Launch produces for a worker dispatch: the permission mode sits
// alongside model and effort, and an empty mode emits no flag at all.
func TestClaudeCommand_PermissionModeWithModelAndEffort(t *testing.T) {
	ac := config.AgentConfig{Model: "opus", EffortLevel: "high"}
	tests := []struct {
		name string
		mode Mode
		want []string
	}{
		{"dispatch", ModeDispatch, []string{
			"claude", "--agent", "a", "--session-id", "sid",
			"--model", "opus", "--effort", "high", "--permission-mode", "auto",
		}},
		{"bare", ModeBare, []string{
			"claude", "--agent", "a", "--session-id", "sid", "--model", "opus", "--effort", "high",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := baseInvocation("a", "", ac, harness.Prepared{})
			inv.SessionID, inv.PermissionMode = "sid", resolvePermissionMode(tt.mode, ac, "")
			got, err := harness.For("").Command(inv)
			if err != nil {
				t.Fatal(err)
			}
			if argv := fields(t, got); !slices.Equal(argv, tt.want) {
				t.Errorf("Command() = %s\n tokenizes to %q\n want         %q", got, argv, tt.want)
			}
		})
	}
}

func TestResolvePermissionMode_Override(t *testing.T) {
	tests := []struct {
		name     string
		mode     Mode
		ac       config.AgentConfig
		override string
		want     string
	}{
		{"override beats config", ModeBare, config.AgentConfig{PermissionMode: "plan"}, "auto", "auto"},
		{"override on bare", ModeBare, config.AgentConfig{}, "auto", "auto"},
		{"override beats dispatch default", ModeDispatch, config.AgentConfig{}, "plan", "plan"},
		{"empty override unchanged", ModeBare, config.AgentConfig{}, "", ""},
		{"empty override keeps config", ModeBare, config.AgentConfig{PermissionMode: "plan"}, "", "plan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolvePermissionMode(tt.mode, tt.ac, tt.override); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidatePermissionMode(t *testing.T) {
	for _, ok := range []string{"", "auto", "plan", "acceptEdits"} {
		if err := ValidatePermissionMode(ok); err != nil {
			t.Errorf("ValidatePermissionMode(%q) = %v, want nil", ok, err)
		}
	}
	if err := ValidatePermissionMode("yolo"); err == nil {
		t.Error("ValidatePermissionMode(yolo) = nil, want error")
	}
}
