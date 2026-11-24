package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/prompttemplate"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/tmux"
)

// The hub's "new session" action: N (listed under ?) opens
// an inline directory prompt in the footer. Enter opens an agent in that
// directory as its own agentctl project: a live group with the same project
// dir just gets a new agent (launchForGroup); anything else goes to the
// hidden `agentctl hub-new-session <dir>` helper, run as a subprocess with
// cwd = dir, which initializes the project if needed, starts its tmux session
// detached and launches the agent.

// fleetNewSessionKey opens the directory prompt.
const fleetNewSessionKey = "N"

// fleetNewSessionPrompt leads the directory prompt in the footer.
const fleetNewSessionPrompt = "  new session in: "

// fleetNewSessionHints replace the footer hints while the prompt is open.
var fleetNewSessionHints = []fleetHint{
	{"⏎", "open"},
	{"esc", "cancel"},
}

// Seams so tests never read the real origin file or spawn agentctl.
var (
	fleetHubOrigin     = tmux.HubOrigin
	checkNewSessionDir = tmux.CheckNewSessionDir
	fleetUserHome      = os.UserHomeDir
	fleetNewSessionRun = runHubNewSessionHelper
)

// runHubNewSessionHelper runs `agentctl hub-new-session <dir>` with cwd = dir
// and env, returning its stdout and, on failure, the last line of its output.
func runHubNewSessionHelper(dir string, env []string) (string, error) {
	return runAgentctlHelper(dir, env, "hub-new-session", dir)
}

// runAgentctlHelper runs `agentctl <args>` with cwd = dir and env, returning
// its stdout and, on failure, the last line of its output.
func runAgentctlHelper(dir string, env []string, args ...string) (string, error) {
	exe, err := selfpath.Spawnable()
	if err != nil {
		return "", fmt.Errorf("locating agentctl: %w", err)
	}
	c := exec.Command(exe, args...)
	c.Dir = dir
	c.Env = env
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		msg = strings.TrimPrefix(msg, "Error: ")
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%s: %w", msg, err)
	}
	return stdout.String(), nil
}

// fleetNewSessionEnv builds the helper's environment: the hub's identity
// (fleetLaunchStripEnv, which includes AGENTCTL_INSTANCE), its runtime dir and its PWD are dropped, and the
// runtime dir is pinned to <dir>/.agentctl so the helper binds to the new
// project, never the hub. Pure: parent is passed in.
func fleetNewSessionEnv(dir string, parent []string) []string {
	out := make([]string, 0, len(parent)+2)
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if name == "AGENTCTL_RUNTIME_DIR" || name == "PWD" || containsString(fleetLaunchStripEnv, name) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "PWD="+dir, "AGENTCTL_RUNTIME_DIR="+filepath.Join(dir, ".agentctl"))
}

// fleetNewSessionCmd runs the helper for dir in the background and reports
// like a per-group launch, so the new row is selected the same way.
func fleetNewSessionCmd(dir string) tea.Cmd {
	session := tmux.SessionNameForProjectRoot(dir)
	return func() tea.Msg {
		out, err := fleetNewSessionRun(dir, fleetNewSessionEnv(dir, os.Environ()))
		if err != nil {
			return fleetLaunchedMsg{session: session, err: fmt.Errorf("new session in %s: %w", dir, err)}
		}
		return fleetLaunchedMsg{session: session, windowID: parseLaunchWindowID(out)}
	}
}

// fleetDefaultDir is the prompt's prefill: where `agentctl hub` was last run
// from, else $HOME.
func fleetDefaultDir() string {
	if dir, ok := fleetHubOrigin(); ok {
		return dir
	}
	home, err := fleetUserHome()
	if err != nil {
		return ""
	}
	return home
}

