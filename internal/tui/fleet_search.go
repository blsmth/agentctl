package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/fleet"
)

// Hub search: `/` opens a type-to-narrow input. Matching is plain,
// case-insensitive substring terms (AND) over metadata already in memory (no
// transcript reads). Search only hides rows; group and tree order never
// change. See docs/design/fleet-view-design.md.

// fleetSearchKey opens the search input.
const fleetSearchKey = "/"

// fleetSearchMaxRunes caps the query (typed or pasted).
const fleetSearchMaxRunes = 200

// fleetSearchDebounce is how long the viewer waits after the last keystroke
// or move before following the selection.
const fleetSearchDebounce = 150 * time.Millisecond

// fleetSearchTagRunes is the longest query shown in the title tag.
const fleetSearchTagRunes = 16

// fleetSearchHints replace the footer hints while the input is open.
var fleetSearchHints = []fleetHint{
	{"⏎", "keep"},
	{"↑/↓", "move"},
	{"esc", "cancel"},
}

// ── Query ────────────────────────────────────────────────────────────────────

// searchTerm is one whitespace-separated term of a query.
type searchTerm struct {
	text  string // lower-cased substring (plain terms)
	num   int    // ticket or PR number (isNum terms)
	isNum bool
}

// parsedQuery is a query split into AND-ed terms.
type parsedQuery struct {
	terms []searchTerm
}

func (q parsedQuery) empty() bool { return len(q.terms) == 0 }

// parseQuery splits s on whitespace. A `#123` term matches a ticket or PR
// number exactly; any other `#...` is plain text.
func parseQuery(s string) parsedQuery {
	var q parsedQuery
	for _, f := range strings.Fields(s) {
		if rest, ok := strings.CutPrefix(f, "#"); ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
			if n, err := strconv.Atoi(rest); err == nil {
				q.terms = append(q.terms, searchTerm{num: n, isNum: true})
				continue
			}
		}
		q.terms = append(q.terms, searchTerm{text: strings.ToLower(f)})
	}
	return q
}

// searchMatch reports whether every term of q matches: plain terms as a
// case-insensitive substring of haystack, `#N` terms as exactly one of nums.
// An empty query matches everything.
func searchMatch(q parsedQuery, haystack string, nums ...int) bool {
	if q.empty() {
		return true
	}
	hay := strings.ToLower(haystack)
	for _, t := range q.terms {
		if t.isNum {
			if !slices.Contains(nums, t.num) {
				return false
			}
			continue
		}
		if !strings.Contains(hay, t.text) {
			return false
		}
	}
	return true
}

// ── Haystacks ────────────────────────────────────────────────────────────────

// searchSep joins fields so a term cannot match across two of them.
const searchSep = "\x00"

func joinHay(fields ...string) string {
	return strings.Join(fields, searchSep)
}

// rowHaystack is what a live row is searched on. The full Prompt is left out
// on purpose: ad-hoc dispatch prompts are long specs that would match almost
// any term; Task already carries the first line. A shell row matches on its
// window name only.
func rowHaystack(r fleet.Row) string {
	if r.Shell {
		return r.Name
	}
	wt := ""
	if r.WorktreeDir != "" {
		wt = filepath.Base(r.WorktreeDir)
	}
	return joinHay(
		r.Recap.Title, r.Recap.Text, r.Recap.Role, r.ShortTitle, r.FirstPrompt, r.Task,
		r.TicketTitle, r.TicketRef, r.Branch, r.Name, r.AgentName, r.Project, r.SessionName,
		r.ModelName, r.Backend, r.Account, r.LastText, r.LastToolDetail, wt,
	)
}

// rowNums are the numbers a `#N` term can hit on a live row.
func rowNums(r fleet.Row) []int {
	if r.Shell {
		return nil
	}
	var nums []int
	if r.Ticket > 0 {
		nums = append(nums, r.Ticket)
	}
	if i := strings.LastIndex(r.TicketRef, "#"); i >= 0 {
		if n, err := strconv.Atoi(r.TicketRef[i+1:]); err == nil && n > 0 {
			nums = append(nums, n)
		}
	}
	if r.PRNumber > 0 {
		nums = append(nums, r.PRNumber)
	}
	return nums
}

// closedHaystack is what a closed (ledger) record is searched on.
func closedHaystack(r closedlog.Record) string {
	return joinHay(r.ShortTitle, r.FirstPrompt, r.TicketTitle, r.Ticket, r.Branch, r.Agent, r.Instance, r.SessionName, r.Model, r.Project())
}

