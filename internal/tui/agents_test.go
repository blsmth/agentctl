package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/prompt"
)

// ── Fixtures ──────────────────────────────────────────────────────────────────

func makeTestAgents() []agent.Agent {
	return []agent.Agent{
		{
			Name:        "go-engineer",
			Description: "Expert Go engineer",
			Model:       "claude-opus-4-5",
			Scope:       agent.ScopeGlobal,
			FilePath:    "/tmp/fake-go-engineer.md",
		},
		{
			Name:        "python-engineer",
			Description: "Expert Python engineer",
			Model:       "",
			Scope:       agent.ScopeGlobal,
			FilePath:    "/tmp/fake-python-engineer.md",
		},
		{
			Name:        "local-agent",
			Description: "A project-local agent",
			Model:       "claude-haiku-4-5",
			Scope:       agent.ScopeLocal,
			ProjectName: "myproject",
			FilePath:    "/tmp/fake-local-agent.md",
		},
	}
}

// ── Construction tests ────────────────────────────────────────────────────────

func TestNewAgentsModel_InitializesWithoutPanic(t *testing.T) {
	t.Helper()
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	if m.termWidth == 0 {
		t.Error("expected non-zero termWidth after construction")
	}
	if len(m.allAgents) != len(agents) {
		t.Errorf("allAgents = %d, want %d", len(m.allAgents), len(agents))
	}
	if len(m.agents) != len(agents) {
		t.Errorf("filtered agents = %d, want %d", len(m.agents), len(agents))
	}
}

func TestNewAgentsModel_AutoSelectsFirstAgent(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	if m.selected == nil {
		t.Fatal("expected first agent to be auto-selected")
	}
	if m.selected.Name != agents[0].Name {
		t.Errorf("selected.Name = %q, want %q", m.selected.Name, agents[0].Name)
	}
}

func TestNewAgentsModel_EmptyAgentList(t *testing.T) {
	InitStyles()

	m := NewAgentsModel(nil, nil)

	if m.selected != nil {
		t.Error("expected selected to be nil for empty agent list")
	}
	if len(m.agents) != 0 {
		t.Errorf("agents = %d, want 0", len(m.agents))
	}
}

// ── Agent list population ─────────────────────────────────────────────────────

func TestAgentsModel_AgentListPopulatesFromDiscovery(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	if len(m.allAgents) != 3 {
		t.Errorf("expected 3 agents, got %d", len(m.allAgents))
	}

	names := make(map[string]bool)
	for _, ag := range m.allAgents {
		names[ag.Name] = true
	}

	for _, want := range []string{"go-engineer", "python-engineer", "local-agent"} {
		if !names[want] {
			t.Errorf("agent %q not found in list", want)
		}
	}
}

// ── Navigation tests ──────────────────────────────────────────────────────────

func TestAgentsModel_NavigateDown(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	if m.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.cursor)
	}

	result, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m2 := result.(AgentsModel)

	if m2.cursor != 1 {
		t.Errorf("cursor after j = %d, want 1", m2.cursor)
	}
}

func TestAgentsModel_NavigateUp(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	// Move down first.
	result, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m2 := result.(AgentsModel)

	// Then move up.
	result2, _ := m2.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	m3 := result2.(AgentsModel)

	if m3.cursor != 0 {
		t.Errorf("cursor after k = %d, want 0", m3.cursor)
	}
}

func TestAgentsModel_NavigateBoundaries(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	// Moving up at top should stay at 0.
	result, _ := m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	m2 := result.(AgentsModel)
	if m2.cursor != 0 {
		t.Errorf("cursor after k at top = %d, want 0", m2.cursor)
	}

	// Move to last position.
	for i := 0; i < len(agents)-1; i++ {
		result, _ = m2.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
		m2 = result.(AgentsModel)
	}

	// Moving down at bottom should stay at last.
	last := m2.cursor
	result, _ = m2.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m3 := result.(AgentsModel)
	if m3.cursor != last {
		t.Errorf("cursor after j at bottom = %d, want %d", m3.cursor, last)
	}
}

// ── Selection / resolve tests ─────────────────────────────────────────────────

