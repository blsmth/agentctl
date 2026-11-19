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
