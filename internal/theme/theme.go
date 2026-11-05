package theme

import (
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/blsmth/agentctl/internal/config"
)

// Theme holds the full color palette for a UI theme.
type Theme struct {
	Name string `json:"name"`

	Base    string `json:"base"`    // darkest bg
	Surface string `json:"surface"` // elevated surface, row highlight
	Overlay string `json:"overlay"` // borders, rules
	Muted   string `json:"muted"`   // dim text, hints
	Subtle  string `json:"subtle"`  // secondary text
	Text    string `json:"text"`    // primary text
	Bright  string `json:"bright"`  // headings, bold

	Accent string `json:"accent"` // primary interactive
	Green  string `json:"green"`  // success / working
	Yellow string `json:"yellow"` // warning / blocked
	Red    string `json:"red"`    // error / danger

	Blue string `json:"blue"` // tool calls, info highlights
	Sky  string `json:"sky"`  // section headers, secondary info
	Info string `json:"info"` // tertiary informational, badges

	Orange string `json:"orange,omitempty"` // warning-warm, between yellow and red

	// Metric bar semantic colors — used by chrome_bar.go renderMetricsLine().
	// All have resolve() fallbacks, so existing user themes work without changes.
	TokenInput   string `json:"token_input,omitempty"`   // color for input token counts (default: Subtle)
	TokenOutput  string `json:"token_output,omitempty"`  // color for output token counts (default: Green)
	CacheGood    string `json:"cache_good,omitempty"`    // cache hit rate ≥60% (default: Green)
	CacheWarn    string `json:"cache_warn,omitempty"`    // cache hit rate 30-59% (default: Yellow)
	CacheBad     string `json:"cache_bad,omitempty"`     // cache hit rate <30% (default: Red)
	TurnNormal   string `json:"turn_normal,omitempty"`   // turns 1-20 (default: Text)
	TurnWarn     string `json:"turn_warn,omitempty"`     // turns 21-50 (default: Yellow)
	TurnHot      string `json:"turn_hot,omitempty"`      // turns >50 (default: Orange)
	MetricHot    string `json:"metric_hot,omitempty"`    // generic "something is expensive" (default: Orange)
	MetricDanger string `json:"metric_danger,omitempty"` // generic "something is alarming" (default: Red)
}

// resolve fills empty fields with sensible fallbacks so that themes that
// predate newer fields remain fully functional.
func (t *Theme) resolve() {
	if t.Blue == "" {
		t.Blue = t.Accent
	}
	if t.Sky == "" {
		t.Sky = t.Blue
	}
	if t.Info == "" {
		t.Info = t.Subtle
	}
	if t.Orange == "" {
		t.Orange = t.Yellow
	}
	if t.TokenInput == "" {
		t.TokenInput = t.Subtle
	}
	if t.TokenOutput == "" {
		t.TokenOutput = t.Green
	}
	if t.CacheGood == "" {
		t.CacheGood = t.Green
	}
	if t.CacheWarn == "" {
		t.CacheWarn = t.Yellow
	}
	if t.CacheBad == "" {
		t.CacheBad = t.Red
	}
	if t.TurnNormal == "" {
		t.TurnNormal = t.Text
	}
	if t.TurnWarn == "" {
		t.TurnWarn = t.Yellow
	}
	if t.TurnHot == "" {
		t.TurnHot = t.Orange
	}
	if t.MetricHot == "" {
		t.MetricHot = t.Orange
	}
	if t.MetricDanger == "" {
		t.MetricDanger = t.Red
	}
}

//go:embed builtin/*.json
var builtinFS embed.FS

var (
	loadOnce sync.Once
	builtins map[string]Theme
)

// defaultTheme is the fallback when no theme is configured or matched.
const defaultTheme = "catppuccin-mocha"

// loadThemes initialises the builtins map by parsing embedded JSON files, then
// overlays any user-defined themes found in ~/.config/agentctl/themes/*.json.
// User themes take precedence on name collision.
func loadThemes() {
	builtins = make(map[string]Theme)

	entries, err := builtinFS.ReadDir("builtin")
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := builtinFS.ReadFile("builtin/" + e.Name())
			if err != nil {
				continue
			}
			var t Theme
			if err := json.Unmarshal(data, &t); err != nil {
				continue
			}
			if t.Name == "" {
				t.Name = strings.TrimSuffix(e.Name(), ".json")
			}
			t.resolve()
			builtins[t.Name] = t
		}
	}

	// Overlay user-defined themes.
	globalDir, err := config.GlobalDir()
	if err == nil {
		userDir := filepath.Join(globalDir, "themes")
		files, err := os.ReadDir(userDir)
		if err == nil {
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(userDir, f.Name()))
				if err != nil {
					continue
				}
				var t Theme
				if err := json.Unmarshal(data, &t); err != nil {
					continue
				}
				if t.Name == "" {
					t.Name = strings.TrimSuffix(f.Name(), ".json")
				}
				t.resolve()
				builtins[t.Name] = t
			}
		}
	}
}

func themes() map[string]Theme {
	loadOnce.Do(loadThemes)
	return builtins
}

// Current returns the active theme based on the global config.
// Falls back to catppuccin-mocha if the configured theme is unknown or unset.
func Current() Theme {
	cfg := config.Load()
	all := themes()
	if t, ok := all[cfg.Theme]; ok {
		return t
	}
	if t, ok := all[defaultTheme]; ok {
		return t
	}
	// Last-resort: return first available theme.
	for _, t := range all {
		return t
	}
	return Theme{Name: defaultTheme}
}

// ByName returns the theme with the given name and whether it was found.
func ByName(name string) (Theme, bool) {
	t, ok := themes()[name]
	return t, ok
}

// Names returns a sorted list of all available theme names (built-ins + user themes).
func Names() []string {
	all := themes()
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
