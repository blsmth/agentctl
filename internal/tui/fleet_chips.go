package tui

import (
	"fmt"
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

// fleetCtxSlotW is the widest the row's context exception column gets
// ("100%" is the widest common value).
const fleetCtxSlotW = 4

// ctxLabel is "42%", or "" when the context use is unknown (0).
func ctxLabel(pct int) string {
	if pct <= 0 {
		return ""
	}
	return fmt.Sprintf("%d%%", min(pct, 100))
}

// fleetExceptions is the list-wide state of a row's exception column: the
// context use, shown only from the warn threshold. Computed once per
// rebuildLines (see computeExceptions). The zero value reserves no context
// column; a row's own context warning still shows. The model is not on the
// row at all: it lives on the hover card and in hub search.
type fleetExceptions struct {
	// ctx reports whether any row shows a context warning.
	ctx bool
}

// computeExceptions builds the exception state under display d from the
// shown rows only.
func computeExceptions(shown []fleet.Group, d fleetDisplay) fleetExceptions {
	var ex fleetExceptions
	for _, g := range shown {
		for _, r := range g.Rows {
			if rowCtxWarn(r, d) {
				ex.ctx = true
			}
		}
	}
	return ex
}

// rowCtxWarn reports whether r's context use is at or above the warn
// threshold, the only time the row shows it.
func rowCtxWarn(r fleet.Row, d fleetDisplay) bool {
	if r.Shell || r.ContextPct <= 0 {
		return false
	}
	warn, _ := d.thresholds()
	return r.ContextPct >= warn
}

// defaultBackends are backend names that mean plain Claude; no chip is shown
// for them (the phone's DEFAULT_BACKENDS).
var defaultBackends = map[string]bool{"": true, "anthropic": true, "claude": true, "default": true}

// rowChips are the identifiers a selected row's chip line shows that the row
// itself does not: #issue, the agent name (rows show their title, not the name),
// a non-default backend and a non-default account. The context, PR and
// cron job are already on the row (or its hover card) and get no chip, and
// the model is on the hover card.
// Shell rows have none; every other row has at least the agent name.
func rowChips(r fleet.Row, d fleetDisplay) []string {
	if r.Shell {
		return nil
	}
	var out []string
	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}
	if r.Ticket > 0 {
		add(fmt.Sprintf("#%d", r.Ticket))
	}
	add(fleet.BaseName(r))
	if b := strings.TrimSpace(r.Backend); !defaultBackends[strings.ToLower(b)] {
		add(b)
	}
	if a := strings.TrimSpace(r.Account); a != "" && a != d.defaultAccount {
		add("acct " + a)
	}
	return out
}

// renderChips renders chips as dim "[chip]" pills one space apart,
// truncated to w with "…".
func renderChips(chips []string, w int) string {
	parts := make([]string, len(chips))
	for i, c := range chips {
		parts[i] = fleetDimStyle.Render("[" + c + "]")
	}
	return truncateToWidth(strings.Join(parts, " "), w)
}
