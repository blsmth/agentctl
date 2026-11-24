package hooks

import (
	"encoding/json"
	"testing"

	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/tui"
)

func TestDispatchPreviewArgs_CarryDetails(t *testing.T) {
	d := remoteapproval.Details{Project: "agentctl", Dir: "~/x", Model: "opus", Worktree: "worktree", PermissionMode: "auto", OtherProject: true, AskingProject: "web"}
	b, err := json.Marshal(DispatchPreviewArgs{Details: d})
	if err != nil {
		t.Fatal(err)
	}
	var got DispatchPreviewArgs
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Details != d {
		t.Errorf("Details = %+v, want %+v", got.Details, d)
	}
	// An args file from before Details existed still parses, with none.
	var old DispatchPreviewArgs
	if err := json.Unmarshal([]byte(`{"agent_name":"a","project":"p"}`), &old); err != nil || old.Details != (remoteapproval.Details{}) {
		t.Errorf("old args: %v %+v", err, old.Details)
	}
}

func TestDispatchPreviewVPHeight(t *testing.T) {
	full := remoteapproval.Details{Project: "p", Model: "m", Worktree: "worktree"}
	if got := dispatchPreviewVPHeight(22, full); got != 19 {
		t.Errorf("3 rows: %d, want 19", got)
	}
	if got := dispatchPreviewVPHeight(22, remoteapproval.Details{}); got != 22 {
		t.Errorf("no rows: %d, want 22", got)
	}
	if got := dispatchPreviewVPHeight(dpMinVPHeight, full); got != dpMinVPHeight {
		t.Errorf("floor: %d, want %d", got, dpMinVPHeight)
	}
	// Total popup height stays what the client budget allows.
	base := tui.DispatchPreviewMaxPopupSize(tui.WithMaxViewportHeight(22)).Height
	sized := tui.DispatchPreviewMaxPopupSize(tui.WithMaxViewportHeight(dispatchPreviewVPHeight(22, full)), tui.WithDetails(full)).Height
	if sized != base {
		t.Errorf("popup height %d, want %d", sized, base)
	}
}
