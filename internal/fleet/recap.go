package fleet

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/tmux"
)

// RecapSource says which signal a RecapLine came from, strongest first.
type RecapSource int

const (
	RecapNone     RecapSource = iota // nothing to show
	RecapSummary                     // Claude Code's away_summary recap
	RecapTask                        // the dispatched ticket or ad-hoc task line
	RecapTitle                       // Claude Code's ai-title
	RecapActivity                    // last tool detail / assistant text
)

// String is the wire name of the source: "summary", "task", "title" or
// "activity"; "" for RecapNone.
func (s RecapSource) String() string {
	switch s {
	case RecapSummary:
		return "summary"
	case RecapTask:
		return "task"
	case RecapTitle:
		return "title"
	case RecapActivity:
		return "activity"
	}
	return ""
}

// RecapLine is the one-line "what is this agent working on" for a row.
// Title, when set, is a short generated title for Text (see TagLogs); Text
// stays the full recap for detail views.
type RecapLine struct {
	Text   string
	Source RecapSource
	Title  string
	// Kind is the work-kind tag of Title ("go", "review", ...), empty when
	// there is none. Only set together with Title.
	Kind string
}

// Short is what a list row shows: Title when there is one, else the first
// sentence of Text, so an untitled row leads with what the recap says rather
// than a paragraph cut off at the edge. Fit it to a width with TruncateWords.
func (l RecapLine) Short() string {
	if l.Title != "" {
		return l.Title
	}
	return FirstSentence(l.Text)
}

// FirstSentence is the first sentence of s with whitespace collapsed and its
// closing punctuation dropped: everything up to the first '.', '!' or '?'
// that ends s or is followed by a space. A URL's dots do not end a sentence.
// s with no sentence end is returned whole.
func FirstSentence(s string) string {
	s = flattenWhitespace(s)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.', '!', '?':
			if i+1 == len(s) || s[i+1] == ' ' {
				if out := strings.TrimRight(s[:i+1], ".!?"); out != "" {
					return out
				}
			}
		}
	}
	return s
}

// TruncateWords fits s into width terminal cells, cutting at a word boundary
// and marking the cut with "…". Only a single word wider than width (a long
// URL) is cut inside the word. Trailing punctuation before the "…" is
// dropped.
func TruncateWords(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	const tail = "…"
	head := ansi.Truncate(s, width-ansi.StringWidth(tail), "")
	// head ends at a word boundary when the next byte of s is a space.
	if len(head) < len(s) && s[len(head)] != ' ' {
		if i := strings.LastIndexByte(head, ' '); i > 0 {
			head = head[:i]
		}
	}
	head = strings.TrimRight(head, " ,;:-–—")
	return head + tail
}

// titleSource reports whether a row with recap l shows the generated short
// title: an away_summary, an activity line (what a bare window shows until
// its first recap, so its title from the first prompt is visible at once) or
// an ad-hoc task line. Ticket titles ("#N title") and AI titles are already
// short and stay as they are.
func titleSource(r Row, l RecapLine) bool {
	switch l.Source {
	case RecapSummary, RecapActivity:
		return true
	case RecapTask:
		return flattenWhitespace(r.TicketTitle) == ""
	}
	return false
}

// Recap picks the row's recap line, first non-empty wins: the newest
// away_summary from the log, then the task (ticket title, prefixed "#N ", or
// the ad-hoc task line recorded at dispatch), then the log's AI title, then
// ActivityLine as the weakest signal.
func Recap(r Row, info LogInfo) RecapLine {
	if info.Recap != "" {
		return RecapLine{Text: info.Recap, Source: RecapSummary}
	}
	if t := TaskText(r); t != "" {
		return RecapLine{Text: t, Source: RecapTask}
	}
	if info.AITitle != "" {
		return RecapLine{Text: info.AITitle, Source: RecapTitle}
	}
	if a := ActivityLine(r); a != "" {
		return RecapLine{Text: a, Source: RecapActivity}
	}
	return RecapLine{}
}

// TaskText is the row's task: "#N title" for a ticket dispatch, else the
// ad-hoc task line, else "".
func TaskText(r Row) string {
	if title := flattenWhitespace(r.TicketTitle); title != "" {
		if n := strings.TrimPrefix(strings.TrimSpace(r.TicketRef), "#"); n != "" {
			return "#" + n + " " + title
		}
		if r.Ticket > 0 {
			return fmt.Sprintf("#%d %s", r.Ticket, title)
		}
		return title
	}
	return flattenWhitespace(r.Task)
}

