package statusline

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/ratelimit"
	"github.com/blsmth/agentctl/internal/render"
	"github.com/blsmth/agentctl/internal/signal"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// Run is the entry point for `agentctl statusline`. main.go routes it here
// on the same fast path as hook subcommands, before Kong is ever
// initialized -- see main.go's isStatuslineIngestCmd. dryRun, when true,
// skips persistWindowState/persistRateLimits entirely (`--dry-run`) so manually invoking this
// command with a synthetic payload to check its rendering never corrupts the shared ratelimits.json or the calling
// window's windowstate.
func Run(dryRun bool) {
	RunWithIO(os.Stdin, os.Stdout, dryRun)
}

// RunWithIO is Run with injectable stdin/stdout, split out for testing.
//
// Contract, and it is load-bearing: Claude Code renders whatever
// this process's stdout contains as the user's status line, and it CANCELS
// an in-flight invocation the instant a new update triggers. That means:
//
//   - This function must never cause the process to exit non-zero. It has
//     no return value and never calls os.Exit; every error path below logs
//     to stderr (via internal/logging) and falls back to printing a plain
//     line instead.
//   - Stdout must always be non-empty, even for malformed/empty stdin or an
//     unresolvable window -- a blank stdout blanks the user's status line.
//     Most formats emit exactly one line; "rich" may emit two.
//   - Every persistence path (windowstate, the shared rate-limits file) is
//     atomic tmp+rename, because a cancelled invocation could be killed
//     mid-write.
//   - No network I/O, no subprocesses: read stdin, resolve the window,
//     write, print.
func RunWithIO(r io.Reader, w io.Writer, dryRun bool) {
	defer func() {
		if rec := recover(); rec != nil {
			logging.Log("statusline: recovered from panic: %v", rec)
			fmt.Fprintln(w, fallbackLine)
		}
	}()

	data, err := io.ReadAll(r)
	if err != nil {
		logging.Log("statusline: reading stdin: %v", err)
		fmt.Fprintln(w, fallbackLine)
		return
	}

	payload, err := Decode(data)
	if err != nil {
		// Debug, not Log: this runs as a fresh process every refresh (1s), so
		// an INFO line here floods the session log.
		logging.Debug("statusline: decoding payload: %v", err)
		fmt.Fprintln(w, fallbackLine)
		return
	}

	// Resolve the window once and reuse it for both persistence and (when
	// format == "rich") the turns/duration lookup below, without
	// calling ResolveWindowID() twice.
	windowID := signal.ResolveWindowID()

	if !dryRun {
		persistWindowState(payload, windowID)
		persistRateLimits(payload)
	}

	format := config.Load().EffectiveStatuslineFormat()
	if format == "rich" {
		fmt.Fprintln(w, renderRichLine(payload, windowID))
		return
	}
	fmt.Fprintln(w, renderLine(payload, format))
}

// fallbackLine is printed whenever the payload can't be parsed, or parses to
// nothing displayable. It must never be empty -- see RunWithIO's contract.
const fallbackLine = "agentctl"

// noDataLine is what "complementary", "minimal", and "rich" print when there
// is nothing to show -- distinct from fallbackLine, which only covers
// undecodable input. Output must stay non-empty either way.
const noDataLine = "-"

// minDisplayCostUSD is the floor below which renderRichLine2 omits the cost
// segment entirely rather than render a noisy "$0.00". Cost is
// legitimately zero on a fresh session and again after /clear (Claude Code
// 2.1.211+); a negative value is clamped to 0 by coerceNonNegative and so is
// also omitted by this same check, never printed as "$0.00".
const minDisplayCostUSD = 0.005

// renderLine builds the single line Claude Code renders as the status line
// for the "full", "complementary", and "minimal" formats -- "rich" (the default) is handled
// separately by renderRichLine since it can emit two lines and needs the resolved window ID for the turns/duration segments.
// format is passed in rather than loaded here so each variant is
// independently unit-testable without touching config's package-level path
// globals.
func renderLine(p *Payload, format string) string {
	switch format {
	case "full":
		return renderFull(p)
	case "minimal":
		return renderMinimal()
	case "complementary":
		return renderComplementary(p)
	default:
		return renderComplementary(p)
	}
}

// renderFull is the original rendering: model display name, context
// percentage (when known), and the agent name (when present) -- no color, no
// widgets, no layout. Kept as the "full" format for anyone who wants the
// redundant-with-chrome-bar original behavior back.
func renderFull(p *Payload) string {
	var parts []string

	if p.Model != nil {
		if p.Model.DisplayName != nil && *p.Model.DisplayName != "" {
			parts = append(parts, *p.Model.DisplayName)
		} else if p.Model.ID != nil && *p.Model.ID != "" {
			parts = append(parts, *p.Model.ID)
		}
	}

	if pct, ok := p.ContextWindow.UsedPercentage(); ok {
		parts = append(parts, fmt.Sprintf("ctx %d%%", int(math.Round(pct))))
	}

	if p.Agent != nil && p.Agent.Name != nil && *p.Agent.Name != "" {
		parts = append(parts, *p.Agent.Name)
	}

	if len(parts) == 0 {
		return fallbackLine
	}
	return strings.Join(parts, " · ")
}

