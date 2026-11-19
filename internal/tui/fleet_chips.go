package tui

import (
	"fmt"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
)

// fleetDisplay is the config the hub's row chips depend on, read with each
// fleet load so a config edit shows up on the next poll. The zero value means
// the built-in context thresholds and no default account.
type fleetDisplay struct {
	ctxWarn, ctxCrit int
	defaultAccount   string
}

// fleetDisplayLoad reads the display config; tests replace it.
var fleetDisplayLoad = func() fleetDisplay {
	cfg := config.Load()
	return fleetDisplay{
		ctxWarn:        cfg.Models.EffectiveContextWarnPct(),
		ctxCrit:        cfg.Models.EffectiveContextCriticalPct(),
		defaultAccount: cfg.DefaultAccount,
	}
}

// thresholds are the warn/critical context percentages, falling back to the
// config defaults when unset.
func (d fleetDisplay) thresholds() (warn, crit int) {
	if d.ctxWarn > 0 && d.ctxCrit > d.ctxWarn {
		return d.ctxWarn, d.ctxCrit
	}
	var m config.ModelsConfig
	return m.EffectiveContextWarnPct(), m.EffectiveContextCriticalPct()
}

// ctxStyle is the style for a context percentage: dim below the warn
// threshold, the blocked (warning) colour from it, the error colour from the
// critical threshold.
func (d fleetDisplay) ctxStyle(pct int) lipgloss.Style {
	warn, crit := d.thresholds()
	switch {
	case pct >= crit:
		return fleetErrStyle
	case pct >= warn:
		return fleetBlockedStyle
	}
	return fleetDimStyle
}

var (
	modelDateSuffix    = regexp.MustCompile(`-\d{8}$`)
	modelBracketSuffix = regexp.MustCompile(`\[.*\]$`)
	modelVersionPart   = regexp.MustCompile(`^[\d.]+$`)
)

// shortModel is the phone's shortModel (internal/serve/web/logic.js): the
// model id without its "claude-" prefix, "-YYYYMMDD" date or "[1m]"-style
// qualifier, e.g. "claude-opus-4-7[1m]" → "opus-4-7". "" stays "".
func shortModel(model string) string {
	m := strings.TrimPrefix(strings.TrimSpace(model), "claude-")
	m = modelBracketSuffix.ReplaceAllString(m, "")
	return modelDateSuffix.ReplaceAllString(m, "")
}

// fleetModelSlotW and fleetCtxSlotW are the widths of the row's model and
// context sub-columns ("sonnet" and "100%" are the widest common values).
const (
	fleetModelSlotW = 6
	fleetCtxSlotW   = 4
)

// slotModel is the model as the row's right-hand column shows it: shortModel
// without its version parts ("opus-4-7" → "opus", "3-5-sonnet" → "sonnet"),
// truncated to fleetModelSlotW. A name that is nothing but version parts
// keeps them.
func slotModel(model string) string {
	m := shortModel(model)
	if m == "" {
		return ""
	}
	var kept []string
	for _, p := range strings.Split(m, "-") {
		if !modelVersionPart.MatchString(p) {
			kept = append(kept, p)
		}
	}
	if len(kept) > 0 {
		m = strings.Join(kept, "-")
	}
	return truncateToWidth(m, fleetModelSlotW)
}

// ctxLabel is "42%", or "" when the context use is unknown (0).
func ctxLabel(pct int) string {
	if pct <= 0 {
		return ""
	}
	return fmt.Sprintf("%d%%", min(pct, 100))
}

// rowModelSlot renders the model and context sub-columns, each right-aligned
// and blank when unknown, so they line up down the sidebar.
func rowModelSlot(r fleet.Row, d fleetDisplay) string {
	var model, ctx string
	if !r.Shell {
		if m := slotModel(r.ModelName); m != "" {
			model = fleetDimStyle.Render(m)
		}
		if c := ctxLabel(r.ContextPct); c != "" {
			ctx = d.ctxStyle(r.ContextPct).Render(c)
		}
	}
	return padLeft(model, fleetModelSlotW) + " " + padLeft(ctx, fleetCtxSlotW)
}

// defaultBackends are backend names that mean plain Claude; no chip is shown
// for them (the phone's DEFAULT_BACKENDS).
var defaultBackends = map[string]bool{"": true, "anthropic": true, "claude": true, "default": true}

// fleetChip is one identifier on a selected row's chip line. ctx marks the
// context chip, which keeps its threshold colour.
type fleetChip struct {
	text string
	ctx  bool
}

// rowChips are a row's chips in the phone's order: PR, #issue, agent, model,
// context, backend, account, scheduled job. The agent chip appears only when
// the name column truncated the name (the phone likewise skips it when the
// headline already shows it); the account only when it is not the default.
// Shell rows have none.
func rowChips(r fleet.Row, nameFits bool, d fleetDisplay) []fleetChip {
	if r.Shell {
		return nil
	}
	var out []fleetChip
	add := func(s string) {
		if s != "" {
			out = append(out, fleetChip{text: s})
		}
	}
	switch {
	case r.PRNumber > 0:
		add(fmt.Sprintf("PR #%d", r.PRNumber))
	case r.PRURL != "":
		add("PR")
	}
	if r.Ticket > 0 {
		add(fmt.Sprintf("#%d", r.Ticket))
	}
	if !nameFits {
		add(fleet.BaseName(r))
	}
	add(shortModel(r.ModelName))
	if c := ctxLabel(r.ContextPct); c != "" {
		out = append(out, fleetChip{text: "ctx " + c, ctx: true})
	}
	if b := strings.TrimSpace(r.Backend); !defaultBackends[strings.ToLower(b)] {
		add(b)
	}
	if a := strings.TrimSpace(r.Account); a != "" && a != d.defaultAccount {
		add("acct " + a)
	}
	add(scheduledTag(r))
	return out
}

// renderChips renders chips as dim "[chip]" pills one space apart, the
// context chip in its threshold colour, truncated to w with "…".
func renderChips(r fleet.Row, chips []fleetChip, d fleetDisplay, w int) string {
	parts := make([]string, len(chips))
	for i, c := range chips {
		st := fleetDimStyle
		if c.ctx {
			st = d.ctxStyle(r.ContextPct)
		}
		parts[i] = fleetDimStyle.Render("[") + st.Render(c.text) + fleetDimStyle.Render("]")
	}
	return truncateToWidth(strings.Join(parts, " "), w)
}
