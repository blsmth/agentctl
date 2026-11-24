package hooks

import (
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestFinishImproveOnStop(t *testing.T) {
	oldSpawn, oldHas := spawnImproveFinishFn, improveSummaryWritten
	t.Cleanup(func() { spawnImproveFinishFn, improveSummaryWritten = oldSpawn, oldHas })
	var spawned []string
	spawnImproveFinishFn = func(id string) error { spawned = append(spawned, id); return nil }

	improve := windowstate.WindowState{Mode: "improve", ImproveRun: "r1"}
	for _, tc := range []struct {
		name    string
		ws      windowstate.WindowState
		summary bool
		want    int
	}{
		{"summary absent", improve, false, 0},
		{"summary present", improve, true, 1},
		{"not improve", windowstate.WindowState{Mode: "bare", ImproveRun: "r1"}, true, 0},
		{"no run", windowstate.WindowState{Mode: "improve"}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spawned = nil
			improveSummaryWritten = func(string) bool { return tc.summary }
			finishImproveOnStop(tc.ws)
			if len(spawned) != tc.want {
				t.Fatalf("spawned %v, want %d", spawned, tc.want)
			}
		})
	}
}
