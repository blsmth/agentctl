// Package tokens is the single source of truth for model identity, context
// window size, output reserve, and pricing. It has zero internal dependencies
// so any package can import it without risk of an import cycle.
package tokens

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Family identifies the Claude model family (opus/sonnet/haiku/fable), used
// for fallback resolution when a specific model version isn't in the
// registry.
type Family string

const (
	FamilyOpus    Family = "opus"
	FamilySonnet  Family = "sonnet"
	FamilyHaiku   Family = "haiku"
	FamilyFable   Family = "fable"
	FamilyUnknown Family = ""
)

// Pricing is $ per million tokens.
type Pricing struct {
	InputPerMTok       float64
	OutputPerMTok      float64
	CacheReadPerMTok   float64
	CacheCreatePerMTok float64
}

// Model is a fully resolved model description.
type Model struct {
	Key           string  // registry key that matched, e.g. "claude-opus-4"
	Family        Family  // opus/sonnet/haiku/fable, "" if unknown
	Short         string  // badge text: "opus" / "sonnet" / "haiku" / "fable" / raw name / "" if unknown
	Label         string  // display label: "opus-4.8" / "sonnet-5" / "haiku-4.5" / "" if unknown
	ContextWindow int     // total window in tokens
	MaxOutput     int     // max output tokens (the reserve)
	Pricing       Pricing // $ per million tokens
	Known         bool    // false = nothing matched (family fallback or total unknown)
	Source        string  // "config", "built-in", "family", or "" (unknown/sentinel)
}

// ModelOverride is a partial override for a single model, sourced from
// config. A zero/empty field means "don't override" — fall through to the
// built-in/family value.
type ModelOverride struct {
	ContextWindow      int
	MaxOutput          int
	Short              string
	InputPerMTok       float64
	OutputPerMTok      float64
	CacheReadPerMTok   float64
	CacheCreatePerMTok float64
}

// DefaultContextWindow is the standard Claude context window size (200k
// tokens), used when nothing else is known about a model.
const DefaultContextWindow = 200_000

// familyDefaults holds the current published Anthropic figures for the
// NEWEST known generation of each model family, used as a fallback when a
// specific model version isn't in the built-in table or config overrides.
// Family fallback exists to handle models newer than the built-in table, so
// it guesses "newest", not "retired" — a brand-new model string is far more
// likely to be a successor of the newest known generation than a repeat of
// an old one. New models should be taught via config override
// (tokens.SetOverrides / ModelsConfig.Overrides) between agentctl releases,
// or by refreshing the builtins table below at the next release.
var familyDefaults = map[Family]Model{
	FamilyOpus: {
		Family:        FamilyOpus,
		Short:         "opus",
		Label:         "opus-5",
		ContextWindow: 1_000_000,
		MaxOutput:     128_000,
		Pricing: Pricing{
			InputPerMTok:       5.00,
			OutputPerMTok:      25.00,
			CacheReadPerMTok:   0.50,
			CacheCreatePerMTok: 6.25,
		},
	},
	FamilySonnet: {
		Family:        FamilySonnet,
		Short:         "sonnet",
		Label:         "sonnet-5",
		ContextWindow: 1_000_000,
		MaxOutput:     128_000,
		Pricing: Pricing{
			InputPerMTok:       2.00,
			OutputPerMTok:      10.00,
			CacheReadPerMTok:   0.20,
			CacheCreatePerMTok: 2.50,
		},
	},
	FamilyHaiku: {
		Family:        FamilyHaiku,
		Short:         "haiku",
		Label:         "haiku-4.5",
		ContextWindow: 200_000,
		MaxOutput:     64_000,
		Pricing: Pricing{
			InputPerMTok:       1.00,
			OutputPerMTok:      5.00,
			CacheReadPerMTok:   0.10,
			CacheCreatePerMTok: 1.25,
		},
	},
	FamilyFable: {
		Family:        FamilyFable,
		Short:         "fable",
		Label:         "fable-5",
		ContextWindow: 1_000_000,
		MaxOutput:     128_000,
		Pricing: Pricing{
			InputPerMTok:       10.00,
			OutputPerMTok:      50.00,
			CacheReadPerMTok:   1.00,
			CacheCreatePerMTok: 12.50,
		},
	},
}

