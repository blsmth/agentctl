package hooks

import (
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/windowstate"
)

// ---------------------------------------------------------------------------
// clearPendingToolIfMatch
// ---------------------------------------------------------------------------

func TestClearPendingToolIfMatch(t *testing.T) {
	t.Run("clears on match", func(t *testing.T) {
		ws := &windowstate.WindowState{
			PendingTool:       "Bash",
			PendingToolDetail: "go test ./...",
			PendingToolAt:     time.Now(),
		}
		clearPendingToolIfMatch(ws, "Bash")
		if ws.PendingTool != "" || ws.PendingToolDetail != "" || !ws.PendingToolAt.IsZero() {
			t.Errorf("expected pending-tool fields cleared, got %+v", ws)
		}
	})

	t.Run("preserved on mismatch", func(t *testing.T) {
		at := time.Now()
		ws := &windowstate.WindowState{
			PendingTool:       "Bash",
			PendingToolDetail: "go test ./...",
			PendingToolAt:     at,
		}
		clearPendingToolIfMatch(ws, "Read")
		if ws.PendingTool != "Bash" || ws.PendingToolDetail != "go test ./..." || !ws.PendingToolAt.Equal(at) {
			t.Errorf("expected pending-tool fields preserved for a different completing tool, got %+v", ws)
		}
	})
}

// ---------------------------------------------------------------------------
// PreTool / PostTool integration: pending-tool lifecycle
// ---------------------------------------------------------------------------

// TestPreTool_WritesPendingTool verifies that PreTool records PendingTool,
// PendingToolDetail, and PendingToolAt in the same windowstate.Update call
// that sets State=working.
func TestPreTool_WritesPendingTool(t *testing.T) {
	setupWindowstate(t)

	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_WINDOW_ID":  "@50",
		"AGENTCTL_AGENT_NAME": "go-engineer",
		"TMUX_PANE":           "",
		"AGENTCTL_SESSION_ID": "",
	}
	stdin := `{"tool_name":"Bash","tool_input":{"command":"git push --force origin main"}}`

	runHookCaptured(t, envVars, stdin, PreTool)

	ws, err := windowstate.Read("@50")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.State != "working" {
		t.Errorf("State = %q, want working", ws.State)
	}
	if ws.PendingTool != "Bash" {
		t.Errorf("PendingTool = %q, want Bash", ws.PendingTool)
	}
	if ws.PendingToolDetail != "git push --force origin main" {
		t.Errorf("PendingToolDetail = %q, want %q", ws.PendingToolDetail, "git push --force origin main")
	}
	if ws.PendingToolAt.IsZero() {
		t.Error("PendingToolAt is zero, want non-zero")
	}
}

// TestPostTool_ClearsMatchingPendingTool verifies that PostTool clears the
// pending-tool fields when the completing tool name matches the recorded
// PendingTool.
func TestPostTool_ClearsMatchingPendingTool(t *testing.T) {
	setupWindowstate(t)

	if err := windowstate.Write("@51", windowstate.WindowState{
		PendingTool:       "Bash",
		PendingToolDetail: "echo hi",
		PendingToolAt:     time.Now(),
	}); err != nil {
		t.Fatalf("windowstate.Write: %v", err)
	}

	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_WINDOW_ID":  "@51",
		"AGENTCTL_AGENT_NAME": "go-engineer",
		"TMUX_PANE":           "",
		"AGENTCTL_SESSION_ID": "",
	}
	stdin := `{"tool_name":"Bash","tool_response":{"stdout":"hi","stderr":""}}`

	runHookCaptured(t, envVars, stdin, PostTool)

	ws, err := windowstate.Read("@51")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.PendingTool != "" || ws.PendingToolDetail != "" || !ws.PendingToolAt.IsZero() {
		t.Errorf("expected pending-tool fields cleared after matching PostTool, got %+v", ws)
	}
}

// TestPostTool_PreservesMismatchedPendingTool is the regression guard for
// Claude Code's parallel tool calls: several PreToolUse events can land
// before the first PostToolUse, so a PostTool for a DIFFERENT tool than the
// one currently recorded as pending must not clear it out from under the
// still-in-flight call.
func TestPostTool_PreservesMismatchedPendingTool(t *testing.T) {
	setupWindowstate(t)

	pendingAt := time.Now()
	if err := windowstate.Write("@52", windowstate.WindowState{
		PendingTool:       "Bash",
		PendingToolDetail: "still running",
		PendingToolAt:     pendingAt,
	}); err != nil {
		t.Fatalf("windowstate.Write: %v", err)
	}

	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_WINDOW_ID":  "@52",
		"AGENTCTL_AGENT_NAME": "go-engineer",
		"TMUX_PANE":           "",
		"AGENTCTL_SESSION_ID": "",
	}
	// A different tool (Read) completes while Bash is still recorded pending.
	stdin := `{"tool_name":"Read","tool_response":{"stdout":"file contents"}}`

	runHookCaptured(t, envVars, stdin, PostTool)

	ws, err := windowstate.Read("@52")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.PendingTool != "Bash" || ws.PendingToolDetail != "still running" {
		t.Errorf("expected pending Bash call preserved after unrelated Read completed, got %+v", ws)
	}
}
