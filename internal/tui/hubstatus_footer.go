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

// footPlan records which optional facts the footer has given up so far.
type footPlan struct {
	dropAgent bool
	parent    int // 0 full, 1 cut to 16 columns, 2 dropped
}

// hubStatusHasFacts reports whether the footer has anything to say beyond the
// agent name: a ticket, PR, parent, cron job, sub-agents or a non-default
// backend/account. Without any, the agent name moves to the title line and
// the footer row goes to the recap.
func hubStatusHasFacts(st *hubStatus) bool {
	return st.SchedBy != "" || st.Ticket > 0 || st.PRNumber > 0 || st.ParentTitle != "" ||
		st.Kids > 0 || len(st.Extra) > 0
}

// hubStatusAgentOnTitle is the agent name the title line carries when the
// footer would hold nothing else ("" when it has other facts or the name just
// repeats the title).
func hubStatusAgentOnTitle(st *hubStatus) string {
	if hubStatusHasFacts(st) || st.Agent == st.Title {
		return ""
	}
	return st.Agent
}

// hubStatusFooter renders the facts footer in at most tw columns, or "" when
// the block has no fact beyond the agent name. Ticket, agent, PR, parent (or
// the cron job and when it fired), sub-agents, non-default backend and
// account. Facts are dropped whole, in order: parent (cut to 16 columns, then
// gone), agent. Never dropped: the PR, ticket, cron job, the sub-agent counts
// and backend/account; if those alone overflow, the footer is end-truncated.
func hubStatusFooter(st *hubStatus, tw int, now time.Time) string {
	if !hubStatusHasFacts(st) {
		return ""
	}
	steps := []func(*footPlan){
		func(p *footPlan) { p.parent = 1 },
		func(p *footPlan) { p.parent = 2 },
		func(p *footPlan) { p.dropAgent = true },
	}
	var plan footPlan
	sep := fleetDimStyle.Render(" · ")
	const sepW = 3
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

// footerSegs builds the footer's segments for plan.
func footerSegs(st *hubStatus, plan footPlan, now time.Time) []footSeg {
	dim := fleetDimStyle
	var left []footSeg
	if st.SchedBy != "" {
		left = append(left, footText(fleetScheduledGlyph+" "+st.SchedBy, dim))
		if !plan.dropAgent && !st.SchedAt.IsZero() {
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
	for _, e := range st.Extra {
		left = append(left, footText(e, dim))
	}
	return left
}

// kidsSeg is "↳ N sub-agents ⏸n ●n" (direct children): the counts of the
// blocked and working ones in their state colours.
func kidsSeg(st *hubStatus) footSeg {
	plain := fmt.Sprintf("↳ %d sub-agent%s", st.Kids, plural(st.Kids))
	seg := footText(plain, fleetDimStyle)
	if st.KidsBlocked > 0 {
		t := fmt.Sprintf("⏸%d", st.KidsBlocked)
		seg.text += " " + fleetBlockedStyle.Render(t)
		seg.w += 1 + lipgloss.Width(t)
	}
	if st.KidsWorking > 0 {
		t := fmt.Sprintf("●%d", st.KidsWorking)
		seg.text += " " + fleetWorkingStyle.Render(t)
		seg.w += 1 + lipgloss.Width(t)
	}
	return seg
}
