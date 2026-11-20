package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestDispatchPreviewModel_Init(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42 --agent python-engineer")
	if m.agentName != "go-engineer" {
		t.Errorf("agentName = %q, want %q", m.agentName, "go-engineer")
	}
	if m.targetAgent != "python-engineer" {
		t.Errorf("targetAgent = %q, want %q", m.targetAgent, "python-engineer")
	}
	if m.issue != 42 {
		t.Errorf("issue = %d, want 42", m.issue)
	}
	if m.decision != "" {
		t.Errorf("decision = %q, want empty", m.decision)
	}
	if !m.expanded {
		t.Error("dispatch preview should open expanded")
	}
}

func TestDispatchPreviewModel_AgentNameDefault(t *testing.T) {
	m := NewDispatchPreview("", "", 0, "do something", "", "agentctl dispatch --prompt 'do something'")
	if m.agentName != "agent" {
		t.Errorf("agentName = %q, want %q", m.agentName, "agent")
	}
	if m.targetAgent != "agent" {
		t.Errorf("targetAgent = %q, want %q", m.targetAgent, "agent")
	}
}

func TestDispatchPreviewModel_AllowKey(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	dm := next.(DispatchPreviewModel)
	if dm.decision != "allow" {
		t.Errorf("decision = %q, want %q", dm.decision, "allow")
	}
	if cmd == nil {
		t.Error("expected non-nil command (tea.Quit)")
	}
}

func TestDispatchPreviewModel_DenyKey(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	dm := next.(DispatchPreviewModel)
	if dm.decision != "deny" {
		t.Errorf("decision = %q, want %q", dm.decision, "deny")
	}
	if cmd == nil {
		t.Error("expected non-nil command (tea.Quit)")
	}
}

func TestDispatchPreviewModel_EscSetsDeny(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	dm := next.(DispatchPreviewModel)
	if dm.decision != "deny" {
		t.Errorf("decision = %q, want %q", dm.decision, "deny")
	}
	if cmd == nil {
		t.Error("expected non-nil command (tea.Quit)")
	}
}

func TestDispatchPreviewModel_QSetsDeny(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	dm := next.(DispatchPreviewModel)
	if dm.decision != "deny" {
		t.Errorf("decision = %q, want %q", dm.decision, "deny")
	}
	if cmd == nil {
		t.Error("expected non-nil command (tea.Quit)")
	}
}

func TestDispatchPreviewModel_CtrlCSetsDeny(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl, Text: "ctrl+c"})
	dm := next.(DispatchPreviewModel)
	if dm.decision != "deny" {
		t.Errorf("decision = %q, want %q", dm.decision, "deny")
	}
	if cmd == nil {
		t.Error("expected non-nil command (tea.Quit)")
	}
}

func TestDispatchPreviewModel_ToggleExpand(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	m.expanded = false // dispatch popups open expanded; start collapsed

	// Initially collapsed.
	if m.expanded {
		t.Error("expected not expanded initially")
	}

	// Press 'v' to expand.
	next, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	dm := next.(DispatchPreviewModel)
	if !dm.expanded {
		t.Error("expected expanded after pressing 'v'")
	}

	// Press 'v' again to collapse.
	next2, _ := dm.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	dm2 := next2.(DispatchPreviewModel)
	if dm2.expanded {
		t.Error("expected collapsed after pressing 'v' again")
	}
}

func TestDispatchPreviewModel_SpaceToggles(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	m.expanded = false // dispatch popups open expanded; start collapsed
	next, _ := m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	dm := next.(DispatchPreviewModel)
	if !dm.expanded {
		t.Error("expected expanded after pressing space")
	}
}

func TestDispatchPreviewModel_ScrollKeysIgnoredWhenCollapsed(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	// Scroll keys should not panic when collapsed.
	m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) //nolint:errcheck
	m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"}) //nolint:errcheck
}

func TestDispatchPreviewModel_MouseScrollIgnoredWhenCollapsed(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	// Mouse scroll should not panic when collapsed.
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown}) //nolint:errcheck
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})   //nolint:errcheck
}

func TestDispatchPreviewModel_MouseScrollWhenExpanded(t *testing.T) {
	longPrompt := strings.Repeat("line of prompt text\n", 50)
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, longPrompt, "", "agentctl dispatch --prompt '...'")
	// Expand first.
	next, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	dm := next.(DispatchPreviewModel)
	// Mouse scroll should not panic when expanded.
	dm.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown}) //nolint:errcheck
	dm.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})   //nolint:errcheck
}

func TestDispatchPreviewModel_PopupSize(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	sz := m.PopupSize()
	if sz.Width <= 0 {
		t.Errorf("PopupSize.Width = %d, want > 0", sz.Width)
	}
	if sz.Height <= 0 {
		t.Errorf("PopupSize.Height = %d, want > 0", sz.Height)
	}
}

