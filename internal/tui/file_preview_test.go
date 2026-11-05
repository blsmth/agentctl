package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestFilePreviewModel_Init(t *testing.T) {
	m := NewFilePreview("/tmp/test.go", "package main\n", "go-engineer")
	if m.filePath != "/tmp/test.go" {
		t.Errorf("filePath = %q, want %q", m.filePath, "/tmp/test.go")
	}
	if m.content != "package main\n" {
		t.Errorf("content = %q, want %q", m.content, "package main\n")
	}
	if m.decision != "" {
		t.Errorf("decision = %q, want empty", m.decision)
	}
	if m.highlighted == "" {
		t.Error("highlighted should not be empty for non-empty content")
	}
	if m.agentName != "go-engineer" {
		t.Errorf("agentName = %q, want %q", m.agentName, "go-engineer")
	}
}

func TestFilePreviewModel_AgentNameDefault(t *testing.T) {
	m := NewFilePreview("/tmp/test.go", "package main\n", "")
	if m.agentName != "Agent" {
		t.Errorf("agentName = %q, want %q", m.agentName, "Agent")
	}
}

func TestFilePreviewModel_AllowKey(t *testing.T) {
	m := NewFilePreview("/tmp/test.go", "package main\n", "go-engineer")
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	fm := next.(FilePreviewModel)
	if fm.decision != "allow" {
		t.Errorf("decision = %q, want %q", fm.decision, "allow")
	}
	if cmd == nil {
		t.Error("expected non-nil command (tea.Quit)")
	}
}

func TestFilePreviewModel_DenyKey(t *testing.T) {
	m := NewFilePreview("/tmp/test.go", "package main\n", "go-engineer")
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	fm := next.(FilePreviewModel)
	if fm.decision != "deny" {
		t.Errorf("decision = %q, want %q", fm.decision, "deny")
	}
	if cmd == nil {
		t.Error("expected non-nil command (tea.Quit)")
	}
}

func TestFilePreviewModel_EscSetsDeny(t *testing.T) {
	m := NewFilePreview("/tmp/test.go", "package main\n", "go-engineer")
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	fm := next.(FilePreviewModel)
	if fm.decision != "deny" {
		t.Errorf("decision = %q, want %q", fm.decision, "deny")
	}
	if cmd == nil {
		t.Error("expected non-nil command (tea.Quit)")
	}
}

func TestFilePreviewModel_QSetsDeny(t *testing.T) {
	m := NewFilePreview("/tmp/test.go", "package main\n", "go-engineer")
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	fm := next.(FilePreviewModel)
	if fm.decision != "deny" {
		t.Errorf("decision = %q, want %q", fm.decision, "deny")
	}
	if cmd == nil {
		t.Error("expected non-nil command (tea.Quit)")
	}
}

func TestFilePreviewModel_EmptyContent(t *testing.T) {
	m := NewFilePreview("/tmp/empty.go", "", "go-engineer")
	if m.highlighted != "(empty file)" {
		t.Errorf("highlighted = %q, want %q", m.highlighted, "(empty file)")
	}
}

func TestFilePreviewModel_BinaryContent(t *testing.T) {
	m := NewFilePreview("/tmp/binary.bin", "hello\x00world", "go-engineer")
	if !m.isBinary {
		t.Error("expected isBinary = true")
	}
	if m.highlighted == "" {
		t.Error("expected non-empty highlighted for binary content")
	}
}

func TestFilePreviewModel_ScrollKeys(t *testing.T) {
	content := ""
	for i := 0; i < 100; i++ {
		content += "line content here\n"
	}
	m := NewFilePreview("/tmp/long.go", content, "go-engineer")

	// Scroll down should not panic.
	m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) //nolint:errcheck
	m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"}) //nolint:errcheck
}

func TestFilePreviewModel_MouseScroll(t *testing.T) {
	content := ""
	for i := 0; i < 100; i++ {
		content += "line content here\n"
	}
	m := NewFilePreview("/tmp/long.go", content, "go-engineer")

	// Mouse wheel should not panic.
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown}) //nolint:errcheck
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})   //nolint:errcheck
}

