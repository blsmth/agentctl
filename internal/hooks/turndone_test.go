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
		// Regression: TurnDone used to be set only for "bare", so every other
		// mode that never gets Done (discuss, review, custom, improve, and
		// records with no mode) sat at ○ after finishing a turn.
		{"discuss gets TurnDone, not Done", "discuss", nil, true, false},
		{"improve gets TurnDone", "improve", nil, true, false},
		{"no mode gets TurnDone", "", nil, true, false},
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

// Regression: an autonomous window whose stop was blocked (exit 2) ends its
// retry turn with stop_hook_active, where Done is never set; it used to stay
// at ○.
func TestCheckDone_RetryStopShowsTurnDone(t *testing.T) {
	const w = "@92"
	stopEnv(t, w, "dispatch")
	setSpawner(t, func(string) error { return nil })
	runHookCaptured(t, nil, `{"stop_hook_active":true,"last_assistant_message":"committed"}`, CheckDone)
	ws, _ := windowstate.Read(w)
	if ws.Done || !ws.TurnDone {
		t.Fatalf("Done=%v TurnDone=%v, want false/true", ws.Done, ws.TurnDone)
	}
}

// Regression: a parent whose ✓ was held back by a busy child stayed ○ after
// the child finished, because nothing re-evaluated it.
func TestCheckDone_ChildStopReleasesParentTurnDone(t *testing.T) {
	const parent, child = "@93", "@94"
	stopEnv(t, parent, "bare")
	setSpawner(t, func(string) error { return nil })
	if err := windowstate.Update(parent, func(ws *windowstate.WindowState) { ws.SessionID = "sess-p" }); err != nil {
		t.Fatal(err)
	}
	if err := windowstate.Update(child, func(ws *windowstate.WindowState) {
		ws.State, ws.Mode, ws.SpawnedBy, ws.SpawnedBySession = "working", "dispatch", parent, "sess-p"
	}); err != nil {
		t.Fatal(err)
	}
	runHookCaptured(t, nil, stopStdin, CheckDone)
	if ws, _ := windowstate.Read(parent); ws.TurnDone || !ws.TurnDoneHeld {
		t.Fatalf("parent TurnDone=%v held=%v, want held", ws.TurnDone, ws.TurnDoneHeld)
	}
	t.Setenv("AGENTCTL_WINDOW_ID", child)
	runHookCaptured(t, nil, stopStdin, CheckDone)
	ws, _ := windowstate.Read(parent)
	if !ws.TurnDone || ws.TurnDoneHeld {
		t.Fatalf("after the child's stop: parent TurnDone=%v held=%v, want released", ws.TurnDone, ws.TurnDoneHeld)
	}
}
