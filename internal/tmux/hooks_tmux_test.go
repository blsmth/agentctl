package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/shellquote"
)

// privateTmux runs tmux against a private, uniquely named server (-L) with no
// user config, so these tests never touch the user's default server.
type privateTmux struct {
	t      *testing.T
	socket string
	env    []string
}

func newPrivateTmux(t *testing.T) *privateTmux {
	t.Helper()
	if os.Getenv("AGENTCTL_TEST_TMUX") == "" {
		t.Skip("set AGENTCTL_TEST_TMUX=1 to run tests that start a private tmux server")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") {
			env = append(env, kv)
		}
	}
	p := &privateTmux{
		t:      t,
		socket: fmt.Sprintf("agentctl-test-%d-%d", os.Getpid(), time.Now().UnixNano()),
		env:    env,
	}
	p.run("-f", "/dev/null", "new-session", "-d", "-s", "s", "-x", "80", "-y", "24", "sleep 60")
	t.Cleanup(func() {
		_ = p.cmd("kill-server").Run()
	})
	return p
}

func (p *privateTmux) cmd(args ...string) *exec.Cmd {
	c := exec.Command("tmux", append([]string{"-L", p.socket}, args...)...)
	c.Env = p.env
	return c
}

func (p *privateTmux) run(args ...string) string {
	p.t.Helper()
	out, err := p.cmd(args...).CombinedOutput()
	if err != nil {
		p.t.Fatalf("tmux %q: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// hostileDir creates a directory whose name carries both a tmux #() command
// substitution and a #{} format, and returns it plus the path the #() payload
// would create if tmux ever expanded it. The payload uses an env var (set in
// the private server's global environment) since the path must be one
// component.
func hostileDir(t *testing.T, p *privateTmux) (dir, hit string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hit = filepath.Join(base, "format-hit")
	p.run("set-environment", "-g", "AGENTCTL_TEST_HIT", hit)
	dir = filepath.Join(base, "proj #(touch $AGENTCTL_TEST_HIT) #{session_name}")
	if err := os.MkdirAll(filepath.Join(dir, ".agentctl"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, hit
}

// standIn writes a fake agentctl into dir that records its runtime dir, args
// and cwd to out.
func standIn(t *testing.T, dir, out string) string {
	t.Helper()
	exe := filepath.Join(dir, "agent ctl")
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s\\n' \"$AGENTCTL_RUNTIME_DIR\" \"$1\" \"$2\" \"$(pwd -P)\" >> " + shellquote.Quote(out) + "\n"
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return exe
}

// waitForFile returns path's contents once non-empty, or "" after a timeout.
func waitForFile(path string) string {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return string(b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return ""
}

func assertNotCreated(t *testing.T, files ...string) {
	t.Helper()
	time.Sleep(300 * time.Millisecond) // let any stray job finish
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("payload ran: %s was created", f)
		}
	}
}

// TestChromeBarHookOnRealTmux applies the real after-new-window hook to a
// private tmux server and creates a window whose name is a shell payload
// (B1), once in a plain project and once in a project whose path is a tmux
// format payload (N3). No payload may run, and the stand-in agentctl must
// receive the paths intact.
func TestChromeBarHookOnRealTmux(t *testing.T) {
	for _, hostile := range []bool{false, true} {
		t.Run(fmt.Sprintf("hostile_path=%v", hostile), func(t *testing.T) {
			p := newPrivateTmux(t)
			dir, hit := hostileDir(t, p)
			if !hostile {
				dir = filepath.Join(filepath.Dir(dir), "plain proj")
				if err := os.MkdirAll(filepath.Join(dir, ".agentctl"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			out := filepath.Join(filepath.Dir(dir), "calls")
			exe := standIn(t, dir, out)
			runtimeDir := filepath.Join(dir, ".agentctl")

			p.run(chromeBarHookArgs("s", exe, runtimeDir, dir)...)

			nameHit := filepath.Join(filepath.Dir(dir), "name-hit")
			windowID := p.run("new-window", "-d", "-t", "s:", "-P", "-F", "#{window_id}",
				"-n", "x$(touch "+nameHit+")`touch "+nameHit+"`", "sleep 30")

			got := waitForFile(out)
			assertNotCreated(t, nameHit, hit)
			want := runtimeDir + "|chrome-bar-pane|" + windowID + "|" + dir + "\n"
			if got != want {
				t.Errorf("stand-in recorded %q, want %q", got, want)
			}
		})
	}
}

// TestActiveWindowHookOnRealTmux applies the real after-select-window hook
// and checks it invokes set-active-window with the right runtime dir and
// window ID, from a format-hostile project path, without running the payload.
func TestActiveWindowHookOnRealTmux(t *testing.T) {
	p := newPrivateTmux(t)
	dir, hit := hostileDir(t, p)
	out := filepath.Join(filepath.Dir(dir), "calls")
	exe := standIn(t, dir, out)
	runtimeDir := filepath.Join(dir, ".agentctl")

	windowID := p.run("new-window", "-d", "-t", "s:", "-P", "-F", "#{window_id}", "sleep 30")
	p.run(activeWindowHookArgs("s", exe, runtimeDir)...)
	p.run("select-window", "-t", windowID)

	got := waitForFile(out)
	assertNotCreated(t, hit)
	if !strings.HasPrefix(got, runtimeDir+"|set-active-window|"+windowID+"|") {
		t.Errorf("stand-in recorded %q, want runtime dir %q and window %s", got, runtimeDir, windowID)
	}
}

// TestFormatEscapeOnRealTmux checks the escaping against tmux itself for the
// positions it is applied to: new-window -n and -c, and rename-window.
func TestFormatEscapeOnRealTmux(t *testing.T) {
	p := newPrivateTmux(t)
	dir, hit := hostileDir(t, p)
	name := "n #(touch $AGENTCTL_TEST_HIT) #{session_name}"

	id := p.run("new-window", "-d", "-t", "s:", "-P", "-F", "#{window_id}",
		"-n", FormatEscape(name), "-c", FormatEscape(dir), "sleep 30")
	if got := p.run("display-message", "-p", "-t", id, "#{window_name}"); got != name {
		t.Errorf("window name = %q, want %q", got, name)
	}
	time.Sleep(200 * time.Millisecond)
	if got := p.run("display-message", "-p", "-t", id, "#{pane_current_path}"); got != dir {
		t.Errorf("pane path = %q, want %q", got, dir)
	}
	p.run("rename-window", "-t", id, FormatEscape(name+"2"))
	if got := p.run("display-message", "-p", "-t", id, "#{window_name}"); got != name+"2" {
		t.Errorf("renamed window = %q, want %q", got, name+"2")
	}
	assertNotCreated(t, hit)
}

func TestSessionIDArgsTrailingColon(t *testing.T) {
	args := sessionIDArgs("foo")
	if got := args[len(args)-2]; got != "=foo:" {
		t.Fatalf("target = %q, want %q", got, "=foo:")
	}
}

func TestSessionIDArgsResolveOnRealTmux(t *testing.T) {
	p := newPrivateTmux(t)
	got := p.run(sessionIDArgs("s")...)
	if !strings.HasPrefix(got, "$") {
		t.Fatalf("session id = %q, want $N", got)
	}
}
