package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/fleet"
)

func TestParseQuerySearchMatch(t *testing.T) {
	tests := []struct {
		name string
		q    string
		hay  string
		nums []int
		want bool
	}{
		{"empty matches all", "", "anything", nil, true},
		{"spaces only is empty", "   \t ", "anything", nil, true},
		{"substring", "flak", "a Flaky test", nil, true},
		{"case insensitive both sides", "FLAKY", "a flaky test", nil, true},
		{"and terms", "flaky test", "a flaky test", nil, true},
		{"and terms one missing", "flaky deploy", "a flaky test", nil, false},
		{"terms in any order", "test flaky", "a flaky test", nil, true},
		{"hash exact", "#278", "x", []int{278}, true},
		{"hash not prefix", "#27", "x", []int{278}, false},
		{"hash not in text", "#278", "issue 278", nil, false},
		{"hash with text term", "#278 flaky", "flaky", []int{278}, true},
		{"hash abc is text", "#abc", "see #abc here", nil, true},
		{"hash abc no match", "#abc", "see abc here", nil, false},
		{"bare hash is text", "#", "a # b", nil, true},
		{"no cross-field match", "ab", "a" + searchSep + "b", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := searchMatch(parseQuery(tt.q), tt.hay, tt.nums...); got != tt.want {
				t.Errorf("searchMatch(%q, %q, %v) = %v, want %v", tt.q, tt.hay, tt.nums, got, tt.want)
			}
		})
	}
}

func TestRowHaystackFields(t *testing.T) {
	set := map[string]func(r *fleet.Row, v string){
		"recap-title": func(r *fleet.Row, v string) { r.Recap.Title = v },
		"recap-text":  func(r *fleet.Row, v string) { r.Recap.Text = v },
		"recap-role":  func(r *fleet.Row, v string) { r.Recap.Role = v },
		"first":       func(r *fleet.Row, v string) { r.FirstPrompt = v },
		"task":        func(r *fleet.Row, v string) { r.Task = v },
		"tickettitle": func(r *fleet.Row, v string) { r.TicketTitle = v },
		"ticketref":   func(r *fleet.Row, v string) { r.TicketRef = v },
		"branch":      func(r *fleet.Row, v string) { r.Branch = v },
		"name":        func(r *fleet.Row, v string) { r.Name = v },
		"agent":       func(r *fleet.Row, v string) { r.AgentName = v },
		"project":     func(r *fleet.Row, v string) { r.Project = v },
		"sessionname": func(r *fleet.Row, v string) { r.SessionName = v },
		"model":       func(r *fleet.Row, v string) { r.ModelName = v },
		"backend":     func(r *fleet.Row, v string) { r.Backend = v },
		"account":     func(r *fleet.Row, v string) { r.Account = v },
		"lasttext":    func(r *fleet.Row, v string) { r.LastText = v },
		"lasttool":    func(r *fleet.Row, v string) { r.LastToolDetail = v },
		"worktree":    func(r *fleet.Row, v string) { r.WorktreeDir = "/x/.worktrees/" + v },
	}
	for name, f := range set {
		t.Run(name, func(t *testing.T) {
			var r fleet.Row
			f(&r, "zebra")
			if !searchMatch(parseQuery("zebra"), rowHaystack(r)) {
				t.Errorf("%s not searched", name)
			}
		})
	}
	t.Run("full prompt is not searched", func(t *testing.T) {
		r := fleet.Row{}
		r.Prompt = "zebra"
		if searchMatch(parseQuery("zebra"), rowHaystack(r)) {
			t.Error("Prompt matched")
		}
	})
	t.Run("no cross-field match", func(t *testing.T) {
		r := fleet.Row{}
		r.Branch, r.Name = "feat-ab", "cd"
		if searchMatch(parseQuery("abcd"), rowHaystack(r)) {
			t.Error("matched across the branch/name boundary")
		}
	})
	t.Run("shell matches its name only", func(t *testing.T) {
		r := fleet.Row{Shell: true, Project: "proj"}
		r.Name = "shell"
		if !searchMatch(parseQuery("shell"), rowHaystack(r)) || searchMatch(parseQuery("proj"), rowHaystack(r)) {
			t.Error("shell haystack is not the window name only")
		}
	})
	t.Run("numbers", func(t *testing.T) {
		r := fleet.Row{PRNumber: 9}
		r.Ticket = 5
		r.TicketRef = "acme/api#278"
		got := rowNums(r)
		for _, n := range []int{5, 9, 278} {
			if !slices.Contains(got, n) {
				t.Errorf("rowNums = %v, missing %d", got, n)
			}
		}
	})
}

