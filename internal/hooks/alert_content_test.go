package hooks

import (
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestApprovalMessage(t *testing.T) {
	tests := []struct {
		name string
		n    popupNotice
		want string
	}{
		{"dispatch on a ticket", popupNotice{Action: "dispatch go-engineer on #3", Detail: "go-engineer on #3"}, "Dispatch go-engineer on #3"},
		{"dispatch with a prompt", popupNotice{Action: "dispatch go-engineer", Detail: "go-engineer: Fix the hub viewer jump"}, "Dispatch go-engineer: Fix the hub viewer jump"},
		{"cron add names the job", popupNotice{Action: "add cron job nightly", Detail: "nightly (go-engineer): 0 3 * * *"}, "Add cron job nightly: nightly (go-engineer): 0 3 * * *"},
		{"bash command is single-lined", popupNotice{Action: "run a command", Detail: "git commit -m \"a\n\nb\""}, "Run a command: git commit -m \"a b\""},
		{"no detail", popupNotice{Action: "run a command"}, "Run a command"},
		{"no action", popupNotice{Detail: "ls"}, "ls"},
		{"long bash command is cut at a word", popupNotice{Action: "run a command", Detail: strings.Repeat("word ", 40)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := approvalMessage(tt.n)
			if tt.want == "" {
				if n := len([]rune(got)); n > maxAlertMessageRunes || !strings.HasSuffix(got, "…") || strings.Contains(got, "\n") {
					t.Errorf("approvalMessage = %q (%d runes)", got, n)
				}
				return
			}
			if got != tt.want {
				t.Errorf("approvalMessage = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestStopFailureAndCompactAlertsLeadWithTitle covers the two hooks that read
// the window's short title themselves.
func TestStopFailureAndCompactAlertsLeadWithTitle(t *testing.T) {
	tests := []struct {
		name         string
		write        windowstate.WindowState
		run          func()
		stdin        string
		wantTitle    string
		wantSubtitle string
		wantMessage  string
	}{
		{"stop_failure with a title", windowstate.WindowState{ShortTitle: windowstate.ShortTitle{Title: "Fix hub viewer jump"}}, StopFailure,
			`{"error_type":"rate_limit"}`, "Fix hub viewer jump", "Turn failed · go-engineer", "Rate limited"},
		{"stop_failure fresh window", windowstate.WindowState{}, StopFailure,
			`{"error_type":"server_error"}`, "go-engineer", "Turn failed", "Server error"},
		{"post_compact with a title", windowstate.WindowState{ShortTitle: windowstate.ShortTitle{Title: "Fix hub viewer jump", Manual: true}}, PostCompact,
			`{}`, "Fix hub viewer jump", "Context compacted · go-engineer", "Context compacted, continuing"},
		{"post_compact fresh window", windowstate.WindowState{}, PostCompact,
			`{}`, "go-engineer", "Context compacted", "Context compacted, continuing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupWindowstate(t)
			resetPathsForTest(t)
			var got []notify.Options
			d := stubDeps(t)
			d.Notify = func(o notify.Options) { got = append(got, o) }
			SetDepsForTest(d)
			t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
			t.Setenv("AGENTCTL", "1")
			t.Setenv("AGENTCTL_AGENT_NAME", "go-engineer")
			t.Setenv("AGENTCTL_WINDOW_ID", "@23")
			t.Setenv("TMUX_PANE", "")
			t.Setenv("AGENTCTL_SESSION_ID", "")
			if err := windowstate.Write("@23", tt.write); err != nil {
				t.Fatal(err)
			}
			runHookCaptured(t, nil, tt.stdin, tt.run)
			if len(got) != 1 {
				t.Fatalf("alerts = %d, want 1", len(got))
			}
			if got[0].Title != tt.wantTitle || got[0].Subtitle != tt.wantSubtitle || got[0].Message != tt.wantMessage {
				t.Errorf("alert = %q / %q / %q, want %q / %q / %q", got[0].Title, got[0].Subtitle, got[0].Message, tt.wantTitle, tt.wantSubtitle, tt.wantMessage)
			}
		})
	}
}
