package tmux

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// stubChromeBarEnabled sets the chrome_bar switch for one test.
func stubChromeBarEnabled(t *testing.T, on bool) {
	t.Helper()
	old := chromeBarEnabled
	t.Cleanup(func() { chromeBarEnabled = old })
	chromeBarEnabled = func() bool { return on }
}

func TestStatusLineOptions(t *testing.T) {
	th := theme.Theme{Base: "#000001", Surface: "#000002", Subtle: "#000003", Bright: "#000004", Accent: "#000005"}

	t.Run("chrome bar on turns the status line off", func(t *testing.T) {
		got := statusLineOptions(true, th)
		if want := [][2]string{{"status", "off"}}; !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("chrome bar off shows a window list", func(t *testing.T) {
		opts := map[string]string{}
		for _, o := range statusLineOptions(false, th) {
			opts[o[0]] = o[1]
		}
		if opts["status"] != "on" {
			t.Errorf("status = %q, want on", opts["status"])
		}
		if opts["status-style"] != "fg=#000003,bg=#000001" {
			t.Errorf("status-style = %q", opts["status-style"])
		}
		format := opts["status-format[0]"]
		for _, sub := range []string{"#{session_name}", "#{W:", "#{window_name}", "range=window|#{window_index}", "#000002"} {
			if !strings.Contains(format, sub) {
				t.Errorf("status-format[0] %q lacks %q", format, sub)
			}
		}
		// The #{W:a,b} loop takes exactly two formats: a comma inside a style
		// would split them wrong.
		inner := format[strings.Index(format, "#{W:")+len("#{W:") : len(format)-1]
		if n := strings.Count(inner, ","); n != 1 {
			t.Errorf("window loop has %d commas, want 1: %q", n, inner)
		}
	})
}

func TestPlanChromeWindow(t *testing.T) {
	const bar = "'/bin/agentctl' chrome-bar @1"
	tests := []struct {
		name    string
		enabled bool
		win     string
		ws      windowstate.WindowState
		panes   map[string]string
		want    chromeAction
	}{
		{"off, no record: nothing", false, "w", windowstate.WindowState{},
			map[string]string{"%1": "claude"}, chromeAction{}},
		{"off, live bar: kill and clear", false, "w", windowstate.WindowState{ChromeBarPane: "%2", MainPane: "%1"},
			map[string]string{"%1": "claude", "%2": bar}, chromeAction{kill: "%2", clear: "%2"}},
		{"off, bar pane gone: clear only", false, "w", windowstate.WindowState{ChromeBarPane: "%2"},
			map[string]string{"%1": "claude"}, chromeAction{clear: "%2"}},
		{"off, record points at a non-bar pane: clear only", false, "w", windowstate.WindowState{ChromeBarPane: "%2"},
			map[string]string{"%1": "claude", "%2": "zsh"}, chromeAction{clear: "%2"}},
		{"off, record equals main pane: never killed", false, "w", windowstate.WindowState{ChromeBarPane: "%2", MainPane: "%2"},
			map[string]string{"%1": "claude", "%2": bar}, chromeAction{clear: "%2"}},
		{"off, bar is the last pane: never killed", false, "w", windowstate.WindowState{ChromeBarPane: "%2"},
			map[string]string{"%2": bar}, chromeAction{clear: "%2"}},
		{"on, no bar: launch", true, "w", windowstate.WindowState{},
			map[string]string{"%1": "claude"}, chromeAction{launch: true}},
		{"on, live bar: nothing", true, "w", windowstate.WindowState{ChromeBarPane: "%2"},
			map[string]string{"%1": "claude", "%2": bar}, chromeAction{}},
		{"on, stale record: clear and launch", true, "w", windowstate.WindowState{ChromeBarPane: "%2"},
			map[string]string{"%1": "claude"}, chromeAction{clear: "%2", launch: true}},
		{"on, unrecorded bar: nothing", true, "w", windowstate.WindowState{},
			map[string]string{"%1": "claude", "%3": bar}, chromeAction{}},
		{"on, utility window: nothing", true, "tickets", windowstate.WindowState{},
			map[string]string{"%1": "tickets"}, chromeAction{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := planChromeWindow(tt.enabled, tt.win, tt.ws, tt.panes); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParsePaneCommands(t *testing.T) {
	got := parsePaneCommands("%1\tclaude --name x\n%2\t'/bin/agentctl' chrome-bar @3\n\ngarbage")
	if len(got) != 2 || got["%1"] != "claude --name x" || !isChromeBarCommand(got["%2"]) {
		t.Errorf("got %q", got)
	}
}

// fakeChromeSync records what syncChromeBars does.
type fakeChromeSync struct {
	wins     []WindowInfo
	listErr  error
	records  map[string]windowstate.WindowState
	panes    map[string]string
	listed   []string
	killed   []string
	cleared  []string
	launched []string
}

func (f *fakeChromeSync) deps() chromeSync {
	return chromeSync{
		listWindows: func() ([]WindowInfo, error) { return f.wins, f.listErr },
		listPanes: func(id string) (string, error) {
			f.listed = append(f.listed, id)
			out, ok := f.panes[id]
			if !ok {
				return "", errors.New("no such window")
			}
			return out, nil
		},
		read:     func(id string) windowstate.WindowState { return f.records[id] },
		killPane: func(p string) error { f.killed = append(f.killed, p); return nil },
		clear:    func(id, p string) { f.cleared = append(f.cleared, id+"="+p) },
		launch:   func(id string) error { f.launched = append(f.launched, id); return nil },
	}
}

func newFakeChromeSync() *fakeChromeSync {
	return &fakeChromeSync{
		wins: []WindowInfo{{ID: "@1", Name: "shell"}, {ID: "@2", Name: "● worker"}, {ID: "@3", Name: "tickets"}, {ID: "@4", Name: "bare"}},
		records: map[string]windowstate.WindowState{
			"@1": {ChromeBarPane: "%11", MainPane: "%10"},
			"@2": {ChromeBarPane: "%21", MainPane: "%20"},
			"@4": {MainPane: "%40"},
		},
		panes: map[string]string{
			"@1": "%10\tzsh\n%11\t'/bin/agentctl' chrome-bar @1",
			"@2": "%20\tclaude\n%21\t'/bin/agentctl' chrome-bar @2",
			"@3": "%30\t'/bin/agentctl' tickets",
			"@4": "%40\tclaude",
		},
	}
}

func TestSyncChromeBars(t *testing.T) {
	t.Run("off kills recorded bars only and clears the records", func(t *testing.T) {
		f := newFakeChromeSync()
		syncChromeBars(false, f.deps())
		if want := []string{"%11", "%21"}; !slices.Equal(f.killed, want) {
			t.Errorf("killed %v, want %v", f.killed, want)
		}
		if want := []string{"@1=%11", "@2=%21"}; !slices.Equal(f.cleared, want) {
			t.Errorf("cleared %v, want %v", f.cleared, want)
		}
		if len(f.launched) != 0 {
			t.Errorf("launched %v, want none", f.launched)
		}
		// Windows with no recorded bar cost no tmux call.
		if want := []string{"@1", "@2"}; !slices.Equal(f.listed, want) {
			t.Errorf("listed panes of %v, want %v", f.listed, want)
		}
	})

	t.Run("on launches bars into windows missing one, skipping utility windows", func(t *testing.T) {
		f := newFakeChromeSync()
		f.records["@1"] = windowstate.WindowState{MainPane: "%10"}
		f.panes["@1"] = "%10\tzsh"
		syncChromeBars(true, f.deps())
		if want := []string{"@1", "@4"}; !slices.Equal(f.launched, want) {
			t.Errorf("launched %v, want %v", f.launched, want)
		}
		if len(f.killed) != 0 || len(f.cleared) != 0 {
			t.Errorf("killed %v cleared %v, want none", f.killed, f.cleared)
		}
	})

	t.Run("a failing window does not stop the rest", func(t *testing.T) {
		f := newFakeChromeSync()
		delete(f.panes, "@1")
		syncChromeBars(false, f.deps())
		if want := []string{"%21"}; !slices.Equal(f.killed, want) {
			t.Errorf("killed %v, want %v", f.killed, want)
		}
	})

	t.Run("no session or no windows is a no-op", func(t *testing.T) {
		for _, f := range []*fakeChromeSync{{listErr: errors.New("no server")}, {}} {
			syncChromeBars(true, f.deps())
			syncChromeBars(false, f.deps())
			if len(f.listed)+len(f.killed)+len(f.launched) != 0 {
				t.Errorf("acted with no windows: %+v", f)
			}
		}
	})
}

// TestLaunchChromeBarGate checks LaunchChromeBar creates a bar only when
// chrome_bar is on. tmux is unreachable in tests (testenv), so the window is
// neither the hub nor a utility window and its main pane cannot be found.
func TestLaunchChromeBarGate(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[on], func(t *testing.T) {
			windowstate.SetDirForTest(t.TempDir())
			stubChromeBarEnabled(t, on)
			var calls []string
			old := chromeBarRun
			t.Cleanup(func() { chromeBarRun = old })
			chromeBarRun = func(args ...string) (string, error) {
				calls = append(calls, args[0])
				if args[0] == "list-panes" {
					return "%5", nil
				}
				return "%6", nil
			}

			if err := LaunchChromeBar("@5"); err != nil {
				t.Fatalf("LaunchChromeBar: %v", err)
			}
			ws, _ := windowstate.Read("@5")
			if !on {
				if len(calls) != 0 || ws.ChromeBarPane != "" {
					t.Errorf("off: tmux calls %v, chrome_bar_pane %q; want none", calls, ws.ChromeBarPane)
				}
				return
			}
			if want := []string{"list-panes", "split-window"}; !slices.Equal(calls, want) {
				t.Errorf("on: tmux calls %v, want %v", calls, want)
			}
			if ws.ChromeBarPane != "%6" {
				t.Errorf("on: chrome_bar_pane = %q, want %%6", ws.ChromeBarPane)
			}
		})
	}
}

// TestStatusLineOnRealTmux sets the chrome-bar-off status options on a
// private tmux server and checks tmux accepts them and the window list
// expands to every window name.
func TestStatusLineOnRealTmux(t *testing.T) {
	p := newPrivateTmux(t)
	p.run("new-window", "-d", "-t", "s:", "-n", "● worker", "sleep 30")
	var format string
	for _, o := range statusLineOptions(false, theme.Theme{Base: "#1e1e2e", Surface: "#313244", Subtle: "#a6adc8", Bright: "#f5e0dc", Accent: "#89b4fa"}) {
		p.run("set-option", "-t", "=s:", o[0], o[1])
		if o[0] == "status-format[0]" {
			format = o[1]
		}
	}
	if got := p.run("show-options", "-v", "-t", "=s:", "status"); got != "on" {
		t.Errorf("status = %q, want on", got)
	}
	got := p.run("display-message", "-p", "-t", "=s:", format)
	for _, sub := range []string{" s ", " ● worker ", "range=window|1"} {
		if !strings.Contains(got, sub) {
			t.Errorf("expanded status %q lacks %q", got, sub)
		}
	}
}
