package tui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/charmbracelet/x/ansi"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// tkMouseModel returns a loaded tickets browser at the given terminal size:
// three issues (#3 is newest, so first and selected), labels, a linked PR on #2, an
// assignee on #1, and a cached preview for every issue.
func tkMouseModel(t *testing.T, width, height int) TicketsModel {
	t.Helper()
	InitStyles()
	now := time.Now()
	m := TicketsModel{
		repoSlug:    "owner/repo",
		issueState:  tkStateOpen,
		labelIdx:    -1,
		searchInput: textinput.New(),
		detailCache: map[int]*tkDetailFetchedMsg{},
		linkedPRs:   map[int]ghPR{2: {Number: 172, URL: "https://github.com/owner/repo/pull/172", HeadRefName: "agent/x/issue-2"}},
		zones:       &zoneList{},
		agents:      []agent.Agent{{Name: "go-engineer"}, {Name: "code-reviewer"}},
		splitPane:   SplitPane{ListRatio: 0.30, MinListRows: 3, MinDetailRows: 4},
	}
	for i := 1; i <= 3; i++ {
		iss := ghIssue{
			Number:    i,
			Title:     fmt.Sprintf("issue number %d", i),
			CreatedAt: now.Add(-time.Duration(4-i) * time.Hour),
			URL:       fmt.Sprintf("https://github.com/owner/repo/issues/%d", i),
			State:     "OPEN",
			Labels:    []ghLabel{{Name: "bug"}},
		}
		if i == 1 {
			iss.Assignees = []ghUser{{Login: "alice"}}
		}
		m.allIssues = append(m.allIssues, iss)
		m.detailCache[i] = &tkDetailFetchedMsg{
			number: i, title: iss.Title, body: "body of " + iss.Title,
			labels: iss.Labels, assignees: iss.Assignees, url: iss.URL, state: "OPEN",
		}
	}
	m.labelOptions = extractLabels(m.allIssues)
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = next.(TicketsModel)
	m.applyFilters()
	m.previewDetail = m.detailCache[m.selectedNumber()]
	return m
}

// tkRender draws the model so its zones are recorded, like a real frame.
func tkRender(m TicketsModel) TicketsModel {
	_ = m.View()
	return m
}

// tkZonePoint returns a point inside zone id of the current frame.
func tkZonePoint(t *testing.T, m TicketsModel, id string) (int, int) {
	t.Helper()
	m = tkRender(m)
	r, ok := m.currentZones().find(id)
	if !ok {
		var ids []string
		for _, z := range m.currentZones() {
			ids = append(ids, z.id)
		}
		t.Fatalf("zone %q not rendered; have %v", id, ids)
	}
	return (r.x0 + r.x1 - 1) / 2, r.y
}

// tkPressRelease presses at (px, py) and releases at (rx, ry).
func tkPressRelease(t *testing.T, m TicketsModel, px, py, rx, ry int) (TicketsModel, tea.Cmd) {
	t.Helper()
	m = tkRender(m)
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: px, Y: py})
	m = next.(TicketsModel)
	next, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: rx, Y: ry})
	return next.(TicketsModel), cmd
}

// tkClickZone clicks the middle of zone id.
func tkClickZone(t *testing.T, m TicketsModel, id string) (TicketsModel, tea.Cmd) {
	t.Helper()
	x, y := tkZonePoint(t, m, id)
	return tkPressRelease(t, m, x, y, x, y)
}

// execRecorder captures every tkExec call made while it is installed.
type execRecorder struct {
	mu    sync.Mutex
	calls []string
	reply func(name string, args ...string) ([]byte, error)
}

