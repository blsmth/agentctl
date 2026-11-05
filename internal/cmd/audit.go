package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/toolinput"
)

// ParseAuditSince parses the --since flag for `agentctl audit`. It accepts
// either an RFC3339 timestamp (e.g. "2026-08-21T10:00:00Z") or a Go duration
// string (e.g. "1h", "30m"), interpreted as "since now minus that duration".
// An empty string means no filter (the zero time.Time).
func ParseAuditSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid --since value %q: want an RFC3339 timestamp or a duration like \"1h\", \"30m\"", s)
}

// Fixed column widths for the audit table, matching the dash-repeat
// separator rows below the header. DETAIL is the only elastic column --
// everything else absorbs the terminal width computation in
// detailColumnWidth.
//
// These constants are only honest because every cell that goes into a fixed
// column is forced to exactly this many display columns (padded or
// truncated) by fixedWidth before it's printed -- an un-enforced constant
// here is purely decorative (see fixedWidth's and writeAuditRow's doc
// comments for what "decorative" broke in practice).
const (
	// auditColTS is sized for auditTSFormat ("01-02 15:04:05" = 14 display
	// columns), not RFC3339Nano (32 columns) -- see formatAuditTS. The full
	// RFC3339Nano value is unaffected and still returned by --json.
	auditColTS      = 14
	auditColProject = 14
	// auditColAgent is sized to fit this repo's longest real agent name
	// ("terraform-engineer", 18 chars) -- see internal/cmd/tables.go's
	// PrintRecentSessions, which separates AGENT at the same 18 for the same
	// reason. Agent names are user-defined and technically unbounded; a name
	// longer than this still truncates (fixedWidth), --json is unaffected.
	auditColAgent  = 18
	auditColWindow = 6
	// auditColTool is sized to fit Claude Code's longest common built-in tool
	// name ("NotebookEdit", 12 chars). Custom MCP tool names
	// ("mcp__server__tool") can still exceed this and truncate -- there is no
	// constant that avoids that for an unbounded name.
	auditColTool = 12
	auditColDur  = 6
	// auditColMode is sized to fit Claude Code's longest known permission
	// mode ("bypassPermissions", 17 chars) -- unlike AGENT/TOOL this is a
	// closed set (default/acceptEdits/bypassPermissions/plan), so 17 never
	// truncates a real value.
	auditColMode = 17
	// auditColOK is sized for "DENIED" (6 chars), the longest of "ok" / "FAIL"
	// / "DENIED".
	auditColOK  = 6
	auditColPad = 2 // literal gap between columns -- see writeAuditRow

	// minAuditDetailWidth is the floor detailColumnWidth will not shrink
	// below, even against a very narrow terminal -- a DETAIL column narrower
	// than this is closer to useless than legible.
	minAuditDetailWidth = 20

	// fallbackAuditDetailWidth is the DETAIL width used for --full (no
	// truncation applied at all, so this only bounds the initial
	// toolinput.Summarize call, not the printed result).
	fallbackAuditDetailWidth = 160

	// fallbackTerminalWidth stands in for terminalWidth() when it can't
	// determine an actual width (e.g. stdout piped to a file with no
	// $COLUMNS set) -- a reasonably wide terminal, not the narrowest
	// plausible one, since truncating too aggressively defeats the point of
	// this column more than an occasional wrapped line would.
	fallbackTerminalWidth = 220
)

// detailColumnWidth returns how many runes the DETAIL column may use,
// given the other fixed-width columns already occupying the row.
// allProjects adds the PROJECT column's width to the fixed budget.
func detailColumnWidth(allProjects bool) int {
	numCols := 8 // TS AGENT WINDOW TOOL DUR MODE OK DETAIL
	fixed := auditColTS + auditColAgent + auditColWindow + auditColTool + auditColDur + auditColMode + auditColOK
	if allProjects {
		numCols++
		fixed += auditColProject
	}
	gaps := (numCols - 1) * auditColPad

	width := terminalWidth()
	if width <= 0 {
		width = fallbackTerminalWidth
	}

	detail := width - fixed - gaps
	if detail < minAuditDetailWidth {
		detail = minAuditDetailWidth
	}
	return detail
}

