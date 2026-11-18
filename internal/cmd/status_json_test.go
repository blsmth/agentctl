package cmd

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
)

// TestStatusJSONGolden pins the `status --json` field names and order. The
// golden was captured before the Row→Window conversion moved into fleet; new
// fields must stay additive (omitempty) so this file does not change.
func TestStatusJSONGolden(t *testing.T) {
	g := fleet.Group{Session: "agentctl-proj", Project: "proj"}
	rows := []statusRow{
		{
			group: g,
			row: fleet.Row{
				Session: "agentctl-proj", Project: "proj",
				AgentState: state.AgentState{
					Name: "go-engineer~2", WindowID: "@5", WindowIndex: 2, State: "working",
					ContextPct: 41, WorktreePath: "/w/t", LastToolDetail: "Editing x.go", MailboxPending: 1,
				},
			},
			meta: fleet.Meta{Mode: "dispatch", Backend: "ollama", Ticket: "#9", Branch: "agent/x/issue-9",
				SpawnedBy: "@1", SpawnedBySession: "uuid"},
			verdict: fleet.PaneBusy, checked: true,
		},
		{
			group: g,
			row: fleet.Row{
				Session: "agentctl-proj", Project: "proj",
				AgentState: state.AgentState{Name: "lead", WindowID: "@1", State: "idle", Done: true},
			},
			meta: fleet.Meta{PRNumber: 61, PRURL: "https://github.com/o/r/pull/61"},
		},
	}
	out := make([]statusJSONRow, 0, len(rows))
	for _, sr := range rows {
		out = append(out, sr.jsonRow())
	}
	got, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/status_json.golden")
	if err != nil {
		t.Fatal(err)
	}
	if string(got)+"\n" != string(want) {
		t.Errorf("status --json changed.\ngot:\n%s\nwant:\n%s", got, want)
	}
}
