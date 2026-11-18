package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestDispatchPreviewReviewPR(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		prompt     string
		wantTarget string
	}{
		{"default reviewer", "", "", "code-reviewer"},
		{"explicit agent with extra instructions", "my-reviewer", "focus on tmux", "my-reviewer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewDispatchPreview("brandon", tt.target, 0, tt.prompt, "", "agentctl dispatch --review-pr 42", WithReviewPR(42))
			if got := m.summaryLine(); got != "#42" {
				t.Errorf("summaryLine() = %q, want #42", got)
			}
			rows := ansi.Strip(strings.Join(m.metadataLines(), "\n"))
			if !strings.Contains(rows, "Review PR") || !strings.Contains(rows, "#42") || !strings.Contains(rows, tt.wantTarget) {
				t.Errorf("metadata rows = %q", rows)
			}
			if len(m.metadataLines()) != 3 {
				t.Errorf("got %d metadata rows, want 3 (PopupSize budget)", len(m.metadataLines()))
			}
		})
	}
}