// TestDispatchPreviewMaxPopupSize pins the chrome-height arithmetic so a
// future change to the metadata block, toggle line, or box chrome fails
// loudly here instead of silently clipping the popup at runtime. The hook
// (internal/hooks/dispatch_preview.go) derives its fixed tmux popup size from
// this same function — see its doc comment for why it can't measure the
// real content directly.
//
// Called with no options, DispatchPreviewMaxPopupSize falls back to the
// dispatchPreviewContentWidth/dpMaxVPHeight DEFAULTS (the field-based model's
// zero-value behavior) rather than the historical package consts being used
// directly -- see TestDispatchPreviewMaxPopupSize_WithOptions for the case
// where the hook supplies client-derived values instead.
func TestDispatchPreviewMaxPopupSize(t *testing.T) {
	sz := DispatchPreviewMaxPopupSize()

	wantW := dispatchPreviewContentWidth + 8
	wantH := dpMaxVPHeight + 16
	if sz.Width != wantW {
		t.Errorf("DispatchPreviewMaxPopupSize().Width = %d, want %d", sz.Width, wantW)
	}
	if sz.Height != wantH {
		t.Errorf("DispatchPreviewMaxPopupSize().Height = %d, want %d", sz.Height, wantH)
	}
	// The hook asks tmux display-popup for a 40-row-or-smaller popup by
	// default. If this ever fails, either dpMaxVPHeight or the chrome math
	// grew too large.
	const hookPopupHeightBudget = 40
	if sz.Height > hookPopupHeightBudget {
		t.Errorf("DispatchPreviewMaxPopupSize().Height = %d exceeds the hook's %d-row budget", sz.Height, hookPopupHeightBudget)
	}
}

// TestDispatchPreviewMaxPopupSize_WithOptions asserts that supplying
// WithContentWidth/WithMaxViewportHeight changes PopupSize()'s output
// through the SAME PopupSize() formula, rather than a separate code path --
// this is the field-based responsive-sizing behavior the fit helper in
// internal/hooks/popup_clamp.go relies on.
func TestDispatchPreviewMaxPopupSize_WithOptions(t *testing.T) {
	sz := DispatchPreviewMaxPopupSize(WithContentWidth(130), WithMaxViewportHeight(24))

	wantW := 130 + 8
	wantH := 24 + 16
	if sz.Width != wantW {
		t.Errorf("DispatchPreviewMaxPopupSize(...).Width = %d, want %d", sz.Width, wantW)
	}
	if sz.Height != wantH {
		t.Errorf("DispatchPreviewMaxPopupSize(...).Height = %d, want %d", sz.Height, wantH)
	}
}

// TestDispatchPreviewOption_ZeroIgnored asserts a zero (or negative) option
// value is ignored rather than zeroing out the model's width/height --
// callers pass 0 to mean "use the default", not "use zero".
func TestDispatchPreviewOption_ZeroIgnored(t *testing.T) {
	sz := DispatchPreviewMaxPopupSize(WithContentWidth(0), WithMaxViewportHeight(-5))

	wantW := dispatchPreviewContentWidth + 8
	wantH := dpMaxVPHeight + 16
	if sz.Width != wantW {
		t.Errorf("Width with zero option = %d, want default %d", sz.Width, wantW)
	}
	if sz.Height != wantH {
		t.Errorf("Height with negative option = %d, want default %d", sz.Height, wantH)
	}
}

// TestDispatchPreviewModel_PopupSizeTracksInjectedDimensions asserts
// PopupSize() on a constructed model reflects the contentWidth/maxVPHeight
// it was built with, not the old package consts -- the point of turning
// dispatchPreviewContentWidth/dpMaxVPHeight into per-model inputs.
func TestDispatchPreviewModel_PopupSizeTracksInjectedDimensions(t *testing.T) {
	// Use the command (syntax-highlight) path rather than the prompt
	// (glamour) path: glamour reflows prose and can merge/rewrap lines, so
	// its output line count doesn't map predictably to the raw input's line
	// count. highlight.Highlight preserves the line count exactly, which is
	// what this test needs to pin down the >max-lines clamp precisely.
	longCmd := strings.Repeat("echo line-of-output\n", 50)
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, "", "", longCmd,
		WithContentWidth(110), WithMaxViewportHeight(15))

	sz := m.PopupSize()
	if sz.Width != 110+8 {
		t.Errorf("PopupSize().Width = %d, want %d", sz.Width, 110+8)
	}
	// vpHeight is capped by WithMaxViewportHeight(15) since the command has
	// far more than 15 lines.
	if sz.Height != 15+16 {
		t.Errorf("PopupSize().Height = %d, want %d", sz.Height, 15+16)
	}

	// A model built with the default options must NOT match the injected
	// size above -- otherwise this test would pass even if the field were
	// never actually threaded through.
	mDefault := NewDispatchPreview("go-engineer", "python-engineer", 0, "", "", longCmd)
	if mDefault.PopupSize() == sz {
		t.Error("default-sized model unexpectedly matches the custom-sized model's PopupSize()")
	}
}

