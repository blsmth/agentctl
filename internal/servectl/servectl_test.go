package servectl

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/launchd"
)

// fakeServe is the world a Controller sees: a launchd job and a serve
// process, driven by the launchctl calls the Controller makes.
type fakeServe struct {
	t         *testing.T
	installed bool
	loaded    bool
	alive     bool // a serve process holds the pidfile
	answering bool // its control socket answers
	managed   bool // launchd started it (bootout stops it)
	// upAfterStart: kickstart/bootstrap brings up an answering server.
	upAfterStart bool
	// ignoreTerm: the process survives SIGTERM.
	ignoreTerm bool
	calls      []string
	terminated []int
}

const fakePID = 4242

func (f *fakeServe) controller() Controller {
	home := f.t.TempDir()
	if f.installed {
		path := launchd.PlistPath(home, Label)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("<plist/>"), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	return Controller{
		Dir:  "/serve",
		GOOS: "darwin",
		Agent: launchd.Agent{Home: home, UID: 501, Sleep: func(time.Duration) {}, Run: func(name string, args ...string) ([]byte, error) {
			f.calls = append(f.calls, strings.Join(args, " "))
			switch args[0] {
			case "print":
				if !f.loaded {
					return nil, errors.New("not loaded")
				}
			case "bootout":
				if !f.loaded {
					return nil, errors.New("not loaded")
				}
				f.loaded = false
				if f.managed {
					f.alive, f.answering = false, false
				}
			case "bootstrap":
				f.loaded = true
				f.start()
			case "kickstart":
				f.start()
			}
			return nil, nil
		}},
		Running: func(string) (int, bool) { return fakePID, f.alive },
		URL: func(string) (string, error) {
			if f.alive && f.answering {
				return "https://mac.local:8443/", nil
			}
			return "", errors.New("not answering")
		},
		Terminate: func(pid int) error {
			f.terminated = append(f.terminated, pid)
			if !f.ignoreTerm {
				f.alive, f.answering = false, false
			}
			return nil
		},
		Sleep: func(time.Duration) {},
		Wait:  time.Second,
	}
}

func (f *fakeServe) start() {
	if f.upAfterStart {
		f.alive, f.answering, f.managed = true, true, true
	}
}

func TestControllerStart(t *testing.T) {
	tests := []struct {
		name      string
		f         fakeServe
		goos      string
		want      string
		wantErr   string
		wantCalls []string
	}{
		{
			name: "already running",
			f:    fakeServe{installed: true, loaded: true, alive: true, answering: true},
			want: "serve is already running at https://mac.local:8443/",
		},
		{
			name:      "installed, not loaded: bootstrap",
			f:         fakeServe{installed: true, upAfterStart: true},
			want:      "serve started at https://mac.local:8443/",
			wantCalls: []string{"print gui/501/" + Label, "bootstrap gui/501 "},
		},
		{
			name:      "loaded, not running: kickstart",
			f:         fakeServe{installed: true, loaded: true, upAfterStart: true},
			want:      "serve started at https://mac.local:8443/",
			wantCalls: []string{"print gui/501/" + Label, "kickstart gui/501/" + Label},
		},
		{
			name:      "loaded, socket dead: kickstart -k",
			f:         fakeServe{installed: true, loaded: true, alive: true, upAfterStart: true},
			want:      "serve started at https://mac.local:8443/",
			wantCalls: []string{"print gui/501/" + Label, "kickstart -k gui/501/" + Label},
		},
		{
			name:    "not installed",
			f:       fakeServe{},
			wantErr: "serve install",
		},
		{
			name:    "unmanaged process with a dead socket",
			f:       fakeServe{installed: true, alive: true},
			wantErr: "outside launchd",
		},
		{
			name:    "started but never answers",
			f:       fakeServe{installed: true},
			wantErr: "not answering yet",
		},
		{
			name: "linux prints systemctl",
			f:    fakeServe{},
			goos: "linux",
			want: "Run: systemctl --user start agentctl-serve",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.f
			f.t = t
			c := f.controller()
			if tt.goos != "" {
				c.GOOS = tt.goos
			}
			got, err := c.Start()
			checkResult(t, got, err, tt.want, tt.wantErr)
			checkCalls(t, f.calls, tt.wantCalls)
			if len(f.terminated) > 0 {
				t.Errorf("Start terminated %v", f.terminated)
			}
		})
	}
}

