package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/theme"
)

// CreateResult is the output of the create wizard.
type CreateResult struct {
	Cancelled   bool
	Name        string
	Description string
	Model       string
	Tools       string
	Dir         string
	Template    string // base template name, empty = standalone
}

type createStep int

const (
	stepTemplate createStep = iota
	stepName
	stepDescription
	stepModel
	stepTools
	stepDir
	stepConfirm
	totalCreateSteps = int(stepConfirm) + 1
)

var modelChoices = []string{"sonnet", "opus", "haiku", "inherit"}

var toolOptions = []struct {
	name    string
	checked bool
}{
	{"Read", true},
	{"Write", true},
	{"Edit", true},
	{"Bash", true},
	{"Grep", true},
	{"Glob", true},
	{"WebFetch", false},
	{"WebSearch", false},
	{"Agent", false},
	{"NotebookEdit", false},
}

// templateChoice is a template entry shown in the picker, including a "None" option.
type templateChoice struct {
	name        string // empty string = no template
	description string
}

// CreateModel is the Bubble Tea model for the agent creation wizard.
type CreateModel struct {
	step      createStep
	textInput textinput.Model
	result    CreateResult
	quitting  bool

	// Model selection
	modelCursor int

	// Tool multi-select
	toolCursor  int
	toolChecked []bool

	// Template selection
	templates      []templateChoice
	templateCursor int
}

// NewCreateModel creates the wizard, optionally pre-filling the name.
func NewCreateModel(name string) CreateModel {
	ti := textinput.New()
	ti.CharLimit = 100
	ti.SetWidth(44)
	s := ti.Styles()
	s.Focused.Prompt = selectedStyle
	s.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	ti.SetStyles(s)

	checked := make([]bool, len(toolOptions))
	for i, t := range toolOptions {
		checked[i] = t.checked
	}

	// Build template choices: "None" first, then built-ins.
	choices := []templateChoice{
		{name: "", description: "Start from scratch"},
	}
	if infos, err := agent.ListTemplates(); err == nil {
		for _, info := range infos {
			choices = append(choices, templateChoice{
				name:        info.Name,
				description: info.Description,
			})
		}
	}

	m := CreateModel{
		step:        stepTemplate,
		textInput:   ti,
		result:      CreateResult{Cancelled: true},
		toolChecked: checked,
		templates:   choices,
	}

	if name != "" {
		m.result.Name = name
		m.step = stepDescription
	}

	m.configureInput()
	return m
}

// PopupSize returns the tmux display-popup dimensions for the create wizard.
func (m CreateModel) PopupSize() PopupSize {
	return PopupDimensions(m.View().Content)
}

// Result returns the wizard output after it quits.
func (m CreateModel) Result() CreateResult {
	return m.result
}

func (m CreateModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m CreateModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}

		switch m.step {
		case stepTemplate:
			return m.updateTemplateSelect(msg)
		case stepName, stepDescription, stepDir:
			return m.updateTextInput(msg)
		case stepModel:
			return m.updateModelSelect(msg)
		case stepTools:
			return m.updateToolSelect(msg)
		case stepConfirm:
			return m.updateConfirm(msg)
		}
	}

	// Pass through for blink messages etc.
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m CreateModel) updateTemplateSelect(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.quitting = true
		return m, tea.Quit
	case "up", "k":
		if m.templateCursor > 0 {
			m.templateCursor--
		}
	case "down", "j":
		if m.templateCursor < len(m.templates)-1 {
			m.templateCursor++
		}
	case "enter":
		chosen := m.templates[m.templateCursor]
		m.result.Template = chosen.name

		// Pre-fill fields from template frontmatter when a template is chosen.
		if chosen.name != "" {
			if data, err := agent.LoadTemplate(chosen.name); err == nil {
				if fm, fmErr := agent.ParseFrontmatter(data); fmErr == nil {
					if m.result.Description == "" && fm.Description != "" {
						m.result.Description = fm.Description
					}
					if fm.Tools != "" {
						m.applyTemplateTools(fm.Tools)
					}
					// Find and set model cursor to match template's model.
					if fm.Model != "" {
						for i, mc := range modelChoices {
							if mc == fm.Model {
								m.modelCursor = i
								break
							}
						}
					}
				}
			}
		}

		m.step++
		m.configureInput()
		return m, nil
	}
	return m, nil
}