func TestDispatchPreviewModel_TitleRendering(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	view := m.View()
	rendered := view.Content
	if !strings.Contains(rendered, "go-engineer wants to dispatch python-engineer") {
		t.Errorf("View() should contain title, got: %s", rendered)
	}
}

func TestDispatchPreviewModel_TitleNamesProject(t *testing.T) {
	m := NewDispatchPreview("brandon", "general-purpose", 0, "post", "", "agentctl dispatch", WithProject("blsmth.com"))
	if !strings.Contains(m.View().Content, "brandon wants to dispatch general-purpose to blsmth.com") {
		t.Errorf("View() title missing the project: %s", m.View().Content)
	}
	// A long project name stays on one line, so the popup's line budget
	// holds.
	long := NewDispatchPreview("brandon", "general-purpose", 0, "post", "", "agentctl dispatch",
		WithProject(strings.Repeat("p", 200)), WithContentWidth(60))
	short := NewDispatchPreview("brandon", "general-purpose", 0, "post", "", "agentctl dispatch", WithContentWidth(60))
	if got, want := strings.Count(long.preToggle(), "\n"), strings.Count(short.preToggle(), "\n"); got != want {
		t.Errorf("long project title takes %d lines above the toggle, want %d", got, want)
	}
}

func TestDispatchPreviewModel_SummaryLineIssue(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 99, "", "", "agentctl dispatch --issue 99")
	summary := m.summaryLine()
	if summary != "#99" {
		t.Errorf("summaryLine() = %q, want %q", summary, "#99")
	}
}

func TestDispatchPreviewModel_SummaryLinePrompt(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, "fix the login bug", "", "agentctl dispatch --prompt 'fix the login bug'")
	summary := m.summaryLine()
	if !strings.Contains(summary, "fix the login bug") {
		t.Errorf("summaryLine() = %q, want to contain %q", summary, "fix the login bug")
	}
}

func TestDispatchPreviewModel_SummaryLineLongPromptTruncated(t *testing.T) {
	longPrompt := strings.Repeat("x", 100)
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, longPrompt, "", "agentctl dispatch --prompt '...'")
	summary := m.summaryLine()
	if len(summary) > 82 { // 80 chars + "…" = 81, plus some slack
		t.Errorf("summaryLine() length = %d, want <= 82 (truncated)", len(summary))
	}
	if !strings.Contains(summary, "…") {
		t.Errorf("summaryLine() = %q, expected truncation ellipsis", summary)
	}
}

func TestDispatchPreviewModel_SummaryLineAdHoc(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, "", "", "agentctl dispatch")
	summary := m.summaryLine()
	if summary != "(ad-hoc dispatch)" {
		t.Errorf("summaryLine() = %q, want %q", summary, "(ad-hoc dispatch)")
	}
}

// TestDispatchPreviewModel_SummaryLinePromptKey asserts that when a prompt
// key is set, summaryLine() shows the key name rather than a preview of the
// resolved content — the full content already appears in the viewport.
func TestDispatchPreviewModel_SummaryLinePromptKey(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, "resolved content of the spec", "ticket-42-spec", "agentctl dispatch --prompt-key ticket-42-spec")
	summary := m.summaryLine()
	if summary != "ticket-42-spec" {
		t.Errorf("summaryLine() = %q, want %q", summary, "ticket-42-spec")
	}
}

// TestDispatchPreviewModel_SummaryLineNoPromptKey asserts summaryLine() falls
// back to the truncated prompt preview when no prompt key is set (the
// existing --prompt behavior must be unaffected).
func TestDispatchPreviewModel_SummaryLineNoPromptKey(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, "fix the login bug", "", "agentctl dispatch --prompt 'fix the login bug'")
	summary := m.summaryLine()
	if summary != "fix the login bug" {
		t.Errorf("summaryLine() = %q, want %q", summary, "fix the login bug")
	}
}

// TestDispatchPreviewModel_PromptTruncationBoundary pins the exact-80 vs
// 81-char truncation boundary: exactly 80 chars must NOT be truncated, 81
// chars must be truncated to 79 chars + the ellipsis (80 chars total).
func TestDispatchPreviewModel_PromptTruncationBoundary(t *testing.T) {
	p80 := strings.Repeat("a", 80)
	got := truncatePromptPreview(p80)
	if got != p80 {
		t.Errorf("80-char prompt should not be truncated, got %q (len %d)", got, len(got))
	}
	if strings.Contains(got, "…") {
		t.Error("80-char prompt should not contain the ellipsis")
	}

	p81 := strings.Repeat("a", 81)
	got81 := truncatePromptPreview(p81)
	want81 := strings.Repeat("a", 79) + "…"
	if got81 != want81 {
		t.Errorf("81-char prompt truncation = %q, want %q", got81, want81)
	}
}

