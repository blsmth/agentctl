package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
)

// setupOverlayTest initialises styles so that style-dependent renders don't
// panic in unit tests.
func setupOverlayTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	config.SetConfigPathForTest(cfgPath)
	if err := os.WriteFile(cfgPath, []byte(`{"theme":"catppuccin-mocha"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	InitStyles()
}

// ─────────────────────────────────────────────────────────────────────────────
// ConfirmCloseModel tests
// ─────────────────────────────────────────────────────────────────────────────

// TestConfirmCloseModel_HintKeys verifies HintKeys returns the expected pairs.
func TestConfirmCloseModel_HintKeys(t *testing.T) {
	m := newConfirmCloseModel(42)
	keys := m.HintKeys()
	want := []string{"y", "confirm", "n/esc", "cancel", "tab", "switch"}
	if len(keys) != len(want) {
		t.Fatalf("HintKeys() len = %d, want %d", len(keys), len(want))
	}
	for i, k := range keys {
		if k != want[i] {
			t.Errorf("HintKeys()[%d] = %q, want %q", i, k, want[i])
		}
	}
}

// TestConfirmCloseModel_InitialState verifies defaults after construction.
func TestConfirmCloseModel_InitialState(t *testing.T) {
	m := newConfirmCloseModel(7)
	if m.issueNumber != 7 {
		t.Errorf("issueNumber = %d, want 7", m.issueNumber)
	}
	if m.cursorIdx != 1 {
		t.Errorf("cursorIdx = %d, want 1 (No)", m.cursorIdx)
	}
	if m.Done() {
		t.Error("Done() should be false initially")
	}
	if m.Confirmed() {
		t.Error("Confirmed() should be false initially")
	}
}

// TestConfirmCloseModel_YConfirms verifies "y" sets confirmed=true, done=true.
func TestConfirmCloseModel_YConfirms(t *testing.T) {
	m := newConfirmCloseModel(1)
	next, _ := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	updated := next.(ConfirmCloseModel)
	if !updated.Done() {
		t.Error("Done() should be true after y")
	}
	if !updated.Confirmed() {
		t.Error("Confirmed() should be true after y")
	}
}

// TestConfirmCloseModel_NorEscCancels verifies "n" and "esc" cancel without confirming.
func TestConfirmCloseModel_NorEscCancels(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: 'n', Text: "n"},
		{Code: tea.KeyEscape},
	} {
		m := newConfirmCloseModel(1)
		next, _ := m.Update(key)
		updated := next.(ConfirmCloseModel)
		if !updated.Done() {
			t.Errorf("Done() should be true after %q", key.String())
		}
		if updated.Confirmed() {
			t.Errorf("Confirmed() should be false after %q", key.String())
		}
	}
}

// TestConfirmCloseModel_TabTogglesCursor verifies tab cycles between Yes(0) and No(1).
func TestConfirmCloseModel_TabTogglesCursor(t *testing.T) {
	m := newConfirmCloseModel(1)
	// Default is No (1).
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(ConfirmCloseModel)
	if m.cursorIdx != 0 {
		t.Errorf("after tab cursorIdx = %d, want 0 (Yes)", m.cursorIdx)
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(ConfirmCloseModel)
	if m.cursorIdx != 1 {
		t.Errorf("after second tab cursorIdx = %d, want 1 (No)", m.cursorIdx)
	}
}

// TestConfirmCloseModel_EnterOnYesConfirms verifies enter on Yes (cursorIdx==0) confirms.
func TestConfirmCloseModel_EnterOnYesConfirms(t *testing.T) {
	m := newConfirmCloseModel(1)
	// Tab to Yes.
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(ConfirmCloseModel)
	if m.cursorIdx != 0 {
		t.Fatalf("expected cursorIdx=0 after tab")
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(ConfirmCloseModel)
	if !m.Confirmed() {
		t.Error("Confirmed() should be true after enter on Yes")
	}
}

// TestConfirmCloseModel_EnterOnNoCancels verifies enter on No (cursorIdx==1) cancels.
func TestConfirmCloseModel_EnterOnNoCancels(t *testing.T) {
	m := newConfirmCloseModel(1)
	// Default is No (1).
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(ConfirmCloseModel)
	if m.Confirmed() {
		t.Error("Confirmed() should be false after enter on No")
	}
	if !m.Done() {
		t.Error("Done() should be true after enter on No")
	}
}

// TestConfirmCloseModel_View verifies the view renders the issue number and buttons.
func TestConfirmCloseModel_View(t *testing.T) {
	setupOverlayTest(t)
	m := newConfirmCloseModel(42)
	view := m.View()
	if !strings.Contains(view.Content, "#42") {
		t.Errorf("View() does not contain issue number #42; got: %q", view.Content)
	}
	if !strings.Contains(view.Content, "Yes") {
		t.Error("View() does not contain 'Yes' button")
	}
	if !strings.Contains(view.Content, "No") {
		t.Error("View() does not contain 'No' button")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AgentSelectModel tests
// ─────────────────────────────────────────────────────────────────────────────

// testAgents returns a small slice of agents for testing.
func testAgents() []agent.Agent {
	return []agent.Agent{
		{Name: "go-engineer", Description: "Writes Go code"},
		{Name: "python-engineer", Description: "Writes Python code"},
		{Name: "terraform-engineer", Description: "Manages infra"},
	}
}

// TestAgentSelectModel_Navigation verifies j/k navigate and clamp at boundaries.
func TestAgentSelectModel_Navigation(t *testing.T) {
	agents := testAgents()
	m := newAgentSelectModel(agents, 1, "test issue", "")

	// Move down twice.
	for range 2 {
		next, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
		m = next.(AgentSelectModel)
	}
	if m.cursor != 2 {
		t.Errorf("cursor = %d after 2×j, want 2", m.cursor)
	}

	// Attempt to move past end — should clamp.
	next, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = next.(AgentSelectModel)
	if m.cursor != 2 {
		t.Errorf("cursor = %d after j past end, want 2 (clamped)", m.cursor)
	}

	// Move back up.
	next, _ = m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	m = next.(AgentSelectModel)
	if m.cursor != 1 {
		t.Errorf("cursor = %d after k, want 1", m.cursor)
	}

	// Attempt to move past start — should clamp.
	for range 5 {
		next, _ = m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
		m = next.(AgentSelectModel)
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d after k past start, want 0 (clamped)", m.cursor)
	}
}

// TestAgentSelectModel_EscCancels verifies esc sets Cancelled.
func TestAgentSelectModel_EscCancels(t *testing.T) {
	m := newAgentSelectModel(testAgents(), 1, "test", "")
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(AgentSelectModel)
	if !m.Done() {
		t.Error("Done() should be true after esc")
	}
	if !m.Cancelled() {
		t.Error("Cancelled() should be true after esc")
	}
	if _, ok := m.Selected(); ok {
		t.Error("Selected() should return false when cancelled")
	}
}

// TestAgentSelectModel_EnterSelects verifies enter selects the current agent.
func TestAgentSelectModel_EnterSelects(t *testing.T) {
	agents := testAgents()
	m := newAgentSelectModel(agents, 1, "test", "")
	// Move to second agent.
	next, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = next.(AgentSelectModel)
	// Select.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(AgentSelectModel)

	if !m.Done() {
		t.Error("Done() should be true after enter")
	}
	if m.Cancelled() {
		t.Error("Cancelled() should be false after enter")
	}
	selected, ok := m.Selected()
	if !ok {
		t.Fatal("Selected() should return true after enter")
	}
	if selected.Name != "python-engineer" {
		t.Errorf("Selected().Name = %q, want %q", selected.Name, "python-engineer")
	}
}

// TestAgentSelectModel_HintKeys verifies HintKeys returns the expected pairs.
func TestAgentSelectModel_HintKeys(t *testing.T) {
	m := newAgentSelectModel(nil, 0, "", "")
	keys := m.HintKeys()
	want := []string{"j/k", "navigate", "enter", "select", "esc", "back"}
	if len(keys) != len(want) {
		t.Fatalf("HintKeys() len = %d, want %d", len(keys), len(want))
	}
	for i, k := range keys {
		if k != want[i] {
			t.Errorf("HintKeys()[%d] = %q, want %q", i, k, want[i])
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PromptSelectModel tests
// ─────────────────────────────────────────────────────────────────────────────

// TestPromptSelectModel_HintKeys verifies HintKeys returns the expected pairs.
func TestPromptSelectModel_HintKeys(t *testing.T) {
	setupOverlayTest(t)
	m := newPromptSelectModel(1, "test", "", "go-engineer")
	keys := m.HintKeys()
	want := []string{"j/k", "navigate", "enter", "select", "esc", "back"}
	if len(keys) != len(want) {
		t.Fatalf("HintKeys() len = %d, want %d", len(keys), len(want))
	}
	for i, k := range keys {
		if k != want[i] {
			t.Errorf("HintKeys()[%d] = %q, want %q", i, k, want[i])
		}
	}
}

// TestPromptSelectModel_Navigation verifies j/k navigate and clamp at boundaries.
func TestPromptSelectModel_Navigation(t *testing.T) {
	setupOverlayTest(t)
	m := newPromptSelectModel(1, "test", "", "go-engineer")
	maxIdx := len(tlPromptOptions) - 1

	// Move down to the end.
	for range maxIdx {
		next, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
		m = next.(PromptSelectModel)
	}
	if m.cursor != maxIdx {
		t.Errorf("cursor = %d after %d×j, want %d", m.cursor, maxIdx, maxIdx)
	}

	// Clamp past end.
	next, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = next.(PromptSelectModel)
	if m.cursor != maxIdx {
		t.Errorf("cursor = %d after j past end, want %d (clamped)", m.cursor, maxIdx)
	}

	// Move back up.
	next, _ = m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	m = next.(PromptSelectModel)
	if m.cursor != maxIdx-1 {
		t.Errorf("cursor = %d after k, want %d", m.cursor, maxIdx-1)
	}
}

// TestPromptSelectModel_EscGoesBack verifies esc sets GoBack state.
func TestPromptSelectModel_EscGoesBack(t *testing.T) {
	setupOverlayTest(t)
	m := newPromptSelectModel(1, "test", "", "go-engineer")
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(PromptSelectModel)
	if !m.Done() {
		t.Error("Done() should be true after esc")
	}
	if !m.GoBack() {
		t.Error("GoBack() should be true after esc")
	}
}

// TestPromptSelectModel_EnterSelectsWork verifies enter on cursor=0 selects "work".
func TestPromptSelectModel_EnterSelectsWork(t *testing.T) {
	setupOverlayTest(t)
	m := newPromptSelectModel(1, "test", "", "go-engineer")
	// cursor=0 by default (work).
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(PromptSelectModel)
	if !m.Done() {
		t.Error("Done() should be true after enter")
	}
	if m.GoBack() {
		t.Error("GoBack() should be false after selecting")
	}
	mode, _ := m.Selected()
	if mode != "work" {
		t.Errorf("Selected() mode = %q, want %q", mode, "work")
	}
}

// TestPromptSelectModel_ShortcutKeys verifies w/t/n shortcut keys select modes.
func TestPromptSelectModel_ShortcutKeys(t *testing.T) {
	setupOverlayTest(t)

	cases := []struct {
		key  rune
		want string
	}{
		{'w', "work"},
		{'t', "triage"},
		{'n', "none"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			m := newPromptSelectModel(1, "test", "", "go-engineer")
			next, _ := m.Update(tea.KeyPressMsg{Code: tc.key, Text: string(tc.key)})
			m = next.(PromptSelectModel)
			if !m.Done() {
				t.Errorf("Done() false after %q", tc.key)
			}
			mode, _ := m.Selected()
			if mode != tc.want {
				t.Errorf("Selected() = %q, want %q", mode, tc.want)
			}
		})
	}
}

// TestPromptSelectModel_PendingFields verifies that ticket info is preserved.
func TestPromptSelectModel_PendingFields(t *testing.T) {
	setupOverlayTest(t)
	m := newPromptSelectModel(42, "my title", "my body", "go-engineer")
	if m.pendingTicketNum != 42 {
		t.Errorf("pendingTicketNum = %d, want 42", m.pendingTicketNum)
	}
	if m.pendingTicketTitle != "my title" {
		t.Errorf("pendingTicketTitle = %q, want %q", m.pendingTicketTitle, "my title")
	}
	if m.pendingTicketBody != "my body" {
		t.Errorf("pendingTicketBody = %q, want %q", m.pendingTicketBody, "my body")
	}
	if m.pendingAgentName != "go-engineer" {
		t.Errorf("pendingAgentName = %q, want %q", m.pendingAgentName, "go-engineer")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TicketsModel integration — confirm/agent/prompt modal flows
// ─────────────────────────────────────────────────────────────────────────────

// TestTicketsModel_AgentModalOpensOnT verifies that pressing T in the list view
// opens the agentModal when agents are present.
func TestTicketsModel_AgentModalOpensOnT(t *testing.T) {
	setupOverlayTest(t)
	m := makeTestTicketsModel(t)
	m.agents = testAgents()

	m = tkSendKey(t, m, charKey('T'))

	if !m.agentModal.Visible {
		t.Fatal("pressing T should open agentModal")
	}
	asm, ok := m.agentModal.Content.(AgentSelectModel)
	if !ok {
		t.Fatalf("agentModal.Content should be AgentSelectModel, got %T", m.agentModal.Content)
	}
	if asm.pendingTicketNum != 42 {
		t.Errorf("pendingTicketNum = %d, want 42", asm.pendingTicketNum)
	}
}

// TestTicketsModel_AgentModalEscCancels verifies esc in agentModal closes it
// without opening promptModal.
func TestTicketsModel_AgentModalEscCancels(t *testing.T) {
	setupOverlayTest(t)
	m := makeTestTicketsModel(t)
	m.agents = testAgents()
	m = tkSendKey(t, m, charKey('T'))
	if !m.agentModal.Visible {
		t.Fatal("expected agentModal to be visible")
	}

	m = tkSendKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.agentModal.Visible {
		t.Error("esc should hide agentModal")
	}
	if m.promptModal.Visible {
		t.Error("esc on agentModal should not open promptModal")
	}
}

// TestTicketsModel_AgentSelectOpensPromptModal verifies that selecting an agent
// opens the promptModal.
func TestTicketsModel_AgentSelectOpensPromptModal(t *testing.T) {
	setupOverlayTest(t)
	m := makeTestTicketsModel(t)
	m.agents = testAgents()
	m = tkSendKey(t, m, charKey('T'))

	m = tkSendKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.agentModal.Visible {
		t.Error("agentModal should be hidden after selecting an agent")
	}
	if !m.promptModal.Visible {
		t.Fatal("promptModal should be visible after agent selection")
	}
	psm, ok := m.promptModal.Content.(PromptSelectModel)
	if !ok {
		t.Fatalf("promptModal.Content should be PromptSelectModel, got %T", m.promptModal.Content)
	}
	if psm.pendingAgentName != "go-engineer" {
		t.Errorf("pendingAgentName = %q, want %q", psm.pendingAgentName, "go-engineer")
	}
}

// TestTicketsModel_PromptEscGoesBackToAgent verifies that esc in promptModal
// re-opens agentModal.
func TestTicketsModel_PromptEscGoesBackToAgent(t *testing.T) {
	setupOverlayTest(t)
	m := makeTestTicketsModel(t)
	m.agents = testAgents()
	m = tkSendKey(t, m, charKey('T'))                        // open agent modal
	m = tkSendKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // select first agent → prompt modal

	if !m.promptModal.Visible {
		t.Fatal("expected promptModal to be visible before esc test")
	}

	m = tkSendKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.promptModal.Visible {
		t.Error("esc should hide promptModal")
	}
	if !m.agentModal.Visible {
		t.Error("esc on promptModal should re-open agentModal")
	}
}
