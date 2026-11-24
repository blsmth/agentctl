package fleet

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestLabel(t *testing.T) {
	fire := time.Date(2026, 11, 24, 7, 1, 0, 0, time.Local) // a Tuesday
	tests := []struct {
		name  string
		row   state.AgentState
		scope Scope
		want  string
	}{
		{"manual title beats everything", state.AgentState{ShortTitle: "my name", ShortManual: true, Ticket: 9, TicketTitle: "x", ScheduledBy: "nightly"}, Scope{}, "my name"},
		{"issue with short title", state.AgentState{Ticket: 306, ShortTitle: "session names", TicketTitle: "User-facing session names"}, Scope{}, "#306 · session names"},
		{"issue with ticket title", state.AgentState{TicketRef: "#12", TicketTitle: "Fix the thing"}, Scope{}, "#12 · Fix the thing"},
		{"issue with no title", state.AgentState{Ticket: 5}, Scope{}, "#5"},
		{"review with branch", state.AgentState{Mode: "review-pr", Branch: "agent/code-reviewer/review-pr-42-ab12"}, Scope{}, "review #42"},
		{"review without number", state.AgentState{Mode: "review-pr"}, Scope{}, "review"},
		{"cron run", state.AgentState{ScheduledBy: "nightly", ScheduledAt: fire}, Scope{}, "nightly · Tue 07:01"},
		{"cron run without time", state.AgentState{ScheduledBy: "nightly"}, Scope{}, "nightly"},
		{"cron run in its job scope", state.AgentState{ScheduledBy: "nightly", ScheduledAt: fire}, Scope{Job: "nightly"}, ""},
		{"cron run in another job's scope", state.AgentState{ScheduledBy: "nightly", ScheduledAt: fire}, Scope{Job: "weekly"}, "nightly · Tue 07:01"},
		{"short title", state.AgentState{Mode: "adhoc", ShortTitle: "esc fix in hub", FirstPrompt: "please fix"}, Scope{}, "esc fix in hub"},
		{"task line", state.AgentState{Mode: "adhoc", Task: "tidy the logs"}, Scope{}, "tidy the logs"},
		{"first prompt is cut at a word", state.AgentState{FirstPrompt: "Please look at the flaky hub test\nand work out why it fails on CI every second run"}, Scope{}, "Please look at the flaky hub test and…"},
		{"agent and start time", state.AgentState{Name: "go-engineer~2", AgentName: "go-engineer", StartTime: time.Date(2026, 11, 24, 9, 5, 0, 0, time.Local).Unix()}, Scope{}, "go-engineer · started 09:05"},
		{"agent only when no start time", state.AgentState{Name: "go-engineer~2", AgentName: "go-engineer"}, Scope{}, "go-engineer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Label(Row{AgentState: tt.row}, tt.scope); got != tt.want {
				t.Errorf("Label = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLabelClosedRecord(t *testing.T) {
	rec := closedlog.Record{Agent: "go-engineer", Ticket: "#7", TicketTitle: "Fix", SessionID: "abcd"}
	if got := Label(RecordRow(rec), Scope{}); got != "#7 · Fix" {
		t.Errorf("Label = %q", got)
	}
	rec = closedlog.Record{Mode: "review-pr", Branch: "agent/x/review-pr-9-zz"}
	if got := Label(RecordRow(rec), Scope{}); got != "review #9" {
		t.Errorf("Label = %q", got)
	}
}

func TestUniqueLabels(t *testing.T) {
	in := []string{"a", "b", "a", "", "", "a", "b"}
	want := []string{"a", "b", "a ·2", "", "", "a ·3", "b ·2"}
	if got := UniqueLabels(in); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if in[2] != "a" {
		t.Error("input modified")
	}
	if got := UniqueLabels(in[:1]); got[0] != "a" {
		t.Errorf("state leaked between calls: %q", got)
	}
}

func TestLabelWidthIsCallerConcern(t *testing.T) {
	got := Label(Row{AgentState: state.AgentState{ShortTitle: strings.Repeat("word ", 20)}}, Scope{})
	if !strings.HasPrefix(got, "word word") {
		t.Errorf("got %q", got)
	}
}

func TestLabelBareWindowFromRecord(t *testing.T) {
	recs := map[string]windowstate.WindowState{
		"@3": {Instance: "x", FirstPrompt: "Please look at the flaky hub test\nand work out why it fails on CI every second run", StartedAt: 1},
	}
	a := agentFromWindow(1, tmux.WindowInfo{ID: "@3"}, "x", recs)
	if got, want := Label(Row{AgentState: a}, Scope{}), "Please look at the flaky hub test and…"; got != want {
		t.Errorf("Label = %q, want %q", got, want)
	}
}
