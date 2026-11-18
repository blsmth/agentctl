package fleet

import (
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/state"
)

func TestToWindow(t *testing.T) {
	since := time.Date(2026, 11, 16, 11, 0, 0, 0, time.UTC)
	row := Row{Session: "agentctl-p", Project: "p", AgentState: state.AgentState{
		Name: "a", WindowID: "@1", State: "blocked", Ticket: 7, LastText: "hello",
	}}
	meta := Meta{Mode: "dispatch", SpawnedBy: "@9", SpawnedBySession: "u", PendingTool: "Bash", BlockedSince: since}

	w := ToWindow(row, meta)
	if w.Parent != "@9" || w.PendingTool != "Bash" || w.BlockedSince != "2026-11-16T11:00:00Z" || w.Issue != 7 || w.Activity != "hello" {
		t.Errorf("blocked = %+v", w)
	}

	row.State = "working"
	meta.SpawnedBySession = ""
	w = ToWindow(row, meta)
	if w.PendingTool != "" || w.BlockedSince != "" {
		t.Errorf("pending tool must only show while blocked: %+v", w)
	}
	if w.Parent != "" {
		t.Errorf("untrusted parent leaked: %q", w.Parent)
	}
}

func TestToWindowPR(t *testing.T) {
	row := Row{AgentState: state.AgentState{Name: "a", WindowID: "@1", State: "idle", Done: true}}
	w := ToWindow(row, Meta{PRNumber: 61, PRURL: "https://github.com/o/r/pull/61"})
	if w.PRNumber != 61 || w.PRURL != "https://github.com/o/r/pull/61" {
		t.Errorf("PR fields not mapped: %+v", w)
	}
	if w := ToWindow(row, Meta{}); w.PRNumber != 0 || w.PRURL != "" {
		t.Errorf("empty meta produced PR: %+v", w)
	}
}