func (r *execRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// called reports whether a recorded call contains every word of want.
func (r *execRecorder) called(want string) bool {
	for _, c := range r.all() {
		ok := true
		for _, w := range strings.Fields(want) {
			if !strings.Contains(" "+c+" ", " "+w+" ") {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// recordExec replaces tkExec for the test; reply may be nil.
func recordExec(t *testing.T, reply func(name string, args ...string) ([]byte, error)) *execRecorder {
	t.Helper()
	r := &execRecorder{reply: reply}
	prev := tkExec
	tkExec = func(name string, args ...string) ([]byte, error) {
		r.mu.Lock()
		r.calls = append(r.calls, name+" "+strings.Join(args, " "))
		r.mu.Unlock()
		if r.reply != nil {
			return r.reply(name, args...)
		}
		return nil, errExecDisabled
	}
	t.Cleanup(func() { tkExec = prev })
	return r
}

// runCmds executes cmd and any batched cmds, returning their messages. A cmd
// that has not returned after a short wait (a timer) is skipped.
func runCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(100 * time.Millisecond):
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runCmds(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// ── click per zone ───────────────────────────────────────────────────────────

func TestTicketsClickZones(t *testing.T) {
	type check func(t *testing.T, m TicketsModel, rec *execRecorder)
	tests := []struct {
		name   string
		zone   string
		detail bool // open the detail view first
		wide   bool // only rendered at wide widths
		check  check
	}{
		{name: "row selects", zone: "row:1", check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if m.selectedNumber() != 1 {
				t.Errorf("selected #%d, want #1", m.selectedNumber())
			}
		}},
		{name: "state chip cycles to closed", zone: "key:S@chip", check: func(t *testing.T, m TicketsModel, rec *execRecorder) {
			if m.issueState != tkStateClosed || !m.loading {
				t.Errorf("issueState = %q loading=%v, want closed and loading", m.issueState, m.loading)
			}
		}},
		{name: "sort chip cycles", zone: "key:s@chip", check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if m.sortBy != sortOldest {
				t.Errorf("sortBy = %v, want oldest", m.sortBy)
			}
		}},
		{name: "label chip opens the filter picker", zone: "key:f@chip", check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			p, ok := m.pickerModal.Content.(tkPickerModel)
			if !m.pickerModal.Visible || !ok || p.kind != pickFilter {
				t.Error("the label chip should open the label filter picker")
			}
		}},
		{name: "search chip opens search", zone: "key:/@chip", check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if !m.searchMode {
				t.Error("search chip should open the search input")
			}
		}},
		{name: "new chip opens create form", zone: "key:c@chip", check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if !m.createModal.Visible {
				t.Error("+ New should open the create form")
			}
		}},
		{name: "# header sorts by number", zone: "sort:num@hdr", check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if m.sortBy != sortNumber {
				t.Errorf("sortBy = %v, want number ascending", m.sortBy)
			}
		}},
		{name: "AGE header toggles to oldest", zone: "sort:age@hdr", check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if m.sortBy != sortOldest {
				t.Errorf("sortBy = %v, want oldest", m.sortBy)
			}
		}},
		{name: "row label filters", zone: "label:bug@2", wide: true, check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if m.filterLabel != "bug" {
				t.Errorf("filterLabel = %q, want bug", m.filterLabel)
			}
		}},
		{name: "PR cell opens the PR", zone: "pr:2@row", wide: true, check: func(t *testing.T, m TicketsModel, rec *execRecorder) {
			if !rec.called("gh pr view https://github.com/owner/repo/pull/172 --web") {
				t.Errorf("want gh pr view --web, got %v", rec.all())
			}
		}},
		{name: "preview Edit button edits in place", zone: "key:e@prev", check: func(t *testing.T, m TicketsModel, rec *execRecorder) {
			if m.pendingEdit != editTitle || m.pendingEditNum != 3 || !rec.called("gh issue view 3 --json") {
				t.Errorf("Edit should load #3 to edit its title; pending=%v/%d calls=%v", m.pendingEdit, m.pendingEditNum, rec.all())
			}
		}},
		{name: "preview Close button confirms", zone: "key:d@prev", check: func(t *testing.T, m TicketsModel, rec *execRecorder) {
			if !m.confirmModal.Visible {
				t.Error("Close should open the confirm modal")
			}
			if rec.called("gh issue close") {
				t.Error("Close must not close without confirming")
			}
		}},
		{name: "preview Dispatch button", zone: "key:T@prev", check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if !m.agentModal.Visible {
				t.Error("Dispatch… should open the agent picker")
			}
		}},
		{name: "preview Triage button", zone: "key:t@prev", check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if !m.quitting || m.result.PromptMode != "triage" || m.result.TicketNumber != 3 {
				t.Errorf("result = %+v, want triage launch of #3", m.result)
			}
		}},
		{name: "preview Open button", zone: "key:o@prev", check: func(t *testing.T, m TicketsModel, rec *execRecorder) {
			if !rec.called("gh issue view 3 --web") {
				t.Errorf("want gh issue view 3 --web, got %v", rec.all())
			}
		}},
		{name: "preview assignee opens the assign picker", zone: "assign:3@prev", check: func(t *testing.T, m TicketsModel, rec *execRecorder) {
			p, ok := m.pickerModal.Content.(tkPickerModel)
			if !m.pickerModal.Visible || !ok || p.kind != pickAssignees || p.number != 3 {
				t.Errorf("assignee click should open the assign picker for #3")
			}
		}},
		{name: "preview label filters", zone: "label:bug@prev", check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if m.filterLabel != "bug" {
				t.Errorf("filterLabel = %q, want bug", m.filterLabel)
			}
		}},
		{name: "help hint opens help", zone: "key:?@hint", check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if !m.helpModal.Visible {
				t.Error("? hint should open help")
			}
		}},
		{name: "quit hint quits", zone: "key:q@hint", check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if !m.quitting {
				t.Error("q hint should quit")
			}
		}},
		{name: "detail back returns to list", zone: "key:esc@back", detail: true, check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if m.viewMode != tkViewList {
				t.Error("← back should return to the list")
			}
		}},
		{name: "detail Close button confirms", zone: "key:d@detail", detail: true, check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if !m.confirmModal.Visible {
				t.Error("detail Close should open the confirm modal")
			}
		}},
		{name: "detail URL opens browser", zone: "url@detail", detail: true, wide: true, check: func(t *testing.T, m TicketsModel, rec *execRecorder) {
			if !rec.called("gh issue view 3 --web") {
				t.Errorf("want gh issue view 3 --web, got %v", rec.all())
			}
		}},
		{name: "detail label filters list", zone: "label:bug@detail", detail: true, check: func(t *testing.T, m TicketsModel, _ *execRecorder) {
			if m.viewMode != tkViewList || m.filterLabel != "bug" {
				t.Errorf("view=%d filter=%q, want list filtered by bug", m.viewMode, m.filterLabel)
			}
		}},
	}
	for _, width := range []int{130, 85} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%d/%s", width, tt.name), func(t *testing.T) {
				if tt.wide && width < tkDropPRCols {
					t.Skip("column dropped at this width")
				}
				rec := recordExec(t, nil)
				m := tkMouseModel(t, width, 40)
				if tt.detail {
					m.detail = m.detailCache[3]
					m.viewMode = tkViewDetail
				}
				m, cmd := tkClickZone(t, m, tt.zone)
				runCmds(cmd)
				tt.check(t, m, rec)
			})
		}
	}
}

