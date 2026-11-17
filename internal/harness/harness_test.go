package harness

import (
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	tests := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{"", Claude, false},
		{"claude", Claude, false},
		{"pi", Pi, false},
		{"PI", Pi, false},
		{"codex", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := Lookup(tt.name)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "valid: claude, pi") {
					t.Fatalf("Lookup(%q) err = %v, want unknown-harness error listing valid names", tt.name, err)
				}
				return
			}
			if err != nil || h.Name() != tt.want {
				t.Fatalf("Lookup(%q) = %v, %v; want %s", tt.name, h, err, tt.want)
			}
		})
	}
}

func TestForFallsBackToDefault(t *testing.T) {
	if got := For("gone").Name(); got != Default {
		t.Errorf("For(unknown) = %s, want %s", got, Default)
	}
	if got := For("pi").Name(); got != Pi {
		t.Errorf("For(pi) = %s", got)
	}
}

func TestResolvePrecedence(t *testing.T) {
	tests := []struct {
		name                           string
		override, configured, frontmat string
		want                           string
		wantErr                        bool
	}{
		{"default", "", "", "", Claude, false},
		{"frontmatter", "", "", "pi", Pi, false},
		{"config beats frontmatter", "", "claude", "pi", Claude, false},
		{"override beats config", "pi", "claude", "", Pi, false},
		{"unknown override", "nope", "", "", "", true},
		{"unknown config is not masked by frontmatter", "", "nope", "pi", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := Resolve(tt.override, tt.configured, tt.frontmat)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Resolve = %s, want error", h.Name())
				}
				return
			}
			if err != nil || h.Name() != tt.want {
				t.Fatalf("Resolve = %v, %v; want %s", h, err, tt.want)
			}
		})
	}
}

func TestCanonical(t *testing.T) {
	for in, want := range map[string]string{"": "", "claude": "", "Claude": "", "pi": "pi"} {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCapsHonest pins the capability declarations callers degrade on.
func TestCapsHonest(t *testing.T) {
	c := For(Claude).Caps()
	if c.RegistersBackends {
		t.Error("claude must keep agentctl's env/settings backend routing")
	}
	if !c.NativeMessaging || !c.BlockedSignal || !c.Statusline || !c.Accounts || !c.AgentFlag || !c.EnvFile {
		t.Errorf("claude caps = %+v", c)
	}
	p := For(Pi).Caps()
	if p.NativeMessaging || p.BlockedSignal || p.Statusline || p.Accounts || p.AgentFlag || p.PermissionModes || p.EnvFile || p.PaneClassifier {
		t.Errorf("pi declares a capability it lacks: %+v", p)
	}
	if !p.PreassignSessionID || !p.BlockingPreTool || !p.ContextInjection || !p.ResumeByID || !p.Backends || !p.RegistersBackends {
		t.Errorf("pi caps missing bridge-provided capability: %+v", p)
	}
}
