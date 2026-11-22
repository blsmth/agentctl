package panestate

import (
	"os"
	"path/filepath"
	"testing"
)

// idleQuotingDialog is the pane that refused a phone reply: an idle window
// whose last answer quotes the dialog question and footer in prose. Every
// marker sits mid-line, and the pane ends in an empty input box.
const idleQuotingDialog = "" +
	"⏺ Two things can block a dispatch:\n" +
	"  - Two prompts at once: the agentctl popup and the Claude Code \"Do you want to proceed?\" dialog.\n" +
	"  - The footer reads `Esc to cancel · Tab to amend`, so press Esc to cancel it.\n" +
	"  - A quoted \"❯ 1. Yes\" option above \"2. No\" in an answer is prose.\n" +
	"  > Do you want to proceed? was the question it asked.\n" +
	"\n" +
	"✻ Worked for 1m 12s · done 7:02 PM\n" +
	"──────────────────────────────────────────────────────────── go-engineer ──\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"  opus · ctx 41% · 5h 12%\n" +
	"  ⏵⏵ auto mode on (shift+tab to cycle)"

// dialogOptionsOnly is a bordered numbered-choice dialog with no question
// marker and no footer: only the option rows identify it.
const dialogOptionsOnly = "" +
	"╭──────────────────────────────────────╮\n" +
	"│ Allow this fetch?                    │\n" +
	"│ ❯ 1. Yes                             │\n" +
	"│   2. No, and tell Claude what to do  │\n" +
	"╰──────────────────────────────────────╯\n"

// dialogFooterOnly is a dialog recognised by its footer alone.
const dialogFooterOnly = "" +
	" Pick a model\n" +
	" ❯ 1. Opus\n" +
	"   2. Sonnet\n" +
	"\n" +
	"\x1b[2m Enter to select · ↑/↓ to navigate · Esc to cancel\x1b[0m\n"

func TestDialogShowing(t *testing.T) {
	tests := []struct {
		name string
		pane string
		want bool
	}{
		{"plain dialog", dialog, true},
		{"numbered fallback", dialogOther, true},
		{"boxed bash dialog", dialogBoxBash, true},
		{"plain edit dialog", dialogPlainEdit, true},
		{"ansi dialog", dialogANSI, true},
		{"long boxed bash dialog", longBashDialog("rm -rf /tmp/scratch"), true},
		{"bordered options only", dialogOptionsOnly, true},
		{"footer only", dialogFooterOnly, true},
		{"askuser multi", askUserMulti, true},
		{"askuser single", askUserSingle, true},
		{"askuser single no desc", askUserSingleNoDesc, true},
		{"askuser multi next", askUserMultiNext, true},
		{"askuser review page", askUserReview, true},
		{"boxed create question", "│ Do you want to create notes.md?   │\n", true},
		{"idle pane quoting dialog", idleQuotingDialog, false},
		{"question mid-line", "the \"Do you want to proceed?\" dialog\n❯ \n", false},
		{"footer hint in prose", "press Esc to cancel the dialog\n❯ \n", false},
		{"options without cursor", "1. Yes\n2. No\n❯ \n", false},
		{"yes without no", "❯ 1. Yes please\n", false},
		{"idle", idleReal, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DialogShowing(tt.pane); got != tt.want {
				t.Errorf("DialogShowing = %v, want %v", got, tt.want)
			}
		})
	}
}

// Regression: dialog markers quoted in an idle pane's scrollback made
// Classify report AwaitingInput, and the phone refused to reply.
func TestClassifyIdleQuotingDialog(t *testing.T) {
	if got := Classify(idleQuotingDialog); got != Idle {
		t.Errorf("Classify = %v, want %v", got, Idle)
	}
}

// Every captured dialog in testdata must keep classifying as a dialog: a miss
// means injected text answers it.
func TestDialogShowingCaptures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "askuser-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	more, err := filepath.Glob(filepath.Join("..", "serve", "servetest", "panes", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, more...)
	if len(paths) == 0 {
		t.Fatal("no captures found")
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := Classify(string(b)); got != AwaitingInput {
				t.Errorf("Classify = %v, want %v", got, AwaitingInput)
			}
		})
	}
}
