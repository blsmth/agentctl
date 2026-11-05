package tui

import (
	tea "charm.land/bubbletea/v2"
)

// standaloneCreateTicket is a top-level tea.Model that wraps a Modal containing
// a CreateTicketModel. It calls tea.Quit automatically when the inner form
// signals Done(), so it can be used as a self-contained popup (ctrl+b Q).
type standaloneCreateTicket struct {
	modal Modal
}

// NewStandaloneCreateTicket creates a standalone create-ticket popup that owns
// a ModalMedium wrapping a CreateTicketModel for the given repo slug.
// labelOptions is the list of available repo labels; pass nil or an empty slice
// to hide the label picker (CreateTicketModel defaults still apply).
func NewStandaloneCreateTicket(repoSlug string, labelOptions []string) standaloneCreateTicket {
	content := NewCreateTicketModel(repoSlug, labelOptions)
	modal := NewModal("New Ticket", content, ModalMedium)
	return standaloneCreateTicket{modal: modal}
}

// Init delegates to the inner content model.
func (m standaloneCreateTicket) Init() tea.Cmd {
	return m.modal.Content.Init()
}

// Update delegates to the modal and calls tea.Quit when the inner form is done.
func (m standaloneCreateTicket) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.modal.Update(msg)
	m.modal = updated
	if ctm, ok := m.modal.Content.(CreateTicketModel); ok && ctm.Done() {
		return m, tea.Quit
	}
	return m, cmd
}

// View renders the modal (border, title, form body, hint bar).
func (m standaloneCreateTicket) View() tea.View {
	return tea.NewView(m.modal.View())
}