func TestAgentsModel_EnterTriggersResolve(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if cmd == nil {
		t.Error("expected a command (resolve) after enter")
	}
}

func TestAgentsModel_SelectionUpdatesSelected(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	// Move to the second agent.
	result, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m2 := result.(AgentsModel)

	// Press enter to select it.
	result2, _ := m2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m3 := result2.(AgentsModel)

	if m3.selected == nil {
		t.Fatal("expected selected to be non-nil after enter")
	}
	if m3.selected.Name != agents[1].Name {
		t.Errorf("selected.Name = %q, want %q", m3.selected.Name, agents[1].Name)
	}
}

func TestAgentsModel_ResolvedMsgUpdatesPreview(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	rp := &prompt.ResolvedPrompt{
		Agent:      "go-engineer",
		FilePath:   "/tmp/fake-go-engineer.md",
		RawContent: "# Go Engineer\n\nYou are a Go expert.\n",
	}

	result, _ := m.Update(agentsPromptResolvedMsg{
		agentName: "go-engineer",
		result:    rp,
	})
	m2 := result.(AgentsModel)

	if m2.resolving {
		t.Error("expected resolving=false after resolve message")
	}
	if m2.resolveErr != nil {
		t.Errorf("unexpected resolveErr: %v", m2.resolveErr)
	}
}

func TestAgentsModel_ResolveErrorIsDisplayed(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	m.termWidth = 120
	m.termHeight = 40

	result, _ := m.Update(agentsPromptResolvedMsg{
		agentName: "go-engineer",
		err:       fmt.Errorf("file not found"),
	})
	m2 := result.(AgentsModel)

	if m2.resolveErr == nil {
		t.Error("expected resolveErr to be set")
	}

	// renderPreviewPane should mention the error.
	view := stripANSI(m2.renderPreviewPane(60))
	if !strings.Contains(view, "Error:") {
		t.Errorf("expected error message in preview pane, got: %q", view)
	}
}

// ── Filter tests ──────────────────────────────────────────────────────────────

func TestAgentsModel_FilterModeActivatesOnSlash(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	result, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m2 := result.(AgentsModel)

	if !m2.searchMode {
		t.Error("expected searchMode=true after /")
	}
}

func TestAgentsModel_FilterReducesList(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	// Apply a filter manually.
	m.searchQuery = "go"
	m.applyFilter()

	// Only go-engineer should match (name contains "go").
	if len(m.agents) != 1 {
		t.Errorf("expected 1 filtered agent, got %d", len(m.agents))
	}
	if m.agents[0].Name != "go-engineer" {
		t.Errorf("filtered agent = %q, want go-engineer", m.agents[0].Name)
	}
}

func TestAgentsModel_FilterEscClearsQuery(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	// Activate search.
	result, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m2 := result.(AgentsModel)

	// Press escape — should clear and deactivate search.
	result2, _ := m2.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m3 := result2.(AgentsModel)

	if m3.searchMode {
		t.Error("expected searchMode=false after esc")
	}
	if m3.searchQuery != "" {
		t.Errorf("expected empty searchQuery after esc, got %q", m3.searchQuery)
	}
	if len(m3.agents) != len(agents) {
		t.Errorf("expected full agent list after esc, got %d", len(m3.agents))
	}
}

func TestAgentsModel_FilterByDescription(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	// "project-local" appears only in local-agent's description.
	m.searchQuery = "project-local"
	m.applyFilter()

	if len(m.agents) != 1 {
		t.Errorf("expected 1 filtered agent, got %d", len(m.agents))
	}
	if m.agents[0].Name != "local-agent" {
		t.Errorf("filtered agent = %q, want local-agent", m.agents[0].Name)
	}
}

// ── Editor integration ────────────────────────────────────────────────────────

func TestAgentsModel_EditorDoneTriggersReResolve(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)
	m.selected = &m.agents[0]

	result, cmd := m.Update(agentsEditorDoneMsg{
		agentName: "go-engineer",
		filePath:  "/tmp/fake-go-engineer.md",
	})
	m2 := result.(AgentsModel)

	if cmd == nil {
		t.Error("expected a re-resolve command after editor done")
	}
	if !m2.resolving {
		t.Error("expected resolving=true after editor done")
	}
}

