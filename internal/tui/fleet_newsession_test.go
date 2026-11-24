package tui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/prompttemplate"
)

// stubNewSession points the prompt's default dir at origin (missing when
// ""), $HOME at home, and records helper and per-group launch calls.
type newSessionCalls struct {
	dirs     []string
	envs     [][]string
	launched []string
}

func stubNewSession(t *testing.T, origin, home string) *newSessionCalls {
	t.Helper()
	calls := &newSessionCalls{}
	origO, origH, origR, origL := fleetHubOrigin, fleetUserHome, fleetNewSessionRun, fleetLaunch
	t.Cleanup(func() { fleetHubOrigin, fleetUserHome, fleetNewSessionRun, fleetLaunch = origO, origH, origR, origL })
	fleetHubOrigin = func() (string, bool) { return origin, origin != "" }
	fleetUserHome = func() (string, error) {
		if home == "" {
			return "", errors.New("no home")
		}
		return home, nil
	}
	t.Setenv("HOME", home) // tmux.CheckNewSessionDir reads it
	origT, origA := fleetNewSessionTemplates, fleetNewSessionAgents
	t.Cleanup(func() { fleetNewSessionTemplates, fleetNewSessionAgents = origT, origA })
	fleetNewSessionTemplates = func(string) []prompttemplate.Template { return nil }
	fleetNewSessionAgents = func(string) []string { return nil }
	fleetNewSessionRun = func(dir string, env []string) (string, error) {
		calls.dirs = append(calls.dirs, dir)
		calls.envs = append(calls.envs, env)
		return "some output\n@77\n", nil
	}
	fleetLaunch = func(g fleet.Group) tea.Cmd {
		calls.launched = append(calls.launched, g.Session)
		return func() tea.Msg { return fleetLaunchedMsg{session: g.Session, windowID: "@88"} }
	}
	return calls
}

func hubKey(t *testing.T, m FleetModel, key string) (FleetModel, tea.Cmd) {
	t.Helper()
	var msg tea.KeyPressMsg
	switch key {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+u":
		msg = tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	default:
		r := []rune(key)[0]
		msg = tea.KeyPressMsg{Code: r, Text: key}
	}
	next, cmd := m.Update(msg)
	return next.(FleetModel), cmd
}

// typeDir clears the prompt and types dir.
func typeDir(t *testing.T, m FleetModel, dir string) FleetModel {
	t.Helper()
	m, _ = hubKey(t, m, "ctrl+u")
	if v := m.dirPrompt.input.Value(); v != "" {
		t.Fatalf("ctrl+u left %q", v)
	}
	for _, r := range dir {
		m, _ = hubKey(t, m, string(r))
	}
	if v := m.dirPrompt.input.Value(); v != dir {
		t.Fatalf("typed %q, prompt holds %q", dir, v)
	}
	return m
}

func TestNewSessionPrefill(t *testing.T) {
	home := t.TempDir()
	origin := t.TempDir()
	t.Run("origin file", func(t *testing.T) {
		stubNewSession(t, origin, home)
		m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
		if !m.newSessionOpen() {
			t.Fatal("N did not open the prompt")
		}
		if v := m.dirPrompt.input.Value(); v != origin {
			t.Errorf("prefill = %q, want origin %q", v, origin)
		}
		if !strings.Contains(ansi.Strip(m.footerBody(m.contentWidth())), strings.TrimSpace(fleetNewSessionPrompt)) {
			t.Error("footer does not show the prompt")
		}
	})
	t.Run("missing origin falls back to home", func(t *testing.T) {
		stubNewSession(t, "", home)
		m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
		if v := m.dirPrompt.input.Value(); v != home {
			t.Errorf("prefill = %q, want $HOME %q", v, home)
		}
	})
}