// builtins is the built-in registry. Sourced from the published model
// overview and pricing pages on platform.claude.com/docs as of 2026-08-11.
// Cache pricing follows the documented multipliers: cache read = 0.1x input,
// 5m cache write = 1.25x input. Refresh this table when new models ship;
// `models.overrides` in config.json is the escape hatch for teaching
// agentctl about a new model between agentctl releases.
var builtins = map[string]Model{
	"claude-fable-5": {
		Key:           "claude-fable-5",
		Family:        FamilyFable,
		Short:         "fable",
		Label:         "fable-5",
		ContextWindow: 1_000_000,
		MaxOutput:     128_000,
		Pricing: Pricing{
			InputPerMTok:       10.00,
			OutputPerMTok:      50.00,
			CacheReadPerMTok:   1.00,
			CacheCreatePerMTok: 12.50,
		},
		Known: true,
	},
	"claude-opus-5": {
		Key:           "claude-opus-5",
		Family:        FamilyOpus,
		Short:         "opus",
		Label:         "opus-5",
		ContextWindow: 1_000_000,
		MaxOutput:     128_000,
		Pricing: Pricing{
			InputPerMTok:       5.00,
			OutputPerMTok:      25.00,
			CacheReadPerMTok:   0.50,
			CacheCreatePerMTok: 6.25,
		},
		Known: true,
	},
	"claude-opus-4-8": {
		Key:           "claude-opus-4-8",
		Family:        FamilyOpus,
		Short:         "opus",
		Label:         "opus-4.8",
		ContextWindow: 1_000_000,
		MaxOutput:     128_000,
		Pricing: Pricing{
			InputPerMTok:       5.00,
			OutputPerMTok:      25.00,
			CacheReadPerMTok:   0.50,
			CacheCreatePerMTok: 6.25,
		},
		Known: true,
	},
	"claude-opus-4-7": {
		Key:           "claude-opus-4-7",
		Family:        FamilyOpus,
		Short:         "opus",
		Label:         "opus-4.7",
		ContextWindow: 1_000_000,
		MaxOutput:     128_000,
		Pricing: Pricing{
			InputPerMTok:       5.00,
			OutputPerMTok:      25.00,
			CacheReadPerMTok:   0.50,
			CacheCreatePerMTok: 6.25,
		},
		Known: true,
	},
	"claude-opus-4-6": {
		Key:           "claude-opus-4-6",
		Family:        FamilyOpus,
		Short:         "opus",
		Label:         "opus-4.6",
		ContextWindow: 1_000_000,
		MaxOutput:     128_000,
		Pricing: Pricing{
			InputPerMTok:       5.00,
			OutputPerMTok:      25.00,
			CacheReadPerMTok:   0.50,
			CacheCreatePerMTok: 6.25,
		},
		Known: true,
	},
	"claude-opus-4-5": {
		Key:           "claude-opus-4-5",
		Family:        FamilyOpus,
		Short:         "opus",
		Label:         "opus-4.5",
		ContextWindow: 200_000,
		MaxOutput:     64_000,
		Pricing: Pricing{
			InputPerMTok:       5.00,
			OutputPerMTok:      25.00,
			CacheReadPerMTok:   0.50,
			CacheCreatePerMTok: 6.25,
		},
		Known: true,
	},
	"claude-opus-4-1": {
		Key:           "claude-opus-4-1",
		Family:        FamilyOpus,
		Short:         "opus",
		Label:         "opus-4.1",
		ContextWindow: 200_000,
		MaxOutput:     32_000,
		Pricing: Pricing{
			InputPerMTok:       15.00,
			OutputPerMTok:      75.00,
			CacheReadPerMTok:   1.50,
			CacheCreatePerMTok: 18.75,
		},
		Known: true,
	},
	"claude-opus-4": {
		Key:           "claude-opus-4",
		Family:        FamilyOpus,
		Short:         "opus",
		Label:         "opus-4",
		ContextWindow: 200_000,
		MaxOutput:     32_000,
		Pricing: Pricing{
			InputPerMTok:       15.00,
			OutputPerMTok:      75.00,
			CacheReadPerMTok:   1.50,
			CacheCreatePerMTok: 18.75,
		},
		Known: true,
	},
	"claude-sonnet-5": {
		Key:           "claude-sonnet-5",
		Family:        FamilySonnet,
		Short:         "sonnet",
		Label:         "sonnet-5",
		ContextWindow: 1_000_000,
		MaxOutput:     128_000,
		Pricing: Pricing{
			InputPerMTok:       2.00,
			OutputPerMTok:      10.00,
			CacheReadPerMTok:   0.20,
			CacheCreatePerMTok: 2.50,
		},
		Known: true,
	},
	"claude-sonnet-4-6": {
		Key:           "claude-sonnet-4-6",
		Family:        FamilySonnet,
		Short:         "sonnet",
		Label:         "sonnet-4.6",
		ContextWindow: 1_000_000,
		MaxOutput:     128_000,
		Pricing: Pricing{
			InputPerMTok:       3.00,
			OutputPerMTok:      15.00,
			CacheReadPerMTok:   0.30,
			CacheCreatePerMTok: 3.75,
		},
		Known: true,
	},
	"claude-sonnet-4-5": {
		Key:           "claude-sonnet-4-5",
		Family:        FamilySonnet,
		Short:         "sonnet",
		Label:         "sonnet-4.5",
		ContextWindow: 200_000,
		MaxOutput:     64_000,
		Pricing: Pricing{
			InputPerMTok:       3.00,
			OutputPerMTok:      15.00,
			CacheReadPerMTok:   0.30,
			CacheCreatePerMTok: 3.75,
		},
		Known: true,
	},
	"claude-sonnet-4": {
		Key:           "claude-sonnet-4",
		Family:        FamilySonnet,
		Short:         "sonnet",
		Label:         "sonnet-4",
		ContextWindow: 200_000,
		MaxOutput:     64_000,
		Pricing: Pricing{
			InputPerMTok:       3.00,
			OutputPerMTok:      15.00,
			CacheReadPerMTok:   0.30,
			CacheCreatePerMTok: 3.75,
		},
		Known: true,
	},
	"claude-haiku-4-5": {
		Key:           "claude-haiku-4-5",
		Family:        FamilyHaiku,
		Short:         "haiku",
		Label:         "haiku-4.5",
		ContextWindow: 200_000,
		MaxOutput:     64_000,
		Pricing: Pricing{
			InputPerMTok:       1.00,
			OutputPerMTok:      5.00,
			CacheReadPerMTok:   0.10,
			CacheCreatePerMTok: 1.25,
		},
		Known: true,
	},
	// Key uses a dash (not the historical dot) so the "-"-delimited prefix
	// walk in Lookup can reach it from dated model IDs like
	// "claude-haiku-3-5-20241022". MaxOutput is left at the pre-existing
	// (unverified) 32k value for this retired model rather than guessing.
	"claude-haiku-3-5": {
		Key:           "claude-haiku-3-5",
		Family:        FamilyHaiku,
		Short:         "haiku",
		Label:         "haiku-3.5",
		ContextWindow: 200_000,
		MaxOutput:     32_000,
		Pricing: Pricing{
			InputPerMTok:       0.80,
			OutputPerMTok:      4.0,
			CacheReadPerMTok:   0.08,
			CacheCreatePerMTok: 1.00,
		},
		Known: true,
	},
}