func TestClosedAndJobHaystack(t *testing.T) {
	rec := closedlog.Record{ProjectDir: "/src/acme", Instance: "inst-1", Ticket: "278", Model: "opus", FirstPrompt: "zebra"}
	for _, q := range []string{"acme", "inst-1", "opus", "zebra", "#278"} {
		if !searchMatch(parseQuery(q), closedHaystack(rec), closedNums(rec)...) {
			t.Errorf("closed record not matched by %q", q)
		}
	}
	job := cronedit.Status{Name: "nightly", Job: cron.Job{Agent: "go-engineer", Prompt: "zebra", Template: "tmpl", Dir: "/src/acme"}}
	for _, q := range []string{"nightly", "go-engineer", "zebra", "tmpl", "acme"} {
		if !searchMatch(parseQuery(q), jobHaystack(job)) {
			t.Errorf("job not matched by %q", q)
		}
	}
	job.Job.PromptKey = "k"
	if searchMatch(parseQuery("zebra"), jobHaystack(job)) {
		t.Error("a prompt_key job matched its (unused) inline prompt")
	}
}

// searchModel is foldModel with searchable text on a few rows.
func searchModel(t *testing.T) FleetModel {
	t.Helper()
	m := foldModel(t)
	for gi := range m.groups {
		for ri := range m.groups[gi].Rows {
			r := &m.groups[gi].Rows[ri]
			switch r.Name {
			case "k":
				r.Branch = "fix-flaky-test"
			case "e":
				r.TicketRef = "acme/api#278"
			case "g":
				r.Recap.Title = "schedform polish"
			}
		}
	}
	m.searchReset()
	m.rebuildLines()
	m.resolveSelection()
	return m
}

func searchType(m FleetModel, s string) FleetModel {
	for _, r := range s {
		next, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = next.(FleetModel)
	}
	return m
}

func pressCode(m FleetModel, code rune) (FleetModel, tea.Cmd) {
	next, cmd := m.Update(tea.KeyPressMsg{Code: code})
	return next.(FleetModel), cmd
}

func searchFor(t *testing.T, m FleetModel, q string) FleetModel {
	t.Helper()
	m, _ = pressHub(m, "/")
	if !m.searchEditing() {
		t.Fatal("/ did not open the search input")
	}
	return searchType(m, q)
}

func TestSearchRebuildHidesGroupsAndKeepsAncestors(t *testing.T) {
	base := searchModel(t)
	base.selectLine(lineIndex(t, base, "R:g"))
	m := searchFor(t, base, "flaky")
	// k matches on its branch; its parent p stays as dimmed context; every
	// other group, its header and dividers are gone.
	want := []string{"H:s1", "R:p~", "R:k"}
	if got := lineKeys(m); !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	if m.selWindowID != "@2" {
		t.Errorf("selection = %q, want the first real match @2 (not the dimmed parent)", m.selWindowID)
	}
	if n := m.searchMatchCount(); n != 1 {
		t.Errorf("match count = %d, want 1 (context rows excluded)", n)
	}
}

