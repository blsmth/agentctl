package panestate

import (
	"strings"
	"testing"
)

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

func TestParseTrustDialog(t *testing.T) {
	trustExitFirst := []Option{{Key: TrustKeyExit, Label: "No, exit"}, {Key: TrustKeyTrust, Label: "Yes, I trust this folder"}}
	tests := []struct {
		name    string
		pane    string
		ok      bool
		folder  string
		options []Option
		cursor  int
	}{
		{name: "scratch 100 cols, cursor on exit", pane: readCapture(t, "trust-scratch-100.txt"), ok: true, folder: "/private/tmp/trusttest.R15FAO", options: trustExitFirst, cursor: 0},
		{name: "scratch 100 cols after Down, cursor on trust", pane: readCapture(t, "trust-scratch-100-yes.txt"), ok: true, folder: "/private/tmp/trusttest.R15FAO", options: trustExitFirst, cursor: 1},
		{name: "scratch 60 cols", pane: readCapture(t, "trust-scratch-60.txt"), ok: true, folder: "/private/tmp/trusttest.Z78Pxd", options: trustExitFirst, cursor: 0},
		{name: "live 80 cols, path wrapped", pane: readCapture(t, "trust-80.txt"), ok: true, folder: trustWorktree, options: trustExitFirst, cursor: 0},
		{
			name: "older numbered layout", pane: trustNumbered, ok: true, folder: "/tmp/repo", cursor: 0,
			options: []Option{{Key: TrustKeyTrust, Label: "Yes, I trust this folder"}, {Key: TrustKeyExit, Label: "No, exit"}},
		},
		{name: "claude exited on No, dialog left above the shell", pane: readCapture(t, "trust-scratch-60-exited.txt")},
		{name: "permission dialog", pane: dialogBoxBash},
		{name: "idle pane", pane: "⏺ Done.\n\n────\n❯ \n────\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := ParseTrustDialog(tt.pane)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if d.Kind != KindTrust || d.Folder != tt.folder || d.Cursor != tt.cursor {
				t.Errorf("kind %q folder %q cursor %d, want trust %q %d", d.Kind, d.Folder, d.Cursor, tt.folder, tt.cursor)
			}
			if len(d.Options) != len(tt.options) {
				t.Fatalf("options = %+v, want %+v", d.Options, tt.options)
			}
			for i := range d.Options {
				if d.Options[i] != tt.options[i] {
					t.Errorf("option %d = %+v, want %+v", i, d.Options[i], tt.options[i])
				}
			}
			if d.Hash != HashDialogText(d.Text) || !strings.HasPrefix(d.Text, "Accessing workspace:") {
				t.Errorf("text/hash mismatch: %q", d.Text)
			}
			if strings.Contains(d.Text, "❯") || strings.Contains(d.Text, "Enter to confirm") {
				t.Errorf("text keeps the cursor or footer: %q", d.Text)
			}
		})
	}
}

// Moving the cursor must not change the dialog's identity: the server checks
// the hash between arrow presses.
func TestTrustDialogHashIgnoresCursor(t *testing.T) {
	a, _ := ParseTrustDialog(readCapture(t, "trust-scratch-100.txt"))
	b, _ := ParseTrustDialog(readCapture(t, "trust-scratch-100-yes.txt"))
	if a.Hash == "" || a.Hash != b.Hash {
		t.Fatalf("hash changed with the cursor: %q vs %q\n%s\n---\n%s", a.Hash, b.Hash, a.Text, b.Text)
	}
}
