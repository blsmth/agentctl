package panestate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readCapture loads a real Claude Code pane capture (ANSI included, v2.1.311,
// 80 columns) from testdata.
func readCapture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAskUserCapturesParse(t *testing.T) {
	tests := []struct {
		file   string
		multi  bool
		submit *SubmitRow
		cursor int
	}{
		{file: "askuser-q1.txt"},
		{file: "askuser-single.txt"},
		{file: "askuser-review.txt"},
		{file: "askuser-q2.txt", multi: true, submit: &SubmitRow{Label: "Submit", Moves: 4}},
		{file: "askuser-q2-live.txt", multi: true, submit: &SubmitRow{Label: "Submit", Moves: 4}},
		{file: "askuser-multi-single.txt", multi: true, submit: &SubmitRow{Label: "Submit", Moves: 4}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			pane := readCapture(t, tt.file)
			if v := Classify(StripANSI(pane)); v != AwaitingInput {
				t.Errorf("Classify = %v, want awaiting-input", v)
			}
			d, ok := ParseDialog(pane)
			if !ok || d.Options[0].Key != "1" {
				t.Fatalf("ok=%v options=%+v", ok, d.Options)
			}
			if d.Multi != tt.multi {
				t.Errorf("multi = %v, want %v", d.Multi, tt.multi)
			}
			if tt.multi && (d.Submit == nil || *d.Submit != *tt.submit || d.Cursor != tt.cursor) {
				t.Errorf("submit = %+v cursor %d, want %+v cursor %d", d.Submit, d.Cursor, tt.submit, tt.cursor)
			}
		})
	}
}

// Real captures of one multi-select tab before and after pressing "2": only
// the box ("[✔]", colored) and the tab mark ("☒ Size") change. The server's
// toggle check relies on the question key holding while the hash moves.
func TestQuestionKeyIgnoresTicks(t *testing.T) {
	a, okA := ParseDialog(readCapture(t, "askuser-q2-live.txt"))
	b, okB := ParseDialog(readCapture(t, "askuser-q2-live-ticked.txt"))
	if !okA || !okB {
		t.Fatal("captures do not parse")
	}
	if Checked(a.Options[1].Label) || !Checked(b.Options[1].Label) {
		t.Errorf("option 2: %q then %q", a.Options[1].Label, b.Options[1].Label)
	}
	if a.Hash == b.Hash {
		t.Error("ticking a box must change the hash")
	}
	if QuestionKey(a) != QuestionKey(b) {
		t.Errorf("question key changed on tick:\n%s\n---\n%s", a.Text, b.Text)
	}
	q1, _ := ParseDialog(readCapture(t, "askuser-q1.txt"))
	if QuestionKey(q1) == QuestionKey(a) {
		t.Error("different tabs share a question key")
	}
}

// Moving the cursor changes Cursor (and Submit.Moves) but not the hash.
func TestCursorTracksArrowMoves(t *testing.T) {
	pane := StripANSI(readCapture(t, "askuser-q2-live.txt"))
	moved := strings.Replace(strings.Replace(pane, "❯ 1. [ ] Small", "  1. [ ] Small", 1), "  2. [ ] Medium", "❯ 2. [ ] Medium", 1)
	a, _ := ParseDialog(pane)
	b, ok := ParseDialog(moved)
	if !ok || b.Cursor != 1 || b.Submit == nil || b.Submit.Moves != 3 {
		t.Fatalf("moved: cursor %d submit %+v", b.Cursor, b.Submit)
	}
	if a.Hash != b.Hash || QuestionKey(a) != QuestionKey(b) {
		t.Error("a cursor move changed the dialog identity")
	}
}

// Regression: the review page matched on two phrases anywhere in the pane,
// so an idle pane quoting them classified as awaiting input and its numbered
// prose became answer buttons on the phone.
func TestReviewPhrasesInScrollbackStayIdle(t *testing.T) {
	panes := map[string]string{
		"prose": "⏺ The \"Review your answers\" page asks \"Ready to submit your answers?\" with 1. Submit answers / 2. Cancel.\n\n" +
			"  1. Fix the parser\n  2. Add tests\n\n✻ Worked for 3s\n\n────\n❯ \n────\n  ⏵⏵ auto mode on\n",
		"question line, other options": "Review your answers\nReady to submit your answers?\n\n  1. Fix the parser\n────\n❯ \n────\n",
		"prose new layout": "⏺ The page headed\nReview your answers\nlists choices: 1. Submit answers and 2. Cancel, then I carry on.\n\n" +
			"────\n❯ \n────\n",
		"scrollback new layout": "Review your answers\n\n❯ 1. Submit answers\n  2. Cancel\n\n⏺ Submitted.\n\nMore output\nMore output\nMore output\n────\n❯ \n────\n",
		"quoted line":           "  > Ready to submit your answers?\n  > 1. Submit answers\n────\n❯ \n────\n",
	}
	for name, pane := range panes {
		if v := Classify(pane); v != Idle {
			t.Errorf("%s: Classify = %v, want idle", name, v)
		}
	}
}
