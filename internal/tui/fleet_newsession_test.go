package tui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
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
		if !strings.Contains(ansi.Strip(nsSettled(m).render()), filepath.Base(origin)) {
			t.Error("dropdown does not show the directory")
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
			if !strings.Contains(m.dirPrompt.err, tt.wantErr) {
				t.Errorf("err = %q, want %q", m.dirPrompt.err, tt.wantErr)
			}
			if m.jumpErr != "" {
				t.Errorf("error leaked to the footer: %q", m.jumpErr)
			}
			if !m.newSessionOpen() {
				t.Error("prompt closed on an invalid dir")
			}
			if got := ansi.Strip(nsSettled(m).render()); !strings.Contains(got, m.dirPrompt.err[:min(len(m.dirPrompt.err), 12)]) {
				t.Errorf("dropdown lacks the error: %q", got)
			}
			if strings.Contains(ansi.Strip(m.footerBody(m.contentWidth())), m.dirPrompt.err) {
				t.Error("footer shows the error")
			}
			if len(calls.dirs)+len(calls.launched) != 0 {
				t.Error("an invalid dir launched something")
			}
			// Typing clears the error and keeps the input.
			m, _ = hubKey(t, m, "x")
			if m.dirPrompt.err != "" || !m.newSessionOpen() {
				t.Errorf("after typing: err=%q open=%v", m.dirPrompt.err, m.newSessionOpen())
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
		if s := l.badge; s[1] > 0 && ansi.Cut(plain, s[0], s[1]) != "📱 down" {
			t.Errorf("w=%d: badge span %v covers %q", w, s, ansi.Cut(plain, s[0], s[1]))
		}
	}
}

func TestNewSessionPanelFitsWidth(t *testing.T) {
	stubNewSession(t, "/a/rather/long/default/directory/for/the/prompt/line", t.TempDir())
	for _, w := range []int{24, 29, 30, 44, 56, 80, 120} {
		m := foldModel(t)
		m.termWidth = w
		m.liveLeft = 0
		m, _ = hubKey(t, m, fleetNewSessionKey)
		m = nsSettled(m)
		cw := m.contentWidth()
		for i, l := range m.nsPanelLines() {
			if lw := ansi.StringWidth(ansi.Strip(l)); lw > cw {
				t.Errorf("w=%d row %d is %d wide, content %d", w, i, lw, cw)
			}
		}
		if x := m.nsPanelX(); x < 0 || x+nsPanelWidth(cw) > cw {
			t.Errorf("w=%d: panel at %d overflows %d", w, x, cw)
		}
		if cw < fleetNsNarrow && nsPanelWidth(cw) != cw {
			t.Errorf("w=%d: narrow panel is %d, want full width %d", w, nsPanelWidth(cw), cw)
		}
	}
}

// nsSettled finishes the slide-in so the panel is at full height.
func nsSettled(m FleetModel) FleetModel {
	if m.dirPrompt != nil {
		m.dirPrompt.animOn, m.dirPrompt.animP = false, 1
	}
	return m
}

func TestDropdownAnchoredUnderButton(t *testing.T) {
	stubNewSession(t, "/start", t.TempDir())
	m := foldModel(t)
	m.liveLeft = 0
	btn := m.titleLayout(m.contentWidth()).open
	if btn[1] == 0 {
		t.Fatal("no button")
	}
	m, _ = hubKey(t, m, fleetNewSessionKey)
	m = nsSettled(m)
	if x := m.nsPanelX(); x != btn[0] {
		t.Errorf("panel x = %d, button at %d", x, btn[0])
	}
	lines := strings.Split(ansi.Strip(m.render()), "\n")
	if !strings.HasPrefix(ansi.Cut(lines[1], btn[0], btn[0]+1), "╭") {
		t.Errorf("row 1 lacks the panel's corner under the button: %q", lines[1])
	}
	if !strings.Contains(lines[2], "/start") {
		t.Errorf("row 2 lacks the input: %q", lines[2])
	}
	if !strings.Contains(lines[0], "agentctl hub") {
		t.Errorf("title overdrawn: %q", lines[0])
	}
}

func TestDropdownClampsAndHiddenButton(t *testing.T) {
	stubNewSession(t, "/start", t.TempDir())
	m := foldModel(t)
	m.liveLeft = 0
	m.termWidth = 60
	cw := m.contentWidth()
	m, _ = hubKey(t, m, fleetNewSessionKey)
	if x, w := m.nsPanelX(), nsPanelWidth(cw); x+w > cw {
		t.Errorf("panel [%d,%d) overflows %d", x, x+w, cw)
	}
	// A hidden button anchors at the left edge.
	n := foldModel(t)
	n.termWidth = 40 // the narrowest content width
	n.serveKnown = true
	n, _ = hubKey(t, n, fleetNewSessionKey)
	if n.titleLayout(n.contentWidth()).open[1] != 0 {
		return // the button fits here; nothing hidden to test
	}
	if x := n.nsPanelX(); x != 0 {
		t.Errorf("hidden button: panel x = %d, want 0", x)
	}
}

