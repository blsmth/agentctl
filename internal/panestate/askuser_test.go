package panestate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const rule = "────────────────────────────────────────────────────────────────────────────────"

// askUserMulti is a multi-select AskUserQuestion captured from a live pane
// (scrollback above the dialog trimmed).
const askUserMulti = "" +
	"✻ Sautéed for 17m 36s · done\n" +
	"\n" +
	rule + "\n" +
	"←  ☐ Next  ✔ Submit  →\n" +
	"\n" +
	"Which threads do you want to pick up now?\n" +
	"\n" +
	"❯ 1. [ ] Merge the ready four\n" +
	"         Re-check #209, #206, #198 and #210 against the new main, then merge them.\n" +
	"  2. [ ] Fix #186, sync main\n" +
	"         Send the Python engineer to fix #186.\n" +
	"  3. [ ] Review #215 and #220\n" +
	"         Run the same read-only review on the two new PRs.\n" +
	"  4. [ ] Draft review comments\n" +
	"         Write up review comments for you to approve before posting.\n" +
	"  5. [ ] Type something\n" +
	"     Submit\n" +
	rule + "\n" +
	"  6. Chat about this\n" +
	"\n" +
	"Enter to select · ↑/↓ to navigate · Esc to cancel\n" +
	rule + " brandon ─\n"

const askUserSingle = "" +
	rule + "\n" +
	"←  ☐ Scope  ✔ Submit  →\n" +
	"\n" +
	"Which approach should I take?\n" +
	"\n" +
	"❯ 1. Small patch\n" +
	"     Touch only the parser.\n" +
	"  2. Full rewrite\n" +
	"     Replace the module.\n" +
	"  3. Type something.\n" +
	rule + "\n" +
	"  4. Chat about this\n" +
	"\n" +
	"Enter to select · ↑/↓ to navigate · Esc to cancel\n"

const askUserSingleNoDesc = "" +
	rule + "\n" +
	"Which approach should I take?\n" +
	"\n" +
	"❯ 1. Small patch\n" +
	"  2. Full rewrite\n" +
	"  3. Type something.\n" +
	rule + "\n" +
	"  4. Chat about this\n" +
	"\n" +
	"Enter to select · ↑/↓ to navigate · Esc to cancel\n"

func keys(d Dialog) []string {
	var ks []string
	for _, o := range d.Options {
		ks = append(ks, o.Key)
	}
	return ks
}

func TestParseDialogAskUserQuestion(t *testing.T) {
	tests := []struct {
		name     string
		pane     string
		keys     []string
		multi    bool
		contains []string
	}{
		// Free-text entries ("Type something", "Chat about this") are not
		// options: they need typing in the terminal.
		{"multi-select", askUserMulti, []string{"1", "2", "3", "4"}, true,
			[]string{"☐ Next", "Which threads", "1. [ ] Merge the ready four"}},
		{"single-select with descriptions", askUserSingle, []string{"1", "2"}, false,
			[]string{"☐ Scope", "Which approach", "Chat about this"}},
		{"single-select without descriptions", askUserSingleNoDesc, []string{"1", "2"}, false,
			[]string{"Which approach"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := ParseDialog(tt.pane)
			if !ok {
				t.Fatal("not parsed")
			}
			if !reflect.DeepEqual(keys(d), tt.keys) {
				t.Errorf("keys = %v, want %v", keys(d), tt.keys)
			}
			if d.Multi != tt.multi {
				t.Errorf("Multi = %v, want %v", d.Multi, tt.multi)
			}
			for _, s := range tt.contains {
				if !strings.Contains(d.Text, s) {
					t.Errorf("text missing %q:\n%s", s, d.Text)
				}
			}
			if strings.Contains(d.Text, "Sautéed") {
				t.Errorf("text swept in scrollback:\n%s", d.Text)
			}
		})
	}
}