// applyTemplateTools updates toolChecked based on a comma-separated tool string.
func (m *CreateModel) applyTemplateTools(toolsStr string) {
	// Reset all to unchecked first.
	for i := range m.toolChecked {
		m.toolChecked[i] = false
	}
	for _, tool := range strings.Split(toolsStr, ",") {
		tool = strings.TrimSpace(tool)
		for i, opt := range toolOptions {
			if strings.EqualFold(opt.name, tool) {
				m.toolChecked[i] = true
				break
			}
		}
	}
}

func (m CreateModel) updateTextInput(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.step == stepName {
			m.step = stepTemplate
			return m, nil
		}
		m.step--
		m.configureInput()
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.textInput.Value())
		switch m.step {
		case stepName:
			if val == "" {
				return m, nil
			}
			m.result.Name = val
		case stepDescription:
			if val == "" {
				return m, nil
			}
			m.result.Description = val
		case stepDir:
			m.result.Dir = val
		}
		m.step++
		m.configureInput()
		return m, nil
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m CreateModel) updateModelSelect(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.step--
		m.configureInput()
		return m, nil
	case "up", "k":
		if m.modelCursor > 0 {
			m.modelCursor--
		}
	case "down", "j":
		if m.modelCursor < len(modelChoices)-1 {
			m.modelCursor++
		}
	case "enter":
		m.result.Model = modelChoices[m.modelCursor]
		m.step++
		m.configureInput()
		return m, nil
	}
	return m, nil
}

func (m CreateModel) updateToolSelect(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.step--
		return m, nil
	case "up", "k":
		if m.toolCursor > 0 {
			m.toolCursor--
		}
	case "down", "j":
		if m.toolCursor < len(toolOptions)-1 {
			m.toolCursor++
		}
	case " ":
		m.toolChecked[m.toolCursor] = !m.toolChecked[m.toolCursor]
	case "enter":
		var tools []string
		for i, t := range toolOptions {
			if m.toolChecked[i] {
				tools = append(tools, t.name)
			}
		}
		m.result.Tools = strings.Join(tools, ", ")
		m.step++
		m.configureInput()
		return m, nil
	}
	return m, nil
}

func (m CreateModel) updateConfirm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.step--
		m.configureInput()
		return m, nil
	case "enter", "y":
		m.result.Cancelled = false
		m.quitting = true
		return m, tea.Quit
	case "q", "n":
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *CreateModel) configureInput() {
	m.textInput.Reset()
	switch m.step {
	case stepName:
		m.textInput.Placeholder = "e.g., go-engineer"
		m.textInput.Focus()
	case stepDescription:
		m.textInput.Placeholder = "e.g., Go engineering agent for production code"
		m.textInput.Focus()
	case stepDir:
		m.textInput.Placeholder = "blank for portable (works in any repo)"
		m.textInput.Focus()
	}
}

// stepHeader renders the title bar + progress dots for a wizard step.
func (m CreateModel) stepHeader(subtitle string) string {
	title := "Create Agent"
	if subtitle != "" {
		title += " — " + subtitle
	}
	return formTitleStyle.Render(title) + "\n" +
		renderStepDots(int(m.step), totalCreateSteps, formContentWidth)
}

func (m CreateModel) View() tea.View {
	var s string
	switch m.step {
	case stepTemplate:
		s = m.viewTemplateSelect()
	case stepName:
		s = m.viewTextStep("Name", "Agent name:", "enter next · esc back")
	case stepDescription:
		s = m.viewTextStep("Description", "Description:", "enter next · esc back")
	case stepModel:
		s = m.viewModelSelect()
	case stepTools:
		s = m.viewToolSelect()
	case stepDir:
		s = m.viewTextStep("Directory", "Working directory (blank = portable):", "enter next · esc back")
	case stepConfirm:
		s = m.viewConfirm()
	}
	return tea.NewView(s)
}

func (m CreateModel) viewTemplateSelect() string {
	var b strings.Builder
	b.WriteString(m.stepHeader("Template"))
	b.WriteString("\n\n")
	b.WriteString(popupDimStyle.Render("Start from a built-in template:"))
	b.WriteString("\n")

	for i, tmpl := range m.templates {
		cursor := "  "
		if i == m.templateCursor {
			cursor = cursorActiveStyle.Render("") + " "
		}

		name := tmpl.name
		if name == "" {
			name = "None"
		}

		label := popupBodyStyle.Render(fmt.Sprintf("%-20s", name))
		desc := popupDimStyle.Render(tmpl.description)
		if i == m.templateCursor {
			label = popupAgentStyle.Render(fmt.Sprintf("%-20s", name))
		}

		inner := cursor + label + "  " + desc
		if i == m.templateCursor {
			b.WriteString(formRowSelectedStyle.Render(inner))
		} else {
			b.WriteString(formRowStyle.Render(inner))
		}
		b.WriteString("\n")
	}

	b.WriteString(renderFormHints("enter", "select", "esc", "cancel"))
	return "\n" + formBoxStyle.Render(b.String()) + "\n"
}