func TestResolveNewSessionDir(t *testing.T) {
	tests := []struct {
		in, want, wantErr string
	}{
		{"", "/base", ""},
		{"  ", "/base", ""},
		{"~", "/home/u", ""},
		{"~/src/proj/", "/home/u/src/proj", ""},
		{"proj", "/base/proj", ""},
		{"../other", "/other", ""},
		{"/abs/./x/../y", "/abs/y", ""},
		{"~bob/x", "", "unsupported"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := resolveNewSessionDir(tt.in, "/base", "/home/u")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("= %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if _, err := resolveNewSessionDir("~", "/base", ""); err == nil {
		t.Error("~ with unknown home should fail")
	}
}

func TestNewSessionRejects(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	file := filepath.Join(base, "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, input, wantErr string
	}{
		{"nonexistent", filepath.Join(base, "nope"), "no such directory: " + filepath.Join(base, "nope")},
		{"relative nonexistent", "nope", "no such directory: " + filepath.Join(base, "nope")},
		{"file", "notes.txt", "not a directory: " + file},
		{"home", "~", "home directory"},
		{"root", "/", "refusing to start a project in /"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := stubNewSession(t, base, home)
			m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
			m = typeDir(t, m, tt.input)
			m, cmd := hubKey(t, m, "enter")
			if cmd != nil {
				t.Error("an invalid dir returned a command")
			}
			if !strings.Contains(m.jumpErr, tt.wantErr) {
				t.Errorf("jumpErr = %q, want %q", m.jumpErr, tt.wantErr)
			}
			if !m.newSessionOpen() {
				t.Error("prompt closed on an invalid dir")
			}
			if got := ansi.Strip(m.footerBody(m.contentWidth())); !strings.Contains(got, m.jumpErr[:min(len(m.jumpErr), 12)]) {
				t.Errorf("footer lacks the error: %q", got)
			}
			if len(calls.dirs)+len(calls.launched) != 0 {
				t.Error("an invalid dir launched something")
			}
			// Typing clears the error and keeps the input.
			m, _ = hubKey(t, m, "x")
			if m.jumpErr != "" || !m.newSessionOpen() {
				t.Errorf("after typing: err=%q open=%v", m.jumpErr, m.newSessionOpen())
			}
		})
	}
}

func TestNewSessionExistingProjectLaunchesInGroup(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	link := filepath.Join(t.TempDir(), "proj-link")
	if err := os.Symlink(proj, link); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{proj, link, proj + "/"} {
		t.Run(input, func(t *testing.T) {
			calls := stubNewSession(t, home, home)
			m := foldModel(t)
			m.groups[1].ProjectDir = proj // beta
			m, _ = hubKey(t, m, fleetNewSessionKey)
			m = typeDir(t, m, input)
			m, cmd := hubKey(t, m, "enter")
			if cmd == nil {
				t.Fatal("no command")
			}
			msg := cmd()
			if !slices.Equal(calls.launched, []string{"s2"}) || len(calls.dirs) != 0 {
				t.Fatalf("launched=%v helper=%v; want launch in s2 only", calls.launched, calls.dirs)
			}
			if m.newSessionOpen() {
				t.Error("prompt still open")
			}
			next, _ := m.Update(msg)
			m = next.(FleetModel)
			if m.pendingFocus == nil || m.pendingFocus.windowID != "@88" {
				t.Errorf("pendingFocus = %+v, want @88", m.pendingFocus)
			}
		})
	}
}

func TestNewSessionNewProjectRunsHelper(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(t.TempDir(), "My Proj")
	if err := os.Mkdir(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	calls := stubNewSession(t, filepath.Dir(proj), home)
	t.Setenv("AGENTCTL_RUNTIME_DIR", "/hub/.agentctl")
	t.Setenv("AGENTCTL_INSTANCE", "hub")
	t.Setenv("AGENTCTL_WINDOW_ID", "@1")
	t.Setenv("TMUX_PANE", "%1")
	t.Setenv("PWD", "/hub")

	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	m = typeDir(t, m, "My Proj") // relative to the default dir
	m, cmd := hubKey(t, m, "enter")
	if cmd == nil {
		t.Fatal("no command")
	}
	if !strings.Contains(m.infoMsg, proj) {
		t.Errorf("progress = %q", m.infoMsg)
	}
	msg := cmd()
	if len(calls.launched) != 0 || !slices.Equal(calls.dirs, []string{proj}) {
		t.Fatalf("launched=%v helper=%v", calls.launched, calls.dirs)
	}
	env := calls.envs[0]
	for _, kv := range env {
		name, val, _ := strings.Cut(kv, "=")
		switch name {
		case "AGENTCTL_INSTANCE", "AGENTCTL_WINDOW_ID", "TMUX_PANE":
			t.Errorf("hub identity leaked: %s", kv)
		case "AGENTCTL_RUNTIME_DIR":
			if val != filepath.Join(proj, ".agentctl") {
				t.Errorf("runtime dir = %q, want the project's", val)
			}
		case "PWD":
			if val != proj {
				t.Errorf("PWD = %q", val)
			}
		}
	}
	lm, ok := msg.(fleetLaunchedMsg)
	if !ok || lm.err != nil || lm.session != "agentctl-My-Proj" || lm.windowID != "@77" {
		t.Fatalf("msg = %+v", msg)
	}
	next, _ := m.Update(msg)
	m = next.(FleetModel)
	if m.pendingFocus == nil || m.pendingFocus.session != "agentctl-My-Proj" || m.pendingFocus.windowID != "@77" {
		t.Errorf("pendingFocus = %+v", m.pendingFocus)
	}
}

func TestNewSessionHelperError(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	stubNewSession(t, proj, home)
	fleetNewSessionRun = func(string, []string) (string, error) { return "", errors.New("boom") }
	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	m, cmd := hubKey(t, m, "enter")
	next, _ := m.Update(cmd())
	m = next.(FleetModel)
	if !strings.Contains(m.jumpErr, "boom") || m.infoMsg != "" {
		t.Errorf("jumpErr=%q info=%q", m.jumpErr, m.infoMsg)
	}
}

func TestFleetNewSessionEnv(t *testing.T) {
	parent := []string{
		"HOME=/h", "TMUX=/tmp/tmux,1,0", "PWD=/hub", "AGENTCTL_RUNTIME_DIR=/hub/.agentctl",
		"AGENTCTL_INSTANCE=hub", "AGENTCTL_SESSION_ID=x", "AGENTCTL_WINDOW_ID=@1", "TMUX_PANE=%1",
	}
	got := fleetNewSessionEnv("/p/new", parent)
	want := []string{"HOME=/h", "TMUX=/tmp/tmux,1,0", "PWD=/p/new", "AGENTCTL_RUNTIME_DIR=/p/new/.agentctl"}
	if !slices.Equal(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
}

func TestNewSessionKeysWhileOpen(t *testing.T) {
	stubNewSession(t, "/start", t.TempDir())
	m := foldModel(t)
	cursor := m.splitPane.Cursor()
	m, _ = hubKey(t, m, fleetNewSessionKey)
	for _, k := range []string{"j", "q", "x", "n", "?", "S", "z", "f"} {
		var cmd tea.Cmd
		m, cmd = hubKey(t, m, k)
		if cmd != nil {
			if _, quit := cmd().(tea.QuitMsg); quit {
				t.Fatalf("%q quit the hub", k)
			}
		}
	}
	if v := m.dirPrompt.input.Value(); v != "/startjqxn?Szf" {
		t.Errorf("prompt = %q, want the typed text", v)
	}
	if m.splitPane.Cursor() != cursor || m.closeArmed() || m.newArmed() || m.menuOpen || m.filter != fleetFilterAll || m.serveBusy {
		t.Error("typed text triggered a hub binding")
	}
	m, _ = hubKey(t, m, "esc")
	if m.newSessionOpen() {
		t.Fatal("esc did not cancel")
	}
	// Closed again: hub keys work.
	m, _ = hubKey(t, m, "j")
	if m.splitPane.Cursor() == cursor {
		t.Error("j swallowed after the prompt closed")
	}
	m, _ = hubKey(t, m, "?")
	if !m.menuOpen || !m.menuHelp {
		t.Error("? swallowed after the prompt closed")
	}
}

func TestNewSessionClosesOtherInlineStates(t *testing.T) {
	stubNewSession(t, "/start", t.TempDir())
	m := foldModel(t)
	m.armNew(0)
	m, _ = hubKey(t, m, fleetNewSessionKey)
	if m.newArmed() || !m.newSessionOpen() {
		t.Errorf("selector armed=%v prompt open=%v", m.newArmed(), m.newSessionOpen())
	}
}

func TestTitleOpenButtonOpensPrompt(t *testing.T) {
	stubNewSession(t, "/start", t.TempDir())
	m := foldModel(t)
	m.liveLeft = 0 // the whole 120 columns: room for anything
	l := m.titleLayout(m.contentWidth())
	line := ansi.Strip(l.line)
	if strings.Contains(line, "+ new") || strings.Contains(line, "+ session") {
		t.Errorf("title shows a stale button: %q", line)
	}
	if l.open[1] == 0 || ansi.Cut(line, l.open[0], l.open[1]) != fleetOpenButton {
		t.Fatalf("open span %v covers %q in %q", l.open, ansi.Cut(line, l.open[0], l.open[1]), line)
	}
	// Only the button's columns open the prompt.
	for x := 0; x < m.contentWidth(); x++ {
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: 0})
		got := next.(FleetModel).newSessionOpen()
		if want := x >= l.open[0] && x < l.open[1]; got != want {
			t.Fatalf("click at %d: prompt open = %v, want %v", x, got, want)
		}
	}
}

func TestTitleOpenButtonDroppedWhenNarrow(t *testing.T) {
	m := foldModel(t)
	m.serveKnown = true
	for _, w := range []int{20, 30, 40, 50, 60, 80, 120} {
		with := m.titleLayout(w)
		plain := ansi.Strip(with.line)
		if ansi.StringWidth(plain) > w {
			t.Errorf("w=%d: title %d wide", w, ansi.StringWidth(plain))
		}
		if s := with.open; s[1] > 0 && ansi.Cut(plain, s[0], s[1]) != fleetOpenButton {
			t.Errorf("w=%d: open span %v covers %q", w, s, ansi.Cut(plain, s[0], s[1]))
		}
		if with.open[1] == 0 && strings.Contains(plain, fleetOpenButton) {
			t.Errorf("w=%d: button drawn without a span: %q", w, plain)
		}
	}
	if l := m.titleLayout(120); l.open[1] == 0 {
		t.Error("no button at 120 columns")
	}
	if l := m.titleLayout(30); l.open[1] != 0 {
		t.Error("button shown at 30 columns")
	}
	// Dropping it never costs another element.
	for _, w := range []int{40, 50, 60, 80} {
		l := m.titleLayout(w)
		if l.open[1] == 0 {
			continue
		}
		stripped := ansi.Strip(l.line)
		without := strings.Replace(stripped, "  "+fleetOpenButton, "", 1)
		for _, want := range []string{"agentctl hub", "📱", "⏸ 1"} {
			if strings.Contains(without, want) && !strings.Contains(stripped, want) {
				t.Errorf("w=%d: lost %q", w, want)
			}
		}
	}
}

func TestNewSessionTitleKeepsCounts(t *testing.T) {
	m := foldModel(t)
	m.serveKnown = true // the 📱 badge takes room too
	for _, w := range []int{40, 44, 50, 56, 80} {
		l := m.titleLayout(w)
		plain := ansi.Strip(l.line)
		if ansi.StringWidth(plain) > w {
			t.Errorf("w=%d: title %d wide: %q", w, ansi.StringWidth(plain), plain)
		}
		if w >= 50 && !strings.Contains(plain, "⏸ 1") {
			t.Errorf("w=%d: counts dropped: %q", w, plain)
		}
		if s := l.badge; s[1] > 0 && ansi.Cut(plain, s[0], s[1]) != "📱 off" {
			t.Errorf("w=%d: badge span %v covers %q", w, s, ansi.Cut(plain, s[0], s[1]))
		}
	}
}

func TestNewSessionPromptFitsWidth(t *testing.T) {
	stubNewSession(t, "/a/rather/long/default/directory/for/the/prompt/line", t.TempDir())
	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	for _, cw := range []int{40, 44, 56, 120} {
		if w := ansi.StringWidth(ansi.Strip(m.newSessionView(cw))); w > cw {
			t.Errorf("cw=%d: prompt line is %d wide", cw, w)
		}
	}
}

func TestHelpListsNewSessionKey(t *testing.T) {
	for _, it := range fleetHelpItems() {
		if it.keys == fleetNewSessionKey && it.action == fleetNewSessionKey {
			return
		}
	}
	t.Errorf("? reference lacks %q", fleetNewSessionKey)
}
