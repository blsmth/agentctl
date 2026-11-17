package tmux

import (
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestPickMainPane(t *testing.T) {
	tests := []struct {
		name, out, chrome, hud, want string
	}{
		{"main first", "%1\n%2\n", "%2", "", "%1"},
		{"chrome first", "%2\n%1\n", "%2", "", "%1"},
		{"chrome and hud", "%3\n%2\n%1\n", "%2", "%3", "%1"},
		{"only chrome", "%2\n", "%2", "", ""},
		{"empty", "", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickMainPane(tt.out, tt.chrome, tt.hud); got != tt.want {
				t.Errorf("pickMainPane = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsPaneID(t *testing.T) {
	for s, want := range map[string]bool{
		"%1": true, "%1234": true,
		"": false, "%": false, "1": false, "@1": false, "%1;ls": false, "%-1": false, "s:%1": false,
	} {
		if got := IsPaneID(s); got != want {
			t.Errorf("IsPaneID(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestPaneTargetsRejectNonPaneIDs(t *testing.T) {
	if err := SendKeyToPane("sess:@1", "1"); err == nil {
		t.Error("SendKeyToPane accepted a non-pane target")
	}
	if _, err := CapturePaneByIDANSI("-t", 10); err == nil {
		t.Error("CapturePaneByIDANSI accepted a non-pane target")
	}
	if err := InjectPromptToPane("", "hi"); err == nil {
		t.Error("InjectPromptToPane accepted an empty pane")
	}
}

// Regression: a recycled window ID kept the previous agent's PR, so the new
// agent's done push announced an old PR.
func TestApplyLaunchBootstrapClearsPreviousPR(t *testing.T) {
	ws := windowstate.WindowState{PRNumber: 41, PRURL: "https://github.com/o/r/pull/41", Seen: true}
	applyLaunchBootstrap(&ws, "go-engineer:2", "go-engineer", "/l.jsonl", map[string]string{"AGENTCTL_MODE": "adhoc"})
	if ws.PRNumber != 0 || ws.PRURL != "" {
		t.Errorf("PR kept: %d %q", ws.PRNumber, ws.PRURL)
	}
	if ws.Instance != "go-engineer:2" || ws.Mode != "adhoc" || ws.Seen {
		t.Errorf("bootstrap fields = %+v", ws)
	}
}