func (m CreateModel) viewTextStep(subtitle, label, hint string) string {
	var b strings.Builder
	b.WriteString(m.stepHeader(subtitle))
	b.WriteString("\n\n")
	b.WriteString(popupDimStyle.Render(label))
	b.WriteString("\n")
	b.WriteString(m.textInput.View())
	b.WriteString("\n")
	b.WriteString(renderFormHints(parseHintPairs(hint)...))
	return "\n" + formBoxStyle.Render(b.String()) + "\n"
}

func (m CreateModel) viewModelSelect() string {
	var b strings.Builder
	b.WriteString(m.stepHeader("Model"))
	b.WriteString("\n\n")

	for i, choice := range modelChoices {
		cursor := "  "
		if i == m.modelCursor {
			cursor = cursorActiveStyle.Render("") + " "
		}
		label := popupBodyStyle.Render(choice)
		if i == m.modelCursor {
			label = popupAgentStyle.Render(choice)
			b.WriteString(formRowSelectedStyle.Render(cursor + label))
		} else {
			b.WriteString(formRowStyle.Render(cursor + label))
		}
		b.WriteString("\n")
	}

	b.WriteString(renderFormHints("enter", "select", "esc", "back"))
	return "\n" + formBoxStyle.Render(b.String()) + "\n"
}

func (m CreateModel) viewToolSelect() string {
	var b strings.Builder
	b.WriteString(m.stepHeader("Tools"))
	b.WriteString("\n\n")
	b.WriteString(popupDimStyle.Render("Space to toggle:"))
	b.WriteString("\n")

	for i, tool := range toolOptions {
		cursor := "  "
		if i == m.toolCursor {
			cursor = cursorActiveStyle.Render("") + " "
		}

		var check string
		if m.toolChecked[i] {
			check = toggleOnStyle.Render("[✓]")
		} else {
			check = toggleOffStyle.Render("[ ]")
		}

		label := popupBodyStyle.Render(tool.name)
		if i == m.toolCursor {
			label = popupAgentStyle.Render(tool.name)
		}

		inner := cursor + check + " " + label
		if i == m.toolCursor {
			b.WriteString(formRowSelectedStyle.Render(inner))
		} else {
			b.WriteString(formRowStyle.Render(inner))
		}
		b.WriteString("\n")
	}

	b.WriteString(renderFormHints("space", "toggle", "enter", "confirm", "esc", "back"))
	return "\n" + formBoxStyle.Render(b.String()) + "\n"
}

func (m CreateModel) viewConfirm() string {
	var b strings.Builder
	b.WriteString(m.stepHeader("Confirm"))
	b.WriteString("\n\n")

	rows := []struct{ k, v string }{
		{"Name", m.result.Name},
		{"Template", templateLabel(m.result.Template)},
		{"Description", m.result.Description},
		{"Model", m.result.Model},
		{"Tools", m.result.Tools},
		{"Directory", dirLabel(m.result.Dir)},
	}
	for _, row := range rows {
		b.WriteString(fmt.Sprintf("  %s  %s\n",
			popupAgentStyle.Render(fmt.Sprintf("%-12s", row.k)),
			popupBodyStyle.Render(row.v),
		))
	}

	b.WriteString(renderFormHints("enter/y", "create", "esc", "back", "q/n", "cancel"))
	return "\n" + formBoxStyle.Render(b.String()) + "\n"
}

func templateLabel(t string) string {
	if t == "" {
		return "none (standalone)"
	}
	return t
}

func dirLabel(dir string) string {
	if dir == "" {
		return "portable (any repo)"
	}
	return dir
}

// parseHintPairs splits a "key action · key action" hint string into a flat
// slice of alternating key, label pairs for renderFormHints / renderPopupHints.
func parseHintPairs(s string) []string {
	var out []string
	for _, part := range strings.Split(s, " · ") {
		idx := strings.Index(part, " ")
		if idx < 0 {
			out = append(out, part, "")
			continue
		}
		out = append(out, part[:idx], part[idx+1:])
	}
	return out
}
