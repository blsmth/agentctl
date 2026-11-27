package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
)

// hubStatusParentMaxW is the width the parent title is cut to before it is
// dropped.
const hubStatusParentMaxW = 16

// footSeg is one footer segment: styled text and its display width.
type footSeg struct {
	text string
	w    int
}

func footText(plain string, st lipgloss.Style) footSeg {
	return footSeg{st.Render(plain), lipgloss.Width(plain)}
}

// footPlan records which optional facts the facts line has given up so far.
type footPlan struct {
	dropFired bool
	dropAgent bool
	parent    int // 0 full, 1 cut to 16 columns, 2 dropped
	dropExtra bool
}

// hubStatusFooterSep joins the facts.
const hubStatusFooterSep = " │ "

// hubStatusFooter renders the facts line in at most tw columns, or "" when
// the block has no fact to show: ticket (or the cron job and when it fired),
// agent name (only when it differs from the title), PR, parent, sub-agent
// counts, and a non-default backend/account. Facts are dropped whole, never
// cut mid-word, in order: when the cron job fired, the agent, the parent
// (cut to 16 columns first), the backend/account. Never dropped: the ticket,
// PR, cron job and sub-agent counts; if those alone overflow, the line is
// end-truncated.
func hubStatusFooter(st *hubStatus, tw int, now time.Time) string {
	steps := []func(*footPlan){
		func(p *footPlan) { p.dropFired = true },
		func(p *footPlan) { p.dropAgent = true },
		func(p *footPlan) { p.parent = 1 },
		func(p *footPlan) { p.parent = 2 },
		func(p *footPlan) { p.dropExtra = true },
	}
	var plan footPlan
	sep := fleetDimStyle.Render(hubStatusFooterSep)
	sepW := lipgloss.Width(hubStatusFooterSep)
	for i := 0; ; i++ {
		lt, lw := joinSegs(footerSegs(st, plan, now), sep, sepW)
		if lw == 0 {
			return ""
		}
		if lw <= tw {
			return lt
		}
		if i < len(steps) {
			steps[i](&plan)
			continue
		}
		return ansi.Truncate(lt, tw, "…")
	}
}

func joinSegs(segs []footSeg, sep string, sepW int) (string, int) {
	if len(segs) == 0 {
		return "", 0
	}
	parts := make([]string, len(segs))
	w := 0
	for i, s := range segs {
		parts[i] = s.text
		w += s.w
		if i > 0 {
			w += sepW
		}
	}
	return strings.Join(parts, sep), w
}

// footerSegs builds the facts line's segments for plan.
func footerSegs(st *hubStatus, plan footPlan, now time.Time) []footSeg {
	dim := fleetDimStyle
	var left []footSeg
	if st.SchedBy != "" {
		left = append(left, footText(fleetScheduledGlyph+" "+st.SchedBy, dim))
		if !plan.dropFired && !st.SchedAt.IsZero() {
			left = append(left, footText("fired "+fleet.CompactAge(now.Sub(st.SchedAt)), dim))
		}
	} else if st.Ticket > 0 {
		left = append(left, footText(fmt.Sprintf("#%d", st.Ticket), dim))
	}
	if st.SchedBy == "" && !plan.dropAgent && st.Agent != "" && st.Agent != st.Title {
		left = append(left, footText(st.Agent, dim))
	}
	if st.PRNumber > 0 {
		seg := footText(fmt.Sprintf("PR #%d", st.PRNumber), dim)
		if st.PRURL != "" {
			seg.text = ansi.SetHyperlink(st.PRURL) + seg.text + ansi.ResetHyperlink()
		}
		left = append(left, seg)
	}
	if st.ParentTitle != "" && plan.parent < 2 {
		t := st.ParentTitle
		if plan.parent == 1 {
			t = truncateToWidth(t, hubStatusParentMaxW)
		}
		left = append(left, footText("← "+t, dim))
	}
	if st.Kids > 0 {
		left = append(left, kidsSeg(st))
	}
	if !plan.dropExtra {
		for _, e := range st.Extra {
			left = append(left, footText(e, dim))
		}
	}
	return left
}

// kidsSeg is "N sub-agents · K blocked · K working" (direct children),
// coloured by the worst state among them: blocked, else working, else dim.
func kidsSeg(st *hubStatus) footSeg {
	plain := fmt.Sprintf("%d sub-agent%s", st.Kids, plural(st.Kids))
	style := fleetDimStyle
	switch {
	case st.KidsBlocked > 0:
		style = fleetBlockedStyle
	case st.KidsWorking > 0:
		style = fleetWorkingStyle
	}
	if st.KidsBlocked > 0 {
		plain += fmt.Sprintf(" · %d blocked", st.KidsBlocked)
	}
	if st.KidsWorking > 0 {
		plain += fmt.Sprintf(" · %d working", st.KidsWorking)
	}
	return footText(plain, style)
}