// TestDispatchPreviewModel_MetadataRows_Issue asserts the metadata block for
// an issue-based dispatch shows the issue number and the target agent.
func TestDispatchPreviewModel_MetadataRows_Issue(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42 --agent python-engineer")
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "#42") {
		t.Errorf("View() should contain issue number #42, got:\n%s", view)
	}
	if !strings.Contains(view, "python-engineer") {
		t.Errorf("View() should contain target agent, got:\n%s", view)
	}
	if !strings.Contains(view, "go-engineer") {
		t.Errorf("View() should contain requesting agent, got:\n%s", view)
	}
	if !strings.Contains(view, "Requested by") || !strings.Contains(view, "Dispatching") || !strings.Contains(view, "Issue") {
		t.Errorf("View() should contain metadata labels, got:\n%s", view)
	}
}

// TestDispatchPreviewModel_MetadataRows_AdHocPrompt asserts an ad-hoc
// (issue==0) prompt dispatch renders the truncated prompt in the metadata
// block.
func TestDispatchPreviewModel_MetadataRows_AdHocPrompt(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, "fix the login bug", "", "agentctl dispatch --prompt 'fix the login bug'")
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "fix the login bug") {
		t.Errorf("View() should contain the prompt preview, got:\n%s", view)
	}
	if !strings.Contains(view, "Prompt") {
		t.Errorf("View() should contain the Prompt label, got:\n%s", view)
	}
}

// TestDispatchPreviewModel_MetadataRows_Neither asserts a dispatch with
// neither an issue nor a prompt renders the ad-hoc placeholder.
func TestDispatchPreviewModel_MetadataRows_Neither(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, "", "", "agentctl dispatch --agent python-engineer")
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "(ad-hoc dispatch)") {
		t.Errorf("View() should contain the ad-hoc placeholder, got:\n%s", view)
	}
}

// TestDispatchPreviewModel_MetadataRows_PromptKey asserts a --prompt-key
// dispatch labels its metadata row "Prompt key" and shows the key name (not a
// preview of the resolved content) in that row.
func TestDispatchPreviewModel_MetadataRows_PromptKey(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, "the resolved markdown content", "ticket-42-spec", "agentctl dispatch --prompt-key ticket-42-spec --agent python-engineer")
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "Prompt key") {
		t.Errorf("View() should contain the 'Prompt key' label, got:\n%s", view)
	}
	if !strings.Contains(view, "ticket-42-spec") {
		t.Errorf("View() should contain the key name in the metadata row, got:\n%s", view)
	}
}

// TestDispatchPreviewModel_RendererSelection_Prompt asserts a prompt-based
// dispatch is rendered through the markdown (glamour) path: markdown syntax
// markers should be gone from the rendered form but still present in the raw
// form.
func TestDispatchPreviewModel_RendererSelection_Prompt(t *testing.T) {
	const md = "**bold** instructions for the task"
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, md, "", "")

	if !strings.Contains(m.rawContent, "**bold**") {
		t.Errorf("rawContent should retain literal markdown syntax, got: %q", m.rawContent)
	}
	if strings.Contains(m.renderedContent, "**bold**") {
		t.Errorf("renderedContent should have markdown syntax stripped by glamour, got: %q", m.renderedContent)
	}
	if !strings.Contains(stripANSI(m.renderedContent), "bold") {
		t.Errorf("renderedContent should still contain the underlying text, got: %q", m.renderedContent)
	}

	// The raw view (via the 'r' toggle) must still show the underlying text.
	next, _ := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	dm := next.(DispatchPreviewModel)
	if !strings.Contains(dm.currentViewportContent(), "**bold**") {
		t.Errorf("raw view should contain literal markdown text, got: %q", dm.currentViewportContent())
	}
}

// TestDispatchPreviewModel_RendererSelection_Command asserts a command-only
// dispatch (no prompt) is rendered through the syntax-highlight path, not
// glamour, and that the underlying command text survives in both the
// rendered and raw views.
func TestDispatchPreviewModel_RendererSelection_Command(t *testing.T) {
	const cmd = "agentctl dispatch --agent python-engineer --issue 7"
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, "", "", cmd)

	if m.rawContent != cmd {
		t.Errorf("rawContent = %q, want %q", m.rawContent, cmd)
	}
	if !strings.Contains(stripANSI(m.renderedContent), "agentctl dispatch") {
		t.Errorf("highlighted renderedContent should retain command text, got: %q", stripANSI(m.renderedContent))
	}

	next, _ := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	dm := next.(DispatchPreviewModel)
	if !strings.Contains(dm.currentViewportContent(), cmd) {
		t.Errorf("raw view should contain the literal command text, got: %q", dm.currentViewportContent())
	}
}

