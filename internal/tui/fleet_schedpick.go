package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/claudehome"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/launchopts"
)

// The Schedules form's inline cycler: `‹ value ›`, the same control as the
// Project row. ←/→ (or space) steps through the options, typing narrows them
// by substring, backspace widens again, and a dim `3/12` counts the options
// when there are more than schedCountMin. A stored value that is not offered
// stays selectable as an extra option marked `(unknown)` in red, so an edit
// never silently drops it.

const schedCountMin = 6

// schedOpt is one option of a cycler.
type schedOpt struct {
	value   string // the job value ("" = the default)
	label   string
	unknown bool // stored value that is not offered
	custom  bool // typed by the user (cyclers that take free text)
}

type schedCycler struct {
	opts   []schedOpt
	idx    int
	filter string
	// free lets text that matches no option become the value.
	free bool
	// customIdx is the index of the typed option, -1 for none.
	customIdx int
}

// newCycler builds a cycler on cur. A cur missing from opts is appended as an
// unknown option, unless plain says it is fine (a valid value the list does
// not offer, such as a plugin agent).
func newCycler(opts []schedOpt, cur string, free bool, plain func(string) bool) schedCycler {
	c := schedCycler{free: free, customIdx: -1}
	c.setOpts(opts, cur, plain)
	return c
}

// setOpts replaces the options and keeps cur selected. The current typed or
// unknown option is carried over when cur is not in the new list.
func (c *schedCycler) setOpts(opts []schedOpt, cur string, plain func(string) bool) {
	var carried *schedOpt
	if c.idx >= 0 && c.idx < len(c.opts) && c.opts[c.idx].value == cur {
		o := c.opts[c.idx]
		carried = &o
	}
	c.opts = slices.Clone(opts)
	c.customIdx = -1
	i := slices.IndexFunc(c.opts, func(o schedOpt) bool { return o.value == cur })
	switch {
	case i >= 0:
	case cur == "":
		i = 0
	default:
		o := schedOpt{value: cur, label: cur, unknown: plain == nil || !plain(cur)}
		if carried != nil {
			o = *carried
		}
		c.opts = append(c.opts, o)
		i = len(c.opts) - 1
	}
	c.idx = max(i, 0)
}

// value is the selected option's job value ("" for no options).
func (c *schedCycler) value() string {
	if c.idx < 0 || c.idx >= len(c.opts) {
		return ""
	}
	return c.opts[c.idx].value
}

// current is the selected option.
func (c *schedCycler) current() schedOpt {
	if c.idx < 0 || c.idx >= len(c.opts) {
		return schedOpt{}
	}
	return c.opts[c.idx]
}

func (c *schedCycler) matches(o schedOpt) bool {
	if c.filter == "" {
		return true
	}
	f := strings.ToLower(c.filter)
	return strings.Contains(strings.ToLower(o.label), f) || strings.Contains(strings.ToLower(o.value), f)
}

// visible lists the option indexes the filter leaves.
func (c *schedCycler) visible() []int {
	var out []int
	for i, o := range c.opts {
		if i != c.customIdx && c.matches(o) {
			out = append(out, i)
		}
	}
	return out
}

// step moves delta options through the visible ones, wrapping.
func (c *schedCycler) step(delta int) {
	vis := c.visible()
	if c.customIdx >= 0 && c.idx == c.customIdx {
		c.dropCustom()
		vis = c.visible()
	}
	c.filter = ""
	if len(vis) == 0 {
		vis = c.visible()
	}
	if len(vis) == 0 {
		return
	}
	pos := slices.Index(vis, c.idx)
	if pos < 0 {
		// Not among the matches: start from the first (or last) of them.
		if delta > 0 {
			c.idx = vis[0]
		} else {
			c.idx = vis[len(vis)-1]
		}
		return
	}
	c.idx = vis[(pos+delta+len(vis))%len(vis)]
}

func (c *schedCycler) dropCustom() {
	if c.customIdx < 0 {
		return
	}
	i := c.customIdx
	c.opts = slices.Delete(c.opts, i, i+1)
	c.customIdx = -1
	if c.idx >= i {
		c.idx = max(c.idx-1, 0)
	}
}

