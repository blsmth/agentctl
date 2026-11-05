package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/theme"
)

// themePickerContentWidth is the usable content width inside the theme picker box.
// Popup is opened at -w 60, so content = 60 - 6 = 54.
const themePickerContentWidth = 54

// themePickerVisibleRows is the max number of theme rows shown at once.
const themePickerVisibleRows = 14

// themePickerRow holds pre-computed display data for one theme entry.
type themePickerRow struct {
	name   string
	swatch string // pre-rendered color swatch blocks
}

// ThemePickerResult is the output after the picker exits.
type ThemePickerResult struct {
	// Selected is the chosen theme name. Empty when cancelled.
	Selected  string
	Cancelled bool
}

// ThemePickerModel is a Bubble Tea popup for picking the active UI theme.
//
// Navigation:
//   - ↑/↓ or j/k — move cursor, live-preview each theme
//   - enter       — confirm selection and exit
//   - esc/q       — cancel and restore the original theme
type ThemePickerModel struct {
	rows          []themePickerRow
	cursor        int
	offset        int    // scroll offset (first visible row index)
	originalTheme string // theme active when the popup opened; restored on cancel
	result        ThemePickerResult
	quitting      bool
}

// themePickerStyles holds package-level styles for the theme picker.
var (
	themePickerBoxStyle     lipgloss.Style
	themePickerTitleStyle   lipgloss.Style
	themePickerRowSelStyle  lipgloss.Style
	themePickerRowStyle     lipgloss.Style
	themePickerHintBarStyle lipgloss.Style
	themePickerActiveStyle  lipgloss.Style
)

// initThemePickerStyles initialises theme-picker styles from the active theme.
// Called by InitStyles() on startup.
func initThemePickerStyles() {
	t := theme.Current()

	themePickerBoxStyle = lipgloss.NewStyle().
		Padding(1, 2).
		Width(themePickerContentWidth + 4)

	themePickerTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Width(themePickerContentWidth).
		Align(lipgloss.Center)

	themePickerRowSelStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.Surface)).
		Width(themePickerContentWidth)

	themePickerRowStyle = lipgloss.NewStyle().
		Width(themePickerContentWidth)

	themePickerHintBarStyle = lipgloss.NewStyle().
		Width(themePickerContentWidth).
		Align(lipgloss.Center).
		MarginTop(1).
		Foreground(lipgloss.Color(t.Muted))

	themePickerActiveStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)
}

// buildSwatch returns a string of small colored blocks representing the theme's
// accent, green, yellow and red roles — a quick visual fingerprint.
func buildSwatch(t theme.Theme) string {
	block := "  "
	accent := lipgloss.NewStyle().Background(lipgloss.Color(t.Accent)).Render(block)
	green := lipgloss.NewStyle().Background(lipgloss.Color(t.Green)).Render(block)
	yellow := lipgloss.NewStyle().Background(lipgloss.Color(t.Yellow)).Render(block)
	red := lipgloss.NewStyle().Background(lipgloss.Color(t.Red)).Render(block)
	return accent + green + yellow + red
}

// NewThemePickerModel creates a ThemePickerModel pre-selected on the currently
// active theme. The original theme is saved so cancel can restore it.
func NewThemePickerModel() ThemePickerModel {
	names := theme.Names()
	rows := make([]themePickerRow, len(names))
	for i, name := range names {
		t, _ := theme.ByName(name)
		rows[i] = themePickerRow{
			name:   name,
			swatch: buildSwatch(t),
		}
	}

	// Pre-select the currently active theme.
	originalTheme := config.Load().Theme
	cursor := 0
	for i, r := range rows {
		if r.name == originalTheme {
			cursor = i
			break
		}
	}

	// Compute initial scroll offset so the selection is centred.
	offset := scrollOffset(cursor, len(rows))

	return ThemePickerModel{
		rows:          rows,
		cursor:        cursor,
		offset:        offset,
		originalTheme: originalTheme,
	}
}

// scrollOffset returns the scroll offset that keeps cursor in view.
func scrollOffset(cursor, total int) int {
	if total <= themePickerVisibleRows {
		return 0
	}
	// Centre the cursor in the visible window.
	offset := cursor - themePickerVisibleRows/2
	if offset < 0 {
		offset = 0
	}
	if offset+themePickerVisibleRows > total {
		offset = total - themePickerVisibleRows
	}
	return offset
}

// PopupSize returns the tmux display-popup dimensions for the theme picker.
func (m ThemePickerModel) PopupSize() PopupSize {
	return PopupDimensions(m.View().Content)
}

// Result returns the picker result after quitting.
func (m ThemePickerModel) Result() ThemePickerResult {
	return m.result
}

func (m ThemePickerModel) Init() tea.Cmd {
	return nil
}

func (m ThemePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			// Cancel: restore original theme.
			_ = applyTheme(m.originalTheme)
			m.result = ThemePickerResult{Cancelled: true}
			m.quitting = true
			return m, tea.Quit

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.offset = scrollOffset(m.cursor, len(m.rows))
				_ = applyTheme(m.rows[m.cursor].name)
			}

		case "down", "j":
			if m.cursor < len(m.rows)-1 {
				m.cursor++
				m.offset = scrollOffset(m.cursor, len(m.rows))
				_ = applyTheme(m.rows[m.cursor].name)
			}

		case "enter":
			// Confirm: leave selected theme saved.
			if len(m.rows) > 0 {
				m.result = ThemePickerResult{Selected: m.rows[m.cursor].name}
			}
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// applyTheme writes the given theme name to config so the chrome bar picks it
// up on its next render tick.
func applyTheme(name string) error {
	cfg := config.Load()
	cfg.Theme = name
	return config.Save(cfg)
}

func (m ThemePickerModel) View() tea.View {
	var b strings.Builder

	b.WriteString(themePickerTitleStyle.Render("Theme Picker"))
	b.WriteString("\n\n")

	end := m.offset + themePickerVisibleRows
	if end > len(m.rows) {
		end = len(m.rows)
	}

	for i := m.offset; i < end; i++ {
		row := m.rows[i]
		selected := i == m.cursor

		// Build the display name (mark the active theme).
		nameStr := row.name
		if row.name == m.originalTheme {
			nameStr = themePickerActiveStyle.Render(nameStr)
		}

		// Pad so swatch is right-aligned within the content width.
		nameWidth := lipgloss.Width(nameStr)
		swatchWidth := lipgloss.Width(row.swatch)
		padding := themePickerContentWidth - nameWidth - swatchWidth
		if padding < 1 {
			padding = 1
		}

		line := nameStr + strings.Repeat(" ", padding) + row.swatch

		if selected {
			b.WriteString(themePickerRowSelStyle.Render(line))
		} else {
			b.WriteString(themePickerRowStyle.Render(line))
		}
		b.WriteString("\n")
	}

	// Scroll indicators.
	if len(m.rows) > themePickerVisibleRows {
		total := len(m.rows)
		scrollInfo := lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Current().Muted)).
			Width(themePickerContentWidth).
			Align(lipgloss.Right).
			Render(func() string {
				if m.offset > 0 && m.offset+themePickerVisibleRows < total {
					return "↑↓ scroll"
				} else if m.offset > 0 {
					return "↑ scroll"
				}
				return "↓ scroll"
			}())
		b.WriteString(scrollInfo)
		b.WriteString("\n")
	}

	b.WriteString(renderHintsInStyle(themePickerHintBarStyle,
		"↑↓", "navigate",
		"enter", "confirm",
		"esc", "cancel",
	))

	return tea.NewView(themePickerBoxStyle.Render(b.String()))
}
