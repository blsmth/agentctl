package improve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
)

// writeSettingsJSON writes a Claude settings JSON file with the given permissions.
func writeSettingsJSON(t *testing.T, dir string, allow, deny []string) string {
	t.Helper()
	type perms struct {
		Allow []string `json:"allow,omitempty"`
		Deny  []string `json:"deny,omitempty"`
	}
	type settingsFile struct {
		Permissions perms `json:"permissions"`
	}
	data, err := json.Marshal(settingsFile{Permissions: perms{Allow: allow, Deny: deny}})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(dir, data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return dir
}

func TestAgentEnvOverrides(t *testing.T) {
	tests := []struct {
		name       string
		ac         config.AgentConfig
		wantKeys   map[string]string
		absentKeys []string
	}{
		{
			name:       "empty config returns empty map",
			ac:         config.AgentConfig{},
			wantKeys:   map[string]string{},
			absentKeys: []string{"ANTHROPIC_MODEL", "CLAUDE_CODE_EFFORT_LEVEL", "MAX_THINKING_TOKENS", "CLAUDE_CODE_SHELL_PREFIX"},
		},
		{
			name: "model is NOT set as env var (passed via --model flag instead)",
			ac:   config.AgentConfig{Model: "claude-opus-4-5"},
			// ANTHROPIC_MODEL must be absent — model is passed via --model CLI flag
			absentKeys: []string{"ANTHROPIC_MODEL", "CLAUDE_CODE_EFFORT_LEVEL", "MAX_THINKING_TOKENS"},
		},
		{
			name: "effort level is NOT set as env var (passed via --effort flag instead)",
			ac:   config.AgentConfig{EffortLevel: "high"},
			// CLAUDE_CODE_EFFORT_LEVEL must be absent — effort is passed via --effort CLI flag
			absentKeys: []string{"ANTHROPIC_MODEL", "CLAUDE_CODE_EFFORT_LEVEL"},
		},
		{
			name:       "max thinking tokens maps to MAX_THINKING_TOKENS",
			ac:         config.AgentConfig{MaxThinkingTokens: 8000},
			wantKeys:   map[string]string{"MAX_THINKING_TOKENS": fmt.Sprintf("%d", 8000)},
			absentKeys: []string{"ANTHROPIC_MODEL"},
		},
		{
			name: "model and effort absent; max thinking tokens present",
			ac: config.AgentConfig{
				Model:             "claude-sonnet-4-5",
				EffortLevel:       "medium",
				MaxThinkingTokens: 4096,
			},
			wantKeys: map[string]string{
				"MAX_THINKING_TOKENS": "4096",
			},
			// model and effort_level go via CLI flags, not env vars
			absentKeys: []string{"ANTHROPIC_MODEL", "CLAUDE_CODE_EFFORT_LEVEL"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dispatch.AgentEnvOverrides(tt.ac)
			for k, v := range tt.wantKeys {
				if got[k] != v {
					t.Errorf("env[%q] = %q, want %q", k, got[k], v)
				}
			}
			for _, k := range tt.absentKeys {
				if _, ok := got[k]; ok {
					t.Errorf("env[%q] should be absent but got %q", k, got[k])
				}
			}
		})
	}
}

// TestAgentEnvOverridesImproveMode verifies that the improve session env construction
// includes AGENTCTL_MODE=improve and that model/effort are NOT in env vars (they are
// passed as CLI flags --model and --effort instead).
func TestAgentEnvOverridesImproveMode(t *testing.T) {
	ac := config.AgentConfig{
		Model:       "claude-opus-4-5",
		EffortLevel: "high",
	}
	env := dispatch.AgentEnvOverrides(ac)
	env["AGENTCTL_MODE"] = "improve"

	if env["AGENTCTL_MODE"] != "improve" {
		t.Errorf("AGENTCTL_MODE = %q, want %q", env["AGENTCTL_MODE"], "improve")
	}
	// model and effort_level are passed via --model/--effort CLI flags, not env vars.
	if v, ok := env["ANTHROPIC_MODEL"]; ok {
		t.Errorf("ANTHROPIC_MODEL = %q should not be set as env var; use --model flag", v)
	}
	if v, ok := env["CLAUDE_CODE_EFFORT_LEVEL"]; ok {
		t.Errorf("CLAUDE_CODE_EFFORT_LEVEL = %q should not be set as env var; use --effort flag", v)
	}
}

// TestBuildImprovePrompt_NoStaleArchitectureClaims is a regression test: the
// improve prompt used to tell the agent (falsely) that a background monitor
// watches JSONL session logs to detect state, and that "plugins" react to
// transitions. State is hook-driven (windowstate files), and there is no
// plugin registry -- transition handlers are called directly. Cover both the
// themed and default focus-block branches since they compose the same
// template.
func TestBuildImprovePrompt_NoStaleArchitectureClaims(t *testing.T) {
	staleStrings := []string{
		"JSONL session logs",
		"watches session logs",
		"plugins react",
	}

	for _, theme := range []string{"", "improve error handling"} {
		got := buildImprovePrompt("/tmp/agent.md", "go-engineer", "CURRENT CONFIGURATION:\n", theme, "Session abc123\n")
		for _, stale := range staleStrings {
			if strings.Contains(got, stale) {
				t.Errorf("buildImprovePrompt(theme=%q) contains stale claim %q, got:\n%s", theme, stale, got)
			}
		}
	}
}

func TestBuildConfigContext(t *testing.T) {
	tests := []struct {
		name         string
		agentName    string
		ac           config.AgentConfig
		setupWorkDir func(t *testing.T) string // returns workDir
		wantContains []string
		wantAbsent   []string
	}{
		{
			name:      "full config with all fields set and settings files",
			agentName: "go-engineer",
			ac: config.AgentConfig{
				Model:              "sonnet",
				EffortLevel:        "high",
				AutoImprove:        true,
				CrossContext:       true,
				AutoAccept:         true,
				BlockedCommands:    []string{"rm -rf", "sudo"},
				BlockedPatterns:    []string{`\bsecret\b`},
				AllowedPermissions: []string{"WebFetch(*)", "Bash(git*)"},
				AlertConfig:        config.AlertConfig{Blocked: true, Idle: false, Gone: true},
			},
			setupWorkDir: func(t *testing.T) string {
				dir := t.TempDir()
				claudeDir := filepath.Join(dir, ".claude")
				os.MkdirAll(claudeDir, 0o755)
				writeSettingsJSON(t, filepath.Join(claudeDir, "settings.local.json"),
					[]string{"Bash(git*)", "WebFetch(*)"}, []string{"Bash(rm*)"})
				return dir
			},
			wantContains: []string{
				"CURRENT CONFIGURATION:",
				"agentctl config (go-engineer):",
				"model:           sonnet",
				"effort_level:    high",
				"auto_improve:    true",
				"cross_context:   true",
				"auto_accept:     true",
				"blocked_commands: rm -rf, sudo",
				`blocked_patterns:`,
				"allowed_permissions:",
				"WebFetch(*)",
				"Bash(git*)",
				"alerts:          blocked=true  idle=false  gone=true",
				"project permissions",
				"settings.local.json",
				"Bash(git*)",
				"Bash(rm*)",
			},
			wantAbsent: []string{
				"audit_commands",
			},
		},
		{
			name:      "empty/default config with no settings files on disk",
			agentName: "my-agent",
			ac:        config.DefaultAgentConfig(),
			setupWorkDir: func(t *testing.T) string {
				return t.TempDir() // no .claude/ subdir
			},
			wantContains: []string{
				"CURRENT CONFIGURATION:",
				"agentctl config (my-agent):",
				"model:           (default)",
				"effort_level:    (default)",
				"auto_improve:    false",
				"cross_context:   true",
				"auto_accept:     false",
				"blocked_commands: (none)",
				"blocked_patterns: (none)",
				"allowed_permissions: (none)",
			},
			wantAbsent: []string{
				// We can only guarantee project permissions are absent — global
				// ~/.claude/settings.json may exist on the host and is out of our control.
				"project permissions",
			},
		},
		{
			name:      "missing settings files - agentctl block present, permissions omitted",
			agentName: "go-engineer",
			ac: config.AgentConfig{
				Model:        "opus",
				AutoImprove:  false,
				CrossContext: false,
				AlertConfig:  config.AlertConfig{Blocked: true, Idle: true, Gone: false},
			},
			setupWorkDir: func(t *testing.T) string {
				return t.TempDir() // directory exists but has no .claude subdir
			},
			wantContains: []string{
				"CURRENT CONFIGURATION:",
				"agentctl config (go-engineer):",
				"model:           opus",
				"blocked_commands: (none)",
			},
			wantAbsent: []string{
				"project permissions",
			},
		},
		{
			name:      "zero-value fields show as default/none",
			agentName: "bare-agent",
			ac: config.AgentConfig{
				// Model, EffortLevel, BlockedCommands all zero
				AlertConfig: config.AlertConfig{Blocked: false, Idle: false, Gone: false},
			},
			setupWorkDir: func(t *testing.T) string {
				return t.TempDir()
			},
			wantContains: []string{
				"model:           (default)",
				"effort_level:    (default)",
				"blocked_commands: (none)",
				"blocked_patterns: (none)",
				"allowed_permissions: (none)",
			},
		},
		{
			name:      "permissions formatting with multiple entries",
			agentName: "go-engineer",
			ac:        config.DefaultAgentConfig(),
			setupWorkDir: func(t *testing.T) string {
				dir := t.TempDir()
				claudeDir := filepath.Join(dir, ".claude")
				os.MkdirAll(claudeDir, 0o755)
				writeSettingsJSON(t, filepath.Join(claudeDir, "settings.local.json"),
					[]string{"Bash(git*)", "WebFetch(*)", "Read(*)"}, nil)
				return dir
			},
			wantContains: []string{
				"project permissions",
				"    allow:",
				"      - Bash(git*)",
				"      - WebFetch(*)",
				"      - Read(*)",
			},
		},
		{
			name:      "empty workDir does not panic",
			agentName: "go-engineer",
			ac:        config.DefaultAgentConfig(),
			setupWorkDir: func(t *testing.T) string {
				return "" // empty workDir
			},
			wantContains: []string{
				"CURRENT CONFIGURATION:",
				"agentctl config (go-engineer):",
			},
			wantAbsent: []string{
				"project permissions",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := tt.setupWorkDir(t)
			got := buildConfigContext(tt.agentName, tt.ac, workDir)

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("expected output to contain %q\ngot:\n%s", want, got)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("expected output NOT to contain %q\ngot:\n%s", absent, got)
				}
			}
		})
	}
}