func TestControllerStop(t *testing.T) {
	tests := []struct {
		name           string
		f              fakeServe
		goos           string
		want           string
		wantErr        string
		wantCalls      []string
		wantTerminated bool
	}{
		{
			name:      "launchd job: bootout, plist stays",
			f:         fakeServe{installed: true, loaded: true, alive: true, answering: true, managed: true},
			want:      "serve stopped (it starts again at login, or with agentctl serve start)",
			wantCalls: []string{"print gui/501/" + Label, "bootout gui/501/" + Label},
		},
		{
			name:           "unmanaged process: SIGTERM",
			f:              fakeServe{installed: true, alive: true, answering: true},
			want:           "serve stopped",
			wantCalls:      []string{"print gui/501/" + Label},
			wantTerminated: true,
		},
		{
			name:           "not installed, unmanaged process",
			f:              fakeServe{alive: true},
			want:           "serve stopped",
			wantTerminated: true,
		},
		{
			name:           "process ignores SIGTERM",
			f:              fakeServe{alive: true, ignoreTerm: true},
			wantErr:        "did not exit",
			wantTerminated: true,
		},
		{
			name:      "loaded but plist deleted: still bootout",
			f:         fakeServe{loaded: true, alive: true, answering: true, managed: true},
			want:      "serve stopped (it starts again at login, or with agentctl serve start)",
			wantCalls: []string{"print gui/501/" + Label, "bootout gui/501/" + Label},
		},
		{
			name: "nothing running",
			f:    fakeServe{installed: true},
			want: "serve is not running",
		},
		{
			name: "linux prints systemctl",
			f:    fakeServe{alive: true},
			goos: "linux",
			want: "Run: systemctl --user stop agentctl-serve",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.f
			f.t = t
			c := f.controller()
			if tt.goos != "" {
				c.GOOS = tt.goos
			}
			got, err := c.Stop()
			checkResult(t, got, err, tt.want, tt.wantErr)
			checkCalls(t, f.calls, tt.wantCalls)
			if (len(f.terminated) > 0) != tt.wantTerminated {
				t.Errorf("terminated = %v, want terminated %v", f.terminated, tt.wantTerminated)
			}
			if tt.f.installed {
				if _, err := os.Stat(launchd.PlistPath(c.Agent.Home, Label)); err != nil {
					t.Errorf("plist removed: %v", err)
				}
			}
		})
	}
}

func TestControllerToggle(t *testing.T) {
	t.Run("running stops", func(t *testing.T) {
		f := fakeServe{t: t, installed: true, loaded: true, alive: true, answering: true, managed: true}
		got, err := f.controller().Toggle()
		checkResult(t, got, err, "serve stopped (it starts again at login, or with agentctl serve start)", "")
	})
	t.Run("unresponsive stops", func(t *testing.T) {
		f := fakeServe{t: t, installed: true, loaded: true, alive: true, managed: true}
		got, err := f.controller().Toggle()
		checkResult(t, got, err, "serve stopped (it starts again at login, or with agentctl serve start)", "")
	})
	t.Run("off starts", func(t *testing.T) {
		f := fakeServe{t: t, installed: true, upAfterStart: true}
		got, err := f.controller().Toggle()
		checkResult(t, got, err, "serve started at https://mac.local:8443/", "")
	})
	t.Run("off and not installed", func(t *testing.T) {
		f := fakeServe{t: t}
		_, err := f.controller().Toggle()
		if !errors.Is(err, ErrNotInstalled) {
			t.Errorf("err = %v, want ErrNotInstalled", err)
		}
	})
}

func checkResult(t *testing.T, got string, err error, want, wantErr string) {
	t.Helper()
	if wantErr != "" {
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("err = %v, want one containing %q", err, wantErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// checkCalls compares launchctl calls by prefix (bootstrap carries a temp
// plist path). A nil want skips the check.
func checkCalls(t *testing.T, got, want []string) {
	t.Helper()
	if want == nil {
		return
	}
	if len(got) != len(want) {
		t.Fatalf("launchctl calls = %q, want %q", got, want)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("call %d = %q, want prefix %q", i, got[i], want[i])
		}
	}
}

func TestProbe(t *testing.T) {
	tests := []struct {
		name    string
		alive   bool
		dialErr error
		want    State
	}{
		{"no process", false, errors.New("no socket"), StateOff},
		{"alive and answering", true, nil, StateRunning},
		{"alive, socket dead", true, errors.New("refused"), StateUnresponsive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checked := false
			got := probe("/d", func(string) (int, bool) { checked = true; return 1, tt.alive }, func(string) error { return tt.dialErr })
			if got != tt.want {
				t.Errorf("probe = %v, want %v", got, tt.want)
			}
			if tt.dialErr == nil && checked {
				t.Error("ran the pidfile check (a ps fork) although the socket answered")
			}
		})
	}
}

func TestProbeMissingDir(t *testing.T) {
	if got := Probe(filepath.Join(t.TempDir(), "nope")); got != StateOff {
		t.Errorf("Probe(missing dir) = %v, want off", got)
	}
}

// shortDir returns a temp dir short enough for a unix socket path.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestDialControlAndQueryURL(t *testing.T) {
	dir := shortDir(t)
	if err := dialControl(dir); err == nil {
		t.Fatal("dialControl succeeded with no socket")
	}
	ln, err := net.Listen("unix", filepath.Join(dir, ControlSocket))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 512)
			n, _ := c.Read(buf)
			if strings.Contains(string(buf[:n]), `"status"`) {
				_, _ = c.Write([]byte(`{"status":{"url":"https://h:1/"}}` + "\n"))
			}
			_ = c.Close()
		}
	}()
	if err := dialControl(dir); err != nil {
		t.Errorf("dialControl: %v", err)
	}
	url, err := QueryURL(dir)
	if err != nil || url != "https://h:1/" {
		t.Errorf("QueryURL = %q, %v", url, err)
	}
}

func TestNewRefusesLaunchctlInTests(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Agent.Run("launchctl", "print", "x"); !errors.Is(err, errTestLaunchctl) {
		t.Errorf("Run err = %v, want errTestLaunchctl", err)
	}
	if err := c.Terminate(os.Getpid()); !errors.Is(err, errTestLaunchctl) {
		t.Errorf("Terminate err = %v, want errTestLaunchctl", err)
	}
}
