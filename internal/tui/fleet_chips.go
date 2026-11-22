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

// fleetModelSlotW and fleetCtxSlotW are the widest the row's model and
// context exception columns get ("sonnet" and "100%" are the widest common
// values).
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

// fleetExceptions is the list-wide state of a row's exception columns: the
// model name, shown only when it differs from the fleet's usual model, and
// the context use, shown only from the warn threshold. Computed once per
// rebuildLines (see computeExceptions). The zero value shows no model and
// reserves no context column; a row's own context warning still shows.
type fleetExceptions struct {
	// common is the usual model (slotModel), "" when there is none or the
	// most common models tie, in which case every row shows its model.
	common string
	// modelW is the widest model any row shows; 0 = no row shows one, and
	// the model column takes no width at all.
	modelW int
	// ctx reports whether any row shows a context warning.
	ctx bool
}

// mostCommonModel is the slotModel most agent rows across groups run, or ""
// when no row has a model or the top count is a tie (show both). Shell rows
// do not count.
func mostCommonModel(groups []fleet.Group) string {
	counts := map[string]int{}
	for _, g := range groups {
		for _, r := range g.Rows {
			if r.Shell {
				continue
			}
			if m := slotModel(r.ModelName); m != "" {
				counts[m]++
			}
		}
	}
	best, top, tie := "", 0, false
	for m, n := range counts {
		switch {
		case n > top:
			best, top, tie = m, n, false
		case n == top:
			tie = true
		}
	}
	if tie {
		return ""
	}
	return best
}

// computeExceptions builds the exception state under display d: the usual
// model across all groups, the column widths from the shown rows only.
func computeExceptions(groups, shown []fleet.Group, d fleetDisplay) fleetExceptions {
	ex := fleetExceptions{common: mostCommonModel(groups)}
	for _, g := range shown {
		for _, r := range g.Rows {
			if m := ex.rowModel(r); m != "" {
				ex.modelW = max(ex.modelW, lipgloss.Width(m))
			}
			if rowCtxWarn(r, d) {
				ex.ctx = true
			}
		}
	}
	return ex
}

// rowModel is the model name r shows in its exception column, or "": shells,
// rows still waiting for their first prompt and rows on the usual model show
// none.
func (ex fleetExceptions) rowModel(r fleet.Row) string {
	if r.Shell || rowNoPrompt(r) {
		return ""
	}
	m := slotModel(r.ModelName)
	if m == "" || m == ex.common {
		return ""
	}
	return m
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
// a non-default backend and a non-default account. The model, context, PR
// and cron job are already on the row (or its hover card) and get no chip.
// Shell rows have none; most rows have none, and then there is no chip line.
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
