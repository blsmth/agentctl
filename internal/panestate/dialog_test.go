package panestate

import (
	"reflect"
	"strings"
	"testing"
)

const (
	dialogBoxBash = "" +
		"● Bash(go test ./...)\n" +
		"╭──────────────────────────────────────────────────────────────────╮\n" +
		"│ Bash command                                                     │\n" +
		"│                                                                  │\n" +
		"│   go test ./...                                                  │\n" +
		"│   Run the test suite                                             │\n" +
		"│                                                                  │\n" +
		"│ Do you want to proceed?                                          │\n" +
		"│ ❯ 1. Yes                                                         │\n" +
		"│   2. Yes, and don't ask again for go test commands in /tmp/proj  │\n" +
		"│   3. No, and tell Claude what to do differently (esc)            │\n" +
		"╰──────────────────────────────────────────────────────────────────╯\n"

	dialogPlainEdit = "" +
		"──────────────────────────────────────────────────────────────────────\n" +
		" Edit file\n" +
		" internal/foo/foo.go\n" +
		"\n" +
		" Do you want to make this edit to foo.go?\n" +
		" ❯ 1. Yes\n" +
		"   2. Yes, allow all edits during this session (shift+tab)\n" +
		"   3. No, and tell Claude what to do differently (esc)\n" +
		"\n" +
		" Esc to cancel · Tab to amend\n"

	dialogANSI = "\x1b[1m Do you want to proceed?\x1b[0m\n" +
		" \x1b[36m❯ 1. Yes\x1b[0m\n" +
		"   2. No\n" +
		" Esc to cancel · Tab to amend · ctrl+e to explain\n"
)

func TestParseDialog(t *testing.T) {
	tests := []struct {
		name     string
		pane     string
		ok       bool
		options  []Option
		contains []string
		excludes []string
	}{
		{
			name:     "boxed bash dialog",
			pane:     dialogBoxBash,
			ok:       true,
			options:  []Option{{"1", "Yes"}, {"2", "Yes, and don't ask again for go test commands in /tmp/proj"}, {"3", "No, and tell Claude what to do differently (esc)"}},
			contains: []string{"Bash command", "go test ./...", "Do you want to proceed?", "1. Yes"},
			excludes: []string{"│", "╭", "❯", "Bash(go test"},
		},
		{
			name:     "plain edit dialog",
			pane:     dialogPlainEdit,
			ok:       true,
			options:  []Option{{"1", "Yes"}, {"2", "Yes, allow all edits during this session (shift+tab)"}, {"3", "No, and tell Claude what to do differently (esc)"}},
			contains: []string{"Edit file", "internal/foo/foo.go", "make this edit"},
			excludes: []string{"Esc to cancel", "───"},
		},
		{
			name:     "ansi escapes",
			pane:     dialogANSI,
			ok:       true,
			options:  []Option{{"1", "Yes"}, {"2", "No"}},
			excludes: []string{"\x1b"},
		},
		{name: "idle pane", pane: "──── lead ──\n❯\n──────\n", ok: false},
		{name: "empty", pane: "", ok: false},
		{name: "numbered list not a dialog", pane: "Plan:\n2. second\n3. third\n", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := ParseDialog(tt.pane)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tt.ok, d)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(d.Options, tt.options) {
				t.Errorf("options = %+v, want %+v", d.Options, tt.options)
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
			if d.Hash != HashDialogText(d.Text) || len(d.Hash) != 64 {
				t.Errorf("hash = %q", d.Hash)
			}
		})
	}
}

func TestParseDialogHashStability(t *testing.T) {
	a, _ := ParseDialog(dialogANSI)
	// Cursor moved to option 2 and styling differs: same dialog, same hash.
	moved := strings.NewReplacer("❯ 1. Yes", "  1. Yes", "   2. No", " ❯ 2. No").Replace(StripANSI(dialogANSI))
	b, _ := ParseDialog(moved)
	if a.Hash != b.Hash {
		t.Errorf("hash changed with cursor/styling: %s vs %s", a.Hash, b.Hash)
	}
	c, _ := ParseDialog(dialogBoxBash)
	d, _ := ParseDialog(strings.Replace(dialogBoxBash, "go test ./...  ", "rm -rf /tmp/x   ", 1))
	if c.Hash == d.Hash {
		t.Error("different commands must hash differently")
	}
}
