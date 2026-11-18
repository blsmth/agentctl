package signal_test

import (
	"os"
	"testing"

	"github.com/blsmth/agentctl/internal/signal"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/winname"
)

// setupDeps installs a test-double Deps that records rename calls and maps
// tmux targets to canned responses. Returns a cleanup function.
func setupDeps(t *testing.T, displayResponses map[string]string) (renames *[]string, cleanup func()) {
	t.Helper()

	renames = &[]string{}
	d := &signal.Deps{
		TmuxDisplay: func(target, format string) string {
			key := target + "|" + format
			return displayResponses[key]
		},
	}
	signal.SetDepsForTest(d)
	restore := winname.SetSeamsForTest(
		func(windowID, name string) error {
			*renames = append(*renames, windowID+"="+name)
			return nil
		},
		func(string) string { return "" },
		func() string { return "" },
	)
	return renames, func() {
		restore()
		signal.SetDepsForTest(signal.DefaultDeps())
	}
}

// withWindowstateDir redirects windowstate to a temp dir for the test.
func withWindowstateDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	windowstate.SetDirForTest(dir)
	t.Cleanup(func() { windowstate.SetDirForTest("") })
}

// clearSignalEnv unsets all env vars used by ResolveWindowID so tests start
// from a known blank slate.
func clearSignalEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"AGENTCTL_SESSION_ID",
		"TMUX_PANE",
		"AGENTCTL_WINDOW_ID",
		"AGENTCTL_INSTANCE_NAME",
		"AGENTCTL_AGENT_NAME",
	} {
		t.Setenv(key, "")
	}
}

// ---------------------------------------------------------------------------
// ResolveWindowID fallback chain
// ---------------------------------------------------------------------------

func TestResolveWindowID_AgentctlWindowID(t *testing.T) {
	clearSignalEnv(t)
	_, cleanup := setupDeps(t, nil)
	defer cleanup()

	t.Setenv("AGENTCTL_WINDOW_ID", "@7")
	got := signal.ResolveWindowID()
	if got != "@7" {
		t.Errorf("ResolveWindowID() = %q, want %q", got, "@7")
	}
}

func TestResolveWindowID_TmuxPane(t *testing.T) {
	clearSignalEnv(t)
	_, cleanup := setupDeps(t, map[string]string{
		"%3|#{window_id}": "@5",
	})
	defer cleanup()

	t.Setenv("TMUX_PANE", "%3")
	got := signal.ResolveWindowID()
	if got != "@5" {
		t.Errorf("ResolveWindowID() via TMUX_PANE = %q, want %q", got, "@5")
	}
}

