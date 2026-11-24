package panestate

import "testing"

const trustWorktree = "/Users/brendansmith/code/personal/weather/.worktrees/agent-typescript-engineer-adhoc-03de1d67"

// trustNumbered is the older layout: numbered options, the question at the
// start of its line, inside a box.
const trustNumbered = "" +
	"╭──────────────────────────────────────────╮\n" +
	"│ Accessing workspace:                     │\n" +
	"│                                          │\n" +
	"│ /tmp/repo                                │\n" +
	"│                                          │\n" +
	"│ Is this a project you created or one you │\n" +
	"│ trust? (Like your own code...)           │\n" +
	"│                                          │\n" +
	"│ ❯ 1. Yes, I trust this folder            │\n" +
	"│   2. No, exit                            │\n" +
	"╰──────────────────────────────────────────╯\n" +
	"   Enter to confirm · Esc to cancel\n"

func TestIsTrustDialog(t *testing.T) {
	tests := []struct {
		name   string
		pane   string
		want   bool
		folder string
	}{
		{name: "live capture 166 cols", pane: readCapture(t, "trust-166.txt"), want: true, folder: trustWorktree},
		{name: "live capture 80 cols, path wrapped", pane: readCapture(t, "trust-80.txt"), want: true, folder: trustWorktree},
		{name: "older numbered layout", pane: trustNumbered, want: true, folder: "/tmp/repo"},
		{name: "permission dialog", pane: dialogBoxBash},
		{name: "idle pane", pane: "⏺ Done.\n\n────\n❯ \n────\n"},
		{name: "prose quoting the dialog", pane: "⏺ The dialog says \"Yes, I trust this folder\" under Accessing workspace: /x\n────\n❯ \n────\n"},
		{name: "option row without heading or question", pane: "❯ Yes, I trust this folder\n  No, exit\n"},
		{name: "heading without option row", pane: "Accessing workspace:\n\n/tmp/x\n────\n❯ \n────\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTrustDialog(tt.pane); got != tt.want {
				t.Errorf("IsTrustDialog = %v, want %v", got, tt.want)
			}
			if got := TrustFolder(tt.pane); got != tt.folder {
				t.Errorf("TrustFolder = %q, want %q", got, tt.folder)
			}
			if tt.want {
				if v := Classify(tt.pane); v != AwaitingInput {
					t.Errorf("Classify = %v, want awaiting-input", v)
				}
			}
		})
	}
}

// The trust dialog counts as a dialog even without its footer (a short pane
// can cut it off).
func TestTrustDialogWithoutFooter(t *testing.T) {
	pane := "Accessing workspace:\n\n/tmp/x\n\n❯ No, exit\n  Yes, I trust this folder\n"
	if !DialogShowing(pane) {
		t.Fatal("DialogShowing = false")
	}
}