// TestTicketsNarrowLayout checks what collapses below the breakpoints.
func TestTicketsNarrowLayout(t *testing.T) {
	wide := tkRender(tkMouseModel(t, 130, 40))
	narrow := tkRender(tkMouseModel(t, 85, 40))
	if !wide.currentZones().has("assign:1@row") {
		t.Error("ASSIGNEE column should render at 130 cols")
	}
	if narrow.currentZones().has("assign:1@row") || narrow.currentZones().has("pr:2@row") {
		t.Error("ASSIGNEE and PR columns should drop at 85 cols")
	}
	if !narrow.currentZones().has("label:bug@1") {
		t.Error("LABELS column should survive at 85 cols")
	}
	// The narrow action strip is a single line of buttons.
	e, _ := narrow.currentZones().find("key:e@prev")
	c, _ := narrow.currentZones().find("key:d@prev")
	if e.y != c.y {
		t.Errorf("narrow action strip spans rows %d..%d, want one line", e.y, c.y)
	}
	for _, m := range []TicketsModel{wide, narrow} {
		v := m.View().Content
		if got := strings.Count(v, "\n") + 1; got != m.termHeight {
			t.Errorf("width %d: frame is %d lines, want %d", m.termWidth, got, m.termHeight)
		}
	}
}

// ── press / release ─────────────────────────────────────────────────────────