func TestResolveWindowID_SessionID(t *testing.T) {
	withWindowstateDir(t)
	clearSignalEnv(t)
	_, cleanup := setupDeps(t, nil)
	defer cleanup()

	// Write a windowstate file with a known session ID.
	if err := windowstate.Write("@11", windowstate.WindowState{
		SessionID: "uuid-abc-123",
		State:     "idle",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	t.Setenv("AGENTCTL_SESSION_ID", "uuid-abc-123")
	got := signal.ResolveWindowID()
	if got != "@11" {
		t.Errorf("ResolveWindowID() via AGENTCTL_SESSION_ID = %q, want %q", got, "@11")
	}
}

func TestResolveWindowID_FallbackOrder(t *testing.T) {
	// When TMUX_PANE is set AND AGENTCTL_WINDOW_ID is set, TMUX_PANE wins.
	clearSignalEnv(t)
	_, cleanup := setupDeps(t, map[string]string{
		"%1|#{window_id}": "@2",
	})
	defer cleanup()

	t.Setenv("TMUX_PANE", "%1")
	t.Setenv("AGENTCTL_WINDOW_ID", "@99") // should be ignored
	got := signal.ResolveWindowID()
	if got != "@2" {
		t.Errorf("ResolveWindowID() priority: TMUX_PANE should beat AGENTCTL_WINDOW_ID, got %q", got)
	}
}

func TestResolveWindowID_Empty(t *testing.T) {
	clearSignalEnv(t)
	_, cleanup := setupDeps(t, nil)
	defer cleanup()

	got := signal.ResolveWindowID()
	if got != "" {
		t.Errorf("ResolveWindowID() with no env = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Apply — state transitions and window rename behavior
// ---------------------------------------------------------------------------

func TestApply_DoneState(t *testing.T) {
	withWindowstateDir(t)
	clearSignalEnv(t)
	renames, cleanup := setupDeps(t, nil)
	defer cleanup()

	if err := signal.Apply("@1", "done", "myagent"); err != nil {
		t.Fatalf("Apply(done): %v", err)
	}

	ws, err := windowstate.Read("@1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if ws.State != "idle" {
		t.Errorf("state = %q, want %q", ws.State, "idle")
	}
	if !ws.Done {
		t.Error("Done should be true after 'done' signal")
	}

	// "done" on an unseen window renders the ✓ glyph.
	if len(*renames) == 0 {
		t.Fatal("expected a rename call")
	}
	wantRename := "@1=✓ myagent"
	if (*renames)[0] != wantRename {
		t.Errorf("rename = %q, want %q", (*renames)[0], wantRename)
	}
}

func TestApply_WorkingState(t *testing.T) {
	withWindowstateDir(t)
	clearSignalEnv(t)
	renames, cleanup := setupDeps(t, nil)
	defer cleanup()

	if err := signal.Apply("@2", "working", "myagent"); err != nil {
		t.Fatalf("Apply(working): %v", err)
	}

	ws, err := windowstate.Read("@2")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if ws.State != "working" {
		t.Errorf("state = %q, want %q", ws.State, "working")
	}
	if ws.Done {
		t.Error("Done should be false after 'working' signal")
	}
	if len(*renames) != 1 || (*renames)[0] != "@2=● myagent" {
		t.Errorf("renames = %v, want [@2=● myagent]", *renames)
	}
}

func TestApply_BlockedState(t *testing.T) {
	withWindowstateDir(t)
	clearSignalEnv(t)
	renames, cleanup := setupDeps(t, nil)
	defer cleanup()

	if err := signal.Apply("@3", "blocked", "myagent"); err != nil {
		t.Fatalf("Apply(blocked): %v", err)
	}

	ws, err := windowstate.Read("@3")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if ws.State != "blocked" {
		t.Errorf("state = %q, want %q", ws.State, "blocked")
	}
	if ws.Done {
		t.Error("Done should be false after 'blocked' signal")
	}
	if len(*renames) == 0 {
		t.Fatal("expected a rename call for 'blocked'")
	}
	wantRename := "@3=⏸ myagent"
	if (*renames)[0] != wantRename {
		t.Errorf("rename = %q, want %q", (*renames)[0], wantRename)
	}
}

func TestApply_IdleState(t *testing.T) {
	withWindowstateDir(t)
	clearSignalEnv(t)
	renames, cleanup := setupDeps(t, nil)
	defer cleanup()

	if err := signal.Apply("@4", "idle", "myagent"); err != nil {
		t.Fatalf("Apply(idle): %v", err)
	}

	ws, err := windowstate.Read("@4")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if ws.State != "idle" {
		t.Errorf("state = %q, want %q", ws.State, "idle")
	}
	if ws.Done {
		t.Error("Done should be false after 'idle' signal")
	}
	// "idle" renames to plain agent name.
	if len(*renames) == 0 {
		t.Fatal("expected a rename call for 'idle'")
	}
	wantRename := "@4=myagent"
	if (*renames)[0] != wantRename {
		t.Errorf("rename = %q, want %q", (*renames)[0], wantRename)
	}
}

func TestApply_AgentNameFromEnv(t *testing.T) {
	withWindowstateDir(t)
	clearSignalEnv(t)
	renames, cleanup := setupDeps(t, nil)
	defer cleanup()

	t.Setenv("AGENTCTL_INSTANCE_NAME", "envagent")

	// Pass empty agentName — Apply should resolve from env.
	if err := signal.Apply("@5", "done", ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(*renames) == 0 {
		t.Fatal("expected rename call")
	}
	wantRename := "@5=✓ envagent"
	if (*renames)[0] != wantRename {
		t.Errorf("rename = %q, want %q", (*renames)[0], wantRename)
	}
}

func TestApply_AgentNameFromWindow(t *testing.T) {
	withWindowstateDir(t)
	clearSignalEnv(t)

	// Return "● myagent" from window name lookup — StripAgentPrefix should strip "● ".
	renames, cleanup := setupDeps(t, map[string]string{
		"=agentctl:@6|#{window_name}": "● myagent",
	})
	defer cleanup()

	// No env vars set for agent name; resolve falls through to window name.
	if err := signal.Apply("@6", "idle", ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(*renames) == 0 {
		t.Fatal("expected rename call")
	}
	// StripAgentPrefix should strip the "● " spinner prefix.
	wantRename := "@6=myagent"
	if (*renames)[0] != wantRename {
		t.Errorf("rename = %q, want %q", (*renames)[0], wantRename)
	}
}

// ---------------------------------------------------------------------------
// Env var cleanup
// ---------------------------------------------------------------------------

func init() {
	// Ensure AGENTCTL_WINDOW_ID is not leaked from the test environment.
	os.Unsetenv("AGENTCTL_WINDOW_ID")
	os.Unsetenv("AGENTCTL_SESSION_ID")
	os.Unsetenv("TMUX_PANE")
}
