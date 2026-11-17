package hooks

import (
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestSubagentTracking(t *testing.T) {
	setupWindowstate(t)
	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", "@80")
	t.Setenv("TMUX_PANE", "")

	count := func() (int, int) {
		ws, err := windowstate.Read("@80")
		if err != nil {
			t.Fatal(err)
		}
		return len(ws.ActiveSubagents), ws.SubagentCount
	}

	start := func(id string) { runHookCaptured(t, nil, `{"agent_id":"`+id+`","agent_type":"x"}`, SubagentStart) }
	stop := func(id string) { runHookCaptured(t, nil, `{"agent_id":"`+id+`"}`, SubagentStop) }

	start("a")
	start("a")
	if m, c := count(); m != 1 || c != 1 {
		t.Fatalf("duplicate start: map=%d count=%d", m, c)
	}
	start("b")
	stop("a")
	if m, c := count(); m != 1 || c != 1 {
		t.Fatalf("after stop a: map=%d count=%d", m, c)
	}
	stop("b")
	if m, c := count(); m != 0 || c != 0 {
		t.Fatalf("after stop b: map=%d count=%d", m, c)
	}

	start("")
	if _, c := count(); c != 1 {
		t.Fatalf("empty-id start: count=%d", c)
	}
	stop("")
	if _, c := count(); c != 0 {
		t.Fatalf("empty-id stop: count=%d", c)
	}
}
