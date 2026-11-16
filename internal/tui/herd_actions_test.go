package tui

import (
	"testing"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/state"
)

func TestViewerSyncTarget(t *testing.T) {
	row := func(s, w string) herdLine {
		return herdLine{kind: herdLineRow, row: herd.Row{Session: s, AgentState: state.AgentState{WindowID: w}}}
	}
	lines := []herdLine{row("agentctl-a", "@1"), row("agentctl-a", "@2"), {kind: herdLineHeader}}
	tests := []struct {
		name                       string
		liveShown, seen, home, win string
		wantKey                    string
		wantFollow                 bool
	}{
		{"external tab click", "agentctl-a|@1", "agentctl-a|@1", "agentctl-a", "@2", "agentctl-a|@2", true},
		{"hub caused change", "agentctl-a|@2", "agentctl-a|@1", "agentctl-a", "@2", "agentctl-a|@2", false},
		{"unchanged", "agentctl-a|@1", "agentctl-a|@1", "agentctl-a", "@1", "agentctl-a|@1", false},
		{"stale poll while hub switching", "agentctl-a|@2", "agentctl-a|@1", "agentctl-a", "@1", "agentctl-a|@1", false},
		{"window not in list", "agentctl-a|@1", "agentctl-a|@1", "agentctl-a", "@9", "agentctl-a|@9", false},
		{"no viewer", "agentctl-a|@1", "agentctl-a|@1", "", "", "agentctl-a|@1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, follow := viewerSyncTarget(lines, tt.liveShown, tt.seen, tt.home, tt.win)
			if key != tt.wantKey || follow != tt.wantFollow {
				t.Errorf("got (%q,%v), want (%q,%v)", key, follow, tt.wantKey, tt.wantFollow)
			}
		})
	}
}
