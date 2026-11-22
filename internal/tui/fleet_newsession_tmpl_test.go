package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/prompttemplate"
)

var testTemplates = []prompttemplate.Template{
	{Name: "audit", Description: "audit logs", Agent: "writer", Model: "opus", Prompt: "Audit\nthe logs."},
	{Name: "orphan", Agent: "retired", Prompt: "Do it."},
	{Name: "noagent", Prompt: "x"},
}

type tmplRun struct {
	dir  string
	args []string
}

func stubTemplates(t *testing.T, tmpls []prompttemplate.Template, agents []string) *tmplRun {
	t.Helper()
	home := t.TempDir()
	stubNewSession(t, t.TempDir(), home)
	fleetNewSessionTemplates = func(string) []prompttemplate.Template { return tmpls }
	fleetNewSessionAgents = func(string) []string { return agents }
	run := &tmplRun{}
	fleetNewSessionTemplateRun = func(dir string, _ []string, args []string) (string, error) {
		run.dir, run.args = dir, args
		return "@55\n", nil
	}
	t.Cleanup(func() { fleetNewSessionTemplateRun = runHubNewSessionTemplateHelper })
	return run
}

func pressNS(t *testing.T, m FleetModel, msg tea.KeyPressMsg) FleetModel {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(FleetModel)
}

var (
	keyTab   = tea.KeyPressMsg{Code: tea.KeyTab}
	keyRight = tea.KeyPressMsg{Code: tea.KeyRight}
)

func TestNewSessionNoTemplatesHidesChoice(t *testing.T) {
	stubTemplates(t, nil, nil)
	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	view := ansi.Strip(m.footerBody(m.contentWidth()))
	if strings.Contains(view, "template") {
		t.Errorf("footer shows a template choice with none defined:\n%s", view)
	}
	if m.newSessionLines() != 1 {
		t.Errorf("lines = %d, want 1", m.newSessionLines())
	}
	m = pressNS(t, m, keyTab) // must not move or crash
	if !m.newSessionOpen() {
		t.Error("tab closed the prompt")
	}
}

func TestNewSessionTemplatePrefillAndLaunch(t *testing.T) {
	run := stubTemplates(t, testTemplates, []string{"writer", "go-engineer"})
	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	if !strings.Contains(ansi.Strip(m.footerBody(m.contentWidth())), "template") {
		t.Fatal("template choice hidden")
	}
	// Enter with no template chosen keeps the plain path (no template run).
	m = pressNS(t, m, tea.KeyPressMsg{Code: tea.KeyUp}) // dir -> template
	m = pressNS(t, m, keyRight)                         // none -> audit
	s := m.dirPrompt
	if s.agent.Value() != "writer" || s.model.Value() != "opus" || s.prompt.Value() != "Audit the logs." {
		t.Fatalf("prefill agent=%q model=%q prompt=%q", s.agent.Value(), s.model.Value(), s.prompt.Value())
	}
	if m.newSessionLines() != 5 {
		t.Errorf("lines = %d, want 5", m.newSessionLines())
	}
	// Edit the model: tab to dir, agent, model; type.
	for range 3 {
		m = pressNS(t, m, keyTab)
	}
	m, _ = hubKey(t, m, "x")
	m, cmd := hubKey(t, m, "enter")
	if cmd == nil {
		t.Fatalf("no command; err=%q", m.jumpErr)
	}
	msg := cmd()
	want := []string{"--template", "audit", "--agent", "writer", "--model", "opusx"}
	if !slices.Equal(run.args, want) {
		t.Errorf("args = %v, want %v (prompt untouched is not sent)", run.args, want)
	}
	if lm, ok := msg.(fleetLaunchedMsg); !ok || lm.windowID != "@55" || lm.err != nil {
		t.Errorf("msg = %+v", msg)
	}
}

func TestNewSessionTemplateEditedPromptIsSent(t *testing.T) {
	run := stubTemplates(t, testTemplates, nil)
	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	m = pressNS(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m = pressNS(t, m, keyRight)
	m = pressNS(t, m, tea.KeyPressMsg{Code: tea.KeyUp}) // wraps to prompt
	if m.dirPrompt.focus != nsFieldPrompt {
		t.Fatalf("focus = %d", m.dirPrompt.focus)
	}
	m, _ = hubKey(t, m, "!")
	_, cmd := hubKey(t, m, "enter")
	cmd()
	if !slices.Contains(run.args, "--prompt=Audit the logs.!") {
		t.Errorf("args = %v", run.args)
	}
}

func TestNewSessionTemplateAgentMissing(t *testing.T) {
	run := stubTemplates(t, testTemplates, []string{"writer"})
	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	m = pressNS(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m = pressNS(t, m, keyRight)
	m = pressNS(t, m, keyRight) // orphan: agent "retired" is gone
	view := ansi.Strip(m.footerBody(m.contentWidth()))
	if !strings.Contains(view, "retired") || !strings.Contains(view, "not found") {
		t.Errorf("missing agent not shown:\n%s", view)
	}
	m, cmd := hubKey(t, m, "enter")
	if cmd != nil || !strings.Contains(m.jumpErr, "retired") || !m.newSessionOpen() {
		t.Fatalf("launched with a missing agent: cmd=%v err=%q open=%v", cmd != nil, m.jumpErr, m.newSessionOpen())
	}
	// Pick another agent and launch.
	if m.dirPrompt.focus != nsFieldAgent {
		t.Fatalf("focus = %d, want agent", m.dirPrompt.focus)
	}
	for range len("retired") {
		m, _ = hubKey(t, m, "backspace")
	}
	for _, r := range "writer" {
		m, _ = hubKey(t, m, string(r))
	}
	_, cmd = hubKey(t, m, "enter")
	if cmd == nil {
		t.Fatal("no command after fixing the agent")
	}
	cmd()
	if !slices.Contains(run.args, "writer") {
		t.Errorf("args = %v", run.args)
	}
}

func TestNewSessionTemplateWithoutAgentNeedsOne(t *testing.T) {
	stubTemplates(t, testTemplates, nil)
	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	m = pressNS(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	for range 3 {
		m = pressNS(t, m, keyRight) // noagent
	}
	m, cmd := hubKey(t, m, "enter")
	if cmd != nil || !strings.Contains(m.jumpErr, "no agent") {
		t.Errorf("cmd=%v err=%q", cmd != nil, m.jumpErr)
	}
}

func TestNewSessionTemplateNoneUsesPlainPath(t *testing.T) {
	stubTemplates(t, testTemplates, nil)
	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	m, cmd := hubKey(t, m, "enter")
	if cmd == nil {
		t.Fatalf("no command; err=%q", m.jumpErr)
	}
	// Plain path runs fleetNewSessionRun (stubbed), never the template helper.
	if lm, ok := cmd().(fleetLaunchedMsg); !ok || lm.windowID != "@77" {
		t.Errorf("plain path not taken: %+v", lm)
	}
}

func TestNewSessionTemplateNarrow(t *testing.T) {
	stubTemplates(t, testTemplates, []string{"writer"})
	m, _ := hubKey(t, foldModel(t), fleetNewSessionKey)
	m = pressNS(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m = pressNS(t, m, keyRight)
	for _, cw := range []int{12, 20, 40} {
		for _, line := range strings.Split(m.newSessionFormView(cw), "\n") {
			if w := ansi.StringWidth(line); w > cw {
				t.Errorf("cw=%d: line is %d wide: %q", cw, w, ansi.Strip(line))
			}
		}
	}
}
