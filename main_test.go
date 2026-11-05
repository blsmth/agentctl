package main

import "testing"

func TestIsStatuslineIngestCmd(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"bare statusline", []string{"agentctl", "statusline"}, true},
		{"statusline --dry-run", []string{"agentctl", "statusline", "--dry-run"}, true},
		{"statusline install", []string{"agentctl", "statusline", "install"}, false},
		{"statusline install --force", []string{"agentctl", "statusline", "install", "--force"}, false},
		{"statusline --dry-run extra arg", []string{"agentctl", "statusline", "--dry-run", "extra"}, false},
		{"statusline unknown flag", []string{"agentctl", "statusline", "--bogus"}, false},
		{"unrelated command", []string{"agentctl", "status"}, false},
		{"hook command", []string{"agentctl", "hook-notify"}, false},
		{"no args", []string{"agentctl"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStatuslineIngestCmd(tt.args); got != tt.want {
				t.Errorf("isStatuslineIngestCmd(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestIsHookCmd(t *testing.T) {
	tests := []struct {
		subcmd string
		want   bool
	}{
		{"hook-notify", true},
		{"hook-session-start", true},
		{"statusline", false},
		{"status", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.subcmd, func(t *testing.T) {
			if got := isHookCmd(tt.subcmd); got != tt.want {
				t.Errorf("isHookCmd(%q) = %v, want %v", tt.subcmd, got, tt.want)
			}
		})
	}
}
