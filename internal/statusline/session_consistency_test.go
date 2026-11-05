package statusline

import (
	"testing"

	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestConsistentAgentState(t *testing.T) {
	const sid = "f2cbcc84-0000-0000-0000-000000000001"
	tests := []struct {
		name    string
		as      *state.AgentState
		payload *Payload
		keep    bool
	}{
		{"nil snapshot", nil, &Payload{SessionID: strPtr(sid)}, false},
		{"matching log", &state.AgentState{LogPath: "/p/a/" + sid + ".jsonl"}, &Payload{SessionID: strPtr(sid)}, true},
		{"mismatched log dropped", &state.AgentState{LogPath: "/p/b/271e25c0.jsonl"}, &Payload{SessionID: strPtr(sid)}, false},
		{"empty log path kept", &state.AgentState{}, &Payload{SessionID: strPtr(sid)}, true},
		{"no payload session kept", &state.AgentState{LogPath: "/p/b/271e25c0.jsonl"}, &Payload{}, true},
		{"empty payload session kept", &state.AgentState{LogPath: "/p/b/271e25c0.jsonl"}, &Payload{SessionID: strPtr("")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := consistentAgentState(tt.as, tt.payload)
			if (got != nil) != tt.keep {
				t.Errorf("kept = %v, want %v", got != nil, tt.keep)
			}
		})
	}
}

func TestPersistWindowState_LogPathBackfill(t *testing.T) {
	const sid = "f2cbcc84-0000-0000-0000-000000000001"
	good := "/home/.claude/projects/-p/" + sid + ".jsonl"
	tests := []struct {
		name    string
		wsSID   string
		wsLog   string
		payload *Payload
		wantLog string
	}{
		{"empty log, matching session: filled", sid, "", &Payload{SessionID: strPtr(sid), TranscriptPath: strPtr(good)}, good},
		{"non-empty log untouched", sid, "/other/" + sid + ".jsonl", &Payload{SessionID: strPtr(sid), TranscriptPath: strPtr(good)}, "/other/" + sid + ".jsonl"},
		{"different session untouched", "someone-else", "", &Payload{SessionID: strPtr(sid), TranscriptPath: strPtr(good)}, ""},
		{"inconsistent transcript basename ignored", sid, "", &Payload{SessionID: strPtr(sid), TranscriptPath: strPtr("/x/271e25c0.jsonl")}, ""},
		{"empty windowstate session untouched", "", "", &Payload{SessionID: strPtr(sid), TranscriptPath: strPtr(good)}, ""},
		{"no transcript path", sid, "", &Payload{SessionID: strPtr(sid)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			windowstate.SetDirForTest(t.TempDir())
			t.Cleanup(func() { windowstate.SetDirForTest("") })
			const w = "@9"
			if err := windowstate.Update(w, func(ws *windowstate.WindowState) {
				ws.SessionID = tt.wsSID
				ws.LogPath = tt.wsLog
			}); err != nil {
				t.Fatal(err)
			}
			persistWindowState(tt.payload, w)
			// Read validates consistency; use All-free raw check via Update.
			var got string
			_ = windowstate.Update(w, func(ws *windowstate.WindowState) { got = ws.LogPath })
			if got != tt.wantLog {
				t.Errorf("LogPath = %q, want %q", got, tt.wantLog)
			}
		})
	}
}