// terminalWidth returns stdout's terminal width when stdout is a TTY, falls
// back to $COLUMNS when that's set (e.g. under tmux without a real tty on
// stdout), and otherwise returns 0 to signal "unknown" to callers.
func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	if v := strings.TrimSpace(os.Getenv("COLUMNS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// auditTSFormat is the short local-time layout rendered in the table's TS
// column. RFC3339Nano's sub-second precision isn't scannable by eye and, at
// 32 display columns, would consume a sixth of even a 200-column terminal.
// --json is unaffected by this -- it keeps printing the full RFC3339Nano
// value from audit.Record.Timestamp unchanged.
const auditTSFormat = "01-02 15:04:05"

// formatAuditTS renders an audit.Record.Timestamp (RFC3339Nano) in the
// table's short local form. Falls back to the raw string (which fixedWidth
// will still truncate/pad like any other cell) if it fails to parse, so a
// malformed timestamp degrades the column rather than the row.
func formatAuditTS(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format(auditTSFormat)
}

// fixedWidth forces s to exactly width display columns: truncated (via
// truncateToWidth) when longer, space-padded when shorter. Width is measured
// with lipgloss.Width, not len() or a rune count -- cells here may already
// carry ANSI styling (ansi escapes are zero-width) or non-ASCII text (a rune
// can be wider than one display column), and either mismeasures.
//
// Every cell in a fixed-width audit table column must go through this before
// it's printed (writeAuditRow just joins pre-sized cells; nothing downstream
// pads). Skipping it for even one cell in a column means that column's real
// width no longer matches its auditCol* constant, and the header, separator,
// and data rows go back to rendering at three different widths -- that's
// how the table ended up misaligned before.
func fixedWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if w := lipgloss.Width(s); w > width {
		s = truncateToWidth(s, width)
	}
	if w := lipgloss.Width(s); w < width {
		s += strings.Repeat(" ", width-w)
	}
	return s
}

// truncateToWidth is truncateDisplay's lipgloss.Width-aware counterpart:
// truncateDisplay counts runes, which mismeasures a cell that carries ANSI
// styling or double-width (e.g. CJK) runes. Walks rune-by-rune, summing
// display width, so truncation lands on the right boundary either way.
func truncateToWidth(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= 1 {
		return truncateDisplay(s, width)
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > width-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	b.WriteRune('…')
	return b.String()
}

// writeAuditRow joins already width-fixed cells with a literal auditColPad
// gap and writes one line.
//
// This deliberately does not use text/tabwriter. Every cell reaching this
// function has already been forced to its declared display width by
// fixedWidth *before* any style was applied, so tabwriter's own per-column
// width computation is not just unnecessary here, it's actively wrong: it
// sizes a column from each cell's raw rune count, which includes ANSI escape
// sequences, and the header row's style (bold) carries a longer escape
// sequence than the muted style data/separator rows use. That mismatch made
// tabwriter believe the header cell was wider than the (equally
// fixedWidth-padded) data cell in the same column, so it padded the *data*
// cell with extra real space characters to compensate -- producing a visibly
// wider data row than header row despite both cells reporting the same
// lipgloss.Width. See printModelsTable's doc comment in models.go for the
// same class of bug found earlier in this codebase. Joining pre-padded cells
// with a fixed literal gap sidesteps the whole class: there is no
// column-width inference left to get wrong.
func writeAuditRow(w io.Writer, cells ...string) {
	fmt.Fprintln(w, strings.Join(cells, strings.Repeat(" ", auditColPad)))
}

// collapseWhitespace collapses every run of whitespace (including embedded
// newlines and tabs) in s to a single space. A cell containing a raw
// newline -- a multi-line Bash stdout snippet is the common case -- would
// otherwise split what's meant to be one printed row into several physical
// lines, breaking both the visual table and the "every line is the same
// width" property writeAuditRow otherwise guarantees.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// auditDetail builds the DETAIL column's contents for rec: the tool INPUT
// summary (the Bash command that ran, the file an Edit touched, etc.) when
// available, falling back to rec.Summary -- which describes the tool's
// OUTPUT -- only when Summarize has nothing to say (unknown tool, malformed
// input, or a tool with no useful input field).
//
// rec.Tool is matched against both "Task" and "Agent" inside
// toolinput.Summarize -- see its doc comment -- so historical records
// written when Claude Code called this tool "Task", and current records
// where it's called "Agent", both render the subagent + description instead
// of a bare "<tool> completed".
//
// For an EventToolDenied record, rec.DeniedBy ("rule" or "human") is folded
// into the front of the cell -- "denied by rule: <detail>" / "denied by
// human: <detail>" -- rather than getting its own column, since it's empty on
// the overwhelming majority of records and a whole column for it would be
// almost entirely blank. The rule/human distinction matters: a policy block
// and a human refusal mean very different things to a reviewer.
func auditDetail(rec audit.Record, maxLen int) string {
	detail := toolinput.Summarize(rec.Tool, rec.Input, maxLen)
	if detail == "" {
		detail = rec.Summary
	}
	detail = collapseWhitespace(detail)
	if rec.Event == audit.EventToolDenied && rec.DeniedBy != "" {
		detail = fmt.Sprintf("denied by %s: %s", rec.DeniedBy, detail)
	}
	return detail
}

// auditStatusWord returns the OK column's plain-text word, before styling or
// width-padding: a distinct "DENIED" for a record whose Event is
// EventToolDenied -- a policy or human block that never ran is not the same
// thing as a tool that ran and failed -- and the existing "ok" / "FAIL"
// otherwise, unchanged.
func auditStatusWord(rec audit.Record) string {
	if rec.Event == audit.EventToolDenied {
		return "DENIED"
	}
	if !rec.OK {
		return "FAIL"
	}
	return "ok"
}

// auditStatusCell renders the OK column: auditStatusWord's word, padded to
// auditColOK (fixedWidth, applied before styling so the pad spaces don't
// count against the declared width) and colored yellow/orange for DENIED,
// red for FAIL, green for ok.
func auditStatusCell(rec audit.Record, green, red, denied lipgloss.Style) string {
	word := fixedWidth(auditStatusWord(rec), auditColOK)
	switch {
	case rec.Event == audit.EventToolDenied:
		return denied.Render(word)
	case !rec.OK:
		return red.Render(word)
	default:
		return green.Render(word)
	}
}

// auditModeCell renders Record.PermissionMode for the MODE column: blank for
// the (overwhelmingly common) "default" mode or when absent, so the column
// stays visually quiet and only draws the eye for a non-default mode like
// "acceptEdits" -- exactly what a security reviewer wants to scan for.
func auditModeCell(mode string) string {
	if mode == "" || mode == "default" {
		return ""
	}
	return mode
}

// fmtAuditDuration formats a duration for the DUR column. Durations under a
// second render in milliseconds rather than through fmtDuration's %ds
// (fmtDuration truncates to whole seconds, which renders as "0s" for the
// large majority of tool calls that complete in under a second).
func fmtAuditDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmtDuration(d)
}

// filterByMode returns the subset of records whose PermissionMode matches
// mode exactly. An empty mode imposes no filter. Implemented here rather
// than as an audit.Filter field -- see this file's package doc / the ticket
// this shipped under -- because audit.Filter is owned by a parallel change.
func filterByMode(records []audit.Record, mode string) []audit.Record {
	if mode == "" {
		return records
	}
	out := make([]audit.Record, 0, len(records))
	for _, r := range records {
		if r.PermissionMode == mode {
			out = append(out, r)
		}
	}
	return out
}

// PrintAuditTable queries the hook-sourced audit trail (internal/audit) with
// filter and prints either a text table (ts, agent, window, tool, duration,
// mode, ok, detail -- plus a PROJECT column when allProjects is true) or,
// when jsonOut is true, the matching raw records as JSON. --json IS the
// export path for this command; there is deliberately no separate export
// subcommand.
//
// mode, when non-empty, restricts the table to records whose PermissionMode
// matches exactly (applied whether or not jsonOut is set). full disables
// DETAIL column truncation.
//
// When allProjects is true, every project known to the session registry is
// scanned instead of just the current one -- see audit.AllProjectAuditDirs
// for the "registered sessions only" limitation.
func PrintAuditTable(filter audit.Filter, jsonOut, allProjects, full bool, mode string) error {
	var records []audit.Record
	if allProjects {
		dirs, err := audit.AllProjectAuditDirs()
		if err != nil {
			return fmt.Errorf("enumerating project audit dirs: %w", err)
		}
		records, err = audit.QueryDirs(dirs, filter)
		if err != nil {
			return fmt.Errorf("querying audit log across projects: %w", err)
		}
	} else {
		var err error
		records, err = audit.Query(filter)
		if err != nil {
			return fmt.Errorf("querying audit log: %w", err)
		}
	}

	records = filterByMode(records, mode)

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		for _, r := range records {
			if err := enc.Encode(r); err != nil {
				return fmt.Errorf("encoding audit record: %w", err)
			}
		}
		return nil
	}

	hdr := cliHeaderStyle()
	muted := cliMuted()
	green := cliGreen()
	red := cliStyle(theme.Current().Red)
	denied := cliStyle(theme.Current().Yellow)

	detailWidth := fallbackAuditDetailWidth
	if !full {
		detailWidth = detailColumnWidth(allProjects)
	}

	// detailPrintWidth is the width the DETAIL header and separator render
	// at. In --full mode the printed detail is only capped (via
	// truncateToWidth below), never padded out to detailWidth's much larger
	// ceiling -- there's no terminal budget driving it in that mode, so
	// forcing every row to a fixed 160-column width would itself cause the
	// wrapping this feature exists to prevent. Header/separator keep the
	// same fixed floor they always rendered at in that mode.
	detailPrintWidth := detailWidth
	if full {
		detailPrintWidth = minAuditDetailWidth
	}

	// The PROJECT column is only useful -- and only added -- when scanning
	// more than one project; in single-project mode every row would carry
	// the same value, which is noise, not signal.
	if allProjects {
		writeAuditRow(os.Stdout,
			hdr.Render(fixedWidth("TS", auditColTS)),
			hdr.Render(fixedWidth("PROJECT", auditColProject)),
			hdr.Render(fixedWidth("AGENT", auditColAgent)),
			hdr.Render(fixedWidth("WINDOW", auditColWindow)),
			hdr.Render(fixedWidth("TOOL", auditColTool)),
			hdr.Render(fixedWidth("DUR", auditColDur)),
			hdr.Render(fixedWidth("MODE", auditColMode)),
			hdr.Render(fixedWidth("OK", auditColOK)),
			hdr.Render(fixedWidth("DETAIL", detailPrintWidth)),
		)
		writeAuditRow(os.Stdout,
			muted.Render(strings.Repeat("─", auditColTS)),
			muted.Render(strings.Repeat("─", auditColProject)),
			muted.Render(strings.Repeat("─", auditColAgent)),
			muted.Render(strings.Repeat("─", auditColWindow)),
			muted.Render(strings.Repeat("─", auditColTool)),
			muted.Render(strings.Repeat("─", auditColDur)),
			muted.Render(strings.Repeat("─", auditColMode)),
			muted.Render(strings.Repeat("─", auditColOK)),
			muted.Render(strings.Repeat("─", detailPrintWidth)),
		)
		for _, r := range records {
			okStr := auditStatusCell(r, green, red, denied)
			detail := auditDetail(r, detailWidth)
			if full {
				detail = truncateToWidth(detail, detailWidth)
			} else {
				detail = fixedWidth(detail, detailWidth)
			}
			writeAuditRow(os.Stdout,
				muted.Render(fixedWidth(formatAuditTS(r.Timestamp), auditColTS)),
				fixedWidth(r.Project, auditColProject),
				fixedWidth(r.Agent, auditColAgent),
				muted.Render(fixedWidth(r.Window, auditColWindow)),
				fixedWidth(r.Tool, auditColTool),
				muted.Render(fixedWidth(fmtAuditDuration(time.Duration(r.DurationMS)*time.Millisecond), auditColDur)),
				muted.Render(fixedWidth(auditModeCell(r.PermissionMode), auditColMode)),
				okStr,
				muted.Render(detail),
			)
		}
		return nil
	}

	writeAuditRow(os.Stdout,
		hdr.Render(fixedWidth("TS", auditColTS)),
		hdr.Render(fixedWidth("AGENT", auditColAgent)),
		hdr.Render(fixedWidth("WINDOW", auditColWindow)),
		hdr.Render(fixedWidth("TOOL", auditColTool)),
		hdr.Render(fixedWidth("DUR", auditColDur)),
		hdr.Render(fixedWidth("MODE", auditColMode)),
		hdr.Render(fixedWidth("OK", auditColOK)),
		hdr.Render(fixedWidth("DETAIL", detailPrintWidth)),
	)
	writeAuditRow(os.Stdout,
		muted.Render(strings.Repeat("─", auditColTS)),
		muted.Render(strings.Repeat("─", auditColAgent)),
		muted.Render(strings.Repeat("─", auditColWindow)),
		muted.Render(strings.Repeat("─", auditColTool)),
		muted.Render(strings.Repeat("─", auditColDur)),
		muted.Render(strings.Repeat("─", auditColMode)),
		muted.Render(strings.Repeat("─", auditColOK)),
		muted.Render(strings.Repeat("─", detailPrintWidth)),
	)
	for _, r := range records {
		okStr := auditStatusCell(r, green, red, denied)
		detail := auditDetail(r, detailWidth)
		if full {
			detail = truncateToWidth(detail, detailWidth)
		} else {
			detail = fixedWidth(detail, detailWidth)
		}
		writeAuditRow(os.Stdout,
			muted.Render(fixedWidth(formatAuditTS(r.Timestamp), auditColTS)),
			fixedWidth(r.Agent, auditColAgent),
			muted.Render(fixedWidth(r.Window, auditColWindow)),
			fixedWidth(r.Tool, auditColTool),
			muted.Render(fixedWidth(fmtAuditDuration(time.Duration(r.DurationMS)*time.Millisecond), auditColDur)),
			muted.Render(fixedWidth(auditModeCell(r.PermissionMode), auditColMode)),
			okStr,
			muted.Render(detail),
		)
	}
	return nil
}