// TagMeta sets the windowstate-derived row fields in place from per-window
// launch metadata: Dispatched, Parent and StateSince. A window with no record
// in meta counts as bare with no parent and an unknown state age.
func TagMeta(rows []Row, meta map[string]Meta) {
	for i := range rows {
		m := meta[rows[i].WindowID]
		rows[i].Dispatched = m.Dispatched()
		rows[i].Detached = m.Detached()
		rows[i].Parent = m.Parent()
		rows[i].StateSince = m.stateSince(rows[i].State)
	}
}

// stateSince is when the window entered state: BlockedSince for blocked
// (falling back to StateSince), StateSince otherwise. Zero when unknown.
func (m Meta) stateSince(state string) time.Time {
	if state == "blocked" && !m.BlockedSince.IsZero() {
		return m.BlockedSince
	}
	return m.StateSince
}

// TagLogs fills each row's Recap and PR link from its session log (see
// ReadLogInfo; cheap on an unchanged log), and the recap's short Title from
// the window's generated title (windowstate, written by hook-title) when the
// recap is one that is worth shortening (see titleSource).
//
// A human title (ShortManual) is the row's title whatever the recap source.
func TagLogs(rows []Row) {
	for i := range rows {
		info := ReadLogInfo(rows[i].LogPath)
		rows[i].PRNumber, rows[i].PRURL = info.PRNumber, info.PRURL
		tagRecap(&rows[i], info)
	}
}

// tagRecap sets r.Recap from info and r's short title (see TagLogs).
func tagRecap(r *Row, info LogInfo) {
	r.Recap = Recap(*r, info)
	applyTitle(r)
}

// applyTitle adds r's short title to its recap line (r.Recap as Recap
// derived it): a window with nothing else to show (a bare one just after its
// first prompt) shows the title as the recap, and a recap worth shortening,
// or any recap when the title is a human one, gets it as its Title.
func applyTitle(r *Row) {
	if r.Recap.Source == RecapNone && r.ShortTitle != "" {
		r.Recap = RecapLine{Text: r.ShortTitle, Source: RecapTitle, Kind: r.ShortKind}
	}
	if r.ShortTitle != "" && (titleSource(*r, r.Recap) || r.ShortManual) {
		r.Recap.Title = r.ShortTitle
		r.Recap.Kind = r.ShortKind
	}
}

// Retitle sets r's short title to a human title or clears it (title "") and
// updates its recap line as TagLogs would, so a caller that just wrote the
// title shows it without a reload. No I/O: the recap text already on r is
// kept. Kind is kept.
func Retitle(r *Row, title string) {
	if r.ShortTitle != "" && r.Recap.Source == RecapTitle && r.Recap.Text == r.ShortTitle {
		r.Recap = RecapLine{} // the old title stood in for a missing recap
	}
	r.ShortTitle, r.ShortManual = title, title != ""
	r.Recap.Title, r.Recap.Kind = "", ""
	applyTitle(r)
}

// BaseName is the row's agent name without its instance suffix: AgentName
// when recorded, else Name with a trailing ":N" or "~N" removed.
func BaseName(r Row) string {
	if r.AgentName != "" {
		return r.AgentName
	}
	return StripInstanceSuffix(r.Name)
}

// StripInstanceSuffix removes a trailing ":N" or "~N" (N all digits) from an
// instance name. Other names are returned unchanged.
func StripInstanceSuffix(name string) string {
	i := strings.LastIndexAny(name, ":~")
	if i <= 0 || i == len(name)-1 {
		return name
	}
	for _, c := range name[i+1:] {
		if c < '0' || c > '9' {
			return name
		}
	}
	return name[:i]
}

// CompactAge formats d as a short age: "45s", "4m", "2h", "3d". Negative
// durations count as zero.
func CompactAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		if d < 0 {
			d = 0
		}
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// DisplayName is the identifier humans see for a window: its instance name
// ("code-reviewer:3", "brandon~2") without any state glyph, else the agent
// name, else the raw window ID. Pass what is recorded (windowstate Instance,
// not a live tmux lookup) so a window that is already gone still reads right.
func DisplayName(instance, agent, windowID string) string {
	for _, s := range []string{instance, agent} {
		if s = strings.TrimSpace(tmux.StripAgentPrefix(s)); s != "" {
			return s
		}
	}
	return windowID
}

// RowDisplayName is DisplayName for a row.
func RowDisplayName(r Row) string {
	return DisplayName(r.Name, r.AgentName, r.WindowID)
}