func TestTicketsReleaseOutsidePressZoneDoesNothing(t *testing.T) {
	rec := recordExec(t, nil)
	m := tkMouseModel(t, 130, 40)
	before := m.selectedNumber()

	// Press on row #1, drag to row #2 before releasing: no selection change.
	x1, y1 := tkZonePoint(t, m, "row:1")
	x2, y2 := tkZonePoint(t, m, "row:2")
	got, cmd := tkPressRelease(t, m, x1, y1, x2, y2)
	runCmds(cmd)
	if got.selectedNumber() != before {
		t.Errorf("drag between rows selected #%d, want #%d unchanged", got.selectedNumber(), before)
	}

	// Press on Close, release on empty space: no confirm modal.
	bx, by := tkZonePoint(t, m, "key:d@prev")
	got, _ = tkPressRelease(t, m, bx, by, 0, 0)
	if got.confirmModal.Visible {
		t.Error("releasing off the button must cancel the click")
	}
	if len(rec.all()) != 0 {
		t.Errorf("no command should run, got %v", rec.all())
	}
}

func TestTicketsRefreshDeferredDuringPress(t *testing.T) {
	m := tkMouseModel(t, 130, 40)
	x, y := tkZonePoint(t, m, "row:1")
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = next.(TicketsModel)

	fresh := ticketsFetchedMsg{repoSlug: "owner/repo", issues: []ghIssue{{Number: 9, Title: "new", CreatedAt: time.Now()}}}
	next, _ = m.Update(fresh)
	m = next.(TicketsModel)
	if len(m.allIssues) != 3 {
		t.Fatalf("refresh applied mid-press: %d issues, want the old 3", len(m.allIssues))
	}
	next, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = next.(TicketsModel)
	if len(m.allIssues) != 1 || m.allIssues[0].Number != 9 {
		t.Errorf("refresh not applied after release: %+v", m.allIssues)
	}
}

// ── double-click ────────────────────────────────────────────────────────────

func TestTicketsDoubleClickTiming(t *testing.T) {
	tests := []struct {
		name       string
		gap        time.Duration
		secondZone string
		wantDetail bool
	}{
		{"fast second click opens", 150 * time.Millisecond, "row:2", true},
		{"at the limit opens", tkDoubleClick, "row:2", true},
		{"slow second click only selects", tkDoubleClick + 50*time.Millisecond, "row:2", false},
		{"second click on another row", 100 * time.Millisecond, "row:1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := recordExec(t, nil)
			clock := time.Unix(1_700_000_000, 0)
			prev := tkNow
			tkNow = func() time.Time { return clock }
			t.Cleanup(func() { tkNow = prev })

			m := tkMouseModel(t, 130, 40)
			m, _ = tkClickZone(t, m, "row:2")
			clock = clock.Add(tt.gap)
			_, cmd := tkClickZone(t, m, tt.secondZone)
			runCmds(cmd)
			opened := rec.called("gh issue view 2 --json")
			if opened != tt.wantDetail {
				t.Errorf("detail fetch = %v, want %v (calls %v)", opened, tt.wantDetail, rec.all())
			}
		})
	}
}

// ── modals own the mouse ────────────────────────────────────────────────────

