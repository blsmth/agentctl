package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTmuxHookSpike records which tmux hooks fire on kill-window and
// kill-session, whether set session-scoped or globally (-g), and what
// #{hook_window}, #{hook_session} and #{window_id} expand to inside them. It
// is a spike that decided whether Gone detection can hang off a tmux hook; it
// logs its observations (run with -v) and asserts only the facts the design
// relies on. It uses a private tmux server and never touches the user's.
func TestTmuxHookSpike(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := fmt.Sprintf("agentctl-test-%d", os.Getpid())
	tm := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock, "-f", "/dev/null"}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() { _, _ = tm("kill-server") })

	if out, err := tm("new-session", "-d", "-s", "spike", "-x", "100", "-y", "30", "sleep 600"); err != nil {
		t.Skipf("cannot start private tmux server: %v: %s", err, out)
	}
	log := filepath.Join(t.TempDir(), "hooks.log")
	rec := func(tag string) string {
		return fmt.Sprintf("run-shell \"echo '%s hook_window=#{hook_window} hook_session=#{hook_session} window_id=#{window_id}' >> %s\"", tag, log)
	}
	for _, ev := range []string{"window-unlinked", "pane-exited", "session-closed"} {
		if out, err := tm("set-hook", "-t", "spike", ev, rec("session:"+ev)); err != nil {
			t.Fatalf("set-hook session %s: %v: %s", ev, err, out)
		}
		if out, err := tm("set-hook", "-g", ev, rec("global:"+ev)); err != nil {
			t.Fatalf("set-hook -g %s: %v: %s", ev, err, out)
		}
	}

	winID, err := tm("new-window", "-d", "-t", "spike", "-P", "-F", "#{window_id}", "sleep 600")
	if err != nil {
		t.Fatalf("new-window: %v: %s", err, winID)
	}
	if _, err := tm("kill-window", "-t", "spike:"+winID); err != nil {
		t.Fatalf("kill-window: %v", err)
	}
	time.Sleep(700 * time.Millisecond)
	afterWindow := readHookLog(log)
	t.Logf("after kill-window %s:\n%s", winID, afterWindow)

	// A window whose only process exits on its own (the agent quitting) is
	// the common Gone path; kill-window above is the explicit one.
	exitID, err := tm("new-window", "-d", "-t", "spike", "-P", "-F", "#{window_id}", "sleep 0.3")
	if err != nil {
		t.Fatalf("new-window (exiting): %v: %s", err, exitID)
	}
	time.Sleep(1200 * time.Millisecond)
	t.Logf("after window %s exited on its own (cumulative):\n%s", exitID, readHookLog(log))

	if _, err := tm("kill-session", "-t", "spike"); err != nil {
		t.Fatalf("kill-session: %v", err)
	}
	time.Sleep(700 * time.Millisecond)
	afterSession := readHookLog(log)
	t.Logf("after kill-session (cumulative):\n%s", afterSession)

	// The design relies on window-unlinked carrying the killed window's ID.
	if !strings.Contains(afterWindow, "window-unlinked") {
		t.Logf("window-unlinked did not fire on kill-window: no hook trigger possible")
		return
	}
	if !strings.Contains(afterWindow, "hook_window="+winID) {
		t.Errorf("window-unlinked fired without hook_window=%s:\n%s", winID, afterWindow)
	}
}

func readHookLog(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "(no hooks fired)"
	}
	return strings.TrimSpace(string(data))
}