func TestFilePreviewModel_PopupSize(t *testing.T) {
	m := NewFilePreview("/tmp/test.go", "package main\n", "go-engineer")
	sz := m.PopupSize()
	if sz.Width <= 0 {
		t.Errorf("PopupSize.Width = %d, want > 0", sz.Width)
	}
	if sz.Height <= 0 {
		t.Errorf("PopupSize.Height = %d, want > 0", sz.Height)
	}
}

// TestFilePreviewMaxPopupSize_Defaults asserts calling with no options
// reproduces the historical fixed 108/32-derived size.
func TestFilePreviewMaxPopupSize_Defaults(t *testing.T) {
	sz := FilePreviewMaxPopupSize()

	wantW := filePreviewContentWidth + 8
	wantH := filePreviewMaxVPHeight + 12
	if sz.Width != wantW {
		t.Errorf("FilePreviewMaxPopupSize().Width = %d, want %d", sz.Width, wantW)
	}
	if sz.Height != wantH {
		t.Errorf("FilePreviewMaxPopupSize().Height = %d, want %d", sz.Height, wantH)
	}
}

// TestFilePreviewMaxPopupSize_WithOptions asserts supplying
// WithFileContentWidth/WithFileMaxViewportHeight changes the output through
// the same PopupSize() formula.
func TestFilePreviewMaxPopupSize_WithOptions(t *testing.T) {
	sz := FilePreviewMaxPopupSize(WithFileContentWidth(130), WithFileMaxViewportHeight(40))

	wantW := 130 + 8
	wantH := 40 + 12
	if sz.Width != wantW {
		t.Errorf("FilePreviewMaxPopupSize(...).Width = %d, want %d", sz.Width, wantW)
	}
	if sz.Height != wantH {
		t.Errorf("FilePreviewMaxPopupSize(...).Height = %d, want %d", sz.Height, wantH)
	}
}

// TestFilePreviewOption_ZeroIgnored asserts a zero (or negative) option
// value is ignored rather than zeroing out the model's width/height.
func TestFilePreviewOption_ZeroIgnored(t *testing.T) {
	sz := FilePreviewMaxPopupSize(WithFileContentWidth(0), WithFileMaxViewportHeight(-3))

	wantW := filePreviewContentWidth + 8
	wantH := filePreviewMaxVPHeight + 12
	if sz.Width != wantW {
		t.Errorf("Width with zero option = %d, want default %d", sz.Width, wantW)
	}
	if sz.Height != wantH {
		t.Errorf("Height with negative option = %d, want default %d", sz.Height, wantH)
	}
}

// TestFilePreviewModel_PopupSizeTracksInjectedDimensions asserts PopupSize()
// on a constructed model reflects the contentWidth/maxVPHeight it was built
// with, not the old package consts.
func TestFilePreviewModel_PopupSizeTracksInjectedDimensions(t *testing.T) {
	content := strings.Repeat("line content here\n", 100)
	m := NewFilePreview("/tmp/long.go", content, "go-engineer",
		WithFileContentWidth(120), WithFileMaxViewportHeight(20))

	sz := m.PopupSize()
	if sz.Width != 120+8 {
		t.Errorf("PopupSize().Width = %d, want %d", sz.Width, 120+8)
	}
	// vpHeight is capped by WithFileMaxViewportHeight(20) since the file has
	// far more than 20 lines.
	if sz.Height != 20+12 {
		t.Errorf("PopupSize().Height = %d, want %d", sz.Height, 20+12)
	}

	mDefault := NewFilePreview("/tmp/long.go", content, "go-engineer")
	if mDefault.PopupSize() == sz {
		t.Error("default-sized model unexpectedly matches the custom-sized model's PopupSize()")
	}
}

func TestFilePreviewModel_TitleRendering(t *testing.T) {
	m := NewFilePreview("/foo/bar.go", "package main\n", "go-engineer")
	view := m.View()
	rendered := view.Content
	if !strings.Contains(rendered, "go-engineer wants to write to") {
		t.Errorf("View() output should contain %q, got: %s", "go-engineer wants to write to", rendered)
	}
	if !strings.Contains(rendered, "/foo/bar.go") {
		t.Errorf("View() output should contain %q", "/foo/bar.go")
	}
}