func closedNums(r closedlog.Record) []int {
	var nums []int
	if n, err := strconv.Atoi(strings.TrimPrefix(r.Ticket, "#")); err == nil && n > 0 {
		nums = append(nums, n)
	}
	if r.PRNumber > 0 {
		nums = append(nums, r.PRNumber)
	}
	return nums
}

// jobHaystack is what a schedule is searched on: name, agent, the prompt
// when it is inline (not a prompt_key lookup), template and dir basename.
func jobHaystack(st cronedit.Status) string {
	prompt := ""
	if st.Job.PromptKey == "" {
		prompt = st.Job.Prompt
	}
	dir := ""
	if st.Job.Dir != "" {
		dir = filepath.Base(st.Job.Dir)
	}
	return joinHay(st.Name, st.Job.Agent, prompt, st.Job.Template, dir)
}

// groupHaystack is a group header's text.
func groupHaystack(g fleet.Group) string {
	dir := ""
	if g.ProjectDir != "" {
		dir = filepath.Base(g.ProjectDir)
	}
	return joinHay(g.Project, g.Session, dir)
}

// searchEntry is one cached haystack with its numbers.
type searchEntry struct {
	hay  string
	nums []int
}

// searchIndex caches each row's haystack so a keystroke never rebuilds
// them. It is cleared whenever groups, closed records or jobs reload.
type searchIndex struct {
	rows, closed, jobs map[string]searchEntry
}

func (ix *searchIndex) reset() {
	if ix != nil {
		ix.rows, ix.closed, ix.jobs = nil, nil, nil
	}
}

func lookup(cache *map[string]searchEntry, key string, build func() searchEntry) searchEntry {
	if e, ok := (*cache)[key]; ok {
		return e
	}
	e := build()
	if *cache == nil {
		*cache = make(map[string]searchEntry)
	}
	(*cache)[key] = e
	return e
}

// ── Model state ──────────────────────────────────────────────────────────────

// fleetSearch is the search state; nil on the model is Off. Editing has the
// input focused; otherwise the query is Committed.
type fleetSearch struct {
	input   textinput.Model
	editing bool
	query   string // trimmed query text
	q       parsedQuery
	// pre is the selection when search opened from Off, restored by cancel.
	preSession, preWindowID, preClosed, preSched string
	preIsHeader                                  bool
}

// fleetSearchShowMsg is the debounced viewer follow while editing.
type fleetSearchShowMsg struct{ seq int }

func (m FleetModel) searchEditing() bool { return m.search != nil && m.search.editing }

// searching reports whether a non-empty query filters the list.
func (m FleetModel) searching() bool { return m.search != nil && !m.search.q.empty() }

// committedQuery is the query to carry across a hot reload ("" in Off).
func (m FleetModel) committedQuery() string {
	if m.search == nil {
		return ""
	}
	return m.search.query
}

func (m *FleetModel) searchReset() { m.searchIdx.reset() }

func (m *FleetModel) setSearchQuery(v string) {
	m.search.query = strings.TrimSpace(v)
	m.search.q = parseQuery(v)
}

func newSearchInput(prefill string) (textinput.Model, tea.Cmd) {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = fleetSearchMaxRunes
	s := ti.Styles()
	s.Cursor.Blink = true
	ti.SetStyles(s)
	ti.SetValue(prefill)
	ti.CursorEnd()
	return ti, ti.Focus()
}

// openSearch is `/`: open the input, prefilled with a committed query.
func (m *FleetModel) openSearch() tea.Cmd {
	m.cancelNewSession()
	m.cancelRename()
	m.cancelClose()
	m.cancelNew()
	m.cancelSweep()
	m.closeMenu()
	m.jumpErr = ""
	if m.search == nil {
		m.search = &fleetSearch{
			preSession: m.selSession, preWindowID: m.selWindowID, preIsHeader: m.selIsHeader,
			preClosed: m.selClosed, preSched: m.selSchedule,
		}
	}
	prefill := m.search.query
	ti, blink := newSearchInput(prefill)
	m.search.input, m.search.editing = ti, true
	return blink
}

// commitSearch is enter: keep the filter, close the input. An empty query
// ends in Off.
func (m *FleetModel) commitSearch() tea.Cmd {
	if m.search == nil {
		return nil
	}
	m.search.editing = false
	if m.search.q.empty() {
		m.search = nil
		m.relayout()
	}
	return m.maybeShowCmd()
}

