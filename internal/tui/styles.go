package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
)

// NOTE: Do NOT add raw `lipgloss.Color("#xxxxxx")` hex literals to this file
// or any other file under internal/tui/. Use theme.Current() role fields or
// the named constants in colors.go so that swapping the active theme
// automatically updates all TUI popups.

// Popup content widths.
// Usable content width inside the box (after padding).
// tmux popup border takes 2 cols, no lipgloss border, padding takes 4.
// So: -w N → usable content = N - 6.
const (
	popupContentWidth = 44 // fits in -w 50 popup (50 - 6 = 44)
	formContentWidth  = 54 // fits in -w 60 popup (60 - 6 = 54)
)

var (
	// ── Toast notifications ──────────────────────────────────────────────────

	// toastInfoStyle is the base style for info-level toasts (foreground /
	// background colours are set per-render from the active theme's Accent role).
	toastInfoStyle lipgloss.Style

	// ── Popup boxes ─────────────────────────────────────────────────────────

	popupBoxStyle lipgloss.Style
	formBoxStyle  lipgloss.Style

	// ── Title bars ──────────────────────────────────────────────────────────

	popupTitleStyle lipgloss.Style
	formTitleStyle  lipgloss.Style

	// ── Horizontal rules ────────────────────────────────────────────────────

	popupRuleStyle lipgloss.Style

	// ── Buttons ─────────────────────────────────────────────────────────────

	btnSelectedStyle lipgloss.Style
	btnDangerStyle   lipgloss.Style
	btnNormalStyle   lipgloss.Style

	// ── Text variants ────────────────────────────────────────────────────────

	popupBodyStyle    lipgloss.Style
	popupDimStyle     lipgloss.Style
	popupAgentStyle   lipgloss.Style
	popupWarningStyle lipgloss.Style

	// ── Toggle indicators ────────────────────────────────────────────────────

	toggleOnStyle  lipgloss.Style
	toggleOffStyle lipgloss.Style

	// ── Row highlight (for settings, form selects) ───────────────────────────

	popupRowSelectedStyle lipgloss.Style
	popupRowStyle         lipgloss.Style
	formRowSelectedStyle  lipgloss.Style
	formRowStyle          lipgloss.Style

	// ── Step progress dots (create wizard) ───────────────────────────────────

	stepDotActiveStyle   lipgloss.Style
	stepDotInactiveStyle lipgloss.Style

	// ── Hint bars ────────────────────────────────────────────────────────────

	popupHintBarStyle lipgloss.Style
	formHintBarStyle  lipgloss.Style

	// ── Modal chrome ─────────────────────────────────────────────────────────

	modalBoxStyle   lipgloss.Style
	modalTitleStyle lipgloss.Style
)

// initToastStyles initialises the package-level toast base styles from the
// active theme. The per-level foreground and background colours are applied
// on top of these base styles at render time inside ToastModel.View.
func initToastStyles() {
	// Base style shared by all levels: bold text, no padding (content string
	// provides its own padding), no raw hex — colours are set per-render.
	base := lipgloss.NewStyle().Bold(true)
	toastInfoStyle = base
}

// initPopupStyles initialises all popup-level styles from the active theme.
// Must be called after config.Init() so that theme.Current() returns the
// correct palette.
func initPopupStyles() {
	t := theme.Current()

	popupBoxStyle = lipgloss.NewStyle().
		Padding(1, 2).
		Width(popupContentWidth + 4)

	formBoxStyle = lipgloss.NewStyle().
		Padding(1, 2).
		Width(formContentWidth + 4)

	popupTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Width(popupContentWidth).
		Align(lipgloss.Center)

	formTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Width(formContentWidth).
		Align(lipgloss.Center)

	popupRuleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Surface))

	btnSelectedStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2)

	btnDangerStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Red)).
		Padding(0, 2)

	btnNormalStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Padding(0, 2)

	popupBodyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	popupDimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	popupAgentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true)
	popupWarningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Red)).Italic(true)

	toggleOnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Green)).Bold(true)
	toggleOffStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Overlay))

	popupRowSelectedStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.Surface)).
		Width(popupContentWidth)
	popupRowStyle = lipgloss.NewStyle().Width(popupContentWidth)

	formRowSelectedStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.Surface)).
		Width(formContentWidth)
	formRowStyle = lipgloss.NewStyle().Width(formContentWidth)

	stepDotActiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent))
	stepDotInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Overlay))

	popupHintBarStyle = lipgloss.NewStyle().
		Width(popupContentWidth).
		Align(lipgloss.Center).
		MarginTop(1).
		Foreground(lipgloss.Color(t.Muted))

	formHintBarStyle = lipgloss.NewStyle().
		Width(formContentWidth).
		Align(lipgloss.Center).
		MarginTop(1).
		Foreground(lipgloss.Color(t.Muted))
}