func TestSearchMatchingParentDoesNotPullInChildren(t *testing.T) {
	m := searchModel(t)
	m.groups[0].Rows[0].Branch = "needle" // p
	m.searchReset()
	m = searchFor(t, m, "needle")
	if got, want := lineKeys(m), []string{"H:s1", "R:p"}; !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

func TestSearchOpensFoldedAndCollapsedThenRestores(t *testing.T) {
	m := searchModel(t)
	m.setFolded("s1", true)
	m.setCollapsed("s2", "@4", true)
	m.rebuildLines()
	m.resolveSelection()
	m = searchFor(t, m, "flaky")
	if !slices.Contains(lineKeys(m), "R:k") {
		t.Fatalf("folded group stayed closed while searching: %v", lineKeys(m))
	}
	if !m.isFolded("s1") {
		t.Error("saved fold state changed")
	}
	m, _ = pressCode(m, tea.KeyEscape) // cancel
	if m.search != nil {
		t.Fatal("esc did not close the search")
	}
	if got := lineKeys(m); slices.Contains(got, "R:k") || !slices.Contains(got, "H:s1") {
		t.Errorf("folded layout not restored: %v", got)
	}
	if !m.isFolded("s1") || !m.isCollapsed("s2", "@4") {
		t.Error("fold/collapse state lost")
	}
}

func TestSearchCollapsedSubtreeOpens(t *testing.T) {
	m := searchModel(t)
	m.setCollapsed("s1", "@1", true)
	m.rebuildLines()
	m = searchFor(t, m, "flaky")
	if got := lineKeys(m); !slices.Equal(got, []string{"H:s1", "R:p~", "R:k"}) {
		t.Fatalf("collapsed subtree stayed closed: %v", got)
	}
}

func TestSearchComposesWithAttentionFilter(t *testing.T) {
	m := searchModel(t)
	m.filter = fleetFilterNeeds // blocked and done-unseen
	m = searchFor(t, m, "needle-nowhere")
	m, _ = pressCode(m, tea.KeyEscape)
	// "flaky" row k is working: the needs filter hides it even though it matches.
	m = searchFor(t, m, "flaky")
	if len(m.lines) != 1 || m.lines[0].kind != fleetLineSearchEmpty {
		t.Fatalf("lines = %v, want only the placeholder", lineKeys(m))
	}
	m, _ = pressCode(m, tea.KeyEscape)
	m = searchFor(t, m, "g")
	if !slices.Contains(lineKeys(m), "R:g") {
		t.Errorf("blocked row g should pass both filters: %v", lineKeys(m))
	}
}

func TestSearchGroupHeaderMatchShowsWholeGroup(t *testing.T) {
	m := searchModel(t)
	for ri := range m.groups[1].Rows {
		m.groups[1].Rows[ri].Project = "" // only the header says "beta"
	}
	m.searchReset()
	m = searchFor(t, m, "beta")
	if got, want := lineKeys(m), []string{"H:s2", "R:d", "R:e"}; !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	// An agentless group whose header matches shows its header.
	m, _ = pressCode(m, tea.KeyEscape)
	m = searchFor(t, m, "delta")
	if got, want := lineKeys(m), []string{"H:s4", "P:s4"}; !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

func TestSearchNoMatchesPlaceholderKeepsSelection(t *testing.T) {
	m := searchModel(t)
	m.selectLine(lineIndex(t, m, "R:g"))
	before := m.selWindowID
	m = searchFor(t, m, "zzzz")
	if len(m.lines) != 1 || m.lines[0].kind != fleetLineSearchEmpty {
		t.Fatalf("lines = %v", lineKeys(m))
	}
	if m.selWindowID != before {
		t.Errorf("selection = %q, want it kept as %q", m.selWindowID, before)
	}
	if got := m.renderLine(0, 80); !strings.Contains(got, `no matches for "zzzz"`) {
		t.Errorf("placeholder = %q", got)
	}
	if got := searchCountText(0); got != "no matches" {
		t.Errorf("count text = %q", got)
	}
}

func TestSearchEmptyHub(t *testing.T) {
	InitStyles()
	m := newFleetModel()
	m.termWidth, m.termHeight = 120, 40
	m.configureSplitPane()
	m.loaded = true
	m = searchFor(t, m, "x")
	if len(m.lines) != 1 || m.lines[0].kind != fleetLineSearchEmpty {
		t.Fatalf("lines = %v", m.lines)
	}
	if out := m.render(); !strings.Contains(out, "no matches") {
		t.Errorf("empty hub render lacks the placeholder:\n%s", out)
	}
}

func TestSearchShellRowsOnlyWhenShown(t *testing.T) {
	m := searchModel(t)
	sh := foldRow("s1", "alpha", "@9", "shell", "", false, "")
	sh.Shell = true
	m.groups[0].Rows = append(m.groups[0].Rows, sh)
	m.searchReset()
	m.rebuildLines()
	m = searchFor(t, m, "shell")
	if len(m.lines) != 1 || m.lines[0].kind != fleetLineSearchEmpty {
		t.Fatalf("hidden shell matched: %v", lineKeys(m))
	}
	m, _ = pressCode(m, tea.KeyEscape)
	m.showShells = true
	m.rebuildLines()
	m = searchFor(t, m, "shell")
	if !slices.Contains(lineKeys(m), "R:shell") {
		t.Fatalf("shown shell not matched: %v", lineKeys(m))
	}
}

func TestSearchClosedSectionOnlyWithC(t *testing.T) {
	m := searchModel(t)
	m.closed = []closedlog.Record{
		{ClosedAt: time.Now(), WindowID: "@50", Session: "s1", ProjectDir: "/src/alpha", Instance: "old-1", FirstPrompt: "unique-needle"},
		{ClosedAt: time.Now(), WindowID: "@51", Session: "s1", ProjectDir: "/src/alpha", Instance: "old-2"},
	}
	m.searchReset()
	m = searchFor(t, m, "needle")
	for _, l := range m.lines {
		if l.kind == fleetLineClosed {
			t.Fatal("Closed section shown without C")
		}
	}
	m, _ = pressCode(m, tea.KeyEscape)
	m.showClosed = true
	m.rebuildLines()
	m = searchFor(t, m, "needle")
	var closed []string
	for _, l := range m.lines {
		if l.kind == fleetLineClosed {
			closed = append(closed, l.closed.Instance)
		}
	}
	if !slices.Equal(closed, []string{"old-1"}) {
		t.Errorf("closed rows = %v, want [old-1]", closed)
	}
}

func TestSearchSchedulesFiltered(t *testing.T) {
	m := searchModel(t)
	m.showSchedules = true
	m.sched.loaded = true
	m.sched.ov = cronedit.Overview{SchedulerInstalled: true, Enabled: true, Jobs: []cronedit.Status{
		{Name: "nightly-flaky", Enabled: true, Job: cron.Job{Name: "nightly-flaky", Dir: "/src/alpha", Schedule: "0 9 * * *"}},
		{Name: "weekly-docs", Enabled: true, Job: cron.Job{Name: "weekly-docs", Dir: "/src/alpha", Schedule: "0 9 * * 1"}},
		{Name: "elsewhere", Enabled: true, Job: cron.Job{Name: "elsewhere", Dir: "/nowhere", Schedule: "0 9 * * 1"}},
	}}
	m.rebuildLines()
	m = searchFor(t, m, "nightly")
	var jobs []string
	for _, l := range m.lines {
		switch l.kind {
		case fleetLineSchedule:
			if l.schedNew {
				t.Error("+ new schedule shown while searching")
			}
			jobs = append(jobs, l.sched.Name)
		case fleetLineSchedLabel:
			t.Errorf("section label shown: %q", l.label)
		}
	}
	if !slices.Equal(jobs, []string{"nightly-flaky"}) {
		t.Fatalf("jobs = %v", jobs)
	}
	if m.searchMatchCount() != 1 {
		t.Errorf("count = %d", m.searchMatchCount())
	}
	// The unmatched bottom block is hidden entirely.
	m, _ = pressCode(m, tea.KeyEscape)
	m = searchFor(t, m, "elsewhere")
	for _, l := range m.lines {
		if l.kind == fleetLineSchedule && l.sched.Name != "elsewhere" {
			t.Errorf("unexpected job %q", l.sched.Name)
		}
	}
}

func TestSearchKeyFlow(t *testing.T) {
	t.Run("enter commits and esc clears keeping selection", func(t *testing.T) {
		base := searchModel(t)
		base.selectLine(lineIndex(t, base, "R:g"))
		m := searchFor(t, base, "flaky")
		m, _ = pressCode(m, tea.KeyEnter)
		if m.searchEditing() || m.search == nil || !m.searching() {
			t.Fatalf("enter did not commit: %+v", m.search)
		}
		if got := m.searchTag(); got != `/"flaky"` {
			t.Errorf("tag = %q", got)
		}
		m, _ = pressCode(m, tea.KeyEscape)
		if m.search != nil {
			t.Fatal("esc did not clear a committed query")
		}
		if m.selWindowID != "@2" {
			t.Errorf("selection = %q, want it kept on @2", m.selWindowID)
		}
		if len(m.lines) < 8 {
			t.Errorf("full list not back: %v", lineKeys(m))
		}
	})
	t.Run("esc while editing restores the pre-search selection", func(t *testing.T) {
		m := searchModel(t)
		m.selectLine(lineIndex(t, m, "R:g"))
		m = searchFor(t, m, "flaky")
		if m.selWindowID != "@2" {
			t.Fatalf("selection = %q", m.selWindowID)
		}
		m, _ = pressCode(m, tea.KeyEscape)
		if m.selWindowID != "@6" {
			t.Errorf("selection = %q, want @6 restored", m.selWindowID)
		}
	})
	t.Run("backspace on empty closes", func(t *testing.T) {
		m := searchModel(t)
		m.selectLine(lineIndex(t, m, "R:e"))
		m = searchFor(t, m, "fl")
		m, _ = pressCode(m, tea.KeyBackspace)
		m, _ = pressCode(m, tea.KeyBackspace)
		if !m.searchEditing() {
			t.Fatal("closed with text left")
		}
		m, _ = pressCode(m, tea.KeyBackspace)
		if m.search != nil || m.selWindowID != "@5" {
			t.Errorf("search = %v, selection %q; want closed and @5", m.search, m.selWindowID)
		}
	})
	t.Run("j and k are text", func(t *testing.T) {
		m := searchFor(t, searchModel(t), "")
		m = searchType(m, "jk")
		if got := m.search.input.Value(); got != "jk" {
			t.Errorf("input = %q", got)
		}
	})
	t.Run("up and down move within the filtered list", func(t *testing.T) {
		m := searchModel(t)
		m.groups[1].Rows[0].Branch = "needle"
		m.groups[2].Rows[0].Branch = "needle"
		m.searchReset()
		m = searchFor(t, m, "needle")
		first := m.selWindowID
		m, _ = pressCode(m, tea.KeyDown) // the next group's header
		m, cmd := pressCode(m, tea.KeyDown)
		if m.selWindowID == first || !m.searchEditing() {
			t.Errorf("down did not move: %q", m.selWindowID)
		}
		if cmd == nil {
			t.Error("a move onto an agent row should schedule the debounced viewer follow")
		}
		m, _ = pressCode(m, tea.KeyUp)
		m, _ = pressCode(m, tea.KeyUp)
		if m.selWindowID != first {
			t.Errorf("up: selection = %q, want %q", m.selWindowID, first)
		}
	})
	t.Run("q in committed is not esc", func(t *testing.T) {
		// q leaves through tmux (no seam here); what matters is that it does
		// not take the clear-search branch, which only esc does.
		m := searchFor(t, searchModel(t), "flaky")
		m, _ = pressCode(m, tea.KeyEnter)
		m, _ = pressHub(m, "q")
		if m.search == nil {
			t.Error("q cleared the search instead of leaving")
		}
	})
	t.Run("empty enter ends in Off", func(t *testing.T) {
		m := searchFor(t, searchModel(t), "  ")
		m, _ = pressCode(m, tea.KeyEnter)
		if m.search != nil {
			t.Error("an empty query should commit to Off")
		}
	})
	t.Run("slash from committed prefills the query", func(t *testing.T) {
		m := searchFor(t, searchModel(t), "flaky")
		m, _ = pressCode(m, tea.KeyEnter)
		m, _ = pressHub(m, "/")
		if !m.searchEditing() || m.search.input.Value() != "flaky" {
			t.Errorf("reopen: %+v", m.search)
		}
	})
	t.Run("ctrl+c quits while editing", func(t *testing.T) {
		m := searchFor(t, searchModel(t), "x")
		next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if !next.(FleetModel).quitting || cmd == nil {
			t.Error("ctrl+c did not quit")
		}
	})
}

func TestSearchPasteCollapsesWhitespace(t *testing.T) {
	m := searchFor(t, searchModel(t), "")
	next, _ := m.Update(tea.PasteMsg{Content: "fix\nflaky\ttest"})
	m = next.(FleetModel)
	if got := m.search.input.Value(); got != "fix flaky test" {
		t.Errorf("input = %q", got)
	}
	long := strings.Repeat("a", 300)
	next, _ = m.Update(tea.PasteMsg{Content: long})
	if n := len([]rune(next.(FleetModel).search.input.Value())); n > fleetSearchMaxRunes {
		t.Errorf("query is %d runes, cap is %d", n, fleetSearchMaxRunes)
	}
}

func TestSearchViewerDebounce(t *testing.T) {
	var shows []string
	old := showLiveViewer
	showLiveViewer = func(sess, win string, _ int) error { shows = append(shows, sess+"|"+win); return nil }
	oldW, oldZ := hubWindowWidth, hubZoomSync
	hubWindowWidth = func() (int, error) { return 200, nil }
	hubZoomSync = func(bool) {}
	t.Cleanup(func() { showLiveViewer, hubWindowWidth, hubZoomSync = old, oldW, oldZ })

	m := searchModel(t)
	m = searchFor(t, m, "f")
	seq1 := m.searchSeq
	m = searchType(m, "l")
	seq2 := m.searchSeq
	if seq2 <= seq1 {
		t.Fatalf("seq did not advance: %d then %d", seq1, seq2)
	}
	// The stale tick shows nothing; the latest shows once.
	next, cmd := m.Update(fleetSearchShowMsg{seq: seq1})
	m = next.(FleetModel)
	if cmd != nil {
		t.Error("stale tick produced a command")
	}
	next, cmd = m.Update(fleetSearchShowMsg{seq: seq2})
	m = next.(FleetModel)
	if cmd == nil {
		t.Fatal("latest tick produced no show")
	}
	cmd()
	if len(shows) != 1 {
		t.Errorf("shows = %v, want exactly one", shows)
	}
	// While editing, the poll tick and reloads never bypass the debounce.
	if c := m.maybeShowCmd(); c != nil {
		t.Error("maybeShowCmd ran while editing")
	}
}

func TestSearchQueryHotReloadHandoff(t *testing.T) {
	m := searchFor(t, searchModel(t), "flaky test")
	m, _ = pressCode(m, tea.KeyEnter)
	var env string
	for _, e := range m.ReexecEnv() {
		if v, ok := strings.CutPrefix(e, HubViewEnv+"="); ok {
			env = v
		}
	}
	var s hubViewState
	if err := json.Unmarshal([]byte(env), &s); err != nil || s.Query != "flaky test" {
		t.Fatalf("env = %q (%v), want the query carried", env, err)
	}
	m2 := searchModel(t)
	m2.applyViewState(s)
	if m2.committedQuery() != "flaky test" || !m2.searching() || m2.searchEditing() {
		t.Errorf("restored search = %+v", m2.search)
	}
}

func TestSearchQueryNeverPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub-view.json")
	if err := writeHubView(path, hubViewState{Filter: "active", Query: "secret"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "query") {
		t.Errorf("hub-view.json carries the query: %s", data)
	}
	if !strings.Contains(string(data), "active") {
		t.Errorf("other fields lost: %s", data)
	}
}

func TestSearchRevealRowClearsHidingQuery(t *testing.T) {
	m := searchFor(t, searchModel(t), "flaky")
	m, _ = pressCode(m, tea.KeyEnter)
	if !m.revealRow("s3", "@6") {
		t.Fatal("revealRow reported no change for a row the query hides")
	}
	if m.search != nil {
		t.Error("query not cleared")
	}
	if m.infoMsg != "search cleared" {
		t.Errorf("info = %q", m.infoMsg)
	}
	if !slices.Contains(lineKeys(m), "R:g") {
		t.Errorf("target still hidden: %v", lineKeys(m))
	}
	// A visible target leaves the query alone.
	m = searchFor(t, searchModel(t), "flaky")
	m, _ = pressCode(m, tea.KeyEnter)
	m.revealRow("s1", "@2")
	if m.search == nil {
		t.Error("query cleared for a visible row")
	}
}

func TestSearchClickWhileEditingCommitsAndSelects(t *testing.T) {
	m := searchModel(t)
	m.groups[1].Rows[0].Branch = "needle"
	m.groups[2].Rows[0].Branch = "needle"
	m.searchReset()
	m = searchFor(t, m, "needle")
	idx := lineIndex(t, m, "R:g")
	y := -1
	for yy := 0; yy < m.termHeight; yy++ {
		if i, ok := m.lineAt(8, yy); ok && i == idx {
			y = yy
			break
		}
	}
	if y < 0 {
		t.Fatal("row not on screen")
	}
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 8, Y: y})
	m = next.(FleetModel)
	if m.searchEditing() || !m.searching() {
		t.Errorf("click did not commit: %+v", m.search)
	}
	if m.selWindowID != "@6" {
		t.Errorf("selection = %q, want the clicked row @6", m.selWindowID)
	}
}

