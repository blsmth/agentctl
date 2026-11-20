package tui

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// logRec is one parsed agentctl runtime log line. Records are kept (not
// pre-rendered strings) so the panes can re-wrap on resize and toggle the
// source column without re-reading the file.
type logRec struct {
	parsed bool
	raw    string // unparsed fallback text
	day    string // 2026/11/19
	clock  string // 09:16:06
	level  string // INF WRN ERR DBG (or first 3 letters of an unknown level)
	source string // hooks/session.go:170
	comp   string // component prefix (before the first ": ")
	body   string // message plus key=value pairs
	count  int    // consecutive identical records collapsed into this one
}

// runtimeLineRE matches "2026/11/19 09:16:06.314 INFO <hooks/session.go:170> rest".
var runtimeLineRE = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2}) (\d{2}:\d{2}:\d{2})(?:\.\d+)? ([A-Za-z]+) (?:<([^<>]*)> )?(.*)$`)

var compRE = regexp.MustCompile(`^[A-Za-z0-9_.@/\-]+$`)

var kvKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.\-]*=`)

// levelBadge maps a log level field to its fixed-width badge.
func levelBadge(level string) string {
	switch strings.ToUpper(level) {
	case "INFO", "INF":
		return "INF"
	case "WARN", "WARNING", "WRN":
		return "WRN"
	case "ERROR", "ERR", "FATAL", "PANIC":
		return "ERR"
	case "DEBUG", "DBG", "TRACE":
		return "DBG"
	}
	r := strings.ToUpper(level)
	if len(r) > 3 {
		r = r[:3]
	}
	return r
}

// parseRuntimeLine parses one runtime log line. Lines in any other format
// come back as an unparsed record that renders as plain text.
func parseRuntimeLine(line string) logRec {
	m := runtimeLineRE.FindStringSubmatch(line)
	if m == nil {
		return logRec{raw: line, count: 1}
	}
	rec := logRec{
		parsed: true,
		day:    m[1],
		clock:  m[2],
		level:  levelBadge(m[3]),
		source: m[4],
		body:   m[5],
		count:  1,
	}
	if i := strings.Index(rec.body, ": "); i > 0 && compRE.MatchString(rec.body[:i]) {
		rec.comp = rec.body[:i]
		rec.body = rec.body[i+2:]
	}
	return rec
}

// sameMessage reports whether b repeats a (ignoring time).
func sameMessage(a, b logRec) bool {
	return a.parsed && b.parsed && a.day == b.day && a.level == b.level &&
		a.comp == b.comp && a.body == b.body
}

// appendLogRecs parses raw lines onto recs, collapsing runs of identical
// messages, and caps the result at maxLogLines records. It returns the new
// slice and how many records were trimmed from the front.
func appendLogRecs(recs []logRec, raw []string) ([]logRec, int) {
	for _, line := range raw {
		rec := parseRuntimeLine(line)
		if n := len(recs); n > 0 && sameMessage(recs[n-1], rec) {
			recs[n-1].count++
			recs[n-1].clock = rec.clock // show the latest occurrence
			continue
		}
		recs = append(recs, rec)
	}
	trim := 0
	if len(recs) > maxLogLines {
		trim = len(recs) - maxLogLines
		recs = append([]logRec(nil), recs[trim:]...)
	}
	return recs, trim
}

// hudMaxToken caps a single key=value value (session IDs, transcript paths).
const hudMaxToken = 48

// shortenHome replaces the home directory prefix of any path in s with ~.
func shortenHome(s string) string {
	home, _ := os.UserHomeDir()
	if home == "" || home == "/" {
		return s
	}
	return strings.ReplaceAll(s, home+"/", "~/")
}

// ellipsizeMiddle cuts s to at most width cells, keeping both ends.
func ellipsizeMiddle(s string, width int) string {
	if width < 1 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	if width < 4 {
		return ansi.Truncate(s, width, "")
	}
	head := (width - 1) / 2
	tail := width - 1 - head
	return ansi.Truncate(s, head, "") + "…" + ansi.TruncateLeft(s, ansi.StringWidth(s)-tail, "")
}

// logToken is one unbreakable piece of a message, already styled.
type logToken struct {
	text string
	w    int
}

// splitBody splits a message into whitespace tokens, keeping a quoted
// key="a b" value together.
func splitBody(body string) []string {
	fields := strings.Fields(body)
	var out []string
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if kvKeyRE.MatchString(f) {
			v := f[strings.IndexByte(f, '=')+1:]
			if strings.HasPrefix(v, `"`) && !(len(v) > 1 && strings.HasSuffix(v, `"`)) {
				for i+1 < len(fields) {
					i++
					f += " " + fields[i]
					if strings.HasSuffix(fields[i], `"`) {
						break
					}
				}
			}
		}
		out = append(out, f)
	}
	return out
}

// bodyTokens styles a message into tokens no wider than avail.
func bodyTokens(body string, avail int) []logToken {
	var toks []logToken
	for _, f := range splitBody(shortenHome(body)) {
		if kvKeyRE.MatchString(f) {
			eq := strings.IndexByte(f, '=')
			key, val := f[:eq+1], f[eq+1:]
			room := min(hudMaxToken, avail-ansi.StringWidth(key))
			if room < 4 {
				key = ellipsizeMiddle(key, avail)
				val = ""
			} else {
				val = ellipsizeMiddle(val, room)
			}
			toks = append(toks, logToken{
				text: hudLogDimStyle.Render(key) + hudRuntimeDefaultStyle.Render(val),
				w:    ansi.StringWidth(key) + ansi.StringWidth(val),
			})
			continue
		}
		f = ellipsizeMiddle(f, avail)
		toks = append(toks, logToken{text: hudRuntimeDefaultStyle.Render(f), w: ansi.StringWidth(f)})
	}
	return toks
}

