package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
)

// issueCreatedMsg is the tea.Msg returned by issue-creation commands when the
// GitHub API call completes (successfully or not).
type issueCreatedMsg struct {
	URL string
	Num int
	Err error
}

// extractIssueNumber parses the issue number from a GitHub issue URL like
// https://github.com/owner/repo/issues/42
func extractIssueNumber(url string) int {
	parts := strings.Split(strings.TrimRight(url, "/"), "/")
	if len(parts) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
}

// CreateTicketResult holds the outcome of the create-ticket form.
// A nil result means the form is still in progress.
type CreateTicketResult struct {
	Cancelled bool
	URL       string
	Number    int
	Err       error
}

// CreateTicketModel is the Bubble Tea model for the create-ticket form.
// It is designed to be used as the content of a Modal — it renders only the
// form body and relies on the Modal for chrome (border, title, hint bar).
type CreateTicketModel struct {
	repoSlug          string
	createInput       textinput.Model
	bodyInput         textarea.Model
	createFocus       createFocus
	labelOptions      []string
	selectedLabels    map[string]bool
	labelPickerCursor int
	creating          bool
	result            *CreateTicketResult
}

// NewCreateTicketModel initialises the form for the given repo slug.
// labelOptions is the current list of labels available for selection.
func NewCreateTicketModel(repoSlug string, labelOptions []string) CreateTicketModel {
	cw := modalContentWidth(ModalMedium)

	ti := textinput.New()
	ti.Placeholder = "Issue title..."
	ti.CharLimit = 200
	ti.SetWidth(cw)
	tiS := ti.Styles()
	tiS.Focused.Prompt = selectedStyle
	tiS.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	ti.SetStyles(tiS)
	ti.Prompt = "Title: "
	ti.Focus()

	bi := textarea.New()
	bi.Placeholder = "Issue body (optional)..."
	bi.SetHeight(4)
	bi.SetWidth(cw)
	bi.CharLimit = 4000
	bi.ShowLineNumbers = false

	return CreateTicketModel{
		repoSlug:     repoSlug,
		createInput:  ti,
		bodyInput:    bi,
		createFocus:  focusTitle,
		labelOptions: labelOptions,
		selectedLabels: map[string]bool{
			"enhancement": true,
			"triage":      true,
		},
	}
}

// HintKeys implements ModalContent so the Modal renders a hint bar.
func (m CreateTicketModel) HintKeys() []string {
	return []string{"tab/shift+tab", "cycle", "ctrl+d", "submit", "esc", "cancel"}
}

// Done reports whether the form has a result (submitted or cancelled).
func (m CreateTicketModel) Done() bool { return m.result != nil }

// Result returns the form result; nil when the form is still active.
func (m CreateTicketModel) Result() *CreateTicketResult { return m.result }

