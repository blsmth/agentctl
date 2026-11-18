package tui

import (
	"os"
	"path/filepath"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/theme"
)

func useThemeConfig(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	config.SetConfigPathForTest(path)
	writeThemeConfig(t, path, name)
	return path
}

func writeThemeConfig(t *testing.T, path, name string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"theme":"`+name+`"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func twoDistinctThemes(t *testing.T) (a, b theme.Theme) {
	t.Helper()
	all := theme.Names()
	for _, n1 := range all {
		for _, n2 := range all {
			x, _ := theme.ByName(n1)
			y, _ := theme.ByName(n2)
			if n1 != n2 && x.Surface != y.Surface && x.Accent != y.Accent {
				return x, y
			}
		}
	}
	t.Skip("need two themes with distinct colours")
	return a, b
}

func TestFleetTickPicksUpThemeChange(t *testing.T) {
	a, b := twoDistinctThemes(t)
	path := useThemeConfig(t, a.Name)
	InitStyles()
	m := newFleetModel()

	if got := fleetSelectedStyle.GetBackground(); got != lipgloss.Color(a.Surface) {
		t.Fatalf("initial background = %v; want %s", got, a.Surface)
	}

	writeThemeConfig(t, path, b.Name)
	m.Update(fleetTickMsg{})

	if got := fleetSelectedStyle.GetBackground(); got != lipgloss.Color(b.Surface) {
		t.Errorf("background after tick = %v; want %s", got, b.Surface)
	}
	if got := fleetPalette()[0]; got != b.Accent {
		t.Errorf("palette[0] = %s; want %s", got, b.Accent)
	}
}

func TestFleetTickUnchangedThemeDoesNotRebuild(t *testing.T) {
	a, _ := twoDistinctThemes(t)
	useThemeConfig(t, a.Name)
	InitStyles()
	m := newFleetModel()

	// A sentinel survives only if no rebuild happens.
	fleetSelectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("#123456"))
	m.Update(fleetTickMsg{})
	if got := fleetSelectedStyle.GetBackground(); got != lipgloss.Color("#123456") {
		t.Errorf("styles rebuilt on unchanged theme; background = %v", got)
	}
}

func TestFleetTickUnknownThemeStable(t *testing.T) {
	useThemeConfig(t, "no-such-theme")
	InitStyles()
	if refreshThemeIfChanged() {
		t.Error("unknown theme (resolved to fallback) rebuilt on every check")
	}
}