func TestSearchCommittedFooterClickReopens(t *testing.T) {
	m := searchFor(t, searchModel(t), "flaky")
	m, _ = pressCode(m, tea.KeyEnter)
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 4, Y: m.footerMessageY()})
	if !next.(FleetModel).searchEditing() {
		t.Error("clicking the committed query line did not reopen the input")
	}
}

func TestSearchChromeText(t *testing.T) {
	m := searchFor(t, searchModel(t), "flaky")
	if got := m.footerBody(100); !strings.Contains(got, "1 match") || !strings.Contains(got, "flaky") {
		t.Errorf("editing footer = %q", got)
	}
	if got := m.footerBody(20); strings.Contains(got, "match") {
		t.Errorf("narrow footer kept the count: %q", got)
	}
	m, _ = pressCode(m, tea.KeyEnter)
	if got := m.titleLayout(100).line; !strings.Contains(got, `/"flaky"`) {
		t.Errorf("title = %q", got)
	}
	hints := m.footerHints()
	if !slices.ContainsFunc(hints, func(h fleetHint) bool { return h.key == "esc" && h.label == "clear search" }) {
		t.Errorf("committed hints = %v", hints)
	}
	m, _ = pressCode(m, tea.KeyEscape)
	if !slices.ContainsFunc(m.footerHints(), func(h fleetHint) bool { return h.key == "/" && h.label == "search" }) {
		t.Errorf("off hints lack / search: %v", m.footerHints())
	}
	long := searchFor(t, searchModel(t), strings.Repeat("x", 40))
	long, _ = pressCode(long, tea.KeyEnter)
	if tag := long.searchTag(); !strings.HasSuffix(tag, `…"`) || len([]rune(tag)) > fleetSearchTagRunes+4 {
		t.Errorf("long tag = %q", tag)
	}
}