func TestDropdownClickOutsideCloses(t *testing.T) {
	stubNewSession(t, "/start", t.TempDir())
	m := foldModel(t)
	m.liveLeft = 0
	m, _ = hubKey(t, m, fleetNewSessionKey)
	m = nsSettled(m)
	x, y, w, h := m.nsPanelRect()
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x + 2, Y: y + 1})
	if !next.(FleetModel).newSessionOpen() {
		t.Error("click on the input closed the dropdown")
	}
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x + w - 1, Y: y + h - 1})
	if !next.(FleetModel).newSessionOpen() {
		t.Error("click on the panel's corner closed the dropdown")
	}
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y + h + 2})
	if next.(FleetModel).newSessionOpen() {
		t.Error("click below the panel kept it open")
	}
	// The button toggles it shut instead of reopening.
	btn := m.titleLayout(m.contentWidth()).open
	next, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: btn[0], Y: 0})
	if next.(FleetModel).newSessionOpen() {
		t.Error("clicking + open while open left it open")
	}
}

func TestDropdownSlideTicks(t *testing.T) {
	stubNewSession(t, "/start", t.TempDir())
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	orig := fleetNow
	t.Cleanup(func() { fleetNow = orig })
	fleetNow = func() time.Time { return now }

	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	gen := m.nsAnimGen
	if !m.dirPrompt.animOn || m.dirPrompt.animP != 0 {
		t.Fatalf("not sliding on open: %+v", m.dirPrompt)
	}
	full := len(m.nsPanelLines())
	if h := m.nsVisibleRows(full); h != 0 {
		t.Errorf("visible rows at p=0: %d", h)
	}
	// Keys typed mid-slide land in the input.
	m, _ = hubKey(t, m, "x")
	if v := m.dirPrompt.input.Value(); v != "/startx" {
		t.Errorf("input = %q", v)
	}
	// A stale tick is ignored.
	now = now.Add(fleetNsSlideDur / 10)
	next, cmd := m.Update(nsDropTickMsg{gen: gen - 1})
	m = next.(FleetModel)
	if cmd != nil || m.dirPrompt.animP != 0 {
		t.Errorf("stale tick advanced the slide: p=%v", m.dirPrompt.animP)
	}
	// A current one advances it, partway.
	next, _ = m.Update(nsDropTickMsg{gen: gen})
	m = next.(FleetModel)
	if p := m.dirPrompt.animP; p <= 0 || p >= 1 || !m.dirPrompt.animOn {
		t.Errorf("mid tick: p=%v on=%v", p, m.dirPrompt.animOn)
	}
	if h := m.nsVisibleRows(full); h <= 0 || h >= full {
		t.Errorf("visible rows mid-slide = %d of %d", h, full)
	}
	// Past the duration it settles.
	now = now.Add(fleetNsSlideDur)
	next, cmd = m.Update(nsDropTickMsg{gen: gen})
	m = next.(FleetModel)
	if cmd != nil || m.dirPrompt.animOn || m.dirPrompt.animP != 1 || m.nsVisibleRows(full) != full {
		t.Errorf("not settled: %+v", m.dirPrompt)
	}
	// Closing and reopening makes the first open's ticks stale.
	m, _ = hubKey(t, m, "esc")
	m, _ = hubKey(t, m, fleetNewSessionKey)
	next, _ = m.Update(nsDropTickMsg{gen: gen})
	if next.(FleetModel).dirPrompt.animP != 0 {
		t.Error("a tick of the earlier open moved the new slide")
	}
}

func TestDropdownKeyAndButtonParity(t *testing.T) {
	stubNewSession(t, "/start", t.TempDir())
	m := foldModel(t)
	m.liveLeft = 0
	byKey, _ := hubKey(t, m, fleetNewSessionKey)
	btn := m.titleLayout(m.contentWidth()).open
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: btn[0], Y: 0})
	byBtn := next.(FleetModel)
	if !byBtn.newSessionOpen() {
		t.Fatal("button did not open")
	}
	if byKey.dirPrompt.input.Value() != byBtn.dirPrompt.input.Value() || byKey.nsAnimGen != byBtn.nsAnimGen ||
		byKey.dirPrompt.animOn != byBtn.dirPrompt.animOn {
		t.Errorf("key and button differ: %+v vs %+v", byKey.dirPrompt, byBtn.dirPrompt)
	}
	if ansi.Strip(nsSettled(byKey).render()) != ansi.Strip(nsSettled(byBtn).render()) {
		t.Error("renders differ")
	}
}

func TestDropdownFooterHints(t *testing.T) {
	stubNewSession(t, "/start", t.TempDir())
	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	foot := ansi.Strip(m.footerBody(m.contentWidth()))
	for _, want := range []string{"open", "cancel"} {
		if !strings.Contains(foot, want) {
			t.Errorf("footer lacks %q: %q", want, foot)
		}
	}
	if strings.Contains(foot, "/start") {
		t.Errorf("footer still holds the prompt: %q", foot)
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
