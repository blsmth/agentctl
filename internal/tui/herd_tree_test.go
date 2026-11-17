package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func tRow(win, parent string, dispatched bool) herd.Row {
	r := testRow("agent"+win, "s1", "idle", "")
	r.WindowID = win
	r.Parent = parent
	r.Dispatched = dispatched
	return r
}

func TestTreeRows(t *testing.T) {
	tests := []struct {
		name string
		rows []herd.Row
		want []string // "<prefix>@id" or "S:label"
	}{
		{"no dispatched", []herd.Row{tRow("@1", "", false), tRow("@2", "", false)},
			[]string{"@1", "@2"}},
		{"two children", []herd.Row{tRow("@1", "", false), tRow("@2", "@1", true), tRow("@3", "@1", true)},
			[]string{"@1", "├─ @2", "└─ @3"}},
		{"grandchild continuation", []herd.Row{
			tRow("@1", "", false), tRow("@2", "@1", true), tRow("@3", "@2", true), tRow("@4", "@1", true), tRow("@5", "", false)},
			[]string{"@1", "├─ @2", "│  └─ @3", "└─ @4", "@5"}},
		{"orphan bucket", []herd.Row{tRow("@1", "", false), tRow("@2", "@99", true)},
			[]string{"@1", "S:dispatched (1)", "└─ @2"}},
		{"untrusted parent", []herd.Row{tRow("@1", "", false), tRow("@2", "", true), tRow("@3", "", true)},
			[]string{"@1", "S:dispatched (2)", "├─ @2", "└─ @3"}},
		{"orphan with child", []herd.Row{tRow("@2", "@99", true), tRow("@3", "@2", true)},
			[]string{"S:dispatched (2)", "└─ @2", "   └─ @3"}},
		{"cycle", []herd.Row{tRow("@1", "@2", true), tRow("@2", "@1", true)},
			[]string{"S:dispatched (2)", "└─ @1", "   └─ @2"}},
		{"self parent", []herd.Row{tRow("@1", "@1", true), tRow("@2", "", false)},
			[]string{"@2", "S:dispatched (1)", "└─ @1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, tr := range treeRows(tt.rows) {
				if tr.Label != "" {
					got = append(got, "S:"+tr.Label)
				} else {
					got = append(got, tr.Prefix+tr.Row.WindowID)
				}
			}
			if !equalStrings(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderTreePrefixes(t *testing.T) {
	m := subModel(t, tRow("@1", "", false), tRow("@2", "@1", true), tRow("@3", "@2", true))
	var out []string
	for i := range m.lines {
		out = append(out, m.renderLine(i, 80))
	}
	joined := strings.Join(out, "\n")
	for _, want := range []string{"└─ ", "agent@2", "agent@3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("render missing %q:\n%s", want, joined)
		}
	}
}

// TestTreeRowsFromBuilder: rows built from windowstate by the herd builder
// (no state.json) nest under their trusted dispatcher in the hub tree.
func TestTreeRowsFromBuilder(t *testing.T) {
	dir := t.TempDir()
	winDir := filepath.Join(dir, "windows")
	if err := os.MkdirAll(winDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(id, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(winDir, id+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("@1", `{"state":"working","instance":"lead"}`)
	write("@2", `{"state":"blocked","instance":"w1","mode":"dispatch","spawned_by":"@1","spawned_by_session":"s"}`)
	write("@3", `{"state":"idle","instance":"w2","mode":"dispatch","spawned_by":"@2","spawned_by_session":"s"}`)
	wins := []tmux.WindowInfo{{ID: "@1", Name: "lead"}, {ID: "@2", Name: "w1"}, {ID: "@3", Name: "w2"}}
	rows := herd.TagAgents("agentctl-x", "x", herd.BuildAgents(wins, windowstate.AllFrom(winDir), nil, time.Now()))
	herd.TagDispatched(rows, herd.LoadMeta(herd.Group{RuntimeDir: dir}))

	var got []string
	for _, tr := range treeRows(rows) {
		got = append(got, tr.Prefix+tr.Row.WindowID)
	}
	want := []string{"@1", "└─ @2", "   └─ @3"}
	if !equalStrings(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	g := herd.Group{Session: "agentctl-x", Project: "x", Rows: rows}
	if b := herd.FirstBlocked([]herd.Group{g}); b == nil || b.WindowID != "@2" {
		t.Errorf("FirstBlocked = %v, want @2", b)
	}
}