const hudLogClockW = 8

// renderLogRec renders rec into display lines no wider than width, with a
// hanging indent under the message column.
func renderLogRec(rec logRec, width int, showSource bool) []string {
	lines, _ := renderLogRecMeta(rec, width, showSource)
	return lines
}

// renderLogRecMeta is renderLogRec plus each line's wrap metadata: every line
// after the first continues the record (a word wrap, so it rejoins with a
// space), under its hanging indent.
func renderLogRecMeta(rec logRec, width int, showSource bool) ([]string, []hudLineMeta) {
	width = max(width, 20)
	if !rec.parsed {
		return renderPlainLogMeta(rec.raw, width)
	}

	prefixW := hudLogClockW + 1 + 3 + 1
	prefix := hudTimeStyle.Render(rec.clock) + " " + levelStyle(rec.level).Render(rec.level) + " "
	if showSource && rec.source != "" {
		src := ellipsizeMiddle(rec.source, 28)
		prefix += hudLogDimStyle.Render(src) + " "
		prefixW += ansi.StringWidth(src) + 1
	}

	avail := width - 1 - prefixW
	var lines []string
	var meta []hudLineMeta
	indent := strings.Repeat(" ", prefixW)
	if avail < 16 {
		// Too narrow for a hanging indent: prefix on its own line, message
		// below with a small indent.
		lines = append(lines, " "+prefix)
		meta = append(meta, hudLineMeta{indent: 1})
		prefix, indent = "", "  "
		prefixW = 2
		avail = width - 1 - prefixW
	}
	first := prefix
	if first == "" {
		first = indent
	}

	var toks []logToken
	if rec.comp != "" {
		c := ellipsizeMiddle(rec.comp, avail-1) + ":"
		toks = append(toks, logToken{text: hudLogCompStyle.Render(c), w: ansi.StringWidth(c)})
	}
	toks = append(toks, bodyTokens(rec.body, avail)...)
	if rec.count > 1 {
		s := "×" + strconv.Itoa(rec.count)
		toks = append(toks, logToken{text: hudLogDimStyle.Render(s), w: ansi.StringWidth(s)})
	}

	cur, curW := first, 0
	started := false
	push := func(line string) {
		lm := hudLineMeta{indent: 1} // the pane margin
		if len(lines) > 0 {
			lm = hudLineMeta{join: joinSpace, indent: 1 + len(indent)}
		}
		lines = append(lines, line)
		meta = append(meta, lm)
	}
	flush := func() {
		push(" " + cur)
		cur, curW, started = indent, 0, false
	}
	for _, t := range toks {
		need := t.w
		if started {
			need++
		}
		if started && curW+need > avail {
			flush()
		}
		if started {
			cur += " "
			curW++
		}
		cur += t.text
		curW += t.w
		started = true
	}
	if started || prefix != "" {
		push(" " + cur)
	}
	return lines, meta
}

// renderPlainLogMeta wraps an unparsed line to width with no styling beyond
// the normal text color. Rows after the first are hard-wrap continuations.
func renderPlainLogMeta(raw string, width int) ([]string, []hudLineMeta) {
	raw = strings.ReplaceAll(raw, "\t", "    ")
	if raw == "" {
		return []string{""}, []hudLineMeta{{indent: 1}}
	}
	var out []string
	var meta []hudLineMeta
	for i, l := range strings.Split(ansi.Hardwrap(raw, width-1, true), "\n") {
		out = append(out, " "+hudRuntimeDefaultStyle.Render(l))
		lm := hudLineMeta{indent: 1}
		if i > 0 {
			lm.join = joinHard
		}
		meta = append(meta, lm)
	}
	return out, meta
}

func levelStyle(level string) lipgloss.Style {
	switch level {
	case "ERR":
		return hudLogErrStyle
	case "WRN":
		return hudLogWarnStyle
	case "DBG":
		return hudLogDimStyle
	case "INF":
		return hudLogInfoStyle
	}
	return hudRuntimeDefaultStyle
}

// renderLogRecs renders every record, inserting a dim date divider before the
// first record of each day. starts[i] is the first display line of recs[i]
// (including its divider).
func renderLogRecs(recs []logRec, width int, showSource bool) (lines []string, starts []int) {
	lines, starts, _ = renderLogRecsMeta(recs, width, showSource)
	return lines, starts
}

// renderLogRecsMeta is renderLogRecs plus the wrap metadata of every line.
func renderLogRecsMeta(recs []logRec, width int, showSource bool) (lines []string, starts []int, meta []hudLineMeta) {
	starts = make([]int, len(recs))
	lastDay := ""
	for i, rec := range recs {
		starts[i] = len(lines)
		if rec.parsed && rec.day != lastDay {
			lastDay = rec.day
			lines = append(lines, " "+hudLogDimStyle.Render("── "+rec.day+" ──"))
			meta = append(meta, hudLineMeta{indent: 1})
		}
		rl, rm := renderLogRecMeta(rec, width, showSource)
		lines = append(lines, rl...)
		meta = append(meta, rm...)
	}
	return lines, starts, meta
}