// typeText appends typed text to the filter.
func (c *schedCycler) typeText(s string) {
	c.filter += s
	c.sync()
}

// backspace removes the last typed rune.
func (c *schedCycler) backspace() {
	if c.filter == "" {
		return
	}
	_, n := utf8.DecodeLastRuneInString(c.filter)
	c.filter = c.filter[:len(c.filter)-n]
	c.sync()
}

// clearFilter drops the filter, reporting whether there was one.
func (c *schedCycler) clearFilter() bool {
	if c.filter == "" {
		return false
	}
	c.filter = ""
	c.sync()
	return true
}

// sync selects the first match of the filter, or the typed option when a
// free cycler has no match.
func (c *schedCycler) sync() {
	wasCustom := c.customIdx >= 0 && c.idx == c.customIdx
	c.dropCustom()
	vis := c.visible()
	switch {
	case c.filter == "":
		if wasCustom {
			c.idx = 0
		}
	case len(vis) > 0:
		if !slices.Contains(vis, c.idx) {
			c.idx = vis[0]
		}
	case c.free:
		c.opts = append(c.opts, schedOpt{value: strings.TrimSpace(c.filter), label: strings.TrimSpace(c.filter), custom: true})
		c.customIdx = len(c.opts) - 1
		c.idx = c.customIdx
	}
}

// view renders the cycler: `‹ value › 3/12 /filter`.
func (c *schedCycler) view(focused bool) string {
	if len(c.opts) == 0 {
		return fleetDimStyle.Render("‹ none to choose ›")
	}
	arrow := fleetDimStyle
	label := fleetGroupNameStyle
	if !focused {
		label = lipgloss.NewStyle()
	}
	if focused {
		arrow = cursorActiveStyle
	}
	o := c.current()
	text := label.Render(o.label)
	switch {
	case c.filter != "" && len(c.visible()) == 0 && c.customIdx < 0:
		text = fleetDimStyle.Render("no match")
	case o.unknown:
		text = fleetErrStyle.Render(o.label + " (unknown)")
	case o.custom:
		text = label.Render(o.label) + fleetDimStyle.Render(" (custom)")
	}
	s := arrow.Render("‹") + " " + text + " " + arrow.Render("›")
	if len(c.opts) > schedCountMin {
		s += " " + fleetDimStyle.Render(fmt.Sprintf("%d/%d", c.idx+1, len(c.opts)))
	}
	if c.filter != "" {
		s += " " + fleetDimStyle.Render("/"+c.filter)
	}
	return s
}

// labelOf is the label of the option with value v (v itself when absent).
func (c *schedCycler) labelOf(v string) string {
	for _, o := range c.opts {
		if o.value == v {
			return o.label
		}
	}
	return v
}

// ── Option data ──────────────────────────────────────────────────────────────

// schedEnv is what the form offers, read once when it opens. Everything is
// in-process (config and cached catalogs): nothing here spawns or waits.
type schedEnv struct {
	opts         cronedit.Options
	defaultAgent string
	usage        config.CronUsageThresholds
	efforts      []string
	agentCfg     func(agent string) config.AgentConfig
	backendModel func(backend string) string
	// account is the account a job in dir running agent uses by default
	// (account_rules, agents.<name>.account, default_account); "" when none.
	account func(agent, dir string) string
}

// fleetSchedEnvLoad reads the real environment; tests replace it.
var fleetSchedEnvLoad = func() schedEnv {
	cfg := config.Load()
	return schedEnv{
		opts:         cronedit.DefaultOptions(),
		defaultAgent: cfg.DefaultAgent,
		usage:        cfg.Cron.EffectiveUsageThresholds(),
		efforts:      launchopts.Efforts,
		agentCfg:     cfg.ForAgent,
		backendModel: func(b string) string { return cfg.Backends[b].Model },
		account: func(agent, dir string) string {
			r, err := claudehome.Resolve(cfg, claudehome.Request{Agent: cfg.ForAgent(agent).Account, RepoRoot: dir})
			if err != nil {
				return ""
			}
			return r.Name
		},
	}
}

func (e schedEnv) agent(name string) config.AgentConfig {
	if e.agentCfg == nil {
		return config.AgentConfig{}
	}
	return e.agentCfg(name)
}