// cancelSearch is esc while editing (and backspace on an empty query): clear
// the query and restore the pre-search selection when that row still exists.
func (m *FleetModel) cancelSearch() tea.Cmd {
	s := m.search
	if s == nil {
		return nil
	}
	m.search = nil
	m.selSession, m.selWindowID, m.selIsHeader = s.preSession, s.preWindowID, s.preIsHeader
	m.selClosed, m.selSchedule = s.preClosed, s.preSched
	m.relayout()
	return m.maybeShowCmd()
}

// clearSearch is esc on a committed query: back to Off, keeping the current
// selection.
func (m *FleetModel) clearSearch() tea.Cmd {
	m.search = nil
	m.relayout()
	return m.maybeShowCmd()
}

// handleSearchKey owns every key while the input is open.
func (m *FleetModel) handleSearchKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return tea.Quit
	case "esc":
		return m.cancelSearch()
	case "enter":
		return m.commitSearch()
	case "backspace":
		if m.search.input.Value() == "" {
			return m.cancelSearch()
		}
	case "down", "ctrl+n":
		m.moveCursor(1)
		return m.searchShowLater()
	case "up", "ctrl+p":
		m.moveCursor(-1)
		return m.searchShowLater()
	}
	var cmd tea.Cmd
	before := m.search.input.Value()
	m.search.input, cmd = m.search.input.Update(msg)
	return tea.Batch(cmd, m.searchEdited(before))
}

// pasteSearch inserts pasted text with newlines and tabs collapsed to spaces.
func (m *FleetModel) pasteSearch(msg tea.PasteMsg) tea.Cmd {
	msg.Content = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(msg.Content)
	var cmd tea.Cmd
	before := m.search.input.Value()
	m.search.input, cmd = m.search.input.Update(msg)
	return tea.Batch(cmd, m.searchEdited(before))
}

// searchEdited re-filters when the input's text changed.
func (m *FleetModel) searchEdited(before string) tea.Cmd {
	v := m.search.input.Value()
	if v == before {
		return nil
	}
	m.setSearchQuery(v)
	m.relayout()
	return m.searchShowLater()
}

// searchShowLater schedules the viewer to follow the selection after a short
// quiet period: only the newest tick (by sequence number) shows.
func (m *FleetModel) searchShowLater() tea.Cmd {
	if m.selIsHeader || m.selSession == "" || m.selWindowID == "" {
		return nil
	}
	m.searchSeq++
	seq := m.searchSeq
	return tea.Tick(fleetSearchDebounce, func(time.Time) tea.Msg { return fleetSearchShowMsg{seq: seq} })
}

// applySearchShow shows the selection when the tick is still the latest.
func (m *FleetModel) applySearchShow(msg fleetSearchShowMsg) tea.Cmd {
	if msg.seq != m.searchSeq {
		return nil
	}
	return m.showSelectedCmd()
}

// ── Predicates ───────────────────────────────────────────────────────────────

// searchRow reports whether a live row matches the active query.
func (m FleetModel) searchRow(r fleet.Row) bool {
	if !m.searching() {
		return true
	}
	if m.searchIdx == nil {
		return searchMatch(m.search.q, rowHaystack(r), rowNums(r)...)
	}
	e := lookup(&m.searchIdx.rows, r.Session+"|"+r.WindowID, func() searchEntry {
		return searchEntry{rowHaystack(r), rowNums(r)}
	})
	return searchMatch(m.search.q, e.hay, e.nums...)
}

func (m FleetModel) searchClosed(r closedlog.Record) bool {
	if !m.searching() {
		return true
	}
	if m.searchIdx == nil {
		return searchMatch(m.search.q, closedHaystack(r), closedNums(r)...)
	}
	e := lookup(&m.searchIdx.closed, r.Key(), func() searchEntry {
		return searchEntry{closedHaystack(r), closedNums(r)}
	})
	return searchMatch(m.search.q, e.hay, e.nums...)
}

func (m FleetModel) searchJob(st cronedit.Status) bool {
	if !m.searching() {
		return true
	}
	if m.searchIdx == nil {
		return searchMatch(m.search.q, jobHaystack(st))
	}
	e := lookup(&m.searchIdx.jobs, st.Name, func() searchEntry { return searchEntry{hay: jobHaystack(st)} })
	return searchMatch(m.search.q, e.hay)
}

// searchJobs keeps the jobs that match.
func (m FleetModel) searchJobs(jobs []cronedit.Status) []cronedit.Status {
	var out []cronedit.Status
	for _, st := range jobs {
		if m.searchJob(st) {
			out = append(out, st)
		}
	}
	return out
}

// searchHeader reports whether the group header's own text matches, which
// shows the whole group.
func (m FleetModel) searchHeader(g fleet.Group) bool {
	return m.searching() && searchMatch(m.search.q, groupHaystack(g))
}

