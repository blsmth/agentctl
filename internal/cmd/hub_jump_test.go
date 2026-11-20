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
			var switched, gotClient string
			oldS, oldL, oldW, oldD := hubSendKey, hubLoad, switchHome, displayMsg
			t.Cleanup(func() { hubSendKey, hubLoad, switchHome, displayMsg = oldS, oldL, oldW, oldD })
			hubSendKey = func(k string) error { keys = append(keys, k); return nil }
			hubLoad = func() ([]fleet.Group, error) { return blocked, nil }
			switchHome = func(c, s, w string) error { gotClient = c; switched = s + ":" + w; return nil }
			displayMsg = func(string) {}

			RunJumpNextBlocked(tt.client, "tty1")

			if len(keys) != len(tt.wantKeys) {
				t.Errorf("keys = %v, want %v", keys, tt.wantKeys)
			}
			if tt.wantSwitch != "" && gotClient != "tty1" {
				t.Errorf("switch client = %q, want tty1", gotClient)
			}
			if switched != tt.wantSwitch {
				t.Errorf("switched = %q, want %q", switched, tt.wantSwitch)
			}
		})
	}
}

func TestRunHubPassesClient(t *testing.T) {
	for _, client := range []string{"", "/dev/ttys004"} {
		old, oldO := hubSwitch, hubRecordOrigin
		t.Cleanup(func() { hubSwitch, hubRecordOrigin = old, oldO })
		var got = "unset"
		hubSwitch = func(c string) error { got = c; return nil }
		hubRecordOrigin = func(string) error { return nil }
		RunHub(client)
		if got != client {
			t.Errorf("switched client = %q, want %q", got, client)
		}
	}
}
