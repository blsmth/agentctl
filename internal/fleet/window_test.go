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

func TestToWindowRecap(t *testing.T) {
	for _, tc := range []struct {
		name       string
		line       RecapLine
		wantText   string
		wantSource string
	}{
		{"none", RecapLine{}, "", ""},
		{"summary", RecapLine{Text: "Building the app.", Source: RecapSummary}, "Building the app.", "summary"},
		{"task", RecapLine{Text: "#7 Add retry", Source: RecapTask}, "#7 Add retry", "task"},
		{"title", RecapLine{Text: "Fix bug", Source: RecapTitle}, "Fix bug", "title"},
		{"activity", RecapLine{Text: "go test", Source: RecapActivity}, "go test", "activity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := Row{AgentState: state.AgentState{Name: "a", WindowID: "@1", State: "idle"}, Recap: tc.line}
			w := ToWindow(row, Meta{})
			if w.Recap != tc.wantText || w.RecapSource != tc.wantSource {
				t.Errorf("got %q/%q, want %q/%q", w.Recap, w.RecapSource, tc.wantText, tc.wantSource)
			}
		})
	}
}

func TestCleanRecapStripsHint(t *testing.T) {
	for in, want := range map[string]string{
		"Building it.\n (disable recaps in /config)": "Building it.",
		"Building it. (disable recaps in /config)":   "Building it.",
		"Plain recap":                                "Plain recap",
	} {
		if got := cleanRecap(in); got != want {
			t.Errorf("cleanRecap(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToWindowModel(t *testing.T) {
	row := Row{AgentState: state.AgentState{Name: "a", WindowID: "@1", State: "idle", ModelName: "claude-x"}}
	if w := ToWindow(row, Meta{}); w.Model != "claude-x" {
		t.Errorf("Model = %q", w.Model)
	}
	if w := ToWindow(Row{}, Meta{}); w.Model != "" {
		t.Errorf("empty row Model = %q", w.Model)
	}
}
