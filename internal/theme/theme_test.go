package theme

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

// resetLoader resets the sync.Once so tests can exercise different load paths.
func resetLoader() {
	loadOnce = sync.Once{}
	builtins = nil
}

// TestNames verifies that Names() returns a sorted, de-duplicated list that
// contains every theme actually registered in the embedded builtin registry.
// It deliberately avoids a hardcoded count so that adding a new builtin
// theme cannot break this test — only *dropping* a registered theme can.
func TestNames(t *testing.T) {
	resetLoader()
	names := Names()

	if len(names) == 0 {
		t.Fatal("Names() returned no themes")
	}

	seen := make(map[string]bool, len(names))
	for i, n := range names {
		if seen[n] {
			t.Errorf("Names() contains duplicate entry %q", n)
		}
		seen[n] = true
		if i > 0 && names[i-1] > n {
			t.Errorf("Names() is not sorted: %q appears after %q", n, names[i-1])
		}
	}

	// Derive the expected theme set from the embedded registry itself, not
	// a literal list, so newly added themes don't require editing this test.
	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		t.Fatalf("reading builtin theme dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("builtin theme dir is empty")
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := builtinFS.ReadFile("builtin/" + e.Name())
		if err != nil {
			t.Fatalf("reading builtin/%s: %v", e.Name(), err)
		}
		var th Theme
		if err := json.Unmarshal(data, &th); err != nil {
			t.Fatalf("unmarshal builtin/%s: %v", e.Name(), err)
		}
		name := th.Name
		if name == "" {
			name = strings.TrimSuffix(e.Name(), ".json")
		}
		if !seen[name] {
			t.Errorf("registered builtin theme %q missing from Names() output %v", name, names)
		}
	}
}

