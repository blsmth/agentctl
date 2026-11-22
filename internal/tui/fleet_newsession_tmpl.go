package tui

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/prompttemplate"
	"github.com/blsmth/agentctl/internal/tmux"
)

// Template choice in the new-session prompt (N). When prompt templates exist
// the footer grows from the single directory line into a small form: a
// template picker, the directory, and, once a template is chosen, agent,
// model and prompt, all prefilled from it and all editable. With no templates
// the prompt is the plain directory line, unchanged. Launching a template
// goes through `agentctl hub-new-session <dir> --template ...`, which
// dispatches with the template so the session records its name. Authoring
// stays in `agentctl template`.

// New-session form fields, in focus order.
const (
	nsFieldTemplate = iota
	nsFieldDir
	nsFieldAgent
	nsFieldModel
	nsFieldPrompt
)

// Seams so tests never read real templates or agents or spawn agentctl.
var (
	fleetNewSessionTemplates   = listPromptTemplates
	fleetNewSessionAgents      = listAgentNames
	fleetNewSessionTemplateRun = runHubNewSessionTemplateHelper
)

// listPromptTemplates is every template visible from dir. A missing templates
// dir yields none; any error yields none (the choice is simply hidden).
func listPromptTemplates(dir string) []prompttemplate.Template {
	ts, _, err := prompttemplate.List(dir)
	if err != nil {
		return nil
	}
	return ts
}

// listAgentNames is the names of the agents visible from dir, or nil when
// discovery fails (then agent names are not validated here).
func listAgentNames(dir string) []string {
	cfg := config.Load()
	as, err := agent.DiscoverAll(cfg, agent.ProjectDirs(cfg, dir))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(as))
	for _, a := range as {
		names = append(names, a.Name)
	}
	return names
}

// runHubNewSessionTemplateHelper runs `agentctl hub-new-session <dir>
// <extra...>`.
func runHubNewSessionTemplateHelper(dir string, env []string, extra []string) (string, error) {
	return runAgentctlHelper(dir, env, append([]string{"hub-new-session", dir}, extra...)...)
}

func newSessionInput(value string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 8192
	s := ti.Styles()
	s.Cursor.Blink = false
	ti.SetStyles(s)
	ti.SetValue(value)
	ti.CursorEnd()
	return ti
}

// hasTemplates reports whether the template choice is shown.
func (s *fleetNewSessionState) hasTemplates() bool { return len(s.tmpls) > 0 }

// chosen is the selected template, or nil for "none".
func (s *fleetNewSessionState) chosen() *prompttemplate.Template {
	if s.sel <= 0 || s.sel > len(s.tmpls) {
		return nil
	}
	return &s.tmpls[s.sel-1]
}

// fields lists the visible fields in focus order.
func (s *fleetNewSessionState) fields() []int {
	switch {
	case !s.hasTemplates():
		return []int{nsFieldDir}
	case s.chosen() == nil:
		return []int{nsFieldTemplate, nsFieldDir}
	}
	return []int{nsFieldTemplate, nsFieldDir, nsFieldAgent, nsFieldModel, nsFieldPrompt}
}

// input is the text input of a text field.
func (s *fleetNewSessionState) inputOf(f int) *textinput.Model {
	switch f {
	case nsFieldAgent:
		return &s.agent
	case nsFieldModel:
		return &s.model
	case nsFieldPrompt:
		return &s.prompt
	}
	return &s.input
}

// loadTemplates lists the templates visible from dir and the known agents,
// keeping the chosen template when it is still there.
func (s *fleetNewSessionState) loadTemplates(dir string) {
	keep := ""
	if t := s.chosen(); t != nil {
		keep = t.Name
	}
	s.tmpls = fleetNewSessionTemplates(dir)
	s.agents = fleetNewSessionAgents(dir)
	s.loadedFor = dir
	s.sel = 0
	for i, t := range s.tmpls {
		if t.Name == keep {
			s.sel = i + 1
		}
	}
	if !slices.Contains(s.fields(), s.focus) {
		s.focus = nsFieldDir
	}
}

// choose selects template i (0 = none) and prefills from it.
func (s *fleetNewSessionState) choose(i int) {
	n := len(s.tmpls) + 1
	s.sel = (i%n + n) % n
	t := s.chosen()
	if t == nil {
		return
	}
	s.agent.SetValue(t.Agent)
	s.agent.CursorEnd()
	s.model.SetValue(t.Model)
	s.model.CursorEnd()
	s.prompt.SetValue("") // extra instructions only; the body is never copied here
	s.prompt.CursorEnd()
	if d := t.ExpandedDir(); d != "" {
		s.input.SetValue(d)
		s.input.CursorEnd()
	}
}

// setFocus moves focus to field f.
func (s *fleetNewSessionState) setFocus(f int) {
	s.focus = f
	for _, g := range []int{nsFieldDir, nsFieldAgent, nsFieldModel, nsFieldPrompt} {
		in := s.inputOf(g)
		if g == f {
			in.Focus()
		} else {
			in.Blur()
		}
	}
}

// step moves focus by delta along the visible fields. Leaving the directory
// field re-lists the templates when the directory changed.
func (m *FleetModel) newSessionStep(delta int) {
	s := m.dirPrompt
	fs := s.fields()
	i := slices.Index(fs, s.focus)
	if i < 0 {
		i = 0
	}
	if s.focus == nsFieldDir {
		m.newSessionReload()
		fs = s.fields()
		i = slices.Index(fs, s.focus)
	}
	s.setFocus(fs[(i+delta+len(fs))%len(fs)])
}

// newSessionReload re-lists the templates for the typed directory.
func (m *FleetModel) newSessionReload() {
	s := m.dirPrompt
	home, _ := fleetUserHome()
	dir, err := resolveNewSessionDir(s.input.Value(), s.base, home)
	if err != nil || dir == s.loadedFor {
		return
	}
	s.loadTemplates(dir)
}