var (
	mu            sync.RWMutex
	overrides     = map[string]ModelOverride{}
	overrideWinow int    // config-provided default window (0 = use DefaultContextWindow)
	reserveOutput = true // whether UsableWindow subtracts the model's max-output reserve
)

// SetOverrides installs config-driven model overrides, replacing any
// previously installed set. defaultWindow, when non-zero, overrides
// DefaultContextWindow for models that resolve to nothing at all (the
// "total unknown" case in Lookup). Race-safe: callers may invoke this
// concurrently with Lookup.
func SetOverrides(o map[string]ModelOverride, defaultWindow int) {
	mu.Lock()
	defer mu.Unlock()
	if len(o) == 0 {
		overrides = map[string]ModelOverride{}
	} else {
		// Copy defensively so callers can't mutate the map after the fact.
		overrides = make(map[string]ModelOverride, len(o))
		for k, v := range o {
			overrides[strings.ToLower(strings.TrimSpace(k))] = v
		}
	}
	overrideWinow = defaultWindow
}

// SetReserveOutput controls whether UsableWindow subtracts the model's
// max-output reserve from its total context window. Defaults to true.
// Race-safe: may be called concurrently with Lookup/UsableWindow/CtxPct (the
// monitor calls config.Load(), which calls this, on every tick).
//
// This is a separate setter from SetOverrides (rather than an extra
// parameter on it) so existing SetOverrides call sites — including the
// phase 1 tests — don't need to change.
func SetReserveOutput(v bool) {
	mu.Lock()
	defer mu.Unlock()
	reserveOutput = v
}

// defaultWindowLocked returns the effective default context window, honoring
// a config override when set. Callers must hold mu (read or write).
func defaultWindowLocked() int {
	if overrideWinow > 0 {
		return overrideWinow
	}
	return DefaultContextWindow
}