func TestParseDialogPermissionNotMulti(t *testing.T) {
	d, ok := ParseDialog(dialogBoxBash)
	if !ok || d.Multi {
		t.Fatalf("ok=%v multi=%v", ok, d.Multi)
	}
}

// A rule directly above "1." must not pull an older numbered list into the run.
func TestParseDialogStopsAtOptionOne(t *testing.T) {
	pane := "1. old\n2. older\n" + rule + "\n❯ 1. Yes\n  2. No\n"
	d, ok := ParseDialog(pane)
	if !ok || !reflect.DeepEqual(keys(d), []string{"1", "2"}) {
		t.Fatalf("ok=%v keys=%v", ok, keys(d))
	}
}

// readCapture loads a real Claude Code pane capture (ANSI included) from
// testdata.
func readCapture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// AskUserQuestion screens captured from Claude Code v2.1.311 at 80 columns.
// Before the fix the description line under each option broke the option
// run, so these returned ok=false and the phone showed no answers.
func TestParseDialogAskUserQuestionCaptures(t *testing.T) {
	tests := []struct {
		file     string
		multi    bool
		options  []Option
		checked  []string
		contains []string
		excludes []string
	}{
		{
			file:    "askuser-q1.txt",
			options: []Option{{"1", "Red"}, {"2", "Green"}, {"3", "Blue"}},
			contains: []string{
				"☐ Color  ☐ Size  ✔ Submit", "Which color should the button be?",
				"1. Red\nBold, high-urgency accent color\n2. Green", "4. Type something.", "5. Chat about this",
			},
			excludes: []string{"Enter to select", "Call the AskUserQuestion", "───", "❯"},
		},
		{
			file:     "askuser-q2.txt",
			multi:    true,
			options:  []Option{{"1", "Small"}, {"2", "Medium"}, {"3", "Large"}},
			contains: []string{"Which sizes should we ship?", "1. [ ] Small", "Compact variant for dense layouts", "Submit"},
		},
		{
			file:     "askuser-single.txt",
			options:  []Option{{"1", "Cat"}, {"2", "Dog"}},
			contains: []string{"☐ Pet", "Cat or dog?"},
			excludes: []string{"Which shape?"},
		},
		{
			file:    "askuser-multi-single.txt",
			multi:   true,
			options: []Option{{"1", "Apple"}, {"2", "Banana"}, {"3", "Cherry"}},
		},
		{
			file:     "askuser-review.txt",
			options:  []Option{{"1", "Submit answers"}, {"2", "Cancel"}},
			contains: []string{"Review your answers", "→ Small, Large", "Ready to submit your answers?"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			pane := readCapture(t, tt.file)
			if v := Classify(StripANSI(pane)); v != AwaitingInput {
				t.Errorf("Classify = %v, want awaiting-input", v)
			}
			d, ok := ParseDialog(pane)
			if !ok {
				t.Fatal("does not parse")
			}
			if d.Multi != tt.multi {
				t.Errorf("multi = %v, want %v", d.Multi, tt.multi)
			}
			if !reflect.DeepEqual(d.Options, tt.options) {
				t.Errorf("options = %+v, want %+v", d.Options, tt.options)
			}
			if !reflect.DeepEqual(d.Checked, tt.checked) {
				t.Errorf("checked = %v, want %v", d.Checked, tt.checked)
			}
			for _, s := range tt.contains {
				if !strings.Contains(d.Text, s) {
					t.Errorf("text missing %q:\n%s", s, d.Text)
				}
			}
			for _, s := range tt.excludes {
				if strings.Contains(d.Text, s) {
					t.Errorf("text contains %q:\n%s", s, d.Text)
				}
			}
		})
	}
}