func TestTicketsModalSwallowsClicks(t *testing.T) {
	t.Run("click outside confirm cancels it", func(t *testing.T) {
		rec := recordExec(t, nil)
		m := tkMouseModel(t, 130, 40)
		m = tkSendKey(t, m, charKey('d'))
		sel := m.selectedNumber()
		// Row #1 is under/near the modal's background; the click must not
		// reach it.
		m, cmd := tkPressRelease(t, m, 0, 0, 0, 0)
		runCmds(cmd)
		if m.confirmModal.Visible {
			t.Error("outside click should cancel the confirm modal")
		}
		if m.selectedNumber() != sel || rec.called("gh issue close") {
			t.Error("outside click must not act on the view behind the modal")
		}
	})
	t.Run("background zones are dead under a modal", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m = tkSendKey(t, m, charKey('d'))
		z := tkRender(m).currentZones()
		for _, r := range z {
			if strings.HasPrefix(r.id, "row:") || strings.HasPrefix(r.id, "key:") {
				t.Fatalf("zone %q is live behind the modal", r.id)
			}
		}
	})
	t.Run("confirm button closes the issue", func(t *testing.T) {
		rec := recordExec(t, nil)
		m := tkMouseModel(t, 130, 40)
		m = tkSendKey(t, m, charKey('d'))
		m, cmd := tkClickZone(t, m, "mkey:y@close")
		runCmds(cmd)
		if m.confirmModal.Visible || !rec.called("gh issue close 3") {
			t.Errorf("Yes should close #3; calls %v", rec.all())
		}
	})
	t.Run("cancel button", func(t *testing.T) {
		rec := recordExec(t, nil)
		m := tkMouseModel(t, 130, 40)
		m = tkSendKey(t, m, charKey('d'))
		m, cmd := tkClickZone(t, m, "mkey:n@close")
		runCmds(cmd)
		if m.confirmModal.Visible || rec.called("gh issue close") {
			t.Error("No should cancel without closing")
		}
	})
	t.Run("wheel does not scroll behind a modal", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		x, y := tkZonePoint(t, m, "row:2")
		m = tkSendKey(t, m, charKey('?'))
		sel := m.selectedNumber()
		next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: x, Y: y})
		if next.(TicketsModel).selectedNumber() != sel {
			t.Error("wheel moved the list behind the help overlay")
		}
	})
	t.Run("outside click keeps a typed create form", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m = tkSendKey(t, m, charKey('c'))
		m = tkSendKey(t, m, charKey('x'))
		m, _ = tkPressRelease(t, m, 0, 0, 0, 0)
		if !m.createModal.Visible {
			t.Error("outside click discarded a typed create form")
		}
	})
	t.Run("outside click closes an empty create form", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m = tkSendKey(t, m, charKey('c'))
		m, _ = tkPressRelease(t, m, 0, 0, 0, 0)
		if m.createModal.Visible {
			t.Error("outside click should close an empty create form")
		}
	})
	t.Run("agent row click picks the agent", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m = tkSendKey(t, m, charKey('T'))
		m, _ = tkClickZone(t, m, "mrow:1@agent")
		if m.agentModal.Visible || !m.promptModal.Visible {
			t.Fatal("clicking an agent should advance to the prompt picker")
		}
		psm := m.promptModal.Content.(PromptSelectModel)
		if psm.pendingAgentName != "code-reviewer" {
			t.Errorf("agent = %q, want code-reviewer", psm.pendingAgentName)
		}
	})
	t.Run("help close button", func(t *testing.T) {
		m := tkMouseModel(t, 130, 40)
		m = tkSendKey(t, m, charKey('?'))
		m, _ = tkClickZone(t, m, "mkey:esc@help")
		if m.helpModal.Visible {
			t.Error("Close should close help")
		}
	})
}

// ── wheel and hover ─────────────────────────────────────────────────────────

func TestTicketsWheelScrollsPaneUnderPointer(t *testing.T) {
	m := tkMouseModel(t, 130, 40)
	m.previewDetail.body = strings.Repeat("line of body text\n\n", 60)
	m.detailCache[3] = m.previewDetail

	lx, ly := tkZonePoint(t, m, "row:2")
	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: lx, Y: ly})
	got := next.(TicketsModel)
	if got.selectedNumber() != 2 {
		t.Errorf("wheel over the list should move the selection to #2, got #%d", got.selectedNumber())
	}

	z := tkRender(m).currentZones()
	var px, py int
	for _, r := range z {
		if r.id == "pane:preview" {
			px, py = r.x0+1, r.y
			break
		}
	}
	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: px, Y: py})
	got = next.(TicketsModel)
	if got.selectedNumber() != 3 {
		t.Error("wheel over the preview must not move the list selection")
	}
	if got.previewScroll != tkWheelStep {
		t.Errorf("preview scroll = %d, want %d", got.previewScroll, tkWheelStep)
	}
}