// TestDispatchPreviewModel_RawToggle verifies 'r' swaps rawMode and the hint
// bar reflects the current mode when the viewport is expanded.
func TestDispatchPreviewModel_RawToggle(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, "some prompt text", "", "")
	m.expanded = false // dispatch popups open expanded; start collapsed

	// Expand first so the raw/rendered hint is surfaced.
	next, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	dm := next.(DispatchPreviewModel)
	if dm.rawMode {
		t.Error("rawMode should start false")
	}
	expandedView := stripANSI(dm.View().Content)
	if !strings.Contains(expandedView, "raw") {
		t.Errorf("expanded, non-raw view's hint bar should offer 'r raw', got:\n%s", expandedView)
	}

	// Toggle to raw.
	next2, _ := dm.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	dm2 := next2.(DispatchPreviewModel)
	if !dm2.rawMode {
		t.Error("rawMode should be true after pressing 'r'")
	}
	rawView := stripANSI(dm2.View().Content)
	if !strings.Contains(rawView, "rendered") {
		t.Errorf("expanded, raw view's hint bar should offer 'r rendered', got:\n%s", rawView)
	}

	// Toggle back.
	next3, _ := dm2.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	dm3 := next3.(DispatchPreviewModel)
	if dm3.rawMode {
		t.Error("rawMode should be false again after a second 'r' press")
	}
}

// TestDispatchPreviewModel_RawToggle_NoOpCueWhenCollapsed asserts the
// raw/rendered hint cue is not shown while the viewport is collapsed, even
// after pressing 'r'.
func TestDispatchPreviewModel_RawToggle_NoOpCueWhenCollapsed(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, "some prompt text", "", "")
	m.expanded = false // dispatch popups open expanded; start collapsed

	next, _ := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	dm := next.(DispatchPreviewModel)
	if dm.expanded {
		t.Fatal("expected still collapsed")
	}
	view := stripANSI(dm.View().Content)
	if strings.Contains(view, "rendered") {
		t.Errorf("collapsed view should not surface the raw/rendered hint cue, got:\n%s", view)
	}
}

// TestDispatchPreviewModel_HintBarContainsAllowDeny asserts the hint bar
// always contains the allow/deny labels regardless of expand/raw state.
func TestDispatchPreviewModel_HintBarContainsAllowDeny(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "allow") {
		t.Errorf("View() hint bar should contain 'allow', got:\n%s", view)
	}
	if !strings.Contains(view, "deny") {
		t.Errorf("View() hint bar should contain 'deny', got:\n%s", view)
	}
}

// TestDispatchPreviewModel_EmptyPromptAndCommand asserts a dispatch with an
// empty (or whitespace-only) prompt and command does not panic and shows the
// ad-hoc placeholder.
func TestDispatchPreviewModel_EmptyPromptAndCommand(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 0, "", "", "")
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "(ad-hoc dispatch)") {
		t.Errorf("View() should show ad-hoc placeholder, got:\n%s", view)
	}
	if !strings.Contains(m.rawContent, "no additional prompt") {
		t.Errorf("rawContent should be the placeholder text, got: %q", m.rawContent)
	}

	// Whitespace-only prompt is still non-empty, so it takes the prompt path,
	// but must not panic.
	m2 := NewDispatchPreview("go-engineer", "python-engineer", 0, "   ", "", "")
	_ = m2.View()

	m3 := NewDispatchPreview("go-engineer", "python-engineer", 0, "", "", "   ")
	_ = m3.View()
}

func TestDispatchPreviewModel_ViewContainsToggle(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	view := m.View()
	rendered := view.Content
	if !strings.Contains(rendered, "View prompt") {
		t.Errorf("View() should contain 'View prompt' toggle, got: %s", rendered)
	}
}

func TestDispatchPreviewModel_ViewCollapsedDoesNotContainPrompt(t *testing.T) {
	// Use an issue dispatch so the prompt only appears in the collapsible viewport,
	// not in the metadata block (which would show "Issue #42", not the prompt).
	const uniquePrompt = "UNIQUE_PROMPT_SENTINEL_XYZ123"
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, uniquePrompt, "", "agentctl dispatch --issue 42 --agent python-engineer")
	view := m.View()
	// When collapsed, the viewport content (prompt) should not appear.
	if strings.Contains(view.Content, uniquePrompt) {
		t.Error("View() should not contain prompt content when collapsed")
	}
}

func TestDispatchPreviewModel_ViewExpandedContainsPrompt(t *testing.T) {
	const uniquePrompt = "UNIQUE_PROMPT_SENTINEL_XYZ123"
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, uniquePrompt, "", "agentctl dispatch --issue 42 --agent python-engineer")
	m.expanded = false // dispatch popups open expanded; start collapsed
	// Expand.
	next, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	dm := next.(DispatchPreviewModel)
	view := dm.View()
	// Glamour may insert ANSI codes mid-word (e.g. at underscore boundaries),
	// so strip ANSI before checking for the literal text — checking the raw
	// escaped string would be a false negative, not a real bug.
	if !strings.Contains(stripANSI(view.Content), uniquePrompt) {
		t.Errorf("View() should contain prompt content when expanded, got:\n%s", stripANSI(view.Content))
	}
}

func TestDispatchPreviewModel_Decision(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	if m.Decision() != "" {
		t.Errorf("Decision() = %q, want empty initially", m.Decision())
	}
}

// ---------------------------------------------------------------------------
// PreviewKindBash -- the plain Bash preview layout used by bash_preview
// ---------------------------------------------------------------------------

