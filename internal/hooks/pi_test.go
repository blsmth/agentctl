package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// usePi switches the hook contract to pi for the test, as main does for
// `agentctl hook-* --harness pi`.
func usePi(t *testing.T, hook string) {
	t.Helper()
	prevHarness, prevHook := active, currentHook
	if err := SetHarness("pi"); err != nil {
		t.Fatal(err)
	}
	currentHook = hook
	t.Cleanup(func() { active, currentHook = prevHarness, prevHook })
}

func TestHarnessFromArgs(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"--harness", "pi"}, "pi"},
		{[]string{"--harness=pi"}, "pi"},
		{[]string{"--other", "x", "--harness", "claude"}, "claude"},
		{[]string{"--harness"}, ""},
	}
	for _, tt := range tests {
		if got := HarnessFromArgs(tt.args); got != tt.want {
			t.Errorf("HarnessFromArgs(%q) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestSetHarnessUnknownKeepsDefault(t *testing.T) {
	prev := active
	t.Cleanup(func() { active = prev })
	if err := SetHarness("codex"); err == nil {
		t.Fatal("want error for unknown harness")
	}
	if active.Name() != "claude" {
		t.Errorf("active = %s after a failed SetHarness", active.Name())
	}
}

// TestValidateBash_PiDenyEncodesBlock: the shared bash gate denies a pi tool
// call and answers in the bridge's {"block","reason"} contract, still
// exiting 2 (which the bridge also treats as a block).
func TestValidateBash_PiDenyEncodesBlock(t *testing.T) {
	usePi(t, "hook-validate-bash")
	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "@1")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	stdout, stderr, codes := captureDenyEmitter(t, func() {
		withStdin(t, `{"session_id":"s","tool_name":"bash","input":{"command":"git push origin main"}}`, ValidateBash)
	})
	var out struct {
		Block  bool   `json:"block"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(stdout, &out); err != nil {
		t.Fatalf("stdout %q: %v", stdout, err)
	}
	if !out.Block || !strings.Contains(out.Reason, "push to main") {
		t.Errorf("decision = %+v", out)
	}
	if strings.Contains(string(stdout), "hookSpecificOutput") {
		t.Errorf("pi got Claude's output contract: %s", stdout)
	}
	if len(codes) == 0 || codes[0] != 2 {
		t.Errorf("exit codes = %v, want 2", codes)
	}
	if !strings.Contains(string(stderr), "push to main") {
		t.Errorf("stderr = %q", stderr)
	}
}

// TestValidateBash_PiAllowsOrdinaryCommand: nothing is printed and nothing
// exits for a command no rule blocks.
func TestValidateBash_PiAllowsOrdinaryCommand(t *testing.T) {
	usePi(t, "hook-validate-bash")
	SetDepsForTest(stubDeps(t))
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", "@1")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	stdout, _, codes := captureDenyEmitter(t, func() {
		withStdin(t, `{"tool_name":"bash","input":{"command":"ls"}}`, ValidateBash)
	})
	if len(stdout) != 0 || len(codes) != 0 {
		t.Errorf("stdout=%q codes=%v, want neither", stdout, codes)
	}
}

// TestSessionStart_PiLinksSessionFile: with no CLAUDE_ENV_FILE (pi has none)
// the session is still linked, to the session file the bridge reported, and
// no Claude orientation JSON is printed.
func TestSessionStart_PiLinksSessionFile(t *testing.T) {
	usePi(t, "hook-session-start")
	setupWindowstate(t)
	SetDepsForTest(stubDeps(t))
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
	t.Setenv("AGENTCTL", "1")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "@8")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "pi-tester")

	file := "/home/u/.pi/agent/sessions/--w--/2026-11-17T20-08-01-304Z_sid-1.jsonl"
	stdout, _ := runHookCaptured(t, nil, `{"session_id":"sid-1","session_file":"`+file+`","cwd":"/w","reason":"startup"}`, SessionStart)

	ws, err := windowstate.Read("@8")
	if err != nil {
		t.Fatal(err)
	}
	if ws.SessionID != "sid-1" || ws.LogPath != file || ws.Harness != "pi" || ws.WorkDir != "/w" {
		t.Errorf("windowstate = session %q log %q harness %q workdir %q", ws.SessionID, ws.LogPath, ws.Harness, ws.WorkDir)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout = %q, want nothing for pi", stdout)
	}
}

// TestSessionStart_ClaudeStillRequiresEnvFile pins the unchanged Claude
// behavior: no CLAUDE_ENV_FILE, no linking.
func TestSessionStart_ClaudeStillRequiresEnvFile(t *testing.T) {
	setupWindowstate(t)
	SetDepsForTest(stubDeps(t))
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
	t.Setenv("AGENTCTL", "1")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "@9")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	runHookCaptured(t, nil, `{"session_id":"sid-2","cwd":"/w"}`, SessionStart)
	if ws, _ := windowstate.Read("@9"); ws.SessionID != "" {
		t.Errorf("claude linked a session without CLAUDE_ENV_FILE: %+v", ws)
	}
}

// TestCheckDone_PiTurnEnd: the bridge's agent_settled drives the same
// idle/done logic as Claude's Stop hook.
func TestCheckDone_PiTurnEnd(t *testing.T) {
	usePi(t, "hook-check-done")
	setupWindowstate(t)
	SetDepsForTest(stubDeps(t))
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
	prevAuto, prevSpawn := autonomousModeFn, spawnCompleteFn
	autonomousModeFn = func(mode string) bool { return mode == "adhoc" }
	var spawned []string
	spawnCompleteFn = func(id string) error { spawned = append(spawned, id); return nil }
	t.Cleanup(func() { autonomousModeFn, spawnCompleteFn = prevAuto, prevSpawn })
	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", "@4")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	if err := windowstate.Update("@4", func(ws *windowstate.WindowState) { ws.State = "working"; ws.Mode = "adhoc" }); err != nil {
		t.Fatal(err)
	}
	runHookCaptured(t, nil, `{"session_id":"s","last_assistant_message":"all done","stop_hook_active":false}`, CheckDone)
	ws, _ := windowstate.Read("@4")
	if ws.State != "idle" || !ws.Done || ws.LastText != "all done" {
		t.Errorf("windowstate = state %q done %v text %q", ws.State, ws.Done, ws.LastText)
	}
	if len(spawned) != 1 || spawned[0] != "@4" {
		t.Errorf("dispatch-complete spawns = %v", spawned)
	}
}

// TestDeliverMailbox_PiContext: mailbox notices reach a pi window as
// {"context": ...}, which the bridge injects with pi.sendMessage.
func TestDeliverMailbox_PiContext(t *testing.T) {
	usePi(t, "hook-post-tool")
	dir := t.TempDir()
	mailbox.SetDirForTest(dir)
	t.Cleanup(func() { mailbox.SetDirForTest("") })
	if _, err := mailbox.Enqueue(mailbox.Message{From: "@1", To: "@2", Kind: mailbox.KindDone, Body: "agentctl notice: worker done"}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { deliverMailbox("PostToolUse", "@2", "") })
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("stdout %q: %v", out, err)
	}
	if !strings.Contains(got["context"], "worker done") {
		t.Errorf("context = %q", got["context"])
	}
}

// withStdin runs fn with os.Stdin fed from payload.
func withStdin(t *testing.T, payload string, fn func()) {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old; f.Close() }()
	fn()
}