// applyOverride merges a non-zero ModelOverride onto a base Model. Zero/empty
// fields in the override mean "don't override" — the base value wins.
func applyOverride(base Model, o ModelOverride) Model {
	m := base
	if o.ContextWindow != 0 {
		m.ContextWindow = o.ContextWindow
	}
	if o.MaxOutput != 0 {
		m.MaxOutput = o.MaxOutput
	}
	if o.Short != "" {
		m.Short = o.Short
	}
	if o.InputPerMTok != 0 {
		m.Pricing.InputPerMTok = o.InputPerMTok
	}
	if o.OutputPerMTok != 0 {
		m.Pricing.OutputPerMTok = o.OutputPerMTok
	}
	if o.CacheReadPerMTok != 0 {
		m.Pricing.CacheReadPerMTok = o.CacheReadPerMTok
	}
	if o.CacheCreatePerMTok != 0 {
		m.Pricing.CacheCreatePerMTok = o.CacheCreatePerMTok
	}
	return m
}

// familyFromName infers a Family by substring match on a normalized model
// name.
func familyFromName(normalized string) Family {
	switch {
	case strings.Contains(normalized, "opus"):
		return FamilyOpus
	case strings.Contains(normalized, "sonnet"):
		return FamilySonnet
	case strings.Contains(normalized, "haiku"):
		return FamilyHaiku
	case strings.Contains(normalized, "fable"):
		return FamilyFable
	default:
		return FamilyUnknown
	}
}

// has1MMarker reports whether the normalized model name contains a 1M
// context-window marker (covers "sonnet[1m]", "-1m", "_1m", or just "1m"
// appearing anywhere).
func has1MMarker(normalized string) bool {
	return strings.Contains(normalized, "1m")
}

// dateSuffixPattern matches a trailing "-YYYYMMDD" date segment, as found in
// dated model IDs like "claude-haiku-4-5-20251001".
var dateSuffixPattern = regexp.MustCompile(`-\d{8}$`)

// isNumeric reports whether s consists entirely of ASCII digits (and is
// non-empty).
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// deriveLabel produces a reasonable display label for a model string that
// isn't an exact builtin/config match: strip the leading "claude-" prefix,
// strip a trailing "-YYYYMMDD" date segment, and — when every remaining
// version segment after the family name is purely numeric — convert the
// last "-" into a "." (major-minor version notation), e.g.
// "claude-opus-4-9" -> "opus-4.9", "claude-haiku-4-5-20251001" -> "haiku-4.5".
// A single trailing version segment (e.g. "claude-opus-5") is left as
// "opus-5" — no dot needed. Strings that don't fit the family-major[-minor]
// pattern (e.g. legacy "claude-3-5-sonnet-...' ids) are returned as-is
// (prefix/date stripped only) rather than guessed at.
func deriveLabel(normalized string) string {
	s := strings.TrimPrefix(normalized, "claude-")
	s = dateSuffixPattern.ReplaceAllString(s, "")
	parts := strings.Split(s, "-")
	if len(parts) <= 2 {
		return s
	}
	for _, p := range parts[1:] {
		if !isNumeric(p) {
			return s
		}
	}
	return strings.Join(parts[:len(parts)-1], "-") + "." + parts[len(parts)-1]
}

// baseForKey returns the built-in/family base Model that a config override
// for the given key should be merged onto, so a partial override (e.g. just
// context_window) doesn't zero out pricing or other fields. It does NOT
// consult overrides itself.
func baseForKey(key string) Model {
	if b, ok := builtins[key]; ok {
		b.Known = true
		return b
	}
	if fam := familyFromName(key); fam != FamilyUnknown {
		fd := familyDefaults[fam]
		fd.Key = "claude-" + string(fam)
		fd.Label = deriveLabel(key)
		return fd
	}
	return Model{Key: key, Family: FamilyUnknown, Label: deriveLabel(key), ContextWindow: defaultWindowLocked()}
}