func TestAgentsModel_EditorDoneWithEmptyPathIsNoop(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	_, cmd := m.Update(agentsEditorDoneMsg{agentName: "go-engineer", filePath: ""})

	if cmd != nil {
		t.Error("expected nil command for editor done with empty path")
	}
}

// ── Quit tests ────────────────────────────────────────────────────────────────

func TestAgentsModel_QuitKeys(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()

	for _, key := range []string{"q", "ctrl+c"} {
		t.Run(key, func(t *testing.T) {
			m := NewAgentsModel(agents, nil)
			var keyMsg tea.KeyPressMsg
			if key == "ctrl+c" {
				keyMsg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
			} else {
				keyMsg = tea.KeyPressMsg{Code: 'q', Text: "q"}
			}
			result, cmd := m.Update(keyMsg)
			m2 := result.(AgentsModel)
			if !m2.quitting {
				t.Errorf("expected quitting=true after %q", key)
			}
			if cmd == nil {
				t.Errorf("expected tea.Quit command after %q", key)
			}
		})
	}
}

func TestAgentsModel_EscWithNoFilterQuits(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	result, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m2 := result.(AgentsModel)

	if !m2.quitting {
		t.Error("expected quitting=true after esc with no filter")
	}
	if cmd == nil {
		t.Error("expected tea.Quit command after esc with no filter")
	}
}

func TestAgentsModel_EscWithFilterClearsFilter(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)
	m.searchQuery = "go"
	m.applyFilter()

	result, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m2 := result.(AgentsModel)

	if m2.quitting {
		t.Error("expected quitting=false after esc with filter (should just clear)")
	}
	if m2.searchQuery != "" {
		t.Error("expected searchQuery to be cleared after esc")
	}
	_ = cmd
}

// ── View rendering tests ──────────────────────────────────────────────────────

func TestAgentsModel_ViewRendersWithoutPanic(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)
	m.termWidth = 160
	m.termHeight = 40

	// Should not panic.
	v := m.View()
	s := v.Content
	if s == "" {
		t.Error("expected non-empty View() output")
	}
}

func TestAgentsModel_ViewContainsAgentNames(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)
	m.termWidth = 160
	m.termHeight = 40

	v := m.View()
	plain := stripANSI(v.Content)

	for _, ag := range agents {
		if !strings.Contains(plain, ag.Name) {
			t.Errorf("expected %q in view output", ag.Name)
		}
	}
}

func TestAgentsModel_ViewNarrowTerminal(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)
	m.termWidth = 80
	m.termHeight = 40

	// SplitPane layout is used at all widths; just verify it renders cleanly.
	v := m.View()
	plain := stripANSI(v.Content)

	if plain == "" {
		t.Error("expected non-empty view on narrow terminal")
	}
	if !strings.Contains(plain, "go-engineer") {
		t.Error("expected go-engineer in narrow terminal view")
	}
}

func TestAgentsModel_ViewContainsSeparator(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)
	m.termWidth = 200
	m.termHeight = 40

	v := m.View()
	plain := stripANSI(v.Content)

	// The detail pane is split horizontally and always contains │.
	if !strings.Contains(plain, "│") {
		t.Error("expected │ separator in view")
	}
}

func TestAgentsModel_RKeyIsNoop(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	// Set preview content so there's something to toggle.
	rp := makeResolvedPrompt(t, "# Hello\n")
	m.preview.SetContent(rp)

	initialRaw := m.preview.rawMode

	// Send 'r' key — raw toggle is removed; it should be a no-op.
	result, _ := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m2 := result.(AgentsModel)

	// Raw mode should NOT have changed since 'r' is no longer handled.
	if m2.preview.rawMode != initialRaw {
		t.Error("expected 'r' to be a no-op in the new layout (raw pane is always visible)")
	}
}

// ── Window resize ─────────────────────────────────────────────────────────────

