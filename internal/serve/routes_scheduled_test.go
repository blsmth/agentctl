package serve

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
)

// TestScheduledWindowJSON: a cron-launched window carries scheduled_by and
// scheduled_at on both the fleet and the window endpoint; other windows omit
// them.
func TestScheduledWindowJSON(t *testing.T) {
	e := newReadEnv(t)
	g := testGroup()
	fired := time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC)
	row := fleet.Row{Session: g.Session, Project: g.Project, AgentState: state.AgentState{
		Name: "triage", WindowID: "@600", WindowIndex: 2, State: "working", ScheduledBy: "nightly-triage", ScheduledAt: fired,
	}}
	meta := fleet.Meta{Mode: "adhoc", ScheduledBy: "nightly-triage", ScheduledAt: fired}
	g.Rows = append(g.Rows, row)
	g.Windows = append(g.Windows, fleet.ToWindow(row, meta))
	g.Meta["@600"] = meta
	e.fleet.set(g)

	var fl struct {
		Groups []struct {
			Windows []map[string]any `json:"windows"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(e.get("/v1/fleet").Body.Bytes(), &fl); err != nil {
		t.Fatal(err)
	}
	wins := fl.Groups[0].Windows
	if len(wins) != 2 {
		t.Fatalf("windows = %d, want 2", len(wins))
	}
	for _, k := range []string{"scheduled_by", "scheduled_at"} {
		if _, ok := wins[0][k]; ok {
			t.Errorf("non-cron window has %s: %v", k, wins[0])
		}
	}
	if wins[1]["scheduled_by"] != "nightly-triage" || wins[1]["scheduled_at"] != "2026-11-18T07:00:00Z" {
		t.Errorf("cron window = %v", wins[1])
	}

	var win map[string]any
	if err := json.Unmarshal(e.get("/v1/sessions/agentctl-proj/windows/@600").Body.Bytes(), &win); err != nil {
		t.Fatal(err)
	}
	if win["scheduled_by"] != "nightly-triage" || win["scheduled_at"] != "2026-11-18T07:00:00Z" {
		t.Errorf("window endpoint = %v", win)
	}
}
