package panestate

import (
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
		{"multi-select", askUserMulti, []string{"1", "2", "3", "4", "5", "6"}, true,
			[]string{"☐ Next", "Which threads", "1. [ ] Merge the ready four"}},
		{"single-select with descriptions", askUserSingle, []string{"1", "2", "3", "4"}, false,
			[]string{"☐ Scope", "Which approach", "Chat about this"}},
		{"single-select without descriptions", askUserSingleNoDesc, []string{"1", "2", "3", "4"}, false,
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

// askUserReview is the "Review your answers" page that ends a multi-question
// AskUserQuestion flow, captured live (Claude Code 2.1.328).
const askUserReview = "" +
	"❯ Use the AskUserQuestion tool right now with two questions.\n" +
	rule + "\n" +
	"←  ☒ Pick  ☒ Mode  ✔ Submit  →\n" +
	"\n" +
	"Review your answers\n" +
	"\n" +
	" ● Pick\n" +
	"   → B\n" +
	" ● Mode\n" +
	"   → X\n" +
	"\n" +
	"Ready to submit your answers?\n" +
	"\n" +
	"❯ 1. Submit answers\n" +
	"  2. Cancel\n"

func TestParseDialogReviewPage(t *testing.T) {
	d, ok := ParseDialog(askUserReview)
	if !ok {
		t.Fatal("not parsed")
	}
	if d.Multi {
		t.Errorf("multi=%v", d.Multi)
	}
	if !reflect.DeepEqual(keys(d), []string{"1", "2"}) || d.Options[0].Label != "Submit answers" {
		t.Errorf("options = %+v", d.Options)
	}
	for _, s := range []string{"Review your answers", "→ B", "→ X"} {
		if !strings.Contains(d.Text, s) {
			t.Errorf("text missing %q:\n%s", s, d.Text)
		}
	}
}

// askUserMultiNext is a live multi-select tab of a two-question flow with B
// ticked ("[✔]") and the cursor moved down onto the Next row.
const askUserMultiNext = "" +
	rule + "\n" +
	"←  ☒ Pick  ☐ Mode  ✔ Submit  →\n" +
	"Pick\n" +
	"  1. [ ] A\n" +
	"         Option A\n" +
	"  2. [✔] B\n" +
	"         Option B\n" +
	"  3. [ ] C\n" +
	"         Option C\n" +
	"  4. [ ] Type something\n" +
	"❯    Next\n" +
	rule + "\n" +
	"  5. Chat about this\n" +
	"Enter to select · Tab/Arrow keys to navigate · Esc to cancel\n"

func TestParseDialogMultiSubmitRow(t *testing.T) {
	chat := strings.Replace(strings.Replace(askUserMultiNext, "❯    Next", "     Next", 1), "  5. Chat", "❯ 5. Chat", 1)
	noCursor := strings.Replace(askUserMultiNext, "❯    Next", "     Next", 1)
	tests := []struct {
		name  string
		pane  string
		want  *SubmitRow
		ticks []bool
	}{
		{"cursor on first option", askUserMulti, &SubmitRow{Label: "Submit", Moves: 5}, nil},
		{"cursor on Next", askUserMultiNext, &SubmitRow{Label: "Next", Moves: 0}, []bool{false, true, false, false}},
		{"cursor on Chat about this", chat, &SubmitRow{Label: "Next", Moves: -1}, nil},
		{"no cursor", noCursor, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := ParseDialog(tt.pane)
			if !ok || !d.Multi {
				t.Fatalf("ok=%v multi=%v", ok, d.Multi)
			}
			if !reflect.DeepEqual(d.Submit, tt.want) {
				t.Errorf("Submit = %+v, want %+v", d.Submit, tt.want)
			}
			for i, want := range tt.ticks {
				if got := Checked(d.Options[i].Label); got != want {
					t.Errorf("Checked(%q) = %v, want %v", d.Options[i].Label, got, want)
				}
			}
		})
	}
}

// Toggling a box changes the hash; moving the cursor does not.
func TestMultiHashTracksTicksNotCursor(t *testing.T) {
	a, _ := ParseDialog(askUserMultiNext)
	moved, _ := ParseDialog(strings.Replace(strings.Replace(askUserMultiNext, "❯    Next", "     Next", 1), "  1. [ ] A", "❯ 1. [ ] A", 1))
	toggled, _ := ParseDialog(strings.Replace(askUserMultiNext, "[✔] B", "[ ] B", 1))
	if a.Hash != moved.Hash || a.Hash == toggled.Hash {
		t.Errorf("hashes: base=%s moved=%s toggled=%s", a.Hash, moved.Hash, toggled.Hash)
	}
}

func TestDialogShowingReviewPage(t *testing.T) {
	if Classify(askUserReview) != AwaitingInput {
		t.Error("review page should classify as awaiting input")
	}
	if Classify("Review your answers\n") == AwaitingInput {
		t.Error("heading alone is not a dialog")
	}
}

func TestParseDialogSubmitRowEdgeCases(t *testing.T) {
	describedLast := rule + "\n" +
		"Pick\n" +
		"❯ 1. [ ] A\n" +
		"         Option A\n" +
		"  2. [ ] C\n" +
		"         Option C\n" +
		"     Next\n" +
		rule + "\n" +
		"  3. Chat about this\n"
	d, ok := ParseDialog(describedLast)
	if !ok || !d.Multi || d.Submit == nil || d.Submit.Label != "Next" || d.Submit.Moves != 2 {
		t.Errorf("described last option: ok=%v submit=%+v", ok, d.Submit)
	}
	noRow := strings.Replace(describedLast, "     Next\n", "", 1)
	if d, _ := ParseDialog(noRow); d.Submit != nil {
		t.Errorf("no confirm row: submit = %+v", d.Submit)
	}
	onFree := strings.Replace(strings.Replace(askUserMultiNext, "❯    Next", "     Next", 1), "  4. [ ] Type something", "❯ 4. [ ] Type something", 1)
	if d, _ := ParseDialog(onFree); d.Submit != nil {
		t.Errorf("cursor on free text: submit = %+v", d.Submit)
	}
}