func TestAgentsModel_WindowResizeUpdatesPreview(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	result, _ := m.Update(tea.WindowSizeMsg{Width: 180, Height: 50})
	m2 := result.(AgentsModel)

	if m2.termWidth != 180 {
		t.Errorf("termWidth = %d, want 180", m2.termWidth)
	}
	if m2.termHeight != 50 {
		t.Errorf("termHeight = %d, want 50", m2.termHeight)
	}

	// SplitPane dimensions should be updated.
	paneH := 50 - 6 // = 44
	if m2.splitPane.termHeight != paneH {
		t.Errorf("splitPane.termHeight = %d, want %d", m2.splitPane.termHeight, paneH)
	}
	if m2.splitPane.termWidth != 180 {
		t.Errorf("splitPane.termWidth = %d, want 180", m2.splitPane.termWidth)
	}
}

// ── SplitPane integration tests ───────────────────────────────────────────────

func TestAgentsModel_SplitPaneIntegrated(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	// splitPane.totalRows should equal the number of agents after construction.
	if m.splitPane.totalRows != len(agents) {
		t.Errorf("splitPane.totalRows = %d, want %d", m.splitPane.totalRows, len(agents))
	}
}

func TestAgentsModel_DetailFocusCyclesOnTab(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	// Initial focus is list (agFocusList = 0).
	if m.detailFocus != agFocusList {
		t.Errorf("initial detailFocus = %d, want %d (agFocusList)", m.detailFocus, agFocusList)
	}

	// Tab 1: list → raw pane.
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = result.(AgentsModel)
	if m.detailFocus != agFocusRaw {
		t.Errorf("after tab 1: detailFocus = %d, want %d (agFocusRaw)", m.detailFocus, agFocusRaw)
	}

	// Tab 2: raw pane → preview pane.
	result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = result.(AgentsModel)
	if m.detailFocus != agFocusPreview {
		t.Errorf("after tab 2: detailFocus = %d, want %d (agFocusPreview)", m.detailFocus, agFocusPreview)
	}

	// Tab 3: preview pane → list (wraps).
	result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = result.(AgentsModel)
	if m.detailFocus != agFocusList {
		t.Errorf("after tab 3: detailFocus = %d, want %d (agFocusList, wrapped)", m.detailFocus, agFocusList)
	}
}

func TestAgentsModel_DetailContainsBothPanes(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)
	m.termWidth = 160
	m.termHeight = 40

	// Provide preview content.
	rp := makeResolvedPrompt(t, "# Hello\n")
	m.preview.SetContent(rp)

	detail := m.buildDetailContent()
	plain := stripANSI(detail)

	// Detail content must contain the │ separator.
	if !strings.Contains(plain, "│") {
		t.Error("expected │ separator in detail content")
	}

	// Detail content must label both panes.
	if !strings.Contains(plain, "raw source") {
		t.Error("expected 'raw source' label in detail content")
	}
	if !strings.Contains(plain, "preview") {
		t.Error("expected 'preview' label in detail content")
	}
}

// ── Helper utilities ──────────────────────────────────────────────────────────

func TestAgentsStripAnsi(t *testing.T) {
	input := "\x1b[32mhello\x1b[0m world"
	want := "hello world"
	got := agentsStripAnsi(input)
	if got != want {
		t.Errorf("agentsStripAnsi(%q) = %q, want %q", input, got, want)
	}
}

func TestAgentsModel_SelectedAgent(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	if m.selected == nil {
		t.Fatal("expected selected to be non-nil after construction")
	}
	if m.selected.Name != agents[0].Name {
		t.Errorf("selected.Name = %q, want %q", m.selected.Name, agents[0].Name)
	}
}

func TestAgentsModel_HintBarContainsTab(t *testing.T) {
	InitStyles()

	agents := makeTestAgents()
	m := NewAgentsModel(agents, nil)

	hint := stripANSI(m.renderHintBar())

	if !strings.Contains(hint, "tab") {
		t.Error("expected 'tab' in hint bar")
	}
	// 'r' raw toggle removed from hint bar.
	// Check that the hint bar doesn't contain "raw/expanded" (old label).
	if strings.Contains(hint, "raw/expanded") {
		t.Error("expected 'raw/expanded' to be removed from hint bar")
	}
}
