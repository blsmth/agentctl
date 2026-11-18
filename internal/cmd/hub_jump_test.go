package cmd

import (
	"testing"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
)

func TestRunJumpNextBlocked(t *testing.T) {
	blocked := []fleet.Group{{Session: "agentctl-p", Rows: []fleet.Row{{Session: "agentctl-p", AgentState: state.AgentState{WindowID: "@7", State: "blocked"}}}}}
	tests := []struct {
		name       string
		client     string
		wantKeys   []string
		wantSwitch string
	}{
		{"hub client sends key", tmux.HubSessionName, []string{"b"}, ""},
		{"other session switches", "agentctl-p", nil, "agentctl-p:@7"},
		{"unknown client switches", "", nil, "agentctl-p:@7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var keys []string
			var switched string
			oldS, oldL, oldW, oldD := hubSendKey, hubLoad, switchHome, displayMsg
			t.Cleanup(func() { hubSendKey, hubLoad, switchHome, displayMsg = oldS, oldL, oldW, oldD })
			hubSendKey = func(k string) error { keys = append(keys, k); return nil }
			hubLoad = func() ([]fleet.Group, error) { return blocked, nil }
			switchHome = func(s, w string) error { switched = s + ":" + w; return nil }
			displayMsg = func(string) {}

			RunJumpNextBlocked(tt.client)

			if len(keys) != len(tt.wantKeys) {
				t.Errorf("keys = %v, want %v", keys, tt.wantKeys)
			}
			if switched != tt.wantSwitch {
				t.Errorf("switched = %q, want %q", switched, tt.wantSwitch)
			}
		})
	}
}
