package fleet

import (
	"testing"

	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestAttentionLine(t *testing.T) {
	tests := []struct {
		name  string
		state string
		a     *windowstate.Attention
		want  string
	}{
		{"none", "idle", nil, ""},
		{"question", "idle", &windowstate.Attention{Reason: windowstate.AttentionQuestion, Detail: "Merge it?"}, "asks you: Merge it?"},
		{"errors", "idle", &windowstate.Attention{Reason: windowstate.AttentionErrors}, "stuck"},
		{"verdict", "idle", &windowstate.Attention{Reason: windowstate.AttentionMessage, Detail: "PR #1 review: BLOCKED"}, "review verdict: PR #1 review: BLOCKED"},
		{"missed approval", "idle", &windowstate.Attention{Reason: windowstate.AttentionApproval, Detail: "dispatch: go-engineer on #3"}, "missed approval: dispatch: go-engineer on #3"},
		{"unknown reason", "idle", &windowstate.Attention{Reason: "other"}, "needs you"},
		{"hidden while working", "working", &windowstate.Attention{Reason: windowstate.AttentionApproval}, ""},
		{"hidden while blocked", "blocked", &windowstate.Attention{Reason: windowstate.AttentionApproval}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AttentionLine(state.AgentState{State: tt.state, Attention: tt.a}); got != tt.want {
				t.Fatalf("AttentionLine = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAttentionLabelTimeout(t *testing.T) {
	if got := AttentionLabel(windowstate.AttentionTimeout); got != "timed out" {
		t.Errorf("AttentionLabel(timeout) = %q", got)
	}
}
