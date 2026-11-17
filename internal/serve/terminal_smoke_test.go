package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
)

// TestTerminalSmokeRealTmux drives the live terminal against a real tmux
// server on a private socket dir (never the user's server). It checks the
// property that matters most: a phone-sized viewer client never resizes the
// laptop's window. Opt in with AGENTCTL_TMUX_SMOKE=1.
func TestTerminalSmokeRealTmux(t *testing.T) {
	if os.Getenv("AGENTCTL_TMUX_SMOKE") != "1" {
		t.Skip("set AGENTCTL_TMUX_SMOKE=1 to run against a real tmux server")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("TMUX_TMPDIR", shortTempDir(t))
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %s: %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	const home = "agentctl-smoke"
	run("-f", "/dev/null", "new-session", "-d", "-s", home, "-x", "100", "-y", "30", "sh")
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-server").Run() })
	run("set-option", "-g", "status", "off") // the window is then exactly the client size
	win := run("display-message", "-p", "-t", "="+home+":", "#{window_id}")

	// The "laptop": a normal client at 100x30.
	laptop, err := creackPTYs{}.Start("tmux", []string{"attach-session", "-t", "=" + home}, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = laptop.Close() })
	go func() { _, _ = drainPTY(laptop) }()
	waitSize := func(cols, rows int) {
		t.Helper()
		waitFor(t, "window size", func() bool {
			c, r, err := tmux.WindowSize(win)
			return err == nil && c == cols && r == rows
		})
	}
	waitSize(100, 30)

	// A phone-sized nested client on a viewer session must not resize it.
	viewer, err := tmux.CreateMobileViewer(home, win)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := creackPTYs{}.Start("tmux", tmux.MobileViewerAttachArgs(viewer, false), 40, 12)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = drainPTY(phone) }()
	waitFor(t, "phone attached", func() bool {
		return strings.Contains(run("list-clients", "-F", "#{client_session}"), viewer)
	})
	time.Sleep(300 * time.Millisecond)
	if c, r, _ := tmux.WindowSize(win); c != 100 || r != 30 {
		t.Fatalf("phone client resized the laptop window to %dx%d", c, r)
	}
	if names := tmux.ListSessionsWithPaths(); slices.ContainsFunc(names, func(s tmux.SessionInfo) bool { return s.Name == viewer }) {
		t.Errorf("discovery lists the viewer: %+v", names)
	}
	_ = phone.Close()
	waitFor(t, "viewer destroyed with its client", func() bool {
		v, _ := tmux.MobileViewerSessions()
		return len(v) == 0
	})
	if _, _, err := tmux.WindowSize(win); err != nil {
		t.Fatalf("home window gone after the viewer died: %v", err)
	}

	// End to end through the server.
	fleet := &fakeFleet{}
	row := herd.Row{Session: home, Project: "smoke", AgentState: state.AgentState{Name: "sh", WindowID: win, State: "idle"}}
	fleet.set(fleetwatch.Group{Session: home, Project: "smoke", RuntimeDir: "/tmp/smoke/.agentctl",
		Windows: []herd.Window{herd.ToWindow(row, herd.Meta{WindowID: win})}, Rows: []herd.Row{row}})
	for _, write := range []bool{false, true} {
		e := newWriteEnvWith(t, write, func(o *Options) { o.Fleet = fleet })
		srv := httptest.NewServer(e.Handler())
		h := http.Header{"Origin": {srv.URL}, "Cookie": {DeviceCookie + "=" + e.tok}}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/sessions/"+home+"/windows/"+win+"/terminal",
			&websocket.DialOptions{HTTPHeader: h})
		if err != nil {
			t.Fatal(err)
		}
		if m := readSize(t, c); m.Cols != 100 || m.Rows != 30 || m.ReadOnly == write {
			t.Errorf("write=%v: size = %+v", write, m)
		}
		marker := "smoke" + map[bool]string{false: "ro", true: "rw"}[write]
		_ = c.Write(ctx, websocket.MessageBinary, []byte("echo "+marker+"\r"))
		time.Sleep(500 * time.Millisecond)
		pane := run("capture-pane", "-p", "-t", win)
		if got := strings.Contains(pane, marker); got != write {
			t.Errorf("write=%v: input reached the pane = %v\n%s", write, got, pane)
		}
		if c, r, _ := tmux.WindowSize(win); c != 100 || r != 30 {
			t.Errorf("write=%v: window resized to %dx%d", write, c, r)
		}
		// The default prefix plus "c" (new-window) and ":" (command prompt)
		// must reach the pane, never tmux.
		_ = c.Write(ctx, websocket.MessageBinary, []byte("\x02c\x02:"))
		time.Sleep(300 * time.Millisecond)
		if n := run("list-windows", "-t", "="+home+":", "-F", "#{window_id}"); n != win {
			t.Errorf("write=%v: phone keys reached tmux: windows %q", write, n)
		}
		_ = c.Close(websocket.StatusNormalClosure, "")
		waitFor(t, "viewer killed on close", func() bool {
			v, _ := tmux.MobileViewerSessions()
			return len(v) == 0
		})
		cancel()
		srv.Close()
	}
}

// drainPTY drains a PTY until it closes.
func drainPTY(p PTY) (int64, error) {
	buf := make([]byte, 4096)
	var n int64
	for {
		k, err := p.Read(buf)
		n += int64(k)
		if err != nil {
			return n, err
		}
	}
}
