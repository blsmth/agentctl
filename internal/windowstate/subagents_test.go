package windowstate

import (
	"encoding/json"
	"testing"
	"time"
)

func TestActiveSubagentCount(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		ws   WindowState
		want int
	}{
		{"empty", WindowState{}, 0},
		{"fresh", WindowState{ActiveSubagents: map[string]time.Time{"a": now.Add(-time.Minute), "b": now}}, 2},
		{"stale ignored", WindowState{ActiveSubagents: map[string]time.Time{"a": now.Add(-3 * time.Hour), "b": now}}, 1},
		{"all stale", WindowState{ActiveSubagents: map[string]time.Time{"a": now.Add(-3 * time.Hour)}}, 0},
		{"legacy counter", WindowState{SubagentCount: 2}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ws.ActiveSubagentCount(now); got != tt.want {
				t.Errorf("got %d want %d", got, tt.want)
			}
		})
	}
}

func TestSubagentStartStop(t *testing.T) {
	now := time.Now()
	var ws WindowState
	ws.StartSubagent("a", now)
	ws.StartSubagent("a", now)
	if ws.SubagentCount != 1 || len(ws.ActiveSubagents) != 1 {
		t.Fatalf("duplicate start: count=%d map=%d", ws.SubagentCount, len(ws.ActiveSubagents))
	}
	ws.ActiveSubagents["old"] = now.Add(-3 * time.Hour)
	ws.StartSubagent("c", now)
	ws.StopSubagent("c", now)
	if _, ok := ws.ActiveSubagents["old"]; ok {
		t.Error("stale entry not pruned on write")
	}
	ws.StopSubagent("a", now)
	if ws.SubagentCount != 0 || ws.ActiveSubagents != nil {
		t.Errorf("after stop: count=%d map=%v", ws.SubagentCount, ws.ActiveSubagents)
	}
	ws.StartSubagent("", now)
	ws.StartSubagent("", now)
	ws.StopSubagent("", now)
	if ws.SubagentCount != 1 || ws.ActiveSubagentCount(now) != 1 {
		t.Errorf("empty-id fallback: count=%d", ws.SubagentCount)
	}
	ws.ClearSubagents()
	if ws.ActiveSubagentCount(now) != 0 {
		t.Error("clear left subagents")
	}
}

func TestActiveSubagentsJSONRoundTrip(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	in := WindowState{ActiveSubagents: map[string]time.Time{"x": ts}}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out WindowState
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if !out.ActiveSubagents["x"].Equal(ts) {
		t.Errorf("round trip lost entry: %v", out.ActiveSubagents)
	}
	data, _ = json.Marshal(WindowState{})
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	if _, ok := m["active_subagents"]; ok {
		t.Error("empty map should be omitted")
	}
}
