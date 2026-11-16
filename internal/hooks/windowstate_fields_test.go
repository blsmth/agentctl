package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func setupFieldsEnv(t *testing.T, windowID string) {
	t.Helper()
	setupWindowstate(t)
	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", windowID)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "")
}

func TestSessionStart_WritesWorkDirAndRevives(t *testing.T) {
	setupFieldsEnv(t, "@11")
	t.Setenv("CLAUDE_ENV_FILE", filepath.Join(t.TempDir(), "env.sh"))

	windowstate.Remove("@11") // tombstone from a previous window with this ID
	runHookCaptured(t, nil, `{"session_id":"s1","cwd":"/work/dir"}`, SessionStart)

	ws, _ := windowstate.Read("@11")
	if ws.WorkDir != "/work/dir" || ws.SessionID != "s1" {
		t.Errorf("WorkDir=%q SessionID=%q", ws.WorkDir, ws.SessionID)
	}
}

func TestPostTool_WritesLastToolDetail(t *testing.T) {
	setupFieldsEnv(t, "@12")
	runHookCaptured(t, nil, `{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`, PostTool)

	ws, _ := windowstate.Read("@12")
	if !strings.Contains(ws.LastToolDetail, "go test") {
		t.Errorf("LastToolDetail = %q", ws.LastToolDetail)
	}
}

func TestCheckDone_WritesLastTextAndTurnCount(t *testing.T) {
	setupFieldsEnv(t, "@13")
	long := strings.Repeat("é", maxLastTextRunes+50)
	for i := 0; i < 2; i++ {
		runHookCaptured(t, nil, `{"last_assistant_message":"`+long+`","stop_hook_active":true}`, CheckDone)
	}
	ws, _ := windowstate.Read("@13")
	if ws.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2", ws.TurnCount)
	}
	if n := len([]rune(ws.LastText)); n != maxLastTextRunes {
		t.Errorf("LastText runes = %d, want %d", n, maxLastTextRunes)
	}
	if ws.State != "idle" {
		t.Errorf("State = %q", ws.State)
	}
}

func TestHookAfterRemoveDoesNotRecreate(t *testing.T) {
	setupFieldsEnv(t, "@14")
	windowstate.Remove("@14")
	runHookCaptured(t, nil, `{"tool_name":"Bash","tool_input":{"command":"ls"}}`, PostTool)
	if _, err := os.Stat(filepath.Join(windowstate.Dir(), "@14.json")); err == nil {
		t.Error("PostTool recreated removed window state")
	}
}
