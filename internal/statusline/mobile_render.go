package statusline

import (
	"fmt"
	"math"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/render"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/theme"
)

// mobileSegment is one rendered piece of a mobile line with its cell width.
//
// drop is the segment's keep priority: when a line is too wide the segment
// with the highest drop value goes first (display order breaks ties).
type mobileSegment struct {
	text string
	w    int
	drop int
}

func newMobileSegment(s string, drop int) mobileSegment {
	return mobileSegment{text: s, w: lipgloss.Width(s), drop: drop}
}

// renderMobileLine renders the "mobile" format for the current config's
// statusline_align, joined with "\n".
func renderMobileLine(p *Payload, windowID string) string {
	align := config.Load().EffectiveStatuslineAlign()
	as := consistentAgentState(lookupAgentState(windowID), p)
	return strings.Join(renderMobileAligned(p, statuslineColumns(), as, align), "\n")
}

// renderMobileAligned applies statusline_align the way renderRichAligned does:
// right alignment shrinks the budget by rightAlignReserve and pads each line
// with padRune to it.
func renderMobileAligned(p *Payload, cols int, snap *state.AgentState, align string) []string {
	budget := cols
	if align == "right" {
		budget = cols - rightAlignReserve
	}
	lines := renderMobile(p, budget, snap)
	if align != "right" {
		return lines
	}
	for i, l := range lines {
		lines[i] = padLeftTo(l, budget)
	}
	return lines
}

// renderMobile builds the (up to) two-line "mobile" statusline:
//
//	model · <battery> N% ctx · session length
//	5h N% · 7d N% · $cost · account
//
// Values, thresholds and trust rules match the rich format (same
// coercePercent/coerceNonNegative, pushedCostTrusted, minDisplayCostUSD).
// Absent values are omitted; when a line exceeds cols, segments are dropped
// by priority (never truncated): line 1 loses session length first; line 2
// loses account, then 7d, then cost, keeping 5h last. If nothing is
// present, noDataLine is returned.
func renderMobile(p *Payload, cols int, snap *state.AgentState) []string {
	sepStr := render.SepStyle().Render(" · ")
	var lines []string
	if l := joinFitted(mobileLine1(p, snap), sepStr, cols); l != "" {
		lines = append(lines, l)
	}
	if l := joinFitted(mobileLine2(p), sepStr, cols); l != "" {
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		return []string{noDataLine}
	}
	return lines
}

// joinFitted drops the lowest-priority segment (highest drop value, later
// position on ties) until the joined line fits cols, keeping at least one, and
// joins the rest in display order. Empty input yields "".
func joinFitted(segs []mobileSegment, sep string, cols int) string {
	sepW := lipgloss.Width(sep)
	for len(segs) > 1 {
		total := 0
		worst := 0
		for i, s := range segs {
			total += s.w
			if i > 0 {
				total += sepW
			}
			if s.drop >= segs[worst].drop {
				worst = i
			}
		}
		if total <= cols {
			break
		}
		segs = append(append([]mobileSegment(nil), segs[:worst]...), segs[worst+1:]...)
	}
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.text
	}
	return strings.Join(parts, sep)
}

// mobileLine1 is model, context and session length.
func mobileLine1(p *Payload, snap *state.AgentState) []mobileSegment {
	th := theme.Current()
	var segs []mobileSegment

	if p.Model != nil {
		name := ""
		if p.Model.DisplayName != nil && *p.Model.DisplayName != "" {
			name = *p.Model.DisplayName
		} else if p.Model.ID != nil && *p.Model.ID != "" {
			name = *p.Model.ID
		}
		if name != "" {
			segs = append(segs, newMobileSegment(render.LabelStyle().Render(render.AbbreviateModel(name)), 1))
		}
	}

	if pct, ok := p.ContextWindow.UsedPercentage(); ok {
		models := config.Load().Models
		segs = append(segs, newMobileSegment(contextSegment(pct, th, models.EffectiveContextWarnPct(), models.EffectiveContextCriticalPct()), 2))
	}

	if d, ok := sessionDuration(p, snap); ok {
		segs = append(segs, newMobileSegment(render.ValueStyle().Render(d), 3))
	}
	return segs
}

// mobileLine2 is 5h, 7d, trusted session cost and account.
func mobileLine2(p *Payload) []mobileSegment {
	th := theme.Current()
	labelStyle := render.LabelStyle()
	thresholds := []render.Threshold{
		{Cutoff: 75, Color: th.Green},
		{Cutoff: 90, Color: th.Yellow},
		{Cutoff: 100, Color: th.Red},
	}
	var segs []mobileSegment

	if rl := p.RateLimits; rl != nil {
		for _, w := range []struct {
			label string
			win   *RateLimitWindow
			rank  int
		}{{"5h ", rl.FiveHour, 1}, {"7d ", rl.SevenDay, 3}} {
			if w.win == nil {
				continue
			}
			if pct, ok := coercePercent(w.win.UsedPercentage); ok {
				pctInt := int(math.Round(pct))
				pctStr := render.ThresholdStyle(pctInt, thresholds).Render(fmt.Sprintf("%d%%", pctInt))
				segs = append(segs, newMobileSegment(labelStyle.Render(w.label)+pctStr, w.rank))
			}
		}
	}

	if p.Cost != nil && pushedCostTrusted(p) {
		if cost, ok := coerceNonNegative(p.Cost.TotalCostUSD); ok && cost >= minDisplayCostUSD {
			segs = append(segs, newMobileSegment(render.ValueStyle().Render(fmt.Sprintf("$%.2f", cost)), 2))
		}
	}

	if p.account != "" {
		segs = append(segs, newMobileSegment(render.ValueStyle().Render(p.account), 4))
	}
	return segs
}
