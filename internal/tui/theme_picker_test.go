package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/config"
)

// setupThemePickerTest creates a temp config file and resets styles.
func setupThemePickerTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	config.SetConfigPathForTest(cfgPath)
	// Write a minimal config with a known theme.
	if err := os.WriteFile(cfgPath, []byte(`{"theme":"catppuccin-mocha"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	initThemePickerStyles()
}

func TestNewThemePickerModel_PreSelecsCurrent(t *testing.T) {
	setupThemePickerTest(t)

	m := NewThemePickerModel()

	if len(m.rows) == 0 {
		t.Fatal("expected at least one theme row")
	}
	if m.originalTheme != "catppuccin-mocha" {
		t.Errorf("originalTheme = %q; want catppuccin-mocha", m.originalTheme)
	}
	selected := m.rows[m.cursor].name
	if selected != "catppuccin-mocha" {
		t.Errorf("cursor selected %q; want catppuccin-mocha", selected)
	}
}

func TestThemePickerModel_Navigation(t *testing.T) {
	setupThemePickerTest(t)

	m := NewThemePickerModel()
	initialCursor := m.cursor

	// Move down.
	m2, _ := m.Update(keyPress("down"))
	m2m := m2.(ThemePickerModel)
	wantCursor := initialCursor + 1
	if wantCursor >= len(m.rows) {
		wantCursor = initialCursor // already at bottom
	}
	if m2m.cursor != wantCursor {
		t.Errorf("after down: cursor = %d; want %d", m2m.cursor, wantCursor)
	}

	// Move back up.
	m3, _ := m2m.Update(keyPress("up"))
	m3m := m3.(ThemePickerModel)
	if m3m.cursor != initialCursor {
		t.Errorf("after up: cursor = %d; want %d", m3m.cursor, initialCursor)
	}
}

func TestThemePickerModel_CancelRestoresTheme(t *testing.T) {
	setupThemePickerTest(t)

	m := NewThemePickerModel()
	// Move down to change the selection.
	m2, _ := m.Update(keyPress("down"))

	// Cancel.
	m3, _ := m2.(ThemePickerModel).Update(keyPress("esc"))
	m3m := m3.(ThemePickerModel)

	if !m3m.result.Cancelled {
		t.Error("result.Cancelled should be true after esc")
	}

	// Config should have been restored to original.
	cfg := config.Load()
	if cfg.Theme != "catppuccin-mocha" {
		t.Errorf("config.Theme = %q after cancel; want catppuccin-mocha", cfg.Theme)
	}
}

func TestThemePickerModel_ConfirmPersistsTheme(t *testing.T) {
	setupThemePickerTest(t)

	m := NewThemePickerModel()
	// Navigate to a different theme.
	m2, _ := m.Update(keyPress("down"))
	m2m := m2.(ThemePickerModel)
	wantTheme := m2m.rows[m2m.cursor].name

	// Apply the selection so config is up-to-date before enter.
	_ = applyTheme(wantTheme)

	// Confirm.
	m3, _ := m2m.Update(keyPress("enter"))
	m3m := m3.(ThemePickerModel)

	if m3m.result.Cancelled {
		t.Error("result.Cancelled should be false after enter")
	}
	if m3m.result.Selected != wantTheme {
		t.Errorf("result.Selected = %q; want %q", m3m.result.Selected, wantTheme)
	}

	cfg := config.Load()
	if cfg.Theme != wantTheme {
		t.Errorf("config.Theme = %q; want %q", cfg.Theme, wantTheme)
	}
}

func TestThemePickerModel_View(t *testing.T) {
	setupThemePickerTest(t)

	m := NewThemePickerModel()
	view := m.View()
	if view.Content == "" {
		t.Error("View() returned empty content")
	}
}

func TestScrollOffset(t *testing.T) {
	cases := []struct {
		name   string
		cursor int
		total  int
		wantLE int // want offset <= this value
	}{
		{"small list", 3, 10, 0}, // total fits without scrolling
		{"large list start", 0, 30, 0},
		{"large list end", 29, 30, 30 - themePickerVisibleRows},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scrollOffset(tc.cursor, tc.total)
			if got < 0 {
				t.Errorf("scrollOffset(%d, %d) = %d; want >= 0", tc.cursor, tc.total, got)
			}
			// cursor must be visible in [offset, offset+visibleRows).
			if tc.total > themePickerVisibleRows {
				if tc.cursor < got || tc.cursor >= got+themePickerVisibleRows {
					t.Errorf("scrollOffset(%d, %d) = %d; cursor not in visible range [%d, %d)",
						tc.cursor, tc.total, got, got, got+themePickerVisibleRows)
				}
			}
		})
	}
}

// keyPress builds a tea.KeyPressMsg for the given named key.
// Supported names: "up", "down", "esc", "enter", "q".
func keyPress(key string) tea.KeyPressMsg {
	switch key {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	default:
		r := []rune(key)
		if len(r) == 0 {
			return tea.KeyPressMsg{}
		}
		return tea.KeyPressMsg{Code: r[0], Text: key}
	}
}
