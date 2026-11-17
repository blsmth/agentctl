package signal_test

import (
	"testing"

	"github.com/blsmth/agentctl/internal/signal"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestApply_DoneSpawnsCompletion(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		doneTwice  bool
		rearm      bool
		wantSpawns int
	}{
		{"autonomous spawns once", "dispatch", false, false, 1},
		{"two dones one spawn", "adhoc", true, false, 1},
		{"re-armed by working spawns again", "dispatch", true, true, 2},
		{"interactive never spawns", "discuss", false, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withWindowstateDir(t)
			clearSignalEnv(t)
			_, cleanup := setupDeps(t, nil)
			defer cleanup()

			spawns := 0
			signal.SetAutonomousModeFn(func(m string) bool { return m == "dispatch" || m == "adhoc" })
			signal.SetSpawnCompleteFn(func(string) error { spawns++; return nil })
			t.Cleanup(func() {
				signal.SetAutonomousModeFn(func(string) bool { return false })
				signal.SetSpawnCompleteFn(func(string) error { return nil })
			})
			if err := windowstate.Update("@1", func(ws *windowstate.WindowState) { ws.Mode = tt.mode }); err != nil {
				t.Fatal(err)
			}

			if err := signal.Apply("@1", "done", "a"); err != nil {
				t.Fatal(err)
			}
			if tt.rearm {
				if err := signal.Apply("@1", "working", "a"); err != nil {
					t.Fatal(err)
				}
				ws, _ := windowstate.Read("@1")
				if ws.Done || !ws.CompleteFiredAt.IsZero() {
					t.Fatalf("working must re-arm, got %+v", ws)
				}
			}
			if tt.doneTwice {
				if err := signal.Apply("@1", "done", "a"); err != nil {
					t.Fatal(err)
				}
			}
			if spawns != tt.wantSpawns {
				t.Errorf("spawns = %d, want %d", spawns, tt.wantSpawns)
			}
		})
	}
}
