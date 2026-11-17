package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

type treeFixtureRow struct {
	WindowID   string `json:"window_id"`
	Parent     string `json:"parent"`
	Dispatched bool   `json:"dispatched"`
	State      string `json:"state"`
	Index      int    `json:"index"`
}

type treeFixtureWant struct {
	ID     string `json:"id"`
	Depth  int    `json:"depth"`
	Prefix string `json:"prefix"`
	Label  string `json:"label"`
}

// TestTreeRows drives treeRows from testdata/tree_cases.json, which the web
// app's logic.js treeRows is checked against too. The hub hands treeRows rows
// already in attention order, so the test sorts the same way the JS port does.
func TestTreeRows(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "tree_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []struct {
			Name string            `json:"name"`
			Rows []treeFixtureRow  `json:"rows"`
			Want []treeFixtureWant `json:"want"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	rank := map[string]int{"blocked": 0, "working": 1, "done": 2, "idle": 3}
	for _, tt := range fx.Cases {
		t.Run(tt.Name, func(t *testing.T) {
			in := append([]treeFixtureRow(nil), tt.Rows...)
			sort.SliceStable(in, func(i, j int) bool {
				if rank[in[i].State] != rank[in[j].State] {
					return rank[in[i].State] < rank[in[j].State]
				}
				return in[i].Index < in[j].Index
			})
			var rows []herd.Row
			for _, r := range in {
				row := tRow(r.WindowID, r.Parent, r.Dispatched)
				row.State = r.State
				rows = append(rows, row)
			}
			var got, want []treeFixtureWant
			for _, tr := range treeRows(rows) {
				if tr.Label != "" {
					got = append(got, treeFixtureWant{Label: tr.Label})
				} else {
					got = append(got, treeFixtureWant{ID: tr.Row.WindowID, Depth: tr.Depth, Prefix: tr.Prefix})
				}
			}
			want = tt.Want
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v, want %+v", got, want)
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
	herd.TagMeta(rows, herd.LoadMeta(herd.Group{RuntimeDir: dir}))

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