// rowKept reports whether row windowID of g survives the filter and the
// query (as a match or as a context ancestor).
func (m FleetModel) rowKept(g *fleet.Group, windowID string) bool {
	if !m.searching() {
		return true
	}
	rows := m.shownRows(g.Rows)
	var kept []fleet.Row
	if m.searchHeader(*g) {
		kept, _ = filterRows(rows, m.filter)
	} else {
		kept, _ = filterRowsBy(rows, func(r fleet.Row) bool { return m.filter.matches(r) && m.searchRow(r) })
	}
	return slices.ContainsFunc(kept, func(r fleet.Row) bool { return r.WindowID == windowID })
}

// firstSearchLine is the first selectable line that is a real match (not a
// dimmed ancestor), else the first selectable line; -1 when none.
func (m FleetModel) firstSearchLine() int {
	first := -1
	for i, l := range m.lines {
		if !l.selectable() {
			continue
		}
		if first < 0 {
			first = i
		}
		switch {
		case l.kind == fleetLineRow && !l.dimmed:
			return i
		case l.kind == fleetLineClosed:
			return i
		case l.kind == fleetLineSchedule && !l.schedNew:
			return i
		}
	}
	return first
}

// searchMatchCount counts the matching lines (not context rows).
func (m FleetModel) searchMatchCount() int {
	n := 0
	for _, l := range m.lines {
		switch l.kind {
		case fleetLineRow:
			if !l.dimmed {
				n++
			}
		case fleetLineClosed:
			n++
		case fleetLineSchedule:
			if !l.schedNew {
				n++
			}
		}
	}
	return n
}

// ── Rendering ────────────────────────────────────────────────────────────────

func searchCountText(n int) string {
	switch n {
	case 0:
		return "no matches"
	case 1:
		return "1 match"
	}
	return fmt.Sprintf("%d matches", n)
}

// searchEmptyText is the placeholder line shown when nothing matches.
func (m FleetModel) searchEmptyText() string {
	return fmt.Sprintf("  no matches for %q", m.search.query)
}

// searchView renders the footer's input line: the query on the left, the
// match count right-aligned and muted (dropped first when narrow).
func (m FleetModel) searchView(cw int) string {
	count := ""
	if m.searching() {
		count = searchCountText(m.searchMatchCount())
	}
	in := m.search.input
	avail := cw - fleetSidebarRightGutter - 2 // "/ "
	room := avail
	if count != "" && avail-lipgloss.Width(count)-3 >= 10 {
		room = avail - lipgloss.Width(count) - 3
	} else {
		count = ""
	}
	in.SetWidth(max(room-1, 1)) // the cursor takes one more column
	left := "  " + fleetCountStyle.Render("/") + " " + in.View()
	if count == "" {
		return left
	}
	pad := cw - fleetSidebarRightGutter - lipgloss.Width(left) - lipgloss.Width(count)
	if pad < 1 {
		return left
	}
	return left + strings.Repeat(" ", pad) + fleetDimStyle.Render(count)
}

// searchCommittedLine is the footer's idle line while a query is committed;
// clicking it reopens the input.
func (m FleetModel) searchCommittedLine(cw int) string {
	text := "/ " + m.search.query + "  " + searchCountText(m.searchMatchCount())
	return fleetDimStyle.Render("  " + truncateToWidth(text, cw-2))
}

// searchTag is the title row's tag for a committed query.
func (m FleetModel) searchTag() string {
	if m.search == nil || m.search.editing || m.search.q.empty() {
		return ""
	}
	q := m.search.query
	if utf8.RuneCountInString(q) > fleetSearchTagRunes {
		q = string([]rune(q)[:fleetSearchTagRunes-1]) + "…"
	}
	return "/\"" + q + "\""
}

// searchHints adds the search hints to the normal footer hints: "/ search"
// in Off, "esc clear search" in Committed.
func (m FleetModel) searchHints(hints []fleetHint) []fleetHint {
	out := make([]fleetHint, 0, len(hints)+1)
	for i, h := range hints {
		// Off: just before "? more", so narrow bars drop it first.
		if m.search == nil && i == len(hints)-1 {
			out = append(out, fleetHint{fleetSearchKey, "search"})
		}
		out = append(out, h)
		if m.search != nil && i == 0 {
			out = append(out, fleetHint{"esc", "clear search"})
		}
	}
	return out
}

// onSearchLine reports whether screen row y is the footer line a committed
// query shows on.
func (m FleetModel) onSearchLine(y int) bool {
	return m.search != nil && !m.search.editing && y == m.footerMessageY()
}