func TestByName(t *testing.T) {
	resetLoader()
	tests := []struct {
		name      string
		wantFound bool
		checkFn   func(t *testing.T, theme Theme)
	}{
		{
			name:      "catppuccin-mocha",
			wantFound: true,
			checkFn: func(t *testing.T, th Theme) {
				check := func(field, got, want string) {
					t.Helper()
					if got != want {
						t.Errorf("%s: got %s, want %s", field, got, want)
					}
				}
				check("Base", th.Base, "#1e1e2e")
				check("Surface", th.Surface, "#313244")
				check("Overlay", th.Overlay, "#45475a")
				check("Muted", th.Muted, "#585b70")
				check("Subtle", th.Subtle, "#6c7086")
				check("Text", th.Text, "#cdd6f4")
				check("Bright", th.Bright, "#ffffff")
				check("Accent", th.Accent, "#7c3aed")
				check("Green", th.Green, "#a6e3a1")
				check("Yellow", th.Yellow, "#f9e2af")
				check("Red", th.Red, "#f38ba8")
				check("Blue", th.Blue, "#89b4fa")
				check("Sky", th.Sky, "#89dceb")
				check("Info", th.Info, "#4fc1ff")
			},
		},
		{
			name:      "bitchx",
			wantFound: true,
			checkFn: func(t *testing.T, th Theme) {
				if th.Accent != "#00aaaa" {
					t.Errorf("bitchx Accent: got %s, want #00aaaa", th.Accent)
				}
				if th.Base != "#000000" {
					t.Errorf("bitchx Base: got %s, want #000000", th.Base)
				}
			},
		},
		{
			name:      "nonexistent",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			th, ok := ByName(tt.name)
			if ok != tt.wantFound {
				t.Fatalf("ByName(%q) found=%v, want %v", tt.name, ok, tt.wantFound)
			}
			if tt.checkFn != nil {
				tt.checkFn(t, th)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name     string
		input    Theme
		wantBlue string
		wantSky  string
		wantInfo string
	}{
		{
			name:     "all empty uses accent/subtle fallbacks",
			input:    Theme{Accent: "#aabbcc", Subtle: "#112233"},
			wantBlue: "#aabbcc",
			wantSky:  "#aabbcc",
			wantInfo: "#112233",
		},
		{
			name:     "blue set but sky and info empty",
			input:    Theme{Accent: "#aaaaaa", Blue: "#0000ff", Subtle: "#999999"},
			wantBlue: "#0000ff",
			wantSky:  "#0000ff",
			wantInfo: "#999999",
		},
		{
			name:     "all fields set, none overridden",
			input:    Theme{Accent: "#aaaaaa", Blue: "#0000ff", Sky: "#00ffff", Info: "#ff00ff", Subtle: "#999999"},
			wantBlue: "#0000ff",
			wantSky:  "#00ffff",
			wantInfo: "#ff00ff",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			th := tt.input
			th.resolve()
			if th.Blue != tt.wantBlue {
				t.Errorf("Blue: got %s, want %s", th.Blue, tt.wantBlue)
			}
			if th.Sky != tt.wantSky {
				t.Errorf("Sky: got %s, want %s", th.Sky, tt.wantSky)
			}
			if th.Info != tt.wantInfo {
				t.Errorf("Info: got %s, want %s", th.Info, tt.wantInfo)
			}
		})
	}
}

func TestCurrentDefaultsToCatppuccinMocha(t *testing.T) {
	resetLoader()
	// Point config at a temp file with no theme set.
	tmp := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(tmp, []byte(`{}`), 0o644) //nolint:errcheck
	config.SetConfigPathForTest(tmp)
	t.Cleanup(func() { config.SetConfigPathForTest("") })

	th := Current()
	if th.Name != "catppuccin-mocha" {
		t.Errorf("expected catppuccin-mocha, got %s", th.Name)
	}
}

func TestCurrentReturnsBitchX(t *testing.T) {
	resetLoader()
	tmp := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(tmp, []byte(`{"theme":"bitchx"}`), 0o644) //nolint:errcheck
	config.SetConfigPathForTest(tmp)
	t.Cleanup(func() { config.SetConfigPathForTest("") })

	th := Current()
	if th.Name != "bitchx" {
		t.Errorf("expected bitchx, got %s", th.Name)
	}
}

// TestResolveNewFields verifies that a theme missing all new fields gets
// sensible non-empty defaults after resolve().
func TestResolveNewFields(t *testing.T) {
	th := Theme{
		Accent: "#aabbcc",
		Subtle: "#112233",
		Green:  "#00ff00",
		Yellow: "#ffff00",
		Red:    "#ff0000",
		Text:   "#ffffff",
	}
	th.resolve()

	checks := []struct {
		field string
		got   string
		want  string
	}{
		{"Orange", th.Orange, th.Yellow},
		{"TokenInput", th.TokenInput, th.Subtle},
		{"TokenOutput", th.TokenOutput, th.Green},
		{"CacheGood", th.CacheGood, th.Green},
		{"CacheWarn", th.CacheWarn, th.Yellow},
		{"CacheBad", th.CacheBad, th.Red},
		{"TurnNormal", th.TurnNormal, th.Text},
		{"TurnWarn", th.TurnWarn, th.Yellow},
		{"TurnHot", th.TurnHot, th.Orange},
		{"MetricHot", th.MetricHot, th.Orange},
		{"MetricDanger", th.MetricDanger, th.Red},
	}
	for _, c := range checks {
		if c.got == "" {
			t.Errorf("%s: got empty string, want non-empty", c.field)
		}
		if c.got != c.want {
			t.Errorf("%s: got %s, want %s", c.field, c.got, c.want)
		}
	}
}

// TestResolveExplicitFieldsPreserved verifies that explicitly set new fields
// are not overwritten by resolve() fallbacks.
func TestResolveExplicitFieldsPreserved(t *testing.T) {
	th := Theme{
		Accent: "#aabbcc",
		Subtle: "#112233",
		Green:  "#00ff00",
		Yellow: "#ffff00",
		Red:    "#ff0000",
		Text:   "#ffffff",
		// Explicit values that should survive resolve().
		Orange:       "#ff8800",
		TokenInput:   "#aaaaaa",
		TokenOutput:  "#bbbbbb",
		CacheGood:    "#cccccc",
		CacheWarn:    "#dddddd",
		CacheBad:     "#eeeeee",
		TurnNormal:   "#111111",
		TurnWarn:     "#222222",
		TurnHot:      "#333333",
		MetricHot:    "#444444",
		MetricDanger: "#555555",
	}
	th.resolve()

	checks := []struct {
		field string
		got   string
		want  string
	}{
		{"Orange", th.Orange, "#ff8800"},
		{"TokenInput", th.TokenInput, "#aaaaaa"},
		{"TokenOutput", th.TokenOutput, "#bbbbbb"},
		{"CacheGood", th.CacheGood, "#cccccc"},
		{"CacheWarn", th.CacheWarn, "#dddddd"},
		{"CacheBad", th.CacheBad, "#eeeeee"},
		{"TurnNormal", th.TurnNormal, "#111111"},
		{"TurnWarn", th.TurnWarn, "#222222"},
		{"TurnHot", th.TurnHot, "#333333"},
		{"MetricHot", th.MetricHot, "#444444"},
		{"MetricDanger", th.MetricDanger, "#555555"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %s, want %s (should not be overwritten)", c.field, c.got, c.want)
		}
	}
}

// TestAllBuiltinThemesHaveOrange verifies that every builtin theme has a
// non-empty Orange field after loading (either explicit or via resolve()).
func TestAllBuiltinThemesHaveOrange(t *testing.T) {
	resetLoader()
	for _, name := range Names() {
		th, ok := ByName(name)
		if !ok {
			t.Errorf("ByName(%q) returned not found", name)
			continue
		}
		if th.Orange == "" {
			t.Errorf("theme %q: Orange is empty after resolve()", name)
		}
	}
}

func TestUserThemeOverlay(t *testing.T) {
	// Create a temp user themes directory with a custom theme.
	userThemeDir := filepath.Join(t.TempDir(), ".config", "agentctl", "themes")
	if err := os.MkdirAll(userThemeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	customJSON := `{
		"name": "my-theme",
		"base": "#111111",
		"surface": "#222222",
		"overlay": "#333333",
		"muted": "#444444",
		"subtle": "#555555",
		"text": "#666666",
		"bright": "#777777",
		"accent": "#888888",
		"green": "#999999",
		"yellow": "#aaaaaa",
		"red": "#bbbbbb"
	}`
	if err := os.WriteFile(filepath.Join(userThemeDir, "my-theme.json"), []byte(customJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// Patch os.UserHomeDir by temporarily overriding HOME.
	t.Setenv("HOME", filepath.Dir(filepath.Dir(filepath.Dir(userThemeDir))))
	t.Setenv("XDG_CONFIG_HOME", "")

	resetLoader()
	t.Cleanup(resetLoader)

	th, ok := ByName("my-theme")
	if !ok {
		t.Fatal("expected my-theme to be found after user overlay")
	}
	if th.Base != "#111111" {
		t.Errorf("my-theme Base: got %s, want #111111", th.Base)
	}

	// Built-ins must still be present.
	if _, ok := ByName("catppuccin-mocha"); !ok {
		t.Error("catppuccin-mocha should still be available after user overlay")
	}

	names := Names()
	if len(names) != 7 {
		t.Errorf("expected 7 themes (6 built-in + 1 user), got %d: %v", len(names), names)
	}
}