// initModalStyles initialises the package-level modal chrome styles from the
// active theme. Must be called after config.Init() so that theme.Current()
// returns the correct palette.
func initModalStyles() {
	t := theme.Current()

	modalBoxStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Accent)).
		Padding(1, 2)

	modalTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Align(lipgloss.Center)
}

// InitStyles initialises all package-level TUI styles from the active theme.
// Call this once from main() after config.Init() has run, so that
// theme.Current() reflects the user's configured theme rather than the
// fallback palette.
func InitStyles() {
	initToastStyles()
	initPopupStyles()
	initMenuStyles()
	initImproveStyles()
	initHudlogStyles()
	initLaunchStyles()
	initTableStyles()
	initSessionStyles()
	initSettingsStyles()
	initTicketStyles()
	initTicketLaunchStyles()
	initSkillsStyles()
	initMemoryStyles()
	initAgentsStyles()
	initPermissionStyles()
	initHudStyles()
	initThemePickerStyles()
	initModalStyles()
	initHerdStyles()
}

// PopupSize describes the tmux display-popup dimensions for a TUI popup.
// These match the -w and -h flags passed to tmux display-popup.
type PopupSize struct {
	Width  int
	Height int
}

// renderPopupHints renders a centred hint bar for a small popup.
// Hints are key/label pairs: renderPopupHints("y", "confirm", "n", "cancel").
func renderPopupHints(hints ...string) string {
	return renderHintsInStyle(popupHintBarStyle, hints...)
}

// renderFormHints renders a centred hint bar for a larger form popup.
func renderFormHints(hints ...string) string {
	return renderHintsInStyle(formHintBarStyle, hints...)
}

// renderHintsInStyle builds the key·label hint string with the given bar style.
func renderHintsInStyle(barStyle lipgloss.Style, hints ...string) string {
	var parts []string
	for i := 0; i+1 < len(hints); i += 2 {
		parts = append(parts, menuHintKeyStyle.Render(hints[i])+" "+menuHintLabelStyle.Render(hints[i+1]))
	}
	sep := "  " + menuHintDotStyle.Render("·") + "  "
	return barStyle.Render(strings.Join(parts, sep))
}

// renderHintLine builds a plain (unstyled-bar) hint line from key/label pairs.
// Used in full-screen views where no box/bar wrapper is needed.
func renderHintLine(hints ...string) string {
	var parts []string
	for i := 0; i+1 < len(hints); i += 2 {
		parts = append(parts, menuHintKeyStyle.Render(hints[i])+" "+menuHintLabelStyle.Render(hints[i+1]))
	}
	sep := "  " + menuHintDotStyle.Render("·") + "  "
	return strings.Join(parts, sep)
}

// renderHintLineStyled behaves like renderHintLine, but lets the caller give
// each key/label pair its own key style (e.g. semantic green/red for
// allow/deny) while keeping the same "key label · key label" shape and dot
// separator. keyStyles must have one entry per key/label pair in hints; use
// menuHintKeyStyle for pairs that don't need a distinct color.
func renderHintLineStyled(keyStyles []lipgloss.Style, hints ...string) string {
	var parts []string
	for i := 0; i+1 < len(hints); i += 2 {
		idx := i / 2
		ks := menuHintKeyStyle
		if idx < len(keyStyles) {
			ks = keyStyles[idx]
		}
		parts = append(parts, ks.Render(hints[i])+" "+menuHintLabelStyle.Render(hints[i+1]))
	}
	sep := "  " + menuHintDotStyle.Render("·") + "  "
	return strings.Join(parts, sep)
}

// renderStepDots renders a row of progress dots for a multi-step wizard.
// total is the number of steps; current is the 0-based active step.
func renderStepDots(current, total int, contentWidth int) string {
	var dots []string
	for i := 0; i < total; i++ {
		if i == current {
			dots = append(dots, stepDotActiveStyle.Render("●"))
		} else {
			dots = append(dots, stepDotInactiveStyle.Render("○"))
		}
	}
	return lipgloss.NewStyle().
		Width(contentWidth).
		Align(lipgloss.Center).
		Render(strings.Join(dots, "  "))
}
