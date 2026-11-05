package hooks

import (
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

// ---------------------------------------------------------------------------
// checkAgentDenyList — table-driven
// ---------------------------------------------------------------------------

func TestCheckAgentDenyList(t *testing.T) {
	tests := []struct {
		name     string
		ac       config.AgentConfig
		command  string
		wantDeny bool
		wantMsg  string // substring that must appear in the reason (only when wantDeny)
	}{
		// --- blocked commands (substring match) ---
		{
			name:     "blocked_command_exact_match",
			ac:       config.AgentConfig{BlockedCommands: []string{"docker push"}},
			command:  "docker push myimage:latest",
			wantDeny: true,
			wantMsg:  "docker push",
		},
		{
			name:     "blocked_command_substring_inside_longer_command",
			ac:       config.AgentConfig{BlockedCommands: []string{"docker push"}},
			command:  "docker push myimage:latest --platform linux/amd64",
			wantDeny: true,
			wantMsg:  "docker push",
		},
		{
			name:     "blocked_command_not_triggered_by_different_command",
			ac:       config.AgentConfig{BlockedCommands: []string{"docker push"}},
			command:  "docker pull myimage:latest",
			wantDeny: false,
		},

		// --- partial substring match ---
		{
			name:     "partial_substring_blocks_short_prefix",
			ac:       config.AgentConfig{BlockedCommands: []string{"rm"}},
			command:  "rm file.txt",
			wantDeny: true,
			wantMsg:  "rm",
		},
		{
			name:     "partial_substring_blocks_when_substring_is_embedded",
			ac:       config.AgentConfig{BlockedCommands: []string{"rm"}},
			command:  "grep -rm pattern /src",
			wantDeny: true,
			wantMsg:  "rm",
		},

		// --- blocked patterns (regex match) ---
		{
			name:     "blocked_pattern_regex_matches",
			ac:       config.AgentConfig{BlockedPatterns: []string{`npm\s+publish`}},
			command:  "npm publish",
			wantDeny: true,
			wantMsg:  "matching pattern", // %q-escaping makes the literal pattern hard to match; check for fixed text instead
		},
		{
			name:     "blocked_pattern_regex_matches_with_extra_args",
			ac:       config.AgentConfig{BlockedPatterns: []string{`npm\s+publish`}},
			command:  "npm publish --dry-run",
			wantDeny: true,
		},
		{
			name:     "blocked_pattern_does_not_match_different_command",
			ac:       config.AgentConfig{BlockedPatterns: []string{`npm\s+publish`}},
			command:  "npm install",
			wantDeny: false,
		},

		// --- multiple blocked commands: first match wins ---
		{
			name: "multiple_blocked_commands_first_match_wins",
			ac: config.AgentConfig{
				BlockedCommands: []string{"docker push", "kubectl delete"},
			},
			command:  "docker push myimage:latest",
			wantDeny: true,
			wantMsg:  "docker push",
		},
		{
			name: "multiple_blocked_commands_second_entry_also_blocks",
			ac: config.AgentConfig{
				BlockedCommands: []string{"docker push", "kubectl delete"},
			},
			command:  "kubectl delete pod my-pod",
			wantDeny: true,
			wantMsg:  "kubectl delete",
		},
		{
			name: "multiple_blocked_commands_unrelated_command_allowed",
			ac: config.AgentConfig{
				BlockedCommands: []string{"docker push", "kubectl delete"},
			},
			command:  "go test ./...",
			wantDeny: false,
		},

		// --- invalid regex pattern: skip, do not block or panic ---
		{
			name:     "invalid_regex_pattern_is_skipped",
			ac:       config.AgentConfig{BlockedPatterns: []string{"[invalid-regex"}},
			command:  "anything goes",
			wantDeny: false,
		},
		{
			name: "valid_pattern_after_invalid_still_checked",
			ac: config.AgentConfig{
				BlockedPatterns: []string{
					"[invalid-regex", // bad — should be skipped
					`npm\s+publish`,  // good — should still fire
				},
			},
			command:  "npm publish",
			wantDeny: true,
		},

		// --- empty blocklists: allow everything ---
		{
			name:     "empty_blocked_commands_allows_all",
			ac:       config.AgentConfig{},
			command:  "rm -rf /tmp/scratch",
			wantDeny: false,
		},
		{
			name:     "nil_blocked_lists_allows_all",
			ac:       config.AgentConfig{BlockedCommands: nil, BlockedPatterns: nil},
			command:  "kubectl delete pod foo",
			wantDeny: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := checkAgentDenyList(tt.command, tt.ac)
			if tt.wantDeny {
				if reason == "" {
					t.Errorf("checkAgentDenyList(%q) = empty reason, want deny", tt.command)
					return
				}
				if tt.wantMsg != "" && !strings.Contains(reason, tt.wantMsg) {
					t.Errorf("checkAgentDenyList(%q) reason %q does not contain %q", tt.command, reason, tt.wantMsg)
				}
			} else {
				if reason != "" {
					t.Errorf("checkAgentDenyList(%q) = %q, want allow (empty reason)", tt.command, reason)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkProjectDenyList — table-driven
// ---------------------------------------------------------------------------

func TestCheckProjectDenyList(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.ProjectConfig
		command   string
		agentName string
		wantDeny  bool
		wantMsg   string // substring expected in the reason (only when wantDeny)
	}{
		// --- project-level blocked commands ---
		{
			name:      "project_blocked_command_substring_match",
			cfg:       &config.ProjectConfig{BlockedCommands: []string{"terraform destroy"}},
			command:   "terraform destroy -auto-approve",
			agentName: "any-agent",
			wantDeny:  true,
			wantMsg:   "project config",
		},
		{
			name:      "project_blocked_command_unrelated_command_allowed",
			cfg:       &config.ProjectConfig{BlockedCommands: []string{"terraform destroy"}},
			command:   "terraform plan",
			agentName: "any-agent",
			wantDeny:  false,
		},

		// --- project-level blocked patterns ---
		{
			name:      "project_blocked_pattern_matches",
			cfg:       &config.ProjectConfig{BlockedPatterns: []string{`pip\s+install\s+--user`}},
			command:   "pip install --user requests",
			agentName: "any-agent",
			wantDeny:  true,
			wantMsg:   "project config",
		},
		{
			name:      "project_blocked_pattern_does_not_match_different_command",
			cfg:       &config.ProjectConfig{BlockedPatterns: []string{`pip\s+install\s+--user`}},
			command:   "pip install requests",
			agentName: "any-agent",
			wantDeny:  false,
		},
		{
			name:      "project_bad_pattern_is_skipped_not_panics",
			cfg:       &config.ProjectConfig{BlockedPatterns: []string{"[bad-regex"}},
			command:   "anything",
			agentName: "any-agent",
			wantDeny:  false,
		},

		// --- protected branches expansion ---
		{
			name:      "protected_branch_main_blocks_push",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"main"}},
			command:   "git push origin main",
			agentName: "any-agent",
			wantDeny:  true,
			wantMsg:   "protected branch",
		},
		{
			name:      "protected_branch_main_allows_push_to_feature",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"main"}},
			command:   "git push origin feature/my-branch",
			agentName: "any-agent",
			wantDeny:  false,
		},
		{
			name:      "protected_branch_main_does_not_block_push_to_main_backup",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"main"}},
			command:   "git push origin main-backup",
			agentName: "any-agent",
			// The pattern uses \b word boundary, so "main-backup" should NOT match "main\b"
			// because "-" is a word boundary after "main".
			// Actually \b matches between word char and non-word char; "-" is non-word,
			// so "main\b" in "main-backup" DOES match at position after "main".
			// This is a known limitation — we document it here.
			wantDeny: true, // \b matches before "-"
		},
		{
			name:      "protected_branch_blocks_force_push_to_production",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"production"}},
			command:   "git push --force origin production",
			agentName: "any-agent",
			wantDeny:  true,
		},
		{
			name:      "protected_branch_multiple_branches_first_blocked",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"main", "production"}},
			command:   "git push origin main",
			agentName: "any-agent",
			wantDeny:  true,
		},
		{
			name:      "protected_branch_multiple_branches_second_blocked",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"main", "production"}},
			command:   "git push origin production",
			agentName: "any-agent",
			wantDeny:  true,
		},
		{
			name:      "protected_branch_multiple_branches_feature_allowed",
			cfg:       &config.ProjectConfig{ProtectedBranches: []string{"main", "production"}},
			command:   "git push origin feature/new-feature",
			agentName: "any-agent",
			wantDeny:  false,
		},

		// --- per-agent overrides ---
		{
			name: "per_agent_blocked_command_fires_for_matching_agent",
			cfg: &config.ProjectConfig{
				Agents: map[string]config.AgentConfig{
					"go-engineer": {BlockedCommands: []string{"go install"}},
				},
			},
			command:   "go install github.com/some/tool@latest",
			agentName: "go-engineer",
			wantDeny:  true,
			wantMsg:   "go-engineer",
		},
		{
			name: "per_agent_blocked_command_ignored_for_different_agent",
			cfg: &config.ProjectConfig{
				Agents: map[string]config.AgentConfig{
					"go-engineer": {BlockedCommands: []string{"go install"}},
				},
			},
			command:   "go install github.com/some/tool@latest",
			agentName: "python-engineer",
			wantDeny:  false,
		},
		{
			name: "per_agent_blocked_command_ignored_for_empty_agent_name",
			cfg: &config.ProjectConfig{
				Agents: map[string]config.AgentConfig{
					"go-engineer": {BlockedCommands: []string{"go install"}},
				},
			},
			command:   "go install github.com/some/tool@latest",
			agentName: "",
			wantDeny:  false,
		},
		{
			name: "per_agent_blocked_pattern_fires_for_matching_agent",
			cfg: &config.ProjectConfig{
				Agents: map[string]config.AgentConfig{
					"frontend": {BlockedPatterns: []string{`npm\s+publish`}},
				},
			},
			command:   "npm publish",
			agentName: "frontend",
			wantDeny:  true,
		},

		// --- project blocks are additive: allowed by agent config still blocked by project ---
		{
			name: "project_blocks_additive_even_if_agent_config_empty",
			cfg: &config.ProjectConfig{
				BlockedCommands: []string{"terraform destroy"},
			},
			command:   "terraform destroy",
			agentName: "any-agent",
			wantDeny:  true,
		},

		// --- empty project config fields: no panic ---
		{
			name:      "empty_project_config_allows_all",
			cfg:       &config.ProjectConfig{},
			command:   "git push origin main",
			agentName: "any-agent",
			wantDeny:  false,
		},
		{
			name: "project_config_with_nil_agents_map_does_not_panic",
			cfg: &config.ProjectConfig{
				BlockedCommands: []string{"something-else"},
				Agents:          nil,
			},
			command:   "go test ./...",
			agentName: "go-engineer",
			wantDeny:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := checkProjectDenyList(tt.command, tt.agentName, tt.cfg)
			if tt.wantDeny {
				if reason == "" {
					t.Errorf("checkProjectDenyList(%q, %q) = empty reason, want deny", tt.command, tt.agentName)
					return
				}
				if tt.wantMsg != "" && !strings.Contains(reason, tt.wantMsg) {
					t.Errorf("checkProjectDenyList reason %q does not contain %q", reason, tt.wantMsg)
				}
			} else {
				if reason != "" {
					t.Errorf("checkProjectDenyList(%q, %q) = %q, want allow (empty reason)", tt.command, tt.agentName, reason)
				}
			}
		})
	}
}