// Init returns the textinput blink command.
func (m CreateTicketModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m CreateTicketModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case issueCreatedMsg:
		m.creating = false
		if msg.Err != nil {
			m.result = &CreateTicketResult{Err: msg.Err}
		} else {
			m.result = &CreateTicketResult{URL: msg.URL, Number: msg.Num}
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.updateKey(msg)

	default:
		// Forward non-key messages to textarea for cursor blink and clipboard events.
		if m.createFocus == focusBody {
			var cmd tea.Cmd
			m.bodyInput, cmd = m.bodyInput.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m CreateTicketModel) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Global shortcuts active from any focus position.
	switch msg.String() {
	case "esc":
		m.result = &CreateTicketResult{Cancelled: true}
		return m, nil

	case "ctrl+d":
		return m.submitForm()

	case "tab":
		return m.cycleFocus(+1)

	case "shift+tab":
		return m.cycleFocus(-1)
	}

	// Focus-specific key handling.
	switch m.createFocus {
	case focusTitle:
		if msg.String() == "enter" {
			return m.cycleFocus(+1)
		}
		var cmd tea.Cmd
		m.createInput, cmd = m.createInput.Update(msg)
		return m, cmd

	case focusBody:
		var cmd tea.Cmd
		m.bodyInput, cmd = m.bodyInput.Update(msg)
		return m, cmd

	case focusLabels:
		switch msg.String() {
		case "up", "k":
			if m.labelPickerCursor > 0 {
				m.labelPickerCursor--
			}
		case "down", "j":
			if m.labelPickerCursor < len(m.labelOptions)-1 {
				m.labelPickerCursor++
			}
		case " ", "space":
			if m.labelPickerCursor >= 0 && m.labelPickerCursor < len(m.labelOptions) {
				lbl := m.labelOptions[m.labelPickerCursor]
				if m.selectedLabels == nil {
					m.selectedLabels = make(map[string]bool)
				}
				m.selectedLabels[lbl] = !m.selectedLabels[lbl]
			}
		case "enter":
			return m.cycleFocus(+1)
		}

	case focusSubmit:
		if msg.String() == "enter" {
			return m.submitForm()
		}

	case focusCancel:
		if msg.String() == "enter" {
			m.result = &CreateTicketResult{Cancelled: true}
			return m, nil
		}
	}

	return m, nil
}

// cycleFocus advances (dir=+1) or retreats (dir=-1) the focused section,
// blurring/focusing inputs as needed.
func (m CreateTicketModel) cycleFocus(dir int) (tea.Model, tea.Cmd) {
	hasLabels := len(m.labelOptions) > 0
	focusOrder := []createFocus{focusTitle, focusBody}
	if hasLabels {
		focusOrder = append(focusOrder, focusLabels)
	}
	focusOrder = append(focusOrder, focusSubmit, focusCancel)

	idx := 0
	for i, f := range focusOrder {
		if f == m.createFocus {
			idx = i
			break
		}
	}

	// Blur the currently focused input.
	if m.createFocus == focusTitle {
		m.createInput.Blur()
	} else if m.createFocus == focusBody {
		m.bodyInput.Blur()
	}

	idx = (idx + dir + len(focusOrder)) % len(focusOrder)
	m.createFocus = focusOrder[idx]

	switch m.createFocus {
	case focusTitle:
		m.createInput.Focus()
		return m, textinput.Blink
	case focusBody:
		cmd := m.bodyInput.Focus()
		return m, cmd
	default:
		return m, nil
	}
}

// submitForm creates the GitHub issue from the current form values.
// Returns early (focusing title) if the title field is empty.
func (m CreateTicketModel) submitForm() (tea.Model, tea.Cmd) {
	title := strings.TrimSpace(m.createInput.Value())
	if title == "" {
		if m.createFocus == focusBody {
			m.bodyInput.Blur()
		}
		m.createFocus = focusTitle
		m.createInput.Focus()
		return m, textinput.Blink
	}
	body := strings.TrimSpace(m.bodyInput.Value())
	m.creating = true
	m.createInput.Blur()
	m.bodyInput.Blur()
	return m, tkCreateIssueCmd(m.repoSlug, title, body, m.selectedLabels)
}

// View renders the form content. The Modal wraps this with border, title, and hint bar.
func (m CreateTicketModel) View() tea.View {
	return tea.NewView(m.viewContent())
}

// viewContent renders the raw form text without any modal chrome.
func (m CreateTicketModel) viewContent() string {
	var b strings.Builder
	t := theme.Current()

	cw := modalContentWidth(ModalMedium)
	focusedSectionLabel := func(name string, focused bool) string {
		if focused {
			return lipgloss.NewStyle().
				Foreground(lipgloss.Color(t.Accent)).
				Bold(true).
				Render(name)
		}
		return tkDimStyle.Render(name)
	}
	rule := "  " + tkRuleStyle.Render(strings.Repeat("╌", cw-2))

	// ── Title section ────────────────────────────────────────────────────────────
	b.WriteString("  " + focusedSectionLabel("Title", m.createFocus == focusTitle))
	b.WriteString("\n")
	b.WriteString("  " + m.createInput.View())
	b.WriteString("\n\n")

	// ── Body section ─────────────────────────────────────────────────────────────
	b.WriteString("  " + focusedSectionLabel("Body", m.createFocus == focusBody))
	b.WriteString("\n")
	b.WriteString("  " + m.bodyInput.View())
	b.WriteString("\n\n")

	// ── Labels section ───────────────────────────────────────────────────────────
	if len(m.labelOptions) > 0 {
		b.WriteString(rule)
		b.WriteString("\n")
		b.WriteString("  " + focusedSectionLabel("Labels", m.createFocus == focusLabels))
		b.WriteString("\n")

		checkmarkStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true)
		for i := range m.labelOptions {
			lbl := m.labelOptions[i]
			selected := m.selectedLabels[lbl]

			cursor := "   "
			if i == m.labelPickerCursor && m.createFocus == focusLabels {
				cursor = " " + cursorActiveStyle.Render("▸") + " "
			}

			check := "[ ]"
			if selected {
				check = "[" + checkmarkStyle.Render("✓") + "]"
			}

			nameStyle := tkTitleColStyle
			if i == m.labelPickerCursor && m.createFocus == focusLabels {
				nameStyle = tkTitleSelectedStyle
			}
			b.WriteString(cursor + check + " " + nameStyle.Render(lbl))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// ── Buttons ──────────────────────────────────────────────────────────────────
	var submitBtn, cancelBtn string
	switch {
	case m.creating:
		submitBtn = btnSelectedStyle.Render("Creating...")
		cancelBtn = btnNormalStyle.Render("Cancel")
	case m.createFocus == focusSubmit:
		submitBtn = btnSelectedStyle.Render("Submit")
		cancelBtn = btnNormalStyle.Render("Cancel")
	case m.createFocus == focusCancel:
		submitBtn = btnNormalStyle.Render("Submit")
		cancelBtn = btnSelectedStyle.Render("Cancel")
	default:
		submitBtn = btnNormalStyle.Render("Submit")
		cancelBtn = btnNormalStyle.Render("Cancel")
	}
	b.WriteString(rule)
	b.WriteString("\n")
	b.WriteString("  " + lipgloss.JoinHorizontal(lipgloss.Top, submitBtn, "   ", cancelBtn))

	return b.String()
}