// renderComplementary renders the "complementary" format: account-level rate limit
// and cost figures that agentctl's own chrome bar does NOT already render,
// rather than duplicating (and disagreeing with) the per-window
// model/ctx%/agent row the chrome bar owns. Any absent component is omitted
// entirely -- never printed as a zero or placeholder -- and if every
// component is absent, noDataLine is printed so stdout is never blank.
// Superseded by "rich" as the default, but kept for anyone who wants
// a single plain line back.
func renderComplementary(p *Payload) string {
	var parts []string

	if p.RateLimits != nil {
		if p.RateLimits.FiveHour != nil {
			if pct, ok := coercePercent(p.RateLimits.FiveHour.UsedPercentage); ok {
				parts = append(parts, fmt.Sprintf("5h %d%%", int(math.Round(pct))))
			}
		}
		if p.RateLimits.SevenDay != nil {
			if pct, ok := coercePercent(p.RateLimits.SevenDay.UsedPercentage); ok {
				parts = append(parts, fmt.Sprintf("7d %d%%", int(math.Round(pct))))
			}
		}
	}

	if p.Cost != nil {
		if cost, ok := coerceNonNegative(p.Cost.TotalCostUSD); ok {
			parts = append(parts, fmt.Sprintf("$%.2f", cost))
		}
	}

	if len(parts) == 0 {
		return noDataLine
	}
	return strings.Join(parts, " · ")
}

// renderMinimal is the "minimal" format: a bare placeholder, for users who
// want `agentctl statusline` installed (e.g. so its windowstate/rate-limit
// side effects run) but nothing rendered at all.
func renderMinimal() string {
	return noDataLine
}

// statuslineColumns reads the COLUMNS environment variable Claude Code sets
// on every statusLine invocation (guaranteed since 2.1.153). Falls back to
// 80 when unset, empty, or unparseable -- including "0" or a negative value,
// either of which would otherwise produce a 0-width (or panicking) render.
func statuslineColumns() int {
	const defaultCols = 80
	v, ok := os.LookupEnv("COLUMNS")
	if !ok {
		return defaultCols
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return defaultCols
	}
	return n
}

// lookupAgentState resolves the current window's state.AgentState for the
// "turns" and "duration" segments of the rich statusline -- neither
// is present in the Claude Code payload itself. This is strictly
// best-effort: state.json may not exist yet (state.Init hasn't run in this
// process, or the monitor has never ticked), be stale, or simply have no
// entry for this window. Any of those cases just means those two segments
// are omitted downstream -- this never errors, never blocks, and never
// delays the line.
func lookupAgentState(windowID string) *state.AgentState {
	if windowID == "" {
		return nil
	}
	snap, err := state.LoadSnapshot()
	if err != nil || snap == nil {
		return nil
	}
	return snap.ByWindowID(windowID)
}

// consistentAgentState drops a snapshot entry that demonstrably belongs to a
// different session than the live process rendering this line. When the
// payload carries a session_id and the entry has a log path whose basename is
// not <session_id>.jsonl, the snapshot's turns/tokens/duration describe some
// other session's log, so omitting those segments is better than showing wrong numbers. An entry
// with no log path, or a payload with no session_id, cannot be judged and is
// kept.
func consistentAgentState(as *state.AgentState, p *Payload) *state.AgentState {
	if as == nil || as.LogPath == "" || p == nil || p.SessionID == nil || *p.SessionID == "" {
		return as
	}
	if filepath.Base(as.LogPath) != *p.SessionID+".jsonl" {
		return nil
	}
	return as
}

// renderRichLine renders the "rich" format and joins its lines with
// "\n" for a single Fprintln call. Resolves the render width from COLUMNS,
// the alignment from statusline_align (config), and the turns/duration
// state from state.json, reusing the windowID the caller already resolved.
func renderRichLine(p *Payload, windowID string) string {
	align := config.Load().EffectiveStatuslineAlign()
	as := consistentAgentState(lookupAgentState(windowID), p)
	lines := renderRichAligned(p, statuslineColumns(), as, align)
	return strings.Join(lines, "\n")
}

// rightAlignReserve is subtracted from cols before trimming/padding when
// statusline_align is "right". Measured empirically via tmux capture-pane
// against a live Claude Code pane (see renderRich's doc comment for the
// methodology): Claude Code prepends its own fixed-width prefix to every
// line this process emits, so padding a line to exactly cols overflows the
// row. A reserve of exactly that measured prefix width (2 columns) turned
// out to be razor's edge in practice -- at pane_width=232, cols-2 padding
// produced rows Claude Code's own renderer truncated with a trailing "…",
// silently eating the tail of the real content (exactly the segment
// right-alignment is meant to keep visible). Padding to a smaller budget
// with a few spare columns of headroom (6, chosen empirically -- 4 spare
// columns after Claude's own 2-column prefix) reproducibly rendered full,
// untruncated content across repeated live captures. Keep this as a single
// named constant rather than inlining "6" at each call site, since the
// value is a measured safety margin, not a mathematical certainty, and is
// the first place to revisit if a future Claude Code release changes its
// own row-reservation behavior.
const rightAlignReserve = 6

