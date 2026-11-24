package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/render"
)

// hubStatusMinCostUSD is the floor below which the footer hides the cost (a
// fresh window, or a model with no known price, reports 0).
const hubStatusMinCostUSD = 0.005

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
	dropLen, dropAgent, dropModel, dropCost, dropCtx bool
	branch                                           int // 0 full, 1 "…/<last>", 2 dropped
	parent                                           int // 0 full, 1 cut to 16 columns, 2 dropped
}

// hubStatusFooter renders the facts footer in at most tw columns, or "" when
// the block has nothing to say. Left zone: ticket, agent, branch, PR, parent
// (or the cron job and when it fired), sub-agents, non-default backend and
// account. Right zone, right-aligned: model, context, cost, session length.
// Facts are dropped whole, in order: session length, branch (shortened, then
// gone), parent (cut to 16 columns, then gone), agent, model, cost, context
// below its warn threshold. Never dropped: the PR, ticket, cron job, the
// sub-agent counts, backend/account and context at or above warn; if those
// alone overflow, the left zone is end-truncated.
func hubStatusFooter(st *hubStatus, tw int, now time.Time) string {
	steps := []func(*footPlan){
		func(p *footPlan) { p.dropLen = true },
		func(p *footPlan) { p.branch = 1 },
		func(p *footPlan) { p.branch = 2 },
		func(p *footPlan) { p.parent = 1 },
		func(p *footPlan) { p.parent = 2 },
		func(p *footPlan) { p.dropAgent = true },
		func(p *footPlan) { p.dropModel = true },
		func(p *footPlan) { p.dropCost = true },
		func(p *footPlan) { p.dropCtx = true },
	}
	var plan footPlan
	sep := fleetDimStyle.Render(" · ")
	const sepW, gap = 3, 2
	for i := 0; ; i++ {
		left, right := footerSegs(st, plan, now)
		lt, lw := joinSegs(left, sep, sepW)
		rt, rw := joinSegs(right, sep, sepW)
		if lw == 0 && rw == 0 {
			return ""
		}
		need := lw
		if rw > 0 {
			need += gap + rw
		}
		if need <= tw {
			if rw == 0 {
				return lt
			}
			return lt + strings.Repeat(" ", tw-lw-rw) + rt
		}
		if i < len(steps) {
			steps[i](&plan)
			continue
		}
		// Nothing left to drop: end-truncate the left zone.
		if room := tw - rw - gap; rw > 0 && room >= 4 {
			lt = ansi.Truncate(lt, room, "…")
			return lt + strings.Repeat(" ", max(room-lipgloss.Width(lt), 0)+gap) + rt
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

// footerSegs builds the two zones' segments for plan.
func footerSegs(st *hubStatus, plan footPlan, now time.Time) (left, right []footSeg) {
	dim := fleetDimStyle
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
	if b := footBranch(st.Branch, plan.branch); b != "" {
		left = append(left, footText(b, dim))
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

	if st.Model != "" && !plan.dropModel {
		right = append(right, footText(st.Model, dim))
	}
	if st.CtxPct > 0 {
		d := fleetDisplay{ctxWarn: st.CtxWarn, ctxCrit: st.CtxCrit}
		warn, crit := d.thresholds()
		if st.CtxPct >= warn || !plan.dropCtx {
			txt := fmt.Sprintf("ctx %d%%", st.CtxPct)
			if st.CtxPct >= crit {
				txt += "!"
			}
			right = append(right, footText(txt, d.ctxStyle(st.CtxPct)))
		}
	}
	if st.CostUSD >= hubStatusMinCostUSD && !plan.dropCost {
		right = append(right, footText(fmt.Sprintf("$%.2f", st.CostUSD), dim))
	}
	if st.StartTime > 0 && !plan.dropLen {
		if d := now.Sub(time.Unix(st.StartTime, 0)); d > 0 {
			right = append(right, footText(render.Duration(d), dim))
		}
	}
	return left, right
}

// footBranch is the branch at the plan's level: whole, "…/<last part>", or "".
func footBranch(b string, level int) string {
	switch level {
	case 0:
		return b
	case 1:
		if i := strings.LastIndex(b, "/"); i >= 0 && i < len(b)-1 {
			return "…/" + b[i+1:]
		}
		return b
	}
	return ""
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