// TestDispatchPreviewModel_BashKind_Title asserts the title wording differs
// for a bash-kind preview -- there is no target agent to name.
func TestDispatchPreviewModel_BashKind_Title(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "", 0, "", "", "go test ./...", WithKind(PreviewKindBash))
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "go-engineer wants to run a command") {
		t.Errorf("View() should contain the bash-kind title, got:\n%s", view)
	}
	if strings.Contains(view, "wants to dispatch") {
		t.Errorf("View() should not contain the dispatch-kind title, got:\n%s", view)
	}
}

// TestDispatchPreviewModel_BashKind_OmitsDispatchMetadata asserts the
// target-agent / issue / prompt-key rows -- meaningless for a plain Bash
// command -- are omitted entirely, while "Requested by" (still meaningful)
// survives.
func TestDispatchPreviewModel_BashKind_OmitsDispatchMetadata(t *testing.T) {
	const cmd = "rm -rf /tmp/scratch"
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "should be ignored", "should-be-ignored-key", cmd, WithKind(PreviewKindBash))
	view := stripANSI(m.View().Content)

	if !strings.Contains(view, "Requested by") {
		t.Errorf("View() should still contain 'Requested by', got:\n%s", view)
	}
	if !strings.Contains(view, "go-engineer") {
		t.Errorf("View() should contain the requesting agent, got:\n%s", view)
	}
	if strings.Contains(view, "Dispatching") {
		t.Errorf("View() should not contain the 'Dispatching' row for a bash-kind preview, got:\n%s", view)
	}
	if strings.Contains(view, "Issue") {
		t.Errorf("View() should not contain an 'Issue' row for a bash-kind preview, got:\n%s", view)
	}
	if strings.Contains(view, "Prompt key") {
		t.Errorf("View() should not contain a 'Prompt key' row for a bash-kind preview, got:\n%s", view)
	}
	if strings.Contains(view, "python-engineer") {
		t.Errorf("View() should not render the (irrelevant) target agent, got:\n%s", view)
	}
}

// TestDispatchPreviewModel_BashKind_ShowsCommandContent asserts the command
// itself is what gets shown in the collapsible content area (same as a
// dispatch preview with no prompt falls back to the command).
func TestDispatchPreviewModel_BashKind_ShowsCommandContent(t *testing.T) {
	const cmd = "go build ./... -unique-sentinel-xyz"
	m := NewDispatchPreview("go-engineer", "", 0, "", "", cmd, WithKind(PreviewKindBash))
	if m.rawContent != cmd {
		t.Errorf("rawContent = %q, want %q", m.rawContent, cmd)
	}
}

// TestDispatchPreviewModel_BashKind_ToggleLabel asserts the toggle line reads
// "View command" rather than "View prompt" for a bash-kind preview.
func TestDispatchPreviewModel_BashKind_ToggleLabel(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "", 0, "", "", "go test ./...", WithKind(PreviewKindBash))
	view := m.View().Content
	if !strings.Contains(view, "View command") {
		t.Errorf("View() should contain 'View command' toggle, got: %s", view)
	}
	if strings.Contains(view, "View prompt") {
		t.Errorf("View() should not contain the dispatch-kind toggle label, got: %s", view)
	}
}

// TestDispatchPreviewModel_KindDefaultsToDispatch asserts that omitting
// WithKind (or passing an empty string) keeps the pre-existing dispatch
// layout unchanged -- this is the backward-compat contract for args files
// written before the Kind field existed.
func TestDispatchPreviewModel_KindDefaultsToDispatch(t *testing.T) {
	m := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42")
	if m.isBash() {
		t.Error("isBash() = true, want false for the zero-value kind")
	}

	m2 := NewDispatchPreview("go-engineer", "python-engineer", 42, "", "", "agentctl dispatch --issue 42", WithKind(""))
	if m2.isBash() {
		t.Error("isBash() = true, want false when WithKind(\"\") is passed explicitly")
	}
}

// TestDispatchPreviewModel_BashKind_PopupSizeUnaffected asserts that the
// popup-sizing contract (PopupSize / DispatchPreviewMaxPopupSize) is
// unaffected by kind -- the hook sizes the tmux popup before the kind-
// specific content is known, so the two must stay in lockstep regardless of
// which layout ends up being rendered.
func TestDispatchPreviewModel_BashKind_PopupSizeUnaffected(t *testing.T) {
	dispatchSz := DispatchPreviewMaxPopupSize(WithContentWidth(100), WithMaxViewportHeight(20))
	bashSz := DispatchPreviewMaxPopupSize(WithContentWidth(100), WithMaxViewportHeight(20), WithKind(PreviewKindBash))
	if dispatchSz != bashSz {
		t.Errorf("DispatchPreviewMaxPopupSize differs by kind: dispatch=%+v bash=%+v, want equal", dispatchSz, bashSz)
	}
}