// renderRichAligned wraps renderRich with optional right-alignment
// (statusline_align = "right"). When align is "right", the trimming budget
// passed to renderRich (and, in turn, to both renderRichLine1 and
// renderRichLine2) is reduced by rightAlignReserve up front, and each
// resulting line is then left-padded with plain, unstyled spaces so every
// emitted line's lipgloss.Width equals that same reduced budget -- sharing
// one right edge. Trimming and padding deliberately share the same budget:
// a line trimmed to cols but padded to cols-rightAlignReserve would overflow.
// When align is anything else ("left", unset, or unrecognized), this is
// exactly renderRich(p, cols, snap) with no padding, so left-aligned output
// stays byte-identical to pre-alignment behavior.
func renderRichAligned(p *Payload, cols int, snap *state.AgentState, align string) []string {
	budget := cols
	if align == "right" {
		budget = cols - rightAlignReserve
	}
	lines := renderRich(p, budget, snap)
	if align != "right" {
		return lines
	}
	for i, l := range lines {
		lines[i] = padLeftTo(l, budget)
	}
	return lines
}

// padRune is the character padLeftTo repeats to fill the leading margin.
//
// It is deliberately NOT U+0020 (plain space), and deliberately NOT any
// other Unicode Zs (Space_Separator) code point either -- U+00A0, U+2000
// through U+200A, U+202F, U+205F, and U+3000 are all in the same trap.
// Live verification (see padLeftTo's doc comment) found that Claude Code
// strips a leading run of whitespace from a statusLine command's stdout
// before compositing its own row, which is consistent with a JS `^\s+`
// strip or an ECMAScript-style `.trim()`: `\s` in ECMAScript covers the
// entire Zs category plus a handful of format/control characters, so any
// character in that set would vanish the same way plain spaces did.
//
// U+2800 BRAILLE PATTERN BLANK (⠀) renders as an empty braille cell but is
// Unicode general category So (Symbol, other) -- not Zs, and not matched
// by `\s` in ECMAScript, POSIX, or Go's own unicode.IsSpace. It survives
// the strip while still being visually blank (mono-spaced fonts render an
// unset braille cell as nothing but background), which is exactly the
// property padding needs.
const padRune = '⠀' // U+2800 BRAILLE PATTERN BLANK -- see comment above; do not "fix" this to ' '.

// padLeftTo left-pads line with padRune (never a plain space -- see its
// doc comment) so its lipgloss.Width equals width. Uses lipgloss.Width
// rather than len() -- these lines are full of ANSI escapes and wide
// block-drawing characters that len() would miscount, producing wildly
// wrong padding. Padding is deliberately never wrapped in a style:
// agentctl doesn't own this terminal row the way the old chrome bar owned
// its pane, and a background-filled pad would paint a visible block on a
// surface Claude Code shares with other content.
//
// The padding count is clamped at zero because line CAN legitimately exceed
// width -- renderRichLine1's mandatory 5h floor segment or renderRichLine2's
// mandatory model+ctx pair can each be wider than a very narrow COLUMNS
// (there is a documented, pre-existing floor around ~42 columns for line 1).
// strings.Repeat panics on a negative count; in that case the line is
// emitted unpadded rather than panicking and blanking the user's status
// line entirely.
func padLeftTo(line string, width int) string {
	padding := width - lipgloss.Width(line)
	if padding <= 0 {
		return line
	}
	return strings.Repeat(string(padRune), padding) + line
}

// renderRich builds the (up to) 3-line "rich" Claude Code status line:
//
//	<battery> 5h 11% · resets 5pm · 7d 1%
//	opus-4.6 · <battery> 34% ctx · 68k/200k · turn: 3k↑ 1k↓ · 78% cached · 12 turns · total: 789k↑ 6k↓ · $13.43 · 1h23m
//	~/code/agentctl · ⎇ main · +-3 · issue-1023 · PR #42 approved
//
// Line 1 (account rate limits), line 2 (session metrics), and line 3
// (workspace/git) are each built independently and only included if they
// have at least one segment to show -- an absent line is omitted entirely
// rather than emitted blank. If every line ends up empty, a single
// noDataLine is returned so the caller's stdout is never blank. cols is
// measured with lipgloss.Width (the output contains ANSI escapes and wide
// block-drawing characters that len() would miscount) and renderRich itself
// NEVER pads its output to cols.
//
// UPDATE (right-align): the comment above used to claim, unconditionally,
// that padding was unsafe here because Claude Code shares this row with
// notifications and would wrap/truncate a full-width line. Re-measured
// against a live pane with tmux capture-pane: the statusline occupies its
// own rows below the input-box separator (a "Tip:" notification rendered
// above the input box, not on these rows), and Claude Code already
// right-aligns its own "● high · /effort" indicator on a nearby row -- so
// padding is in fact viable, and right-alignment is idiomatic for this
// surface. The real hazard is narrower than the original blanket claim, but
// also less exact than a first measurement suggested: Claude Code prepends
// a fixed-width prefix to every line this process emits (measured as 2
// spaces at pane_width=214), so padding all the way to cols overflows the
// row. In further live testing at pane_width=232, padding to exactly
// cols-2 didn't wrap -- it triggered a DIFFERENT failure: Claude Code's own
// renderer truncated the resulting row with a trailing "…", silently
// eating the tail of the real content, which is worse for a right-aligned
// line than either wrapping or not padding at all, since the truncated
// tail is exactly the data right-alignment exists to keep visible. A
// slightly larger reserve (rightAlignReserve, empirically settled at 6 --
// see its doc comment) leaves enough spare columns that this hasn't been
// observed to recur. This function still never pads on its own -- padding,
// when requested, lives one level up in renderRichAligned, which reduces
// the budget by rightAlignReserve before it ever reaches this function's
// trimming loops. (Separately: the actual pad character used by
// renderRichAligned/padLeftTo is NOT a plain space -- see padRune's doc
// comment for why a literal space is invisible on this surface for an
// entirely different reason than wrapping or truncation.)
func renderRich(p *Payload, cols int, snap *state.AgentState) []string {
	var lines []string
	if l1, ok := renderRichLine1(p, cols); ok {
		lines = append(lines, l1)
	}
	if l2, ok := renderRichLine2(p, cols, snap); ok {
		lines = append(lines, l2)
	}
	if l3, ok := renderRichLine3(p, cols); ok {
		lines = append(lines, l3)
	}
	if len(lines) == 0 {
		return []string{noDataLine}
	}
	return lines
}

