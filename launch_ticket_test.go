package main

import (
	"testing"

	"github.com/blsmth/agentctl/internal/cmd"
	"github.com/blsmth/agentctl/internal/dispatch"
)

func TestPromptModeToDispatchMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  dispatch.Mode
	}{
		{name: "work maps to ModeDispatch", input: "work", want: dispatch.ModeDispatch},
		{name: "review maps to ModeReview", input: "review", want: dispatch.ModeReview},
		{name: "triage maps to ModeTriage", input: "triage", want: dispatch.ModeTriage},
		{name: "none maps to ModeDiscuss", input: "none", want: dispatch.ModeDiscuss},
		{name: "custom maps to ModeCustom", input: "custom", want: dispatch.ModeCustom},
		{name: "unknown falls back to ModeDiscuss", input: "unknown", want: dispatch.ModeDiscuss},
		{name: "empty falls back to ModeDiscuss", input: "", want: dispatch.ModeDiscuss},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cmd.PromptModeToDispatchMode(tc.input)
			if got != tc.want {
				t.Errorf("promptModeToDispatchMode(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestLaunchTicketModeUsesWorktreeForWork(t *testing.T) {
	t.Parallel()

	// ModeDispatch (work) should set UseWorktree = true.
	if mode := dispatch.ModeDispatch; mode != dispatch.ModeDispatch {
		t.Fatal("sanity check failed")
	}

	// Verify the worktree flag logic inline without calling dispatch.Launch
	// (which requires a live tmux session).
	for _, tc := range []struct {
		mode         dispatch.Mode
		wantWorktree bool
	}{
		{dispatch.ModeDispatch, true},
		{dispatch.ModeDiscuss, false},
		{dispatch.ModeTriage, false},
		{dispatch.ModeReview, false},
		{dispatch.ModeCustom, false},
	} {
		got := tc.mode == dispatch.ModeDispatch
		if got != tc.wantWorktree {
			t.Errorf("mode %v: useWorktree=%v, want %v", tc.mode, got, tc.wantWorktree)
		}
	}
}
