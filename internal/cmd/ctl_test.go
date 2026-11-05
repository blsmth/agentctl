package cmd

import (
	"slices"
	"testing"

	"mvdan.cc/sh/v3/shell"
)

func TestCtlPopupCmdRoundTrips(t *testing.T) {
	tests := []struct {
		name      string
		exe       string
		extraArgs []string
	}{
		{"no extra args", "/usr/local/bin/agentctl", nil},
		{"binary path with spaces", "/Users/Jane Doe/bin/agentctl", []string{"--reason", "done"}},
		{"semicolon", "agentctl", []string{"x; touch /tmp/pwned"}},
		{"command substitution", "agentctl", []string{"$(touch /tmp/pwned)", "`id`"}},
		{"quotes", "agentctl", []string{`it's "quoted"`}},
		{"newline", "agentctl", []string{"line1\nline2"}},
		{"empty arg", "agentctl", []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := ctlPopupCmd(tt.exe, "restart-menu", tt.extraArgs)
			got, err := shell.Fields(cmd, func(string) string { return "EXPANDED" })
			if err != nil {
				t.Fatalf("shell.Fields(%q): %v", cmd, err)
			}
			want := append([]string{tt.exe, "restart-menu"}, tt.extraArgs...)
			if !slices.Equal(got, want) {
				t.Errorf("ctlPopupCmd = %s\n tokenizes to %q\n want         %q", cmd, got, want)
			}
		})
	}
}
