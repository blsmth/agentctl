package hooks

import (
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestCheckDone_TurnDone(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		children     map[string]string // child window ID -> state
		wantTurnDone bool
		wantDone     bool
	}{
		{"bare sets TurnDone, not Done", "bare", nil, true, false},
		{"bare with working child", "bare", map[string]string{"@91": "working"}, false, false},
		{"bare with blocked child", "bare", map[string]string{"@91": "blocked"}, false, false},
		{"bare with idle child", "bare", map[string]string{"@91": "idle"}, true, false},
		{"dispatch gets Done, not TurnDone", "dispatch", nil, false, true},
		{"discuss gets neither", "discuss", nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const w = "@90"
			stopEnv(t, w, tt.mode)
			setSpawner(t, func(string) error { return nil })
			if err := windowstate.Update(w, func(ws *windowstate.WindowState) { ws.SessionID = "sess-p" }); err != nil {
				t.Fatal(err)
			}
			for id, st := range tt.children {
				if err := windowstate.Update(id, func(ws *windowstate.WindowState) {
					ws.State, ws.SpawnedBy, ws.SpawnedBySession = st, w, "sess-p"
				}); err != nil {
					t.Fatal(err)
				}
			}
			runHookCaptured(t, nil, stopStdin, CheckDone)
			ws, _ := windowstate.Read(w)
			if ws.TurnDone != tt.wantTurnDone || ws.Done != tt.wantDone {
				t.Fatalf("TurnDone=%v Done=%v, want %v/%v", ws.TurnDone, ws.Done, tt.wantTurnDone, tt.wantDone)
			}
			runHookCaptured(t, nil, "{}", UserPrompt)
			ws, _ = windowstate.Read(w)
			if ws.TurnDone {
				t.Fatal("UserPrompt must clear TurnDone")
			}
		})
	}
}

func TestHasBusyChild(t *testing.T) {
	all := map[string]windowstate.WindowState{
		"@1": {State: "working", SpawnedBy: "@0", SpawnedBySession: "s1"},
		"@2": {State: "working", SpawnedBy: "@0", SpawnedBySession: "other"},
		"@3": {State: "working", SpawnedBy: "@0", SpawnedBySession: "s1", ScheduledBy: "job"},
	}
	if !windowstate.HasBusyChild("@0", "s1", all) {
		t.Error("want busy child")
	}
	if windowstate.HasBusyChild("@0", "", all) {
		t.Error("empty session must not match")
	}
	if windowstate.HasBusyChild("@0", "s2", all) {
		t.Error("session mismatch must not match")
	}
}
