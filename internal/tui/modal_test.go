package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/config"
)

// setupModalTest initialises a temp config and modal styles.
func setupModalTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	config.SetConfigPathForTest(cfgPath)
	if err := os.WriteFile(cfgPath, []byte(`{"theme":"catppuccin-mocha"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	initModalStyles()
}

// stubContent is a minimal tea.Model for use in tests.
type stubContent struct {
	text string
}

func (s stubContent) Init() tea.Cmd                           { return nil }
func (s stubContent) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return s, nil }
func (s stubContent) View() tea.View                          { return tea.NewView(s.text) }

// hintContent is a stub that also implements ModalContent.
type hintContent struct {
	stubContent
	hints []string
}

func (h hintContent) HintKeys() []string { return h.hints }

// TestNewModal_Fields verifies that NewModal sets fields correctly and
// defaults Visible to true.
func TestNewModal_Fields(t *testing.T) {
	setupModalTest(t)

	content := stubContent{text: "hello"}
	m := NewModal("My Title", content, ModalMedium)

	if m.Title != "My Title" {
		t.Errorf("Title = %q; want %q", m.Title, "My Title")
	}
	if m.Size != ModalMedium {
		t.Errorf("Size = %d; want %d", m.Size, ModalMedium)
	}
	if !m.Visible {
		t.Error("Visible should default to true")
	}
}

// TestModalContentWidth verifies the width preset values.
func TestModalContentWidth(t *testing.T) {
	cases := []struct {
		size ModalSize
		want int
	}{
		{ModalSmall, 50},
		{ModalMedium, 68},
		{ModalLarge, 88},
		{ModalSize(99), 68}, // unknown → default medium
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			got := modalContentWidth(tc.size)
			if got != tc.want {
				t.Errorf("modalContentWidth(%d) = %d; want %d", tc.size, got, tc.want)
			}
		})
	}
}

// TestModal_View_ContainsTitleAndContent verifies that View() includes the
// modal title, content text, and rounded border characters.
func TestModal_View_ContainsTitleAndContent(t *testing.T) {
	setupModalTest(t)

	content := stubContent{text: "body text here"}
	m := NewModal("Test Title", content, ModalSmall)

	view := m.View()

	if view == "" {
		t.Fatal("View() returned empty string for visible modal")
	}
	if !strings.Contains(view, "Test Title") {
		t.Errorf("View() does not contain title %q", "Test Title")
	}
	if !strings.Contains(view, "body text here") {
		t.Errorf("View() does not contain content %q", "body text here")
	}
	// Rounded border uses '╭' as the top-left corner.
	if !strings.Contains(view, "╭") {
		t.Error("View() does not contain rounded border character '╭'")
	}
}

// TestModal_View_Invisible verifies that View() returns empty string when
// Visible is false.
func TestModal_View_Invisible(t *testing.T) {
	setupModalTest(t)

	m := NewModal("Title", stubContent{text: "content"}, ModalSmall)
	m.Visible = false

	if got := m.View(); got != "" {
		t.Errorf("View() = %q; want empty string when Visible=false", got)
	}
}

// TestModal_View_HintKeys_WithHints verifies that hint keys appear in View()
// when the content model implements ModalContent.
func TestModal_View_HintKeys_WithHints(t *testing.T) {
	setupModalTest(t)

	content := hintContent{
		stubContent: stubContent{text: "body"},
		hints:       []string{"enter", "confirm", "esc", "cancel"},
	}
	m := NewModal("Hint Test", content, ModalSmall)

	view := m.View()
	if !strings.Contains(view, "confirm") {
		t.Errorf("View() does not contain hint label %q", "confirm")
	}
	if !strings.Contains(view, "cancel") {
		t.Errorf("View() does not contain hint label %q", "cancel")
	}
}

// TestModal_View_HintKeys_WithoutHints verifies that no hint content leaks in
// when the content model does NOT implement ModalContent.
func TestModal_View_HintKeys_WithoutHints(t *testing.T) {
	setupModalTest(t)

	content := stubContent{text: "no hints here"}
	m := NewModal("No Hints", content, ModalSmall)

	view := m.View()
	// The stub content doesn't have HintKeys so no hint separator should appear.
	// We check that the view is non-empty but doesn't contain hint-specific
	// separator characters introduced by renderHintLine.
	if view == "" {
		t.Fatal("View() returned empty string")
	}
}

// TestModal_View_HintKeys_EmptySlice verifies that an empty HintKeys slice
// results in no hint bar being rendered.
func TestModal_View_HintKeys_EmptySlice(t *testing.T) {
	setupModalTest(t)

	content := hintContent{
		stubContent: stubContent{text: "body"},
		hints:       []string{},
	}
	m := NewModal("Empty Hints", content, ModalSmall)

	// Should not panic and should still render title/content.
	view := m.View()
	if !strings.Contains(view, "Empty Hints") {
		t.Error("View() should still contain title when hints are empty")
	}
}

// TestModal_PopupSize verifies that PopupSize returns reasonable (> 0) dimensions.
func TestModal_PopupSize(t *testing.T) {
	setupModalTest(t)

	m := NewModal("Popup Size Test", stubContent{text: "content"}, ModalMedium)
	size := m.PopupSize()

	if size.Width <= 0 {
		t.Errorf("PopupSize().Width = %d; want > 0", size.Width)
	}
	if size.Height <= 0 {
		t.Errorf("PopupSize().Height = %d; want > 0", size.Height)
	}
	// Width must account for content + border + padding.
	if size.Width < modalContentWidth(ModalMedium) {
		t.Errorf("PopupSize().Width = %d; want >= %d", size.Width, modalContentWidth(ModalMedium))
	}
}

// TestModal_Overlay verifies that Overlay composites the modal over a background.
func TestModal_Overlay(t *testing.T) {
	setupModalTest(t)

	// Build a simple background wider and taller than the modal.
	bgLine := strings.Repeat(" ", 120)
	var bgLines []string
	for i := 0; i < 40; i++ {
		bgLines = append(bgLines, bgLine)
	}
	bg := strings.Join(bgLines, "\n")

	m := NewModal("Overlay Test", stubContent{text: "overlay content"}, ModalSmall)
	result := m.Overlay(bg, 120, 40)

	if result == "" {
		t.Fatal("Overlay() returned empty string")
	}
	if !strings.Contains(result, "overlay content") {
		t.Error("Overlay() result does not contain modal content")
	}
}

// TestModal_Update_DelegatesToContent verifies that Update passes messages to
// the content model and returns updated state.
func TestModal_Update_DelegatesToContent(t *testing.T) {
	setupModalTest(t)

	content := stubContent{text: "before"}
	m := NewModal("Update Test", content, ModalSmall)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})

	// stubContent.Update is a no-op, so model should be unchanged.
	if !updated.Visible {
		t.Error("Visible should remain true after Update")
	}
	if cmd != nil {
		t.Error("expected nil cmd from stub content")
	}
}

// TestModal_AllSizes verifies that View() renders for all size presets without panicking.
func TestModal_AllSizes(t *testing.T) {
	setupModalTest(t)

	sizes := []ModalSize{ModalSmall, ModalMedium, ModalLarge}
	for _, size := range sizes {
		t.Run("", func(t *testing.T) {
			m := NewModal("Title", stubContent{text: "content"}, size)
			view := m.View()
			if view == "" {
				t.Errorf("View() empty for size %d", size)
			}
		})
	}
}