func TestDispatchPreviewModel_AutoDetectLabel(t *testing.T) {
	m := NewDispatchPreview("lead", "", 7, "", "", "agentctl dispatch --issue 7")
	if m.targetAgent != "(auto-detect from labels)" {
		t.Errorf("targetAgent = %q, want auto-detect label", m.targetAgent)
	}
	if !strings.Contains(stripANSI(m.View().Content), "(auto-detect from labels)") {
		t.Error("View() missing auto-detect label")
	}
}

func TestTruncatePromptPreviewOneLine(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"short", "hello", "hello"},
		{"heading first", "## Problem\n\nbody text", "## Problem"},
		{"leading blanks", "\n\n  \n  do   the\tthing \nmore", "do the thing"},
		{"empty", " \n ", ""},
		{"multibyte truncation", strings.Repeat("é", 100), strings.Repeat("é", 79) + "…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncatePromptPreview(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "\n") || !utf8.ValidString(got) {
				t.Errorf("preview %q must be one valid-UTF-8 line", got)
			}
		})
	}
}

// The rendered view must fit inside the popup tmux opens for it, in every
// state, or the renderer scrolls and flickers.
func TestDispatchPreviewViewFitsPopup(t *testing.T) {
	long := "## Problem\n\n" + strings.Repeat("A long line of prompt text that wraps around the popup width. ", 6) +
		strings.Repeat("\n\n- item with some text", 80)
	prompts := map[string]string{
		"short":   "do a thing",
		"long":    long,
		"heading": "## Problem\n\nSomething is broken\n\n## Fix\n\nfix it",
		"unicode": "## Überblick\n\n" + strings.Repeat("日本語のテキスト ", 40),
	}
	for _, kind := range []string{PreviewKindBash, PreviewKindDispatch} {
		for _, width := range []int{60, 88, 120} {
			for name, prompt := range prompts {
				for _, expanded := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/w%d/%s/expanded=%v", kind, width, name, expanded), func(t *testing.T) {
						opts := []DispatchPreviewOption{WithContentWidth(width), WithMaxViewportHeight(22), WithKind(kind)}
						var m DispatchPreviewModel
						if kind == PreviewKindBash {
							m = NewDispatchPreview("lead", "", 0, "", "", prompt, opts...)
						} else {
							m = NewDispatchPreview("lead", "go-engineer", 0, prompt, "", "agentctl dispatch", opts...)
						}
						m.expanded = expanded
						view := m.View()
						if !view.AltScreen {
							t.Error("AltScreen must be set")
						}
						max := DispatchPreviewMaxPopupSize(opts...)
						if h := lipgloss.Height(view.Content); h > max.Height-2 {
							t.Errorf("height %d > popup inner %d", h, max.Height-2)
						}
						if w := lipgloss.Width(view.Content); w > max.Width-2 {
							t.Errorf("width %d > popup inner %d", w, max.Width-2)
						}
					})
				}
			}
		}
	}
}

// --- mouse ---

type dpClock struct{ t time.Time }

func (c *dpClock) now() time.Time { return c.t }

func newClickModel(kind string, expanded bool) (DispatchPreviewModel, *dpClock) {
	clk := &dpClock{t: time.Unix(1000, 0)}
	m := NewDispatchPreview("brandon", "go-engineer", 7, "do the thing\nline two", "", "agentctl dispatch --issue 7",
		WithKind(kind), WithClock(clk.now))
	m.expanded = expanded
	clk.t = clk.t.Add(dpClickArmDelay + time.Millisecond)
	return m, clk
}

// findCell locates needle in the ANSI-stripped rendered view and returns its
// (x, y) cell coordinates.
func findCell(t *testing.T, m DispatchPreviewModel, needle string) (int, int) {
	t.Helper()
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	for y, l := range lines {
		if i := strings.Index(l, needle); i >= 0 {
			return utf8.RuneCountInString(l[:i]), y
		}
	}
	t.Fatalf("%q not found in view:\n%s", needle, strings.Join(lines, "\n"))
	return 0, 0
}

func click(m DispatchPreviewModel, x, y int, btn tea.MouseButton) (DispatchPreviewModel, tea.Cmd) {
	mm, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: btn})
	mm, cmd := mm.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: btn})
	return mm.(DispatchPreviewModel), cmd
}

