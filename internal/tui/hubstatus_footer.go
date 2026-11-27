package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/verdicts"
)

// hubStatusParentMaxW is the width the parent title is cut to before it is
// dropped.
const hubStatusParentMaxW = 16

// footSeg is one footer segment: styled text and its display width.
type footSeg struct {
	text string
	w    int
	link string // URL a click on the segment opens; "" for none
}

// footSpan is a clickable stretch of the facts line: columns [x0, x1) open url.
type footSpan struct {
	x0, x1 int
	url    string
}

func footText(plain string, st lipgloss.Style) footSeg {
	return footSeg{text: st.Render(plain), w: lipgloss.Width(plain)}
}

// footPlan records which optional facts the facts line has given up so far.
type footPlan struct {
	dropFired   bool
	dropVerdict bool
	dropAgent   bool
	parent      int // 0 full, 1 cut to 16 columns, 2 dropped
	dropExtra   bool
}

// hubStatusFooterSep joins the facts.
const hubStatusFooterSep = " │ "

// hubStatusFooter renders the facts line in at most tw columns, or "" when
// the block has no fact to show: ticket (or the cron job and when it fired),
// agent name (only when it differs from the title), PR, parent, sub-agent
// counts, and a non-default backend/account. Facts are dropped whole, never
// cut mid-word, in order: when the cron job fired, the review verdict, the agent, the parent
// (cut to 16 columns first), the backend/account. Never dropped: the ticket,
// PR, cron job and sub-agent dots; if those alone overflow, the line is
// end-truncated.
func hubStatusFooter(st *hubStatus, tw int, now time.Time) string {
	line, _ := hubStatusFooterSpans(st, tw, now)
	return line
}

// hubStatusFooterSpans is hubStatusFooter plus the clickable spans (the PR and
// ticket facts) in the columns they were drawn at after fact dropping and
// truncation, relative to the start of the line.
func hubStatusFooterSpans(st *hubStatus, tw int, now time.Time) (string, []footSpan) {
	steps := []func(*footPlan){
		func(p *footPlan) { p.dropFired = true },
		func(p *footPlan) { p.dropVerdict = true },
		func(p *footPlan) { p.dropAgent = true },
		func(p *footPlan) { p.parent = 1 },
		func(p *footPlan) { p.parent = 2 },
		func(p *footPlan) { p.dropExtra = true },
	}
	var plan footPlan
	sep := fleetDimStyle.Render(hubStatusFooterSep)
	sepW := lipgloss.Width(hubStatusFooterSep)
	for i := 0; ; i++ {
		lt, lw, spans := joinSegs(footerSegs(st, plan, now), sep, sepW)
		if lw == 0 {
			return "", nil
		}
		if lw <= tw {
			return lt, spans
		}
		if i < len(steps) {
			steps[i](&plan)
			continue
		}
		return ansi.Truncate(lt, tw, "…"), clipSpans(spans, tw)
	}
}

// clipSpans cuts spans to the first w columns and drops those that start
// beyond them.
func clipSpans(spans []footSpan, w int) []footSpan {
	var out []footSpan
	for _, sp := range spans {
		if sp.x0 >= w {
			continue
		}
		sp.x1 = min(sp.x1, w)
		out = append(out, sp)
	}
	return out
}

func joinSegs(segs []footSeg, sep string, sepW int) (string, int, []footSpan) {
	if len(segs) == 0 {
		return "", 0, nil
	}
	parts := make([]string, len(segs))
	var spans []footSpan
	w := 0
	for i, s := range segs {
		if i > 0 {
			w += sepW
		}
		parts[i] = s.text
		if s.link != "" {
			spans = append(spans, footSpan{x0: w, x1: w + s.w, url: s.link})
		}
		w += s.w
	}
	return strings.Join(parts, sep), w, spans
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
		seg := footText(fmt.Sprintf("#%d", st.Ticket), dim)
		seg.link = st.TicketURL
		left = append(left, seg)
	}
	if st.SchedBy == "" && !plan.dropAgent && st.Agent != "" && st.Agent != st.Title {
		left = append(left, footText(st.Agent, dim))
	}
	if st.PRNumber > 0 {
		left = append(left, prSeg(st, plan))
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

// prSeg is "PR #N", followed by " · <verdict>" when the review ledger has a
// verdict for it (and the line has room). The whole segment is the OSC 8
// link and the click target for st.PRURL.
func prSeg(st *hubStatus, plan footPlan) footSeg {
	seg := footText(fmt.Sprintf("PR #%d", st.PRNumber), fleetDimStyle)
	if label, style := hubStatusVerdict(st.Verdict); label != "" && !plan.dropVerdict {
		v := footText(label, style)
		dot := footText(" · ", fleetDimStyle)
		seg.text += dot.text + v.text
		seg.w += dot.w + v.w
	}
	if st.PRURL != "" {
		seg.text = ansi.SetHyperlink(st.PRURL) + seg.text + ansi.ResetHyperlink()
		seg.link = st.PRURL
	}
	return seg
}

// hubStatusVerdict is the label and style for a review verdict (see
// internal/verdicts); "" for none or an unknown value.
func hubStatusVerdict(verdict string) (string, lipgloss.Style) {
	switch verdict {
	case verdicts.Merge, verdicts.MergeWithFollowup:
		return "approved", fleetDoneStyle
	case verdicts.Blocked:
		return "changes", fleetAttentionStyle
	case verdicts.NeedsBrendan:
		return "needs you", fleetBlockedStyle
	}
	return "", fleetDimStyle
}

// hubStatusKidsMax is how many sub-agent dots are drawn before "+N".
const hubStatusKidsMax = 8

// kidsSeg is one glyph per direct child in its state colour (the hub rows'
// glyphs), worst state first: blocked, needs attention, working, done, idle.
// Only counts are published, so the dots are drawn from them. Above
// hubStatusKidsMax the first eight are shown, then "+N".
func kidsSeg(st *hubStatus) footSeg {
	type group struct {
		glyph string
		n     int
	}
	groups := []group{
		{"⏸", st.KidsBlocked},
		{fleet.AttentionGlyph, st.KidsAttention},
		{"●", st.KidsWorking},
		{"✓", st.KidsDone},
	}
	rest := st.Kids
	for _, g := range groups {
		rest -= g.n
	}
	groups = append(groups, group{"○", max(rest, 0)})
	var parts []string
	w, shown := 0, 0
	for _, g := range groups {
		for range g.n {
			if shown == hubStatusKidsMax {
				break
			}
			if shown > 0 {
				w++
			}
			parts = append(parts, hubStatusGlyphStyle(g.glyph).Render(g.glyph))
			w += lipgloss.Width(g.glyph)
			shown++
		}
	}
	text := strings.Join(parts, " ")
	if extra := st.Kids - shown; extra > 0 {
		more := fmt.Sprintf("+%d", extra)
		text += " " + fleetDimStyle.Render(more)
		w += 1 + lipgloss.Width(more)
	}
	return footSeg{text: text, w: w}
}