// effectiveBackend is the backend a job on agent runs on given its pinned
// backend value.
func (e schedEnv) effectiveBackend(agent, pinned string) string {
	o := e.opts
	return o.EffectiveBackend(cron.Job{Agent: agent, Backend: pinned})
}

// defaultModel is the model the job would run without a pin ("" = unknown).
func (e schedEnv) defaultModel(agent, backend string) string {
	if backend == launchopts.BackendAnthropic {
		if ac := e.agent(agent); ac.Backend == "" {
			return ac.Model
		}
		return ""
	}
	if e.backendModel == nil {
		return ""
	}
	return e.backendModel(backend)
}

func withDefault(base, resolved string) string {
	if resolved == "" || resolved == launchopts.Inherit {
		return base
	}
	return base + " (" + resolved + ")"
}

func (e schedEnv) modelOpts(agent, pinnedBackend string) []schedOpt {
	backend := e.effectiveBackend(agent, pinnedBackend)
	out := []schedOpt{{value: "", label: withDefault("agent default", e.defaultModel(agent, backend))}}
	if backend == launchopts.BackendAnthropic {
		out = append(out, schedOpt{value: launchopts.Inherit, label: launchopts.Inherit})
	}
	for _, m := range e.opts.Models[backend] {
		out = append(out, schedOpt{value: m, label: m})
	}
	return out
}

func (e schedEnv) effortOpts(agent string) []schedOpt {
	out := []schedOpt{{value: "", label: withDefault("default", e.agent(agent).EffortLevel)}}
	for _, v := range e.efforts {
		out = append(out, schedOpt{value: v, label: v})
	}
	return append(out, schedOpt{value: launchopts.Inherit, label: launchopts.Inherit})
}

func (e schedEnv) backendOpts(agent string) []schedOpt {
	def := e.agent(agent).Backend
	if def == "" {
		def = launchopts.BackendAnthropic
	}
	out := []schedOpt{{value: "", label: "default (" + def + ")"}, {value: launchopts.BackendNone, label: "anthropic"}}
	for _, b := range e.opts.Backends {
		out = append(out, schedOpt{value: b, label: b})
	}
	return out
}

func (e schedEnv) accountOpts(agent, dir string) []schedOpt {
	def := "default"
	if e.account != nil {
		def = withDefault("default", e.account(agent, dir))
	}
	out := []schedOpt{{value: "", label: def}}
	for _, a := range e.opts.Accounts {
		out = append(out, schedOpt{value: a, label: a})
	}
	return out
}

// schedTimeoutCustom is the value of the timeout cycler's "custom…" option.
const schedTimeoutCustom = "\x00custom"

var schedTimeoutPresets = []string{"15m", "30m", "45m", "1h", "2h"}

func timeoutOpts() []schedOpt {
	out := []schedOpt{{value: "", label: "none"}}
	for _, p := range schedTimeoutPresets {
		out = append(out, schedOpt{value: p, label: p})
	}
	return append(out, schedOpt{value: schedTimeoutCustom, label: "custom…"})
}

// schedUsageOff is the stored value of the usage gate's "off": 100%, which
// only trips at a full window.
const schedUsageOff = "100"

func usageOpts(global float64, cur string) []schedOpt {
	out := []schedOpt{{value: "", label: fmt.Sprintf("global (%g%%)", global)}}
	presets := []string{"50", "60", "70", "80", "90"}
	if cur != "" && cur != schedUsageOff && !slices.Contains(presets, cur) {
		presets = append(presets, cur)
		slices.SortFunc(presets, func(a, b string) int {
			x, _ := strconv.ParseFloat(a, 64)
			y, _ := strconv.ParseFloat(b, 64)
			return cmp.Compare(x, y)
		})
	}
	for _, v := range presets {
		out = append(out, schedOpt{value: v, label: v + "%"})
	}
	return append(out, schedOpt{value: schedUsageOff, label: "off"})
}

// usageValue is a stored threshold as a cycler value ("" = global).
func usageValue(v float64) string { return fmtPct(v) }

// plainAgent: a plugin agent (plugin:name) is valid though never listed.
func plainAgent(v string) bool { return strings.Contains(v, ":") }