// renderRichLine1 renders the account rate-limit row: 5h/7d/spend usage plus
// the 5h window's reset time. Returns ok=false when p.RateLimits is nil or
// every window fails coercion, so the caller omits the line entirely rather
// than printing a blank one -- rate_limits is absent for non-Pro/Max
// accounts, and that's a normal, common state, not an error.
//
// Segments are dropped right-to-left (spend first, then 7d) when the line
// would exceed cols, mirroring renderRichLine2's trimming loop -- this line
// shares the same status-line row and must obey the same COLUMNS budget.
// The first present segment (5h, or whichever comes first if 5h is absent)
// is the floor and is never dropped.
func renderRichLine1(p *Payload, cols int) (string, bool) {
	if p.RateLimits == nil {
		return "", false
	}

	// One theme.Current() lookup for the whole render pass -- reused for
	// every threshold and every style helper below rather than re-fetched
	// per segment (this is a ~25ms hot path invoked at 1Hz).
	th := theme.Current()
	// Cutoffs from the deleted chrome_bar.go renderRateLimitLine: 75/90/100.
	// Shared by 5h, 7d, and spend -- ThresholdStyle already returns the last
	// color for values above the last cutoff, so spend over 100% correctly
	// reads Red without a clamp.
	thresholds := []render.Threshold{
		{Cutoff: 75, Color: th.Green},
		{Cutoff: 90, Color: th.Yellow},
		{Cutoff: 100, Color: th.Red},
	}
	labelStyle := render.LabelStyle()
	mutedStyle := render.MutedStyle()
	sepStyle := render.SepStyle()
	sepStr := sepStyle.Render(" · ")

	type segment struct {
		text string
		w    int
	}
	var segments []segment
	addSeg := func(s string) {
		segments = append(segments, segment{text: s, w: lipgloss.Width(s)})
	}

	if fh := p.RateLimits.FiveHour; fh != nil {
		if pct, ok := coercePercent(fh.UsedPercentage); ok {
			pctInt := int(math.Round(pct))
			pctStr := render.ThresholdStyle(pctInt, thresholds).Render(fmt.Sprintf("%d%%", pctInt))
			seg := render.Battery(pctInt) + " " + labelStyle.Render("5h ") + pctStr
			if resetsAt, ok := coerceNonNegative(fh.ResetsAt); ok {
				if rs := render.ResetTime(int64(resetsAt)); rs != "" {
					seg += sepStr + mutedStyle.Render(rs)
				}
			}
			addSeg(seg)
		}
	}

	if sd := p.RateLimits.SevenDay; sd != nil {
		if pct, ok := coercePercent(sd.UsedPercentage); ok {
			pctInt := int(math.Round(pct))
			pctStr := render.ThresholdStyle(pctInt, thresholds).Render(fmt.Sprintf("%d%%", pctInt))
			addSeg(labelStyle.Render("7d ") + pctStr)
		}
	}

	// spend_limit can legitimately exceed 100% -- coerceNonNegative, never
	// coercePercent, and never clamp.
	if sl := p.RateLimits.SpendLimit; sl != nil {
		if pct, ok := coerceNonNegative(sl.UsedPercentage); ok {
			pctInt := int(math.Round(pct))
			pctStr := render.ThresholdStyle(pctInt, thresholds).Render(fmt.Sprintf("%d%%", pctInt))
			addSeg(labelStyle.Render("spend ") + pctStr)
		}
	}

	if len(segments) == 0 {
		return "", false
	}

	const minKeep = 1
	sepW := lipgloss.Width(sepStr)
	for len(segments) > minKeep {
		total := 0
		for i, s := range segments {
			total += s.w
			if i > 0 {
				total += sepW
			}
		}
		if total <= cols {
			break
		}
		segments = segments[:len(segments)-1]
	}

	var sb strings.Builder
	for i, s := range segments {
		if i > 0 {
			sb.WriteString(sepStr)
		}
		sb.WriteString(s.text)
	}
	return sb.String(), true
}

