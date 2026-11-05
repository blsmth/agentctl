package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// ModalSize presets for consistent sizing across the app.
type ModalSize int

const (
	ModalSmall  ModalSize = iota // ~50 col content width — confirmations, simple pickers
	ModalMedium                  // ~68 col content width — forms, multi-select
	ModalLarge                   // ~88 col content width — detailed views, wide content
)

// modalContentWidth returns the content width for a given size preset.
func modalContentWidth(s ModalSize) int {
	switch s {
	case ModalSmall:
		return 50
	case ModalMedium:
		return 68
	case ModalLarge:
		return 88
	default:
		return 68
	}
}

// ModalContent is the interface content models can implement to provide
// hint bar text. Optional — if not implemented, no hint bar is shown.
type ModalContent interface {
	// HintKeys returns alternating key/label pairs for the hint bar.
	// e.g. ["enter", "confirm", "esc", "cancel"]
	HintKeys() []string
}

// Modal wraps a content tea.Model with consistent chrome.
// The content model is a standard tea.Model — it doesn't need to know
// it's inside a Modal.
type Modal struct {
	Title   string
	Content tea.Model
	Size    ModalSize
	Visible bool
}

// NewModal creates a modal with the given title, content, and size.
// Starts visible by default.
func NewModal(title string, content tea.Model, size ModalSize) Modal {
	return Modal{
		Title:   title,
		Content: content,
		Size:    size,
		Visible: true,
	}
}

// Update delegates to the content model. If the content model returns
// a tea.Quit command, the modal sets Visible = false.
func (m Modal) Update(msg tea.Msg) (Modal, tea.Cmd) {
	var cmd tea.Cmd
	m.Content, cmd = m.Content.Update(msg)
	return m, cmd
}

// View renders the modal chrome: border box + title bar + content + optional hints.
// Returns an empty string when Visible is false.
func (m Modal) View() string {
	if !m.Visible {
		return ""
	}
	cw := modalContentWidth(m.Size)

	var b strings.Builder

	// Title bar
	b.WriteString(modalTitleStyle.Width(cw).Render(m.Title))
	b.WriteString("\n")

	// Content
	b.WriteString(m.Content.View().Content)

	// Hint bar (if content implements ModalContent)
	if mc, ok := m.Content.(ModalContent); ok {
		keys := mc.HintKeys()
		if len(keys) > 0 {
			b.WriteString("\n")
			b.WriteString(renderHintLine(keys...))
		}
	}

	// Wrap in border box
	return modalBoxStyle.Width(cw + 4).Render(b.String())
}

// PopupSize returns tmux display-popup dimensions for this modal.
// Adds border (2) + box padding (4) + tmux popup border (2).
func (m Modal) PopupSize() PopupSize {
	return PopupDimensions(m.View())
}

// Overlay composites this modal centered over a background string.
// Convenience wrapper around compositeOverlay.
func (m Modal) Overlay(bg string, termW, termH int) string {
	return compositeOverlay(bg, m.View(), termW, termH)
}