func TestTicketsHover(t *testing.T) {
	m := tkRender(tkMouseModel(t, 130, 40))
	x, y := tkZonePoint(t, m, "key:e@prev")
	next, _ := m.Update(tea.MouseMotionMsg{X: x, Y: y})
	m = next.(TicketsModel)
	if m.hover != "key:e@prev" {
		t.Fatalf("hover = %q, want the Edit button", m.hover)
	}
	// Moving within the same zone changes nothing.
	next, cmd := m.Update(tea.MouseMotionMsg{X: x + 1, Y: y})
	if cmd != nil || next.(TicketsModel).hover != m.hover {
		t.Error("motion inside the same zone should be a no-op")
	}

	rx, ry := tkZonePoint(t, m, "row:1")
	next, _ = m.Update(tea.MouseMotionMsg{X: rx, Y: ry})
	m = next.(TicketsModel)
	if m.hoverRow != "row:1" {
		t.Errorf("hoverRow = %q, want row:1", m.hoverRow)
	}
}

func TestTicketsCopyURLUsesTmuxBuffer(t *testing.T) {
	rec := recordExec(t, func(string, ...string) ([]byte, error) { return nil, nil })
	m := tkMouseModel(t, 130, 40)
	_, cmd := m.Update(charKey('y'))
	msgs := runCmds(cmd)
	if !rec.called("tmux set-buffer -- https://github.com/owner/repo/issues/3") {
		t.Errorf("want tmux set-buffer of the issue URL, got %v", rec.all())
	}
	if len(msgs) != 1 {
		t.Fatalf("want one tkCopiedMsg, got %v", msgs)
	}
	next, _ := m.Update(msgs[0])
	if !strings.Contains(next.(TicketsModel).flash, "copied") {
		t.Errorf("flash = %q, want a copied confirmation", next.(TicketsModel).flash)
	}
}

// TestTicketsFrameFitsEverySize renders the list, detail and edit views over
// a sweep of terminal sizes: every line must fit the width and the frame
// must be exactly the terminal height, or a real terminal wraps and leaves
// stale lines.
func TestTicketsFrameFitsEverySize(t *testing.T) {
	views := map[string]func(m TicketsModel) TicketsModel{
		"list": func(m TicketsModel) TicketsModel { return m },
		"detail": func(m TicketsModel) TicketsModel {
			m.detail, m.viewMode = m.detailCache[3], tkViewDetail
			return m
		},
		"edit": func(m TicketsModel) TicketsModel {
			m.detail, m.viewMode = m.detailCache[3], tkViewDetail
			next, _ := m.startEdit(editBody)
			return tkType(t, next.(TicketsModel), "x")
		},
		"search": func(m TicketsModel) TicketsModel {
			m.searchQuery = "issue"
			m.filterLabel = "bug"
			m.applyFilters()
			return m
		},
	}
	for name, setup := range views {
		for _, w := range []int{60, 70, 75, 85, 95, 110, 140, 200} {
			for _, h := range []int{15, 24, 40} {
				m := setup(tkMouseModel(t, w, h))
				m.autopilot = tkAutopilot{on: true, interval: 24 * time.Hour, ticker: true, nextPoll: tkNow().Add(23 * time.Hour)}
				lines := strings.Split(m.View().Content, "\n")
				if len(lines) != h {
					t.Errorf("%s %dx%d: %d lines", name, w, h, len(lines))
				}
				for i, l := range lines {
					if lw := ansi.StringWidth(l); lw > w {
						t.Errorf("%s %dx%d: line %d is %d wide: %q", name, w, h, i, lw, ansi.Strip(l))
					}
				}
			}
		}
	}
}