// renderRichLine2 renders the session metrics row: model, context%, used/
// window tokens, last-turn tokens, cache hit rate, turns, cumulative tokens,
// cost, and duration, in that order, dropping segments right-to-left
// (duration first) when the rendered line would exceed cols. Model and
// context are never dropped (minKeep = 2); if the two alone -- via a long
// abbreviated Label -- already overflow cols, the model segment falls back
// to the shorter family badge (render.AbbreviateModelFamily, e.g. "opus"
// instead of "opus-4.6") rather than let the always-kept pair alone blow the
// budget. Returns ok=false when every segment is absent so the caller omits
// the line entirely.
//
// The last-turn and cumulative token segments, and the cache hit rate's
// state.json fallback, restore functionality the old chrome
// bar metrics row had (`turn: 81k↑ 1k↓` / `total: 789k↑ 6k↓` / cache computed from cumulative
// counters) that was dropped, not redesigned, when the rich statusline
// replaced it -- restoring the pre-rich-statusline chrome-bar behavior.
func renderRichLine2(p *Payload, cols int, snap *state.AgentState) (string, bool) {
	// One theme.Current() lookup for the whole render pass, reused for every
	// style/threshold below (this is a ~25ms hot path invoked at 1Hz).
	th := theme.Current()
	labelStyle := render.LabelStyle()
	mutedStyle := render.MutedStyle()
	valueStyle := render.ValueStyle()
	sepStyle := render.SepStyle()
	sepStr := sepStyle.Render(" · ")

	modelsCfg := config.Load().Models
	warnPct := modelsCfg.EffectiveContextWarnPct()
	critPct := modelsCfg.EffectiveContextCriticalPct()

	type segment struct {
		text string
		w    int
	}
	var segments []segment
	addSeg := func(s string) {
		segments = append(segments, segment{text: s, w: lipgloss.Width(s)})
	}

	// 1. Model.
	modelIdx := -1
	var modelName string
	if p.Model != nil {
		if p.Model.DisplayName != nil && *p.Model.DisplayName != "" {
			modelName = *p.Model.DisplayName
		} else if p.Model.ID != nil && *p.Model.ID != "" {
			modelName = *p.Model.ID
		}
		if modelName != "" {
			modelIdx = len(segments)
			addSeg(labelStyle.Render(render.AbbreviateModel(modelName)))
		}
	}

	// 2. Context %. Displayed exactly as UsedPercentage() returns it -- this
	// value is input-only by design (excludes output_tokens) and must never
	// be recomputed, or it would disagree with Claude Code's own UI. Colored
	// against the CONFIGURED thresholds (defaults 75/90), matching the
	// deleted chrome_bar.go renderMetricsLine exactly, including its special
	// case: a 0% reading renders in LabelStyle rather than green, since 0%
	// usually means "no data yet" rather than "healthy".
	ctxIdx := -1
	if pct, ok := p.ContextWindow.UsedPercentage(); ok {
		pctInt := int(math.Round(pct))
		ctxIdx = len(segments)
		var pctStr string
		if pctInt == 0 {
			pctStr = labelStyle.Render("0%")
		} else {
			thresholds := []render.Threshold{
				{Cutoff: warnPct - 1, Color: th.Green},
				{Cutoff: critPct - 1, Color: th.Yellow},
				{Cutoff: 100, Color: th.Red},
			}
			pctStr = render.ThresholdStyle(pctInt, thresholds).Render(fmt.Sprintf("%d%%", pctInt))
		}
		addSeg(render.Battery(pctInt) + " " + pctStr + labelStyle.Render(" ctx"))
	}

	// The model badge and ctx% segment are the two segments the trimming
	// loop below never drops (minKeep=2 further down), so a long Label
	// (e.g. "opus-4.8") could make the always-kept pair alone overflow a
	// narrow COLUMNS. Fall back to the short family badge in that case only.
	if modelIdx >= 0 && ctxIdx >= 0 {
		sepW := lipgloss.Width(sepStr)
		if segments[modelIdx].w+sepW+segments[ctxIdx].w > cols {
			shortBadge := labelStyle.Render(render.AbbreviateModelFamily(modelName))
			segments[modelIdx] = segment{text: shortBadge, w: lipgloss.Width(shortBadge)}
		}
	}

	// 3. Tokens used/window. "used" is the SAME input-only numerator
	// UsedPercentage() reads (input + cache_creation + cache_read), so this
	// segment can never disagree with the ctx% segment above. Mirrors
	// persistWindowState's ContextWindowSize/ContextTokens exactly.
	if cw := p.ContextWindow; cw != nil {
		if size, ok := coerceNonNegative(cw.ContextWindowSize); ok && size > 0 {
			if cu := cw.CurrentUsage; cu != nil {
				if input, ok := coerceNonNegative(cu.InputTokens); ok {
					cacheCreate, _ := coerceNonNegative(cu.CacheCreationInputTokens)
					cacheRead, _ := coerceNonNegative(cu.CacheReadInputTokens)
					used := input + cacheCreate + cacheRead
					addSeg(mutedStyle.Render(render.TokensK(int(used)) + "/" + render.TokensK(int(size))))
				}
			}
		}
	}

	// 4. Last-turn tokens ("turn: 81k↑ 1k↓") -- from state.json, not the
	// payload; the payload has no per-turn breakdown. Ported verbatim
	// (thresholds, glyphs, and label color) from the deleted
	// chrome_bar.go renderMetricsLine step 4, restoring a segment that
	// the rich statusline would otherwise lack.
	if snap != nil && (snap.LastTurnInput > 0 || snap.LastTurnOutput > 0) {
		turnLabelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(th.TokenInput))
		inThresholds := []render.Threshold{
			{Cutoff: 50_000, Color: th.TokenInput},
			{Cutoff: 200_000, Color: th.Yellow},
			{Cutoff: 999_999_999, Color: th.Red},
		}
		outThresholds := []render.Threshold{
			{Cutoff: 5_000, Color: th.Subtle},
			{Cutoff: 20_000, Color: th.Yellow},
			{Cutoff: 999_999_999, Color: th.Red},
		}
		ltIn := render.ThresholdStyle(snap.LastTurnInput, inThresholds).Render(render.TokensK(snap.LastTurnInput) + "↑")
		ltOut := render.ThresholdStyle(snap.LastTurnOutput, outThresholds).Render(render.TokensK(snap.LastTurnOutput) + "↓")
		addSeg(turnLabelStyle.Render("turn: ") + ltIn + " " + ltOut)
	}

	// 5. Cache hit rate -- two-source cascade. Prefer the payload's
	// prompt_cache.hit_ratio (a FRACTION in [0, 1], not a percentage) when
	// present; this is the value Claude Code itself computes and it's
	// authoritative when it exists. Fall back to the cumulative counters
	// already tracked in state.AgentState -- cache_read_tokens*100/
	// input_tokens, integer division, exactly as the deleted
	// chrome_bar.go renderMetricsLine step 5 computed it -- for the
	// common case where PromptCache is nil in the real Claude Code
	// payload. Either path is omitted entirely when the resulting
	// percentage is 0: that reads as "no data yet", not "a real 0%
	// hit rate", matching the chrome bar's deliberate `if cachePct > 0`
	// guard.
	cachePct, haveCachePct := 0, false
	if p.PromptCache != nil {
		if ratio, ok := coerceNonNegative(p.PromptCache.HitRatio); ok {
			cachePct = int(math.Round(ratio * 100))
			haveCachePct = true
		}
	}
	if !haveCachePct && snap != nil && snap.InputTokens > 0 {
		cachePct = snap.CacheReadTokens * 100 / snap.InputTokens
		haveCachePct = true
	}
	if haveCachePct && cachePct > 0 {
		thresholds := []render.Threshold{
			{Cutoff: 29, Color: th.CacheBad},
			{Cutoff: 59, Color: th.CacheWarn},
			{Cutoff: 100, Color: th.CacheGood},
		}
		pctStr := render.ThresholdStyle(cachePct, thresholds).Render(fmt.Sprintf("%d%%", cachePct))
		addSeg(pctStr + labelStyle.Render(" cached"))
	}

	// 6. Turns -- from state.json, not the payload.
	if snap != nil && snap.TurnCount > 0 {
		thresholds := []render.Threshold{
			{Cutoff: 0, Color: th.Muted},
			{Cutoff: 20, Color: th.TurnNormal},
			{Cutoff: 50, Color: th.TurnWarn},
			{Cutoff: 999999, Color: th.TurnHot},
		}
		turnStr := render.ThresholdStyle(snap.TurnCount, thresholds).Render(fmt.Sprintf("%d", snap.TurnCount))
		addSeg(turnStr + labelStyle.Render(" turns"))
	}

	// 7. Cumulative tokens ("total: 789k↑ 6k↓") -- from state.json, not the
	// payload; cumulative since session start. Ported from the deleted
	// chrome_bar.go renderMetricsLine step 7, with one deliberate
	// deviation: the chrome bar rendered a "0↑ 0↓" placeholder when both
	// counters were zero because it owned a fixed-width pane and needed
	// the placeholder to hold layout. This surface owns no such fixed
	// width, so an all-zero total is skipped entirely rather than shown
	// as noise.
	if snap != nil {
		total := snap.InputTokens + snap.OutputTokens
		if total > 0 {
			totalLabelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(th.TokenInput))
			inThresholds := []render.Threshold{
				{Cutoff: 100_000, Color: th.TokenInput},
				{Cutoff: 500_000, Color: th.Yellow},
				{Cutoff: 999_999_999, Color: th.Red},
			}
			outThresholds := []render.Threshold{
				{Cutoff: 50_000, Color: th.Green},
				{Cutoff: 200_000, Color: th.Yellow},
				{Cutoff: 999_999_999, Color: th.Red},
			}
			inStr := render.ThresholdStyle(snap.InputTokens, inThresholds).Render(render.TokensK(snap.InputTokens) + "↑")
			outStr := render.ThresholdStyle(snap.OutputTokens, outThresholds).Render(render.TokensK(snap.OutputTokens) + "↓")
			addSeg(totalLabelStyle.Render("total: ") + inStr + " " + outStr)
		}
	}

	// 8. Cost. Omitted below minDisplayCostUSD -- a fresh/post-/clear session
	// legitimately has $0.00, which is noise, not information. A negative
	// value is clamped to 0 by coerceNonNegative and so is also omitted
	// here, never printed as "$0.00".
	if p.Cost != nil {
		if cost, ok := coerceNonNegative(p.Cost.TotalCostUSD); ok && cost >= minDisplayCostUSD {
			addSeg(valueStyle.Render(fmt.Sprintf("$%.2f", cost)))
		}
	}

	// 9. Duration -- from state.json, not the payload.
	if snap != nil && snap.StartTime > 0 {
		addSeg(valueStyle.Render(render.Duration(time.Since(time.Unix(snap.StartTime, 0)))))
	}

	if len(segments) == 0 {
		return "", false
	}

	const minKeep = 2
	sepW := lipgloss.Width(sepStr)
	for len(segments) > minKeep {
		total := 0
		for i, s := range segments {
			total += s.w
			if i > 0 {
				total += sepW
			}
		}
		if total <= cols {
			break
		}
		segments = segments[:len(segments)-1]
	}

	var sb strings.Builder
	for i, s := range segments {
		if i > 0 {
			sb.WriteString(sepStr)
		}
		sb.WriteString(s.text)
	}
	return sb.String(), true
}