func TestDispatchPreviewMouse_Targets(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		expanded bool
		needle   string
		check    func(t *testing.T, m DispatchPreviewModel, cmd tea.Cmd)
	}{
		{"allow", PreviewKindDispatch, false, "allow", func(t *testing.T, m DispatchPreviewModel, cmd tea.Cmd) {
			if m.Decision() != "allow" || cmd == nil {
				t.Errorf("decision=%q cmd=%v", m.Decision(), cmd)
			}
		}},
		{"deny", PreviewKindDispatch, false, "deny", func(t *testing.T, m DispatchPreviewModel, cmd tea.Cmd) {
			if m.Decision() != "deny" || cmd == nil {
				t.Errorf("decision=%q cmd=%v", m.Decision(), cmd)
			}
		}},
		{"allow bash", PreviewKindBash, true, "allow", func(t *testing.T, m DispatchPreviewModel, cmd tea.Cmd) {
			if m.Decision() != "allow" || cmd == nil {
				t.Errorf("decision=%q", m.Decision())
			}
		}},
		{"hint toggle collapsed", PreviewKindDispatch, false, "toggle prompt", func(t *testing.T, m DispatchPreviewModel, _ tea.Cmd) {
			if !m.expanded {
				t.Error("want expanded")
			}
		}},
		{"hint toggle expanded", PreviewKindBash, true, "toggle prompt", func(t *testing.T, m DispatchPreviewModel, _ tea.Cmd) {
			if m.expanded {
				t.Error("want collapsed")
			}
		}},
		{"box toggle collapsed", PreviewKindDispatch, false, "▶ View prompt", func(t *testing.T, m DispatchPreviewModel, _ tea.Cmd) {
			if !m.expanded {
				t.Error("want expanded")
			}
		}},
		{"box toggle bash expanded", PreviewKindBash, true, "▼ View command", func(t *testing.T, m DispatchPreviewModel, _ tea.Cmd) {
			if m.expanded {
				t.Error("want collapsed")
			}
		}},
		{"raw", PreviewKindDispatch, true, "raw", func(t *testing.T, m DispatchPreviewModel, _ tea.Cmd) {
			if !m.rawMode {
				t.Error("want rawMode")
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newClickModel(tc.kind, tc.expanded)
			x, y := findCell(t, m, tc.needle)
			// "raw" also appears elsewhere only in the hint bar; use last line.
			if tc.name == "raw" {
				x, y = findCell(t, m, "r raw")
				x += 2
			}
			got, cmd := click(m, x, y, tea.MouseLeft)
			tc.check(t, got, cmd)
		})
	}
}

func TestDispatchPreviewMouse_RawUnclickableWhenCollapsed(t *testing.T) {
	m, _ := newClickModel(PreviewKindDispatch, false)
	if strings.Contains(ansi.Strip(m.View().Content), "r raw") {
		t.Fatal("raw hint should be absent when collapsed")
	}
	for _, seg := range m.hintSegments() {
		if seg.target == dpTargetRaw {
			t.Fatal("raw segment present when collapsed")
		}
	}
}

func TestDispatchPreviewMouse_Negative(t *testing.T) {
	m, clk := newClickModel(PreviewKindDispatch, true)
	ax, ay := findCell(t, m, "allow")
	dx, dy := findCell(t, m, "deny")

	t.Run("press one release another", func(t *testing.T) {
		mm, _ := m.Update(tea.MouseClickMsg{X: ax, Y: ay, Button: tea.MouseLeft})
		mm, cmd := mm.Update(tea.MouseReleaseMsg{X: dx, Y: dy, Button: tea.MouseLeft})
		if mm.(DispatchPreviewModel).Decision() != "" || cmd != nil {
			t.Error("expected no decision")
		}
	})
	t.Run("release without press", func(t *testing.T) {
		mm, cmd := m.Update(tea.MouseReleaseMsg{X: ax, Y: ay, Button: tea.MouseLeft})
		if mm.(DispatchPreviewModel).Decision() != "" || cmd != nil {
			t.Error("expected no decision")
		}
	})
	t.Run("right click", func(t *testing.T) {
		got, cmd := click(m, ax, ay, tea.MouseRight)
		if got.Decision() != "" || cmd != nil {
			t.Error("expected no decision")
		}
	})
	t.Run("wheel", func(t *testing.T) {
		mm, cmd := m.Update(tea.MouseWheelMsg{X: ax, Y: ay, Button: tea.MouseWheelDown})
		if mm.(DispatchPreviewModel).Decision() != "" || cmd != nil {
			t.Error("expected no decision")
		}
	})
	for _, needle := range []string{"wants to dispatch", "Requested by", "brandon", "scroll"} {
		t.Run("inert "+needle, func(t *testing.T) {
			x, y := findCell(t, m, needle)
			got, cmd := click(m, x, y, tea.MouseLeft)
			if got.Decision() != "" || cmd != nil || got.expanded != m.expanded || got.rawMode != m.rawMode {
				t.Error("click on inert area changed state")
			}
		})
	}
	t.Run("whitespace", func(t *testing.T) {
		got, cmd := click(m, 0, 0, tea.MouseLeft)
		if got.Decision() != "" || cmd != nil || got.expanded != m.expanded {
			t.Error("changed state")
		}
	})
	t.Run("arming delay", func(t *testing.T) {
		fresh, c := newClickModel(PreviewKindDispatch, true)
		c.t = fresh.openedAt.Add(dpClickArmDelay - time.Millisecond)
		got, cmd := click(fresh, ax, ay, tea.MouseLeft)
		if got.Decision() != "" || cmd != nil {
			t.Error("click inside arming delay should be ignored")
		}
		c.t = fresh.openedAt.Add(dpClickArmDelay)
		got, _ = click(fresh, ax, ay, tea.MouseLeft)
		if got.Decision() != "allow" {
			t.Error("click after arming delay should act")
		}
	})
	_ = clk
}