func TestParseDialogMultiChecked(t *testing.T) {
	pane := readCapture(t, "askuser-q2.txt")
	ticked := strings.Replace(strings.Replace(pane, "[ ] Medium", "[✔] Medium", 1), "☐ Size", "☒ Size", 1)
	a, _ := ParseDialog(pane)
	b, ok := ParseDialog(ticked)
	if !ok {
		t.Fatal("does not parse")
	}
	if !reflect.DeepEqual(b.Checked, []string{"2"}) || !b.IsChecked("2") || b.IsChecked("1") {
		t.Errorf("checked = %v", b.Checked)
	}
	if a.Hash == b.Hash {
		t.Error("ticking a box must change the hash")
	}
	if QuestionKey(a) != QuestionKey(b) {
		t.Error("ticking a box must not change the question key")
	}
	q1, _ := ParseDialog(readCapture(t, "askuser-q1.txt"))
	if QuestionKey(q1) == QuestionKey(a) {
		t.Error("different tabs share a question key")
	}
}

// The option run continues through indented lines only while it has not
// reached option 1, so text above a dialog is never swept into its options.
func TestParseDialogWrappedOption(t *testing.T) {
	pane := "" +
		" Do you want to proceed?\n" +
		" ❯ 1. Yes\n" +
		"   2. Yes, and don't ask again for go test commands in\n" +
		"      /tmp/proj\n" +
		"   3. No\n"
	d, ok := ParseDialog(pane)
	if !ok {
		t.Fatal("does not parse")
	}
	want := []Option{{"1", "Yes"}, {"2", "Yes, and don't ask again for go test commands in"}, {"3", "No"}}
	if !reflect.DeepEqual(d.Options, want) || d.Multi {
		t.Errorf("dialog = %+v", d)
	}
}

func TestOptionRunStopsAtUnindentedText(t *testing.T) {
	// A continuation-looking line not inside a run must not start one.
	pane := "Plan:\n   detail\n2. second\n3. third\n"
	if _, ok := ParseDialog(pane); ok {
		t.Error("a list without option 1 parsed as a dialog")
	}
}

// Regression: the review-tab phrase anywhere in the pane classified an idle
// pane as awaiting input, and its numbered prose then became answer buttons.
func TestReviewPhraseInScrollbackStaysIdle(t *testing.T) {
	pane := "⏺ The review tab shows \"Ready to submit your answers?\" with Submit/Cancel.\n\n" +
		"  1. Fix the parser\n  2. Add tests\n\n✻ Worked for 3s\n\n" +
		"────\n❯ \n────\n  ⏵⏵ auto mode on\n"
	if v := Classify(pane); v != Idle {
		t.Errorf("Classify = %v, want idle", v)
	}
	// The question on its own line but not followed by Submit answers.
	other := "Ready to submit your answers?\n\n  1. Fix the parser\n────\n❯ \n────\n"
	if v := Classify(other); v != Idle {
		t.Errorf("Classify(other) = %v, want idle", v)
	}
}

// Real captures of the same multi-select tab before and after pressing "2"
// (Claude Code v2.1.311): only the box ("[✔]", colored) and the tab mark
// ("☒ Size") change, so the question key holds while the hash moves. The
// server's toggle check depends on exactly this.
func TestParseDialogMultiCheckedLive(t *testing.T) {
	a, okA := ParseDialog(readCapture(t, "askuser-q2-live.txt"))
	b, okB := ParseDialog(readCapture(t, "askuser-q2-live-ticked.txt"))
	if !okA || !okB {
		t.Fatal("captures do not parse")
	}
	if len(a.Checked) != 0 || !reflect.DeepEqual(b.Checked, []string{"2"}) {
		t.Errorf("checked = %v then %v, want none then [2]", a.Checked, b.Checked)
	}
	if !b.Multi || !reflect.DeepEqual(a.Options, b.Options) {
		t.Errorf("multi %v, options %+v vs %+v", b.Multi, a.Options, b.Options)
	}
	if a.Hash == b.Hash {
		t.Error("ticking a box must change the hash")
	}
	if QuestionKey(a) != QuestionKey(b) {
		t.Errorf("question key changed on tick:\n%s\n---\n%s", a.Text, b.Text)
	}
}