// renderRichLine3 renders the workspace/git row: current directory, branch,
// dirty-tree count, agentctl worktree name, and PR, in that order, dropping
// segments right-to-left (PR first) when the rendered line would exceed
// cols. The directory segment is never dropped (minKeep = 1) -- it is the
// one piece of context every other segment is relative to, and omitting it
// while keeping e.g. just a PR number would be more confusing than showing
// nothing. Returns ok=false when no directory can be resolved at all, so
// the caller omits the line entirely rather than emitting one that's
// nothing but branch/PR noise with no anchor.
//
// Branch resolution (gitBranch) and the dirty count (gitDirtyCount) are
// both best-effort and never block: gitBranch never shells out at all, and
// gitDirtyCount is hard-timeout-bounded (see its doc comment) and degrades
// to omitting that one segment on any error.
func renderRichLine3(p *Payload, cols int) (string, bool) {
	th := theme.Current()
	labelStyle := render.LabelStyle()
	mutedStyle := render.MutedStyle()
	valueStyle := render.ValueStyle()
	sepStyle := render.SepStyle()
	sepStr := sepStyle.Render(" · ")
	dirtyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(th.Yellow))

	type segment struct {
		text string
		w    int
	}
	var segments []segment
	addSeg := func(s string) {
		segments = append(segments, segment{text: s, w: lipgloss.Width(s)})
	}

	// 1. Directory -- mandatory, never dropped. Prefer workspace.current_dir
	// over the top-level cwd (workspace.current_dir tracks `cd` within a
	// session; cwd is fixed at session start).
	dir := ""
	if p.Workspace != nil && p.Workspace.CurrentDir != nil && *p.Workspace.CurrentDir != "" {
		dir = *p.Workspace.CurrentDir
	} else if p.Cwd != nil && *p.Cwd != "" {
		dir = *p.Cwd
	}
	if dir == "" {
		return "", false
	}
	addSeg(valueStyle.Render(formatDir(dir)))

	// 2. Branch (droppable).
	if branch := gitBranch(dir); branch != "" {
		addSeg(labelStyle.Render(string(branchGlyph)) + " " + mutedStyle.Render(branch))
	}

	// 3. Dirty count (droppable). Omitted on any gitDirtyCount error/timeout,
	// and when the tree is clean (n == 0) -- a "+-0" segment would be noise.
	if n, ok := gitDirtyCount(context.Background(), dir); ok && n > 0 {
		addSeg(dirtyStyle.Render(fmt.Sprintf("+-%d", n)))
	}

	// 4. agentctl worktree name (droppable). Bare name, no branch semantics
	// -- see payload.go's NOTE on why this is NOT windowstate.Branch/
	// WorktreeDir and must not be treated as one.
	if p.Workspace != nil && p.Workspace.GitWorktree != nil && *p.Workspace.GitWorktree != "" {
		addSeg(mutedStyle.Render(*p.Workspace.GitWorktree))
	}

	// 5. PR (droppable).
	if p.PR != nil && p.PR.Number != nil {
		prSeg := valueStyle.Render(fmt.Sprintf("PR #%d", int(*p.PR.Number)))
		if p.PR.ReviewState != nil && *p.PR.ReviewState != "" {
			prSeg += " " + mutedStyle.Render(*p.PR.ReviewState)
		}
		addSeg(prSeg)
	}

	const minKeep = 1
	sepW := lipgloss.Width(sepStr)
	for len(segments) > minKeep {
		total := 0
		for i, s := range segments {
			total += s.w
			if i > 0 {
				total += sepW
			}
		}
		if total <= cols {
			break
		}
		segments = segments[:len(segments)-1]
	}

	var sb strings.Builder
	for i, s := range segments {
		if i > 0 {
			sb.WriteString(sepStr)
		}
		sb.WriteString(s.text)
	}
	return sb.String(), true
}

