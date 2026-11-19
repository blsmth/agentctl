package statusline

import (
	"fmt"
	"math"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/render"
	"github.com/blsmth/agentctl/internal/theme"
)

// mobileSegment is one rendered piece of a mobile line with its cell width.
type mobileSegment struct {
	text string
	w    int
}

func newMobileSegment(s string) mobileSegment {
	return mobileSegment{text: s, w: lipgloss.Width(s)}
}

// renderMobileLine renders the "mobile" format for the current config's
// statusline_align, joined with "\n".
func renderMobileLine(p *Payload) string {
	align := config.Load().EffectiveStatuslineAlign()
	return strings.Join(renderMobileAligned(p, statuslineColumns(), align), "\n")
}

// renderMobileAligned applies statusline_align the way renderRichAligned does:
// right alignment shrinks the budget by rightAlignReserve and pads each line
// with padRune to it.
func renderMobileAligned(p *Payload, cols int, align string) []string {
	budget := cols
	if align == "right" {
		budget = cols - rightAlignReserve
	}
	lines := renderMobile(p, budget)
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
//	model · ctx N% · $cost
//	5h N% · 7d N% · account · ⎇ branch
//
// Values, thresholds and trust rules match the rich format (same
// coercePercent/coerceNonNegative, pushedCostTrusted, minDisplayCostUSD).
// Absent values are omitted; when a line exceeds cols, segments are dropped
// from its end (never truncated), always keeping the first one. If nothing is
// present, noDataLine is returned.
func renderMobile(p *Payload, cols int) []string {
	sepStr := render.SepStyle().Render(" · ")
	var lines []string
	if l := joinFitted(mobileLine1(p), sepStr, cols); l != "" {
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

// joinFitted drops trailing segments until the joined line fits cols (keeping
// at least one) and joins the rest. Empty input yields "".
func joinFitted(segs []mobileSegment, sep string, cols int) string {
	sepW := lipgloss.Width(sep)
	for len(segs) > 1 {
		total := 0
		for i, s := range segs {
			total += s.w
			if i > 0 {
				total += sepW
			}
		}
		if total <= cols {
			break
		}
		segs = segs[:len(segs)-1]
	}
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.text
	}
	return strings.Join(parts, sep)
}

// mobileLine1 is model, context % and trusted session cost.
func mobileLine1(p *Payload) []mobileSegment {
	th := theme.Current()
	labelStyle := render.LabelStyle()
	var segs []mobileSegment

	if p.Model != nil {
		name := ""
		if p.Model.DisplayName != nil && *p.Model.DisplayName != "" {
			name = *p.Model.DisplayName
		} else if p.Model.ID != nil && *p.Model.ID != "" {
			name = *p.Model.ID
		}
		if name != "" {
			segs = append(segs, newMobileSegment(labelStyle.Render(render.AbbreviateModel(name))))
		}
	}

	if pct, ok := p.ContextWindow.UsedPercentage(); ok {
		pctInt := int(math.Round(pct))
		var pctStr string
		if pctInt == 0 {
			pctStr = labelStyle.Render("0%")
		} else {
			models := config.Load().Models
			thresholds := []render.Threshold{
				{Cutoff: models.EffectiveContextWarnPct() - 1, Color: th.Green},
				{Cutoff: models.EffectiveContextCriticalPct() - 1, Color: th.Yellow},
				{Cutoff: 100, Color: th.Red},
			}
			pctStr = render.ThresholdStyle(pctInt, thresholds).Render(fmt.Sprintf("%d%%", pctInt))
		}
		segs = append(segs, newMobileSegment(labelStyle.Render("ctx ")+pctStr))
	}

	if p.Cost != nil && pushedCostTrusted(p) {
		if cost, ok := coerceNonNegative(p.Cost.TotalCostUSD); ok && cost >= minDisplayCostUSD {
			segs = append(segs, newMobileSegment(render.ValueStyle().Render(fmt.Sprintf("$%.2f", cost))))
		}
	}
	return segs
}

// mobileLine2 is 5h, 7d, account and git branch.
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
		}{{"5h ", rl.FiveHour}, {"7d ", rl.SevenDay}} {
			if w.win == nil {
				continue
			}
			if pct, ok := coercePercent(w.win.UsedPercentage); ok {
				pctInt := int(math.Round(pct))
				pctStr := render.ThresholdStyle(pctInt, thresholds).Render(fmt.Sprintf("%d%%", pctInt))
				segs = append(segs, newMobileSegment(labelStyle.Render(w.label)+pctStr))
			}
		}
	}

	if p.account != "" {
		segs = append(segs, newMobileSegment(render.ValueStyle().Render(p.account)))
	}

	dir := ""
	if p.Workspace != nil && p.Workspace.CurrentDir != nil && *p.Workspace.CurrentDir != "" {
		dir = *p.Workspace.CurrentDir
	} else if p.Cwd != nil && *p.Cwd != "" {
		dir = *p.Cwd
	}
	if dir != "" {
		if branch := gitBranch(dir); branch != "" {
			segs = append(segs, newMobileSegment(labelStyle.Render(string(branchGlyph))+" "+render.MutedStyle().Render(branch)))
		}
	}
	return segs
}