// handleNewSessionFormKey handles the keys the form adds. handled is false
// for keys that belong to the focused input.
func (m *FleetModel) handleNewSessionFormKey(key string) bool {
	s := m.dirPrompt
	if !s.hasTemplates() {
		return false
	}
	switch key {
	case "tab", "down":
		m.newSessionStep(1)
		return true
	case "shift+tab", "up":
		m.newSessionStep(-1)
		return true
	}
	if s.focus == nsFieldTemplate {
		switch key {
		case "right", "l":
			s.choose(s.sel + 1)
		case "left", "h":
			s.choose(s.sel - 1)
		}
		return true // the picker takes no text
	}
	return false
}

// updateNewSessionInput feeds msg to the focused input.
func (m *FleetModel) updateNewSessionInput(msg tea.Msg) tea.Cmd {
	s := m.dirPrompt
	in := s.inputOf(s.focus)
	var cmd tea.Cmd
	*in, cmd = in.Update(msg)
	return cmd
}

// submitNewSessionTemplate launches the chosen template in dir after checking
// the agent. Problems keep the form open with the error in the footer.
func (m *FleetModel) submitNewSessionTemplate(dir string) tea.Cmd {
	s := m.dirPrompt
	t := s.chosen()
	agentName := strings.TrimSpace(s.agent.Value())
	switch {
	case agentName == "":
		m.jumpErr = fmt.Sprintf("template %s sets no agent: fill in the agent field", t.Name)
		return nil
	case len(s.agents) > 0 && !slices.Contains(s.agents, agentName):
		m.jumpErr = fmt.Sprintf("agent %q not found: pick another (%s)", agentName, strings.Join(s.agents, ", "))
		s.setFocus(nsFieldAgent)
		return nil
	}
	args := []string{"--template", t.Name, "--agent", agentName}
	if v := strings.TrimSpace(s.model.Value()); v != "" {
		args = append(args, "--model", v)
	}
	if extra := strings.TrimSpace(s.prompt.Value()); extra != "" {
		args = append(args, "--prompt="+extra)
	}
	m.cancelNewSession()
	m.jumpErr = ""
	m.setProgress("starting " + t.Name + " in " + dir + "…")
	return fleetNewSessionTemplateCmd(dir, args)
}

// fleetNewSessionTemplateCmd runs the helper with the template args in the
// background and reports like fleetNewSessionCmd.
func fleetNewSessionTemplateCmd(dir string, args []string) tea.Cmd {
	session := tmux.SessionNameForProjectRoot(dir)
	return func() tea.Msg {
		out, err := fleetNewSessionTemplateRun(dir, fleetNewSessionEnv(dir, os.Environ()), args)
		if err != nil {
			return fleetLaunchedMsg{session: session, err: fmt.Errorf("new session in %s: %w", dir, err)}
		}
		return fleetLaunchedMsg{session: session, windowID: parseLaunchWindowID(out)}
	}
}

// newSessionHints are the footer hints while the prompt is open.
func (m FleetModel) newSessionHints() []fleetHint {
	s := m.dirPrompt
	if !s.hasTemplates() {
		return fleetNewSessionHints
	}
	hs := []fleetHint{{"tab", "field"}}
	if s.focus == nsFieldTemplate {
		hs = append(hs, fleetHint{"←/→", "template"})
	}
	return append(hs, fleetHint{"⏎", "open"}, fleetHint{"esc", "cancel"})
}

// newSessionLines is how many footer lines the prompt takes.
func (m FleetModel) newSessionLines() int {
	if !m.newSessionOpen() {
		return 0
	}
	return len(m.dirPrompt.fields())
}

// Labels of the form lines, all the same width.
var nsLabels = map[int]string{
	nsFieldTemplate: "  template: ",
	nsFieldDir:      "  dir:      ",
	nsFieldAgent:    "  agent:    ",
	nsFieldModel:    "  model:    ",
	nsFieldPrompt:   "  extra:    ",
}

// newSessionFormView renders the form lines for content width cw.
func (m FleetModel) newSessionFormView(cw int) string {
	s := m.dirPrompt
	var lines []string
	for _, f := range s.fields() {
		label := nsLabels[f]
		style := fleetDimStyle
		if f == s.focus {
			style = fleetBlockedStyle
		}
		var body string
		if f == nsFieldTemplate {
			body = s.templateLabel()
		} else {
			in := *s.inputOf(f)
			note := ""
			if f == nsFieldAgent && s.agentMissing() {
				note = "  not found"
			}
			in.SetWidth(max(cw-lipgloss.Width(label)-fleetSidebarRightGutter-1-len(note), 4))
			in.SetCursor(in.Position())
			body = in.View()
			if note != "" {
				body += fleetErrStyle.Render(note)
			}
		}
		lines = append(lines, truncateToWidth(style.Render(label)+body, cw))
	}
	return strings.Join(lines, "\n")
}

// agentMissing reports a non-empty agent that no agent has.
func (s *fleetNewSessionState) agentMissing() bool {
	v := strings.TrimSpace(s.agent.Value())
	return v != "" && len(s.agents) > 0 && !slices.Contains(s.agents, v)
}

// templateLabel is the picker line: ‹ name › (i/n) description.
func (s *fleetNewSessionState) templateLabel() string {
	name, desc := "none", ""
	if t := s.chosen(); t != nil {
		name, desc = t.Name, t.Description
	}
	out := fmt.Sprintf("‹ %s › %d/%d", name, s.sel, len(s.tmpls))
	if desc != "" {
		out += "  " + fleetDimStyle.Render(desc)
	}
	return out
}