// resolveNewSessionDir turns the prompt's input into a clean absolute path:
// "~" and "~/..." expand to home, relative paths are taken against base, and
// empty input means base.
func resolveNewSessionDir(input, base, home string) (string, error) {
	p := strings.TrimSpace(input)
	switch {
	case p == "":
		p = base
	case p == "~" || strings.HasPrefix(p, "~/"):
		if home == "" {
			return "", fmt.Errorf("cannot expand ~: home directory unknown")
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	case strings.HasPrefix(p, "~"):
		return "", fmt.Errorf("unsupported path: %s", p)
	}
	if !filepath.IsAbs(p) {
		if base == "" {
			return "", fmt.Errorf("not an absolute path: %s", p)
		}
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p), nil
}

// groupForDir finds the live, launchable group whose project dir is dir,
// comparing with symlinks resolved on both sides.
func (m *FleetModel) groupForDir(dir string) *fleet.Group {
	for i := range m.groups {
		g := &m.groups[i]
		if groupCanLaunch(*g) && tmux.SamePath(g.ProjectDir, dir) {
			return g
		}
	}
	return nil
}

// ── Prompt state ─────────────────────────────────────────────────────────────

// newSessionOpen reports whether the directory prompt is showing.
func (m FleetModel) newSessionOpen() bool { return m.dirPrompt != nil }

// fleetNewSessionState is the open directory prompt: the input and the
// directory relative paths resolve against (the prefill).
type fleetNewSessionState struct {
	input textinput.Model
	base  string

	// Template choice (fleet_newsession_tmpl.go); tmpls is empty, and the
	// rest unused, when no templates exist.
	tmpls                []prompttemplate.Template
	agents               []string // known agent names; empty = unchecked
	sel                  int      // 0 = none, i = tmpls[i-1]
	focus                int
	agent, model, prompt textinput.Model
	loadedFor            string // dir the templates were listed for

	// Recent projects (fleet_newsession_recent.go): candidates after the
	// live-group filter, the highlighted visible entry (-1 = none) and
	// whether the user edited the input (the prefill is not a filter).
	recent []string
	hl     int
	edited bool
}

// openNewSession opens the directory prompt, prefilled with the default dir.
// Every other inline state (selector, close confirmation, menu) closes.
func (m *FleetModel) openNewSession() tea.Cmd {
	if m.newSessionOpen() {
		return nil // keep what was typed
	}
	m.cancelClose()
	m.cancelNew()
	m.cancelRename()
	m.closeMenu()
	m.jumpErr = ""
	base := fleetDefaultDir()
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 4096
	s := ti.Styles()
	s.Cursor.Blink = false
	ti.SetStyles(s)
	ti.SetWidth(fleetNewSessionInputWidth(m.contentWidth()))
	ti.SetValue(base)
	ti.CursorEnd()
	ti.Focus()
	st := &fleetNewSessionState{
		input: ti, base: base, focus: nsFieldDir,
		agent: newSessionInput(""), model: newSessionInput(""), prompt: newSessionInput(""),
		hl: -1,
	}
	st.loadTemplates(base)
	st.setFocus(nsFieldDir)
	m.dirPrompt = st
	m.ensureCursorVisible()
	origin, _ := fleetHubOrigin()
	return recentDirsCmd(origin)
}

// cancelNewSession closes the prompt.
func (m *FleetModel) cancelNewSession() { m.dirPrompt = nil }

// handleNewSessionKey owns every key while the prompt is open, so typed text
// never triggers a hub binding.
func (m *FleetModel) handleNewSessionKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.cancelNewSession()
		return nil
	case "enter":
		if d, ok := m.highlightedRecent(); ok {
			return m.openRecent(d)
		}
		return m.submitNewSession()
	case "ctrl+c":
		m.quitting = true
		return tea.Quit
	}
	if m.recentKey(msg.String()) {
		return nil
	}
	if m.handleNewSessionFormKey(msg.String()) {
		return nil
	}
	before := m.dirPrompt.input.Value()
	cmd := m.updateNewSessionInput(msg)
	if m.dirPrompt.focus == nsFieldDir && m.dirPrompt.input.Value() != before {
		m.dirPrompt.edited = true
		m.dirPrompt.hl = -1
	}
	return cmd
}

// submitNewSession validates the typed directory and starts the action. An
// invalid directory keeps the prompt open with the error in the footer.
func (m *FleetModel) submitNewSession() tea.Cmd {
	home, _ := fleetUserHome()
	dir, err := resolveNewSessionDir(m.dirPrompt.input.Value(), m.dirPrompt.base, home)
	if err == nil {
		err = checkNewSessionDir(dir)
	}
	if err != nil {
		m.jumpErr = err.Error()
		return nil
	}
	if t := m.dirPrompt.chosen(); t != nil {
		// Templates and agents come from the typed dir, not the last one listed.
		name := t.Name
		m.newSessionReload()
		if m.dirPrompt.chosen() == nil {
			m.jumpErr = fmt.Sprintf("template %s not found in %s", name, dir)
			return nil
		}
		return m.submitNewSessionTemplate(dir)
	}
	m.cancelNewSession()
	if g := m.groupForDir(dir); g != nil {
		return m.launchForGroup(g)
	}
	m.jumpErr = ""
	m.setProgress("starting a session in " + dir + "…")
	return fleetNewSessionCmd(dir)
}

// fleetNewSessionInputWidth is the input's width on a cw-wide footer line
// (one column stays free for the cursor).
func fleetNewSessionInputWidth(cw int) int {
	return max(cw-lipgloss.Width(fleetNewSessionPrompt)-fleetSidebarRightGutter-1, 4)
}

// newSessionView renders the prompt line for content width cw. The input is
// sized here, not when the prompt opens, so a resize never overflows it.
func (m FleetModel) newSessionView(cw int) string {
	if m.dirPrompt.hasTemplates() {
		return m.newSessionFormView(cw)
	}
	in := m.dirPrompt.input
	in.SetWidth(fleetNewSessionInputWidth(cw))
	in.SetCursor(in.Position()) // re-run the scroll for this width
	return strings.Join(append([]string{fleetBlockedStyle.Render(fleetNewSessionPrompt) + in.View()}, m.recentLines(cw)...), "\n")
}
