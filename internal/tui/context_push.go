package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	ctxstore "github.com/blsmth/agentctl/internal/context"
)

// ContextPushResult is the output of the context-push popup.
type ContextPushResult struct {
	// Cancelled is true when the user dismissed without selecting.
	Cancelled bool
	// Key is the selected context store key to push.
	Key string
	// TargetAgent is the agent name to push the context to.
	TargetAgent string
}

// ContextPushEntry is a display item for a context store entry.
type ContextPushEntry struct {
	Key     string
	Agent   string
	Age     string
	Preview string
}

// ContextPushModel is the Bubble Tea model for the "Push context" popup.
// The operator sees a list of context store entries and selects one to
// push to the target agent window.
type ContextPushModel struct {
	targetAgent string
	entries     []ContextPushEntry
	cursor      int
	result      ContextPushResult
	quitting    bool
}

// NewContextPushModel creates the push-context popup for the given target agent.
// entries is the list of available context store items to display.
func NewContextPushModel(targetAgent string, entries []ContextPushEntry) ContextPushModel {
	result := ContextPushResult{Cancelled: true}
	return ContextPushModel{
		targetAgent: targetAgent,
		entries:     entries,
		result:      result,
	}
}

// Result returns the popup's output after it quits.
func (m ContextPushModel) Result() ContextPushResult {
	return m.result
}

// PopupSize returns the recommended tmux display-popup dimensions.
func (m ContextPushModel) PopupSize() PopupSize {
	return PopupDimensions(m.View().Content)
}

func (m ContextPushModel) Init() tea.Cmd {
	return nil
}

func (m ContextPushModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.entries) > 0 {
				m.result = ContextPushResult{
					Key:         m.entries[m.cursor].Key,
					TargetAgent: m.targetAgent,
				}
				m.quitting = true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m ContextPushModel) View() tea.View {
	var b strings.Builder

	title := "Push Context"
	if m.targetAgent != "" {
		title = "Push Context → " + m.targetAgent
	}
	b.WriteString(popupTitleStyle.Render(title))
	b.WriteString("\n\n")

	if len(m.entries) == 0 {
		b.WriteString(popupDimStyle.Render("No context entries found."))
		b.WriteString("\n")
		b.WriteString(popupDimStyle.Render("Use 'agentctl context set' to add entries."))
		b.WriteString("\n")
		b.WriteString(renderPopupHints("esc", "cancel"))
		return tea.NewView("\n" + popupBoxStyle.Render(b.String()) + "\n")
	}

	for i, e := range m.entries {
		cursor := "  "
		if i == m.cursor {
			cursor = cursorActiveStyle.Render("") + " "
		}

		keyPart := e.Key
		if len(keyPart) > 18 {
			keyPart = keyPart[:17] + "…"
		}
		agentPart := ""
		if e.Agent != "" {
			agentPart = popupDimStyle.Render(" [" + e.Agent + "]")
		}
		ageLine := popupDimStyle.Render(" " + e.Age)

		var row string
		if i == m.cursor {
			label := popupAgentStyle.Render(keyPart) + agentPart + ageLine
			row = popupRowSelectedStyle.Render(cursor + label)
		} else {
			label := popupBodyStyle.Render(keyPart) + agentPart + ageLine
			row = popupRowStyle.Render(cursor + label)
		}
		b.WriteString(row)
		b.WriteString("\n")

		// Show preview for selected row
		if i == m.cursor && e.Preview != "" {
			preview := e.Preview
			if len(preview) > formContentWidth-4 {
				preview = preview[:formContentWidth-7] + "…"
			}
			b.WriteString(popupDimStyle.Render("  " + preview))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(popupRuleStyle.Render(strings.Repeat("─", popupContentWidth)))
	b.WriteString("\n")
	b.WriteString(renderPopupHints("j/k", "navigate", "enter", "push", "esc", "cancel"))

	return tea.NewView("\n" + popupBoxStyle.Render(b.String()) + "\n")
}

// FormatContextAge returns a human-friendly age string for a context entry timestamp.
func FormatContextAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw", int(d.Hours()/(24*7)))
	}
}

// BuildContextPushEntries converts context store entries to display entries.
func BuildContextPushEntries(entries []ctxstore.Entry) []ContextPushEntry {
	result := make([]ContextPushEntry, 0, len(entries))
	for _, e := range entries {
		preview := strings.ReplaceAll(e.Content, "\n", " ")
		if len(preview) > 60 {
			preview = preview[:57] + "…"
		}
		result = append(result, ContextPushEntry{
			Key:     e.Key,
			Agent:   e.Agent,
			Age:     FormatContextAge(e.Timestamp),
			Preview: preview,
		})
	}
	return result
}