// persistWindowState writes the subset of the payload owned by this ticket
// into the current window's windowstate record via the existing
// locked read-modify-write helper. It deliberately never touches Branch,
// WorktreeDir, State, or Done -- those have other owners. LogPath has one
// narrow self-heal: when the windowstate SessionID equals the payload's
// session_id and LogPath is empty, it is filled from the payload's
// transcript_path (only if that path's basename is <session_id>.jsonl). That
// path comes from the live process of this very window, so it cannot be
// another project's log. A non-empty LogPath is never overwritten --
// the SessionStart hook owns it. windowID is resolved once by the caller (RunWithIO) rather than re-resolved here,
// since "rich" rendering also needs it.
func persistWindowState(p *Payload, windowID string) {
	if windowID == "" {
		logging.Log("statusline: could not resolve window id; skipping windowstate update")
		return
	}

	err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		if pct, ok := p.ContextWindow.UsedPercentage(); ok {
			ws.CtxPct = int(math.Round(pct))
			ws.CtxPctSource = "statusline"
			ws.CtxPushedAt = time.Now()

			// ContextWindowSize/ContextTokens mirror the exact
			// numerator/denominator UsedPercentage used (raw
			// context_window_size, and the same input-only usage total --
			// input + cache_creation + cache_read) so a downstream consumer
			// (the monitor's persistState) that renders "X/Y
			// tokens" never disagrees with CtxPct itself.
			if size, ok := coerceNonNegative(p.ContextWindow.ContextWindowSize); ok && size > 0 {
				ws.ContextWindowSize = int(size)
			}
			if cu := p.ContextWindow.CurrentUsage; cu != nil {
				input, hasInput := coerceNonNegative(cu.InputTokens)
				cacheCreate, _ := coerceNonNegative(cu.CacheCreationInputTokens)
				cacheRead, _ := coerceNonNegative(cu.CacheReadInputTokens)
				if hasInput {
					ws.ContextTokens = int(input + cacheCreate + cacheRead)
				}
			}
		}
		backfillLogPath(ws, p)
		if p.Model != nil && p.Model.ID != nil && *p.Model.ID != "" {
			ws.Model = *p.Model.ID
		}
		if p.Cost != nil {
			if v, ok := coerceNonNegative(p.Cost.TotalCostUSD); ok {
				ws.CostUSD = v
			}
		}
	})
	if err != nil {
		logging.Log("statusline: updating windowstate for %s: %v", windowID, err)
	}
}