func TestHelpListsSearchKey(t *testing.T) {
	for _, it := range fleetHelpItems() {
		if it.keys == "/" && it.action == "/" {
			m := searchModel(t)
			next, _ := m.runKey(it.action)
			_ = next
			if !m.searchEditing() {
				t.Error("help entry for / does not open the input")
			}
			return
		}
	}
	t.Error("help lacks a / entry")
}

func TestSearchHoldsOffReexecWhileEditing(t *testing.T) {
	m := searchFor(t, searchModel(t), "x")
	m.exeChanged = func() bool { return true }
	next, _ := m.Update(fleetTickMsg{})
	if next.(FleetModel).reexec {
		t.Error("hot reload fired while editing")
	}
}

func TestSearchReloadKeepsQuery(t *testing.T) {
	m := searchFor(t, searchModel(t), "flaky")
	m, _ = pressCode(m, tea.KeyEnter)
	groups := foldGroups()
	groups[0].Rows[1].Branch = "fix-flaky-test"
	groups[0].Rows[2].Branch = "also-flaky" // x now matches
	next, _ := m.Update(fleetLoadedMsg{groups: groups})
	m = next.(FleetModel)
	if !slices.Contains(lineKeys(m), "R:x") || !slices.Contains(lineKeys(m), "R:k") {
		t.Errorf("reload lost matches: %v", lineKeys(m))
	}
}