// Lookup resolves a model name to a fully-populated Model description.
// Resolution order:
//  1. Normalize (lowercase, trim).
//  2. Sentinel guard: "" or "<synthetic>" -> unknown, never logged.
//  3. Walk from the full normalized name down through progressively shorter
//     "-"-delimited prefixes (e.g. "claude-opus-4-8" -> "claude-opus-4" ->
//     "claude-opus" -> "claude"). At each prefix length, check config
//     overrides first, then the built-in table — so an exact match (config
//     or built-in) at a given specificity always wins over a match at a
//     coarser (shorter-prefix) specificity, and config wins over built-in
//     only when they match at the *same* specificity.
//  4. Family fallback (substring match on opus/sonnet/haiku/fable), logged
//     once. Uses the newest known generation's figures for that family (see
//     familyDefaults) and derives a Label from the actual model string.
//  5. 1M marker override, applied on top of whatever was resolved above.
//  6. Total unknown: default window, zero pricing, Short = raw model string,
//     Label derived from the raw model string.
func Lookup(model string) Model {
	normalized := strings.ToLower(strings.TrimSpace(model))

	mu.RLock()
	defer mu.RUnlock()

	// Step 2: sentinel guard.
	if normalized == "" || normalized == "<synthetic>" {
		return Model{
			Family:        FamilyUnknown,
			Short:         "",
			ContextWindow: defaultWindowLocked(),
			MaxOutput:     0,
			Known:         false,
		}
	}

	var resolved Model
	matched := false

	// Step 3: interleaved prefix walk — config then built-in at each level.
	for candidate := normalized; ; {
		if o, ok := overrides[candidate]; ok {
			m := applyOverride(baseForKey(candidate), o)
			m.Key = candidate
			m.Source = "config"
			resolved, matched = m, true
			break
		}
		if b, ok := builtins[candidate]; ok {
			b.Known = true
			b.Source = "built-in"
			resolved, matched = b, true
			break
		}
		idx := strings.LastIndex(candidate, "-")
		if idx < 0 {
			break
		}
		candidate = candidate[:idx]
	}

	// Step 4: family fallback.
	if !matched {
		if fam := familyFromName(normalized); fam != FamilyUnknown {
			fd := familyDefaults[fam]
			fd.Key = "claude-" + string(fam)
			fd.Label = deriveLabel(normalized)
			fd.Known = false
			fd.Source = "family"
			resolved, matched = fd, true
		}
	}

	// Step 6: total unknown.
	if !matched {
		resolved = Model{
			Key:           normalized,
			Family:        FamilyUnknown,
			Short:         model,
			Label:         deriveLabel(normalized),
			ContextWindow: defaultWindowLocked(),
			MaxOutput:     0,
			Known:         false,
		}
	}

	// Step 5: 1M marker override, applied on top of whatever was resolved.
	if has1MMarker(normalized) {
		resolved.ContextWindow = 1_000_000
	}

	return resolved
}

// Abbreviate returns a short human-readable display name for a model name:
// the resolved Label (e.g. "opus-4.8", "sonnet-5", "haiku-4.5") when known,
// falling back to Short (the bare family name) when Label is empty, and
// finally to the raw model string when both are empty. The empty/synthetic
// sentinel and empty string always return "".
func Abbreviate(model string) string {
	m := Lookup(model)
	if m.Label != "" {
		return m.Label
	}
	if m.Short != "" {
		return m.Short
	}
	return m.Key
}

// UsableWindow returns the context budget actually available for input,
// i.e. ContextWindow minus the model's max-output reserve. Floored at 50%
// of the total window so a bogus MaxOutput override (config or otherwise)
// can never collapse the usable budget to near zero.
//
// Honors SetReserveOutput: when reserve accounting is disabled, the full
// ContextWindow is returned.
func UsableWindow(model string) int {
	m := Lookup(model)

	mu.RLock()
	reserve := reserveOutput
	mu.RUnlock()

	if !reserve {
		return m.ContextWindow
	}

	usable := m.ContextWindow - m.MaxOutput
	if floor := m.ContextWindow / 2; usable < floor {
		usable = floor
	}
	return usable
}

// CtxPct returns used/usable as an integer percentage, clamped to [0, 100].
// Returns 0 when used <= 0 or the resolved usable window is <= 0, so callers
// never need their own divide-by-zero or negative-input guards.
func CtxPct(model string, used int) int {
	if used <= 0 {
		return 0
	}
	usable := UsableWindow(model)
	if usable <= 0 {
		return 0
	}
	pct := used * 100 / usable
	if pct > 100 {
		pct = 100
	}
	if pct < 0 {
		pct = 0
	}
	return pct
}

// Registry returns a sorted snapshot of the built-in registry plus any
// config overrides, for inspection (e.g. the `agentctl models` command).
// Sorted by family, then by Label descending (newest generation first) so
// e.g. "opus-5" sorts above "opus-4.8" above "opus-4". Family-fallback-only
// defaults are not included since they aren't "in" the registry — they're
// what Lookup falls back to when nothing matches.
func Registry() []Model {
	mu.RLock()
	defer mu.RUnlock()

	seen := make(map[string]Model, len(builtins)+len(overrides))
	for k, m := range builtins {
		m.Key = k
		m.Known = true
		m.Source = "built-in"
		seen[k] = m
	}
	for k, o := range overrides {
		m := applyOverride(baseForKey(k), o)
		m.Key = k
		m.Source = "config"
		seen[k] = m
	}

	out := make([]Model, 0, len(seen))
	for _, m := range seen {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].Label > out[j].Label
	})
	return out
}