// backfillLogPath implements persistWindowState's narrow LogPath self-heal.
func backfillLogPath(ws *windowstate.WindowState, p *Payload) {
	if ws.LogPath != "" || ws.SessionID == "" || p.SessionID == nil || p.TranscriptPath == nil {
		return
	}
	sid, tp := *p.SessionID, *p.TranscriptPath
	if sid == "" || tp == "" || ws.SessionID != sid || filepath.Base(tp) != sid+".jsonl" {
		return
	}
	ws.LogPath = tp
}

// persistRateLimits writes the account-level rate-limit windows present in
// this payload to the shared, atomic ~/.local/state/agentctl/ratelimits.json.
// Absent windows are omitted, never zero-filled. If rate_limits
// is nil, or present but every window fails coercion, the existing file (if
// any) is left untouched rather than clobbered with an empty one.
func persistRateLimits(p *Payload) {
	if p.RateLimits == nil {
		return
	}

	pr := &ratelimit.PushedRateLimits{
		UpdatedAt: time.Now().Unix(),
		Source:    "statusline",
		FiveHour:  pushedWindow(p.RateLimits.FiveHour, coercePercent),
		SevenDay:  pushedWindow(p.RateLimits.SevenDay, coercePercent),
		// spend_limit can legitimately exceed 100 -- must not clamp.
		SpendLimit: pushedWindow(p.RateLimits.SpendLimit, coerceNonNegative),
	}

	if pr.FiveHour == nil && pr.SevenDay == nil && pr.SpendLimit == nil {
		return
	}

	if err := ratelimit.SavePushed(pr); err != nil {
		logging.Log("statusline: saving pushed rate limits: %v", err)
	}
}

// pushedWindow converts one payload RateLimitWindow into the persisted
// ratelimit.PushedWindow shape, using coercePct to read used_percentage
// (coercePercent for five_hour/seven_day, coerceNonNegative for
// spend_limit -- see persistRateLimits). Returns nil if w is nil or either
// field fails coercion, so a malformed window is dropped rather than
// persisted as a plausible-looking zero.
func pushedWindow(w *RateLimitWindow, coercePct func(*float64) (float64, bool)) *ratelimit.PushedWindow {
	if w == nil {
		return nil
	}
	pct, ok := coercePct(w.UsedPercentage)
	if !ok {
		return nil
	}
	resetsAt, ok := coerceNonNegative(w.ResetsAt)
	if !ok {
		return nil
	}
	return &ratelimit.PushedWindow{
		UsedPercentage: pct,
		ResetsAt:       int64(resetsAt),
	}
}
