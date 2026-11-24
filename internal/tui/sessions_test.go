package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/theme"
)

// stripANSI removes ANSI escape codes from s, returning plain text.
func stripANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return re.ReplaceAllString(s, "")
}

func TestSessionsModel_Init(t *testing.T) {
	m := NewSessionsModel("")
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() returned nil cmd; expected a non-nil fetch command")
	}
}

func TestSessionsModel_WindowSizeMsg(t *testing.T) {
	m := NewSessionsModel("")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	sm := updated.(SessionsModel)
	if sm.termWidth != 200 {
		t.Errorf("termWidth = %d, want 200", sm.termWidth)
	}
	if sm.termHeight != 50 {
		t.Errorf("termHeight = %d, want 50", sm.termHeight)
	}
}

func TestSessionsModel_ContentWidth(t *testing.T) {
	tests := []struct {
		name      string
		termWidth int
		want      int
	}{
		{"normal", 120, 116},
		{"wide", 200, 196},
		{"very narrow", 10, 20}, // floored at 20, never wider than the pane allows
		{"exactly 24", 24, 20},  // 24-4=20
		{"below min", 20, 20},   // floored
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewSessionsModel("")
			m.termWidth = tc.termWidth
			if got := m.contentWidth(); got != tc.want {
				t.Errorf("contentWidth() = %d, want %d (termWidth=%d)", got, tc.want, tc.termWidth)
			}
		})
	}
}

// TestListHeight: the list body takes every row the chrome leaves, at any
// window height (inverted from the old SplitPane ratio and minimum rows).
func TestListHeight(t *testing.T) {
	for _, h := range []int{40, 60, 24, 13, 5} {
		m := NewSessionsModel("")
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: h})
		sm := updated.(SessionsModel)
		want := h - sessChromRows
		if want < 1 {
			want = 1
		}
		if got := sm.listHeight(); got != want {
			t.Errorf("height %d: listHeight() = %d, want %d", h, got, want)
		}
	}
}

// TestPreviewWidthFollowsTerminal: the preview panel collapses below the
// width threshold and the list takes the whole content width.
func TestPreviewWidthFollowsTerminal(t *testing.T) {
	m := NewSessionsModel("")
	m.termWidth = 80
	if m.showPreview() || m.listWidth() != m.contentWidth() {
		t.Errorf("80 cols: preview=%v listWidth=%d, want collapsed full width", m.showPreview(), m.listWidth())
	}
	m.termWidth = 120
	if !m.showPreview() || m.listWidth() >= m.contentWidth() {
		t.Errorf("120 cols: preview=%v listWidth=%d, want side panel", m.showPreview(), m.listWidth())
	}
}

// TestWindowSizeMsgResizesFrame: a resize changes the frame the model draws.
func TestWindowSizeMsgResizesFrame(t *testing.T) {
	m := NewSessionsModel("")
	m.loading = false
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 20})
	sm := updated.(SessionsModel)
	if n := len(strings.Split(sm.View().Content, "\n")); n != 20 {
		t.Errorf("frame has %d lines, want 20", n)
	}
}

func TestSessionsModel_ApplyFilters(t *testing.T) {
	entries := []SessionEntry{
		{Session: sessions.Session{Agent: "go-engineer"}, Agent: "go-engineer", Project: "agentctl"},
		{Session: sessions.Session{Agent: "go-engineer"}, Agent: "go-engineer", Project: "agentctl"},
		{Session: sessions.Session{Agent: "python-engineer"}, Agent: "python-engineer", Project: "client-api"},
	}

	t.Run("filter to specific agent", func(t *testing.T) {
		m := NewSessionsModel("go-engineer")
		m.allEntries = entries
		m.filterAgent = "go-engineer"
		m.showAll = false
		m.applyFilters()
		if len(m.entries) != 2 {
			t.Errorf("got %d entries, want 2", len(m.entries))
		}
		for _, e := range m.entries {
			if e.Agent != "go-engineer" {
				t.Errorf("unexpected agent %q in filtered results", e.Agent)
			}
		}
	})

	t.Run("show all", func(t *testing.T) {
		m := NewSessionsModel("")
		m.allEntries = entries
		m.showAll = true
		m.applyFilters()
		if len(m.entries) != 3 {
			t.Errorf("got %d entries, want 3", len(m.entries))
		}
	})

	t.Run("cursor clamped on filter", func(t *testing.T) {
		m := NewSessionsModel("go-engineer")
		m.allEntries = entries
		m.cursor = 2 // beyond go-engineer results
		m.filterAgent = "go-engineer"
		m.showAll = false
		m.applyFilters()
		if m.cursor >= len(m.entries) {
			t.Errorf("cursor %d not clamped (len=%d)", m.cursor, len(m.entries))
		}
	})
}

func TestSessionsModel_SearchFilter(t *testing.T) {
	entries := []SessionEntry{
		{Session: sessions.Session{Agent: "go-engineer"}, Agent: "go-engineer", Project: "agentctl"},
		{Session: sessions.Session{Agent: "go-engineer"}, Agent: "go-engineer", Project: "client-api"},
		{Session: sessions.Session{Agent: "python-engineer"}, Agent: "python-engineer", Project: "data-pipeline"},
	}

	t.Run("search by project", func(t *testing.T) {
		m := NewSessionsModel("")
		m.allEntries = entries
		m.showAll = true
		m.searchQuery = "client"
		m.applyFilters()
		if len(m.entries) != 1 {
			t.Errorf("got %d entries, want 1", len(m.entries))
		}
		if m.entries[0].Project != "client-api" {
			t.Errorf("unexpected project %q", m.entries[0].Project)
		}
	})

	t.Run("search by agent", func(t *testing.T) {
		m := NewSessionsModel("")
		m.allEntries = entries
		m.showAll = true
		m.searchQuery = "python"
		m.applyFilters()
		if len(m.entries) != 1 {
			t.Errorf("got %d entries, want 1", len(m.entries))
		}
		if m.entries[0].Agent != "python-engineer" {
			t.Errorf("unexpected agent %q", m.entries[0].Agent)
		}
	})

	t.Run("search case insensitive", func(t *testing.T) {
		m := NewSessionsModel("")
		m.allEntries = entries
		m.showAll = true
		m.searchQuery = "PIPELINE"
		m.applyFilters()
		if len(m.entries) != 1 {
			t.Errorf("got %d entries, want 1", len(m.entries))
		}
	})

	t.Run("no match", func(t *testing.T) {
		m := NewSessionsModel("")
		m.allEntries = entries
		m.showAll = true
		m.searchQuery = "zzznomatch"
		m.applyFilters()
		if len(m.entries) != 0 {
			t.Errorf("got %d entries, want 0", len(m.entries))
		}
	})
}

func TestSessionsModel_AgentCycle(t *testing.T) {
	entries := []SessionEntry{
		{Session: sessions.Session{Agent: "go-engineer"}, Agent: "go-engineer"},
		{Session: sessions.Session{Agent: "python-engineer"}, Agent: "python-engineer"},
		{Session: sessions.Session{Agent: "python-engineer"}, Agent: "python-engineer"},
	}

	m := NewSessionsModel("")
	m.allEntries = entries
	m.agentOptions = []string{"go-engineer", "python-engineer"}
	m.agentIdx = -1
	m.showAll = true
	m.applyFilters()

	if len(m.entries) != 3 {
		t.Fatalf("initial: got %d entries, want 3", len(m.entries))
	}

	aKey := tea.KeyPressMsg{Code: 'a', Text: "a"}

	// Press "a" — should cycle to go-engineer
	updated, _ := m.Update(aKey)
	m = updated.(SessionsModel)
	if m.agentIdx != 0 {
		t.Errorf("after 1st 'a': agentIdx = %d, want 0", m.agentIdx)
	}
	if m.filterAgent != "go-engineer" {
		t.Errorf("after 1st 'a': filterAgent = %q, want go-engineer", m.filterAgent)
	}
	if len(m.entries) != 1 {
		t.Errorf("after 1st 'a': got %d entries, want 1", len(m.entries))
	}

	// Press "a" again — should cycle to python-engineer
	updated, _ = m.Update(aKey)
	m = updated.(SessionsModel)
	if m.agentIdx != 1 {
		t.Errorf("after 2nd 'a': agentIdx = %d, want 1", m.agentIdx)
	}
	if m.filterAgent != "python-engineer" {
		t.Errorf("after 2nd 'a': filterAgent = %q, want python-engineer", m.filterAgent)
	}
	if len(m.entries) != 2 {
		t.Errorf("after 2nd 'a': got %d entries, want 2", len(m.entries))
	}

	// Press "a" again — should wrap back to all
	updated, _ = m.Update(aKey)
	m = updated.(SessionsModel)
	if m.agentIdx != -1 {
		t.Errorf("after 3rd 'a': agentIdx = %d, want -1", m.agentIdx)
	}
	if !m.showAll {
		t.Error("after 3rd 'a': showAll should be true")
	}
	if len(m.entries) != 3 {
		t.Errorf("after 3rd 'a': got %d entries, want 3", len(m.entries))
	}
}

func TestDateGroup(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"today", now, "Today"},
		{"yesterday", now.AddDate(0, 0, -1), "Yesterday"},
		{"3 days ago", now.AddDate(0, 0, -3), "This Week"},
		{"6 days ago", now.AddDate(0, 0, -6), "This Week"},
		{"7 days ago", now.AddDate(0, 0, -7), "Older"},
		{"10 days ago", now.AddDate(0, 0, -10), "Older"},
		{"30 days ago", now.AddDate(0, 0, -30), "Older"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dateGroup(tc.t)
			if got != tc.want {
				t.Errorf("dateGroup(%v) = %q, want %q", tc.t, got, tc.want)
			}
		})
	}
}

func TestSessionsModel_SessLoadedMsg(t *testing.T) {
	entries := []SessionEntry{
		{Session: sessions.Session{Agent: "go-engineer"}, Agent: "go-engineer", Project: "agentctl"},
		{Session: sessions.Session{Agent: "python-engineer"}, Agent: "python-engineer", Project: "service"},
	}

	m := NewSessionsModel("")
	if !m.loading {
		t.Fatal("model should start in loading state")
	}

	updated, _ := m.Update(sessLoadedMsg{entries: entries})
	m = updated.(SessionsModel)

	if m.loading {
		t.Error("loading should be false after sessLoadedMsg")
	}
	if len(m.allEntries) != 2 {
		t.Errorf("allEntries: got %d, want 2", len(m.allEntries))
	}
	if len(m.agentOptions) != 2 {
		t.Errorf("agentOptions: got %d, want 2", len(m.agentOptions))
	}
	// agentOptions should be sorted
	if m.agentOptions[0] != "go-engineer" {
		t.Errorf("agentOptions[0] = %q, want go-engineer", m.agentOptions[0])
	}
	if m.agentOptions[1] != "python-engineer" {
		t.Errorf("agentOptions[1] = %q, want python-engineer", m.agentOptions[1])
	}
}

func TestSessionsModel_SessLoadedMsgError(t *testing.T) {
	m := NewSessionsModel("")
	updated, _ := m.Update(sessLoadedMsg{err: errTest})
	m = updated.(SessionsModel)

	if m.loading {
		t.Error("loading should be false after error")
	}
	if m.err == nil {
		t.Error("err should be set")
	}
}

func TestViewList_GroupHeadersDoNotSkipEntries(t *testing.T) {
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)

	// 3 sessions across 2 date groups with distinct agent names so we can verify each appears.
	entries := []SessionEntry{
		{
			Session: sessions.Session{Agent: "alpha-agent", StartTime: now},
			Agent:   "alpha-agent",
			Project: "proj-a",
		},
		{
			Session: sessions.Session{Agent: "beta-agent", StartTime: now.Add(-10 * time.Minute)},
			Agent:   "beta-agent",
			Project: "proj-b",
		},
		{
			Session: sessions.Session{Agent: "gamma-agent", StartTime: yesterday},
			Agent:   "gamma-agent",
			Project: "proj-c",
		},
	}

	m := NewSessionsModel("")
	m.termWidth = 160
	m.termHeight = 40
	m.loading = false
	m.entries = entries
	m.allEntries = entries
	m.showAll = true

	output := stripANSI(m.viewList())

	for _, e := range entries {
		if !strings.Contains(output, e.Agent) {
			t.Errorf("viewList() output missing entry agent %q; group header may have skipped it", e.Agent)
		}
	}
}

// errTest is a simple sentinel error for testing.
var errTest = &sentinelErr{"test error"}

type sentinelErr struct{ msg string }

func (e *sentinelErr) Error() string { return e.msg }

// ── New tests for enriched columns ───────────────────────────────────────────

func TestFmtTokensShort(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{9999, "10.0k"},  // 9999/1000 = 9.999 → "10.0k"
		{10000, "10.0k"}, // below 100k threshold → decimal k
		{42381, "42.4k"}, // below 100k threshold → decimal k
		{100000, "100k"}, // at 100k threshold → integer k
		{142381, "142k"},
		{999999, "999k"},
		{1_000_000, "1.0M"},
		{1_409_000, "1.4M"},
		{1_500_000, "1.5M"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := fmtTokensShort(tc.n)
			if got != tc.want {
				t.Errorf("fmtTokensShort(%d) = %q, want %q", tc.n, got, tc.want)
			}
		})
	}
}

func TestRenderCtxBar(t *testing.T) {
	// Use an empty theme so lipgloss does not inject ANSI colors.
	emptyTheme := &theme.Theme{}

	tests := []struct {
		pct  int
		want string
	}{
		{0, "░░░░░  0%"},
		{20, "▓░░░░ 20%"},
		{50, "▓▓░░░ 50%"},
		{62, "▓▓▓░░ 62%"},
		{100, "▓▓▓▓▓100%"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := stripANSI(renderCtxBar(tc.pct, emptyTheme))
			if got != tc.want {
				t.Errorf("renderCtxBar(%d) = %q, want %q", tc.pct, got, tc.want)
			}
		})
	}
}

func TestProjectFilter(t *testing.T) {
	entries := []SessionEntry{
		{Session: sessions.Session{Agent: "go-engineer"}, Agent: "go-engineer", Project: "agentctl"},
		{Session: sessions.Session{Agent: "go-engineer"}, Agent: "go-engineer", Project: "agentctl"},
		{Session: sessions.Session{Agent: "python-engineer"}, Agent: "python-engineer", Project: "client-api"},
	}

	m := NewSessionsModel("")
	m.allEntries = entries
	m.projectOptions = []string{"agentctl", "client-api"}
	m.projectIdx = -1
	m.showAll = true
	m.applyFilters()

	if len(m.entries) != 3 {
		t.Fatalf("initial: got %d entries, want 3", len(m.entries))
	}

	pKey := tea.KeyPressMsg{Code: 'p', Text: "p"}

	// Press "p" — should filter to agentctl
	updated, _ := m.Update(pKey)
	m = updated.(SessionsModel)
	if m.projectIdx != 0 {
		t.Errorf("after 1st 'p': projectIdx = %d, want 0", m.projectIdx)
	}
	if m.filterProject != "agentctl" {
		t.Errorf("after 1st 'p': filterProject = %q, want agentctl", m.filterProject)
	}
	if len(m.entries) != 2 {
		t.Errorf("after 1st 'p': got %d entries, want 2", len(m.entries))
	}

	// Press "p" again — filter to client-api
	updated, _ = m.Update(pKey)
	m = updated.(SessionsModel)
	if m.filterProject != "client-api" {
		t.Errorf("after 2nd 'p': filterProject = %q, want client-api", m.filterProject)
	}
	if len(m.entries) != 1 {
		t.Errorf("after 2nd 'p': got %d entries, want 1", len(m.entries))
	}

	// Press "p" again — wrap back to all
	updated, _ = m.Update(pKey)
	m = updated.(SessionsModel)
	if m.projectIdx != -1 {
		t.Errorf("after 3rd 'p': projectIdx = %d, want -1", m.projectIdx)
	}
	if m.filterProject != "" {
		t.Errorf("after 3rd 'p': filterProject = %q, want empty", m.filterProject)
	}
	if len(m.entries) != 3 {
		t.Errorf("after 3rd 'p': got %d entries, want 3", len(m.entries))
	}
}

func TestSplitPaneLayout(t *testing.T) {
	now := time.Now()
	sum := &sessions.Summary{
		TurnCount:         5,
		TotalInputTokens:  10000,
		TotalOutputTokens: 2000,
		ModelName:         "claude-sonnet-4-20250514",
		ToolCalls: []sessions.ToolCall{
			{Name: "Read"},
			{Name: "Edit"},
		},
	}
	entries := []SessionEntry{
		{
			Session:      sessions.Session{Agent: "go-engineer", StartTime: now, Project: "/Users/test/agentctl"},
			Summary:      sum,
			Agent:        "go-engineer",
			Project:      "agentctl",
			Turns:        5,
			InputTokens:  10000,
			OutputTokens: 2000,
			ModelShort:   "sonnet",
			Duration:     10 * time.Minute,
			CostUSD:      0.42,
		},
	}

	m := NewSessionsModel("")
	m.termWidth = 160
	m.termHeight = 50
	m.loading = false
	m.entries = entries
	m.allEntries = entries
	m.showAll = true

	output := stripANSI(m.viewList())

	// Verify list columns are present.
	listCols := []string{"AGE", "AGENT", "PROJECT", "MODEL", "DUR", "COST", "IN", "OUT", "CTX", "TURNS"}
	for _, col := range listCols {
		if !strings.Contains(output, col) {
			t.Errorf("viewList() missing list column %q", col)
		}
	}

	// Verify detail pane section headers are present.
	detailLabels := []string{"Session Details", "Token Usage"}
	for _, label := range detailLabels {
		if !strings.Contains(output, label) {
			t.Errorf("viewList() missing detail label %q", label)
		}
	}

	// Verify the agent name appears in the detail pane metadata.
	if !strings.Contains(output, "go-engineer") {
		t.Error("viewList() output missing agent name 'go-engineer'")
	}
}

func TestDetailUpdatesOnCursorMove(t *testing.T) {
	now := time.Now()

	sum1 := &sessions.Summary{TurnCount: 3, TotalInputTokens: 1000, TotalOutputTokens: 200}
	sum2 := &sessions.Summary{TurnCount: 7, TotalInputTokens: 5000, TotalOutputTokens: 800}

	entries := []SessionEntry{
		{
			Session: sessions.Session{Agent: "alpha-agent", StartTime: now, Project: "/Users/test/proj1"},
			Summary: sum1,
			Agent:   "alpha-agent",
			Project: "proj1",
			Turns:   3,
		},
		{
			Session: sessions.Session{Agent: "beta-agent", StartTime: now.Add(-5 * time.Minute), Project: "/Users/test/proj2"},
			Summary: sum2,
			Agent:   "beta-agent",
			Project: "proj2",
			Turns:   7,
		},
	}

	m := NewSessionsModel("")
	m.termWidth = 160
	m.termHeight = 50
	m.loading = false
	m.entries = entries
	m.allEntries = entries
	m.showAll = true

	// Initial render should show first entry.
	out1 := stripANSI(m.viewList())
	if !strings.Contains(out1, "alpha-agent") {
		t.Error("initial view missing alpha-agent in detail pane")
	}

	// Move cursor down.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = updated.(SessionsModel)

	out2 := stripANSI(m.viewList())
	if !strings.Contains(out2, "beta-agent") {
		t.Error("after j, view missing beta-agent in detail pane")
	}
}

// TestNoRowSeparators: rows are dense (no ╌ separator between entries); the
// only ╌ is the header rule, so a short pane shows more sessions.
func TestNoRowSeparators(t *testing.T) {
	now := time.Now()
	var entries []SessionEntry
	for i, p := range []string{"proj-a", "proj-b", "proj-c"} {
		entries = append(entries, SessionEntry{
			Session: sessions.Session{Agent: "go-engineer", StartTime: now.Add(-time.Duration(i) * time.Minute)},
			Agent:   "go-engineer",
			Project: p,
		})
	}
	m := NewSessionsModel("")
	m.termWidth, m.termHeight = 160, 60
	m.loading = false
	m.entries, m.allEntries, m.showAll = entries, entries, true

	rows := 0
	for _, l := range strings.Split(stripANSI(m.viewList()), "\n") {
		if strings.Contains(l, "╌") {
			rows++
		}
	}
	if rows != 1 {
		t.Errorf("%d lines with ╌, want only the header rule", rows)
	}
}

func TestEnterKeyOpensDetail(t *testing.T) {
	entry := SessionEntry{
		Session: sessions.Session{
			Agent:   "go-engineer",
			Project: "/Users/test/proj",
			ID:      "abc123",
		},
		Agent:   "go-engineer",
		Project: "proj",
	}

	m := NewSessionsModel("")
	m.entries = []SessionEntry{entry}
	m.allEntries = []SessionEntry{entry}
	m.showAll = true
	m.loading = false

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(SessionsModel)

	if m.viewMode != 1 {
		t.Errorf("enter key: viewMode = %d, want 1 (detail)", m.viewMode)
	}
	if m.quitting {
		t.Error("enter key: should not quit (only opens detail view)")
	}
	if m.result.Action != "" {
		t.Errorf("enter key: result.Action = %q, want empty (no action yet)", m.result.Action)
	}
}

func TestRKeyResumesSession(t *testing.T) {
	entry := SessionEntry{
		Session: sessions.Session{
			Agent:   "go-engineer",
			Project: "/Users/test/proj",
			ID:      "abc123",
		},
		Agent:   "go-engineer",
		Project: "proj",
	}

	m := NewSessionsModel("")
	m.entries = []SessionEntry{entry}
	m.allEntries = []SessionEntry{entry}
	m.showAll = true
	m.loading = false

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = updated.(SessionsModel)

	if m.result.Action != "resume" {
		t.Errorf("r key: result.Action = %q, want %q", m.result.Action, "resume")
	}
	if m.result.SessionID != "abc123" {
		t.Errorf("r key: result.SessionID = %q, want abc123", m.result.SessionID)
	}
}

func TestEscFromDetailReturnsToList(t *testing.T) {
	entry := SessionEntry{
		Session: sessions.Session{Agent: "go-engineer", Project: "/Users/test/proj", ID: "abc123"},
		Agent:   "go-engineer",
		Project: "proj",
	}

	m := NewSessionsModel("")
	m.entries = []SessionEntry{entry}
	m.allEntries = []SessionEntry{entry}
	m.showAll = true
	m.loading = false
	m.viewMode = 1 // simulate being in detail view

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape, Text: "esc"})
	m = updated.(SessionsModel)

	if m.viewMode != 0 {
		t.Errorf("esc from detail: viewMode = %d, want 0 (list)", m.viewMode)
	}
	if m.quitting {
		t.Error("esc from detail: should not quit")
	}
}

func TestRKeyResumesFromDetailView(t *testing.T) {
	entry := SessionEntry{
		Session: sessions.Session{Agent: "go-engineer", Project: "/Users/test/proj", ID: "abc123"},
		Agent:   "go-engineer",
		Project: "proj",
	}

	m := NewSessionsModel("")
	m.entries = []SessionEntry{entry}
	m.allEntries = []SessionEntry{entry}
	m.showAll = true
	m.loading = false
	m.viewMode = 1 // in detail view

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = updated.(SessionsModel)

	if m.result.Action != "resume" {
		t.Errorf("r key in detail: result.Action = %q, want resume", m.result.Action)
	}
	if m.result.SessionID != "abc123" {
		t.Errorf("r key in detail: result.SessionID = %q, want abc123", m.result.SessionID)
	}
}

func TestDetailViewContainsSessionInfo(t *testing.T) {
	now := time.Now()
	sum := &sessions.Summary{
		TurnCount:         5,
		TotalInputTokens:  10000,
		TotalOutputTokens: 2000,
		ModelName:         "claude-sonnet-4-20250514",
	}
	entry := SessionEntry{
		Session:      sessions.Session{Agent: "go-engineer", StartTime: now, Project: "/Users/test/agentctl", ID: "abc123"},
		Summary:      sum,
		Agent:        "go-engineer",
		Project:      "agentctl",
		Turns:        5,
		InputTokens:  10000,
		OutputTokens: 2000,
		ModelShort:   "sonnet",
		Duration:     10 * time.Minute,
		CostUSD:      0.42,
	}

	m := NewSessionsModel("")
	m.termWidth = 160
	m.termHeight = 50
	m.entries = []SessionEntry{entry}
	m.allEntries = []SessionEntry{entry}
	m.showAll = true
	m.loading = false
	m.viewMode = 1

	output := stripANSI(m.viewDetail())

	checks := []string{"Session Detail", "go-engineer", "Session Details", "Token Usage"}
	for _, c := range checks {
		if !strings.Contains(output, c) {
			t.Errorf("viewDetail() missing %q", c)
		}
	}

	// Hint bar should show esc/back and r/resume.
	if !strings.Contains(output, "esc") {
		t.Error("viewDetail() hint bar missing 'esc'")
	}
	if !strings.Contains(output, "back") {
		t.Error("viewDetail() hint bar missing 'back'")
	}
}

func TestListHintBarShowsDetailAndResumeSeparately(t *testing.T) {
	m := NewSessionsModel("")
	m.termWidth = 160
	m.termHeight = 50
	m.loading = false
	m.entries = nil
	m.allEntries = nil
	m.showAll = true

	output := stripANSI(m.viewList())

	if !strings.Contains(output, "detail") {
		t.Error("hint bar should show 'detail' for enter key")
	}
	if !strings.Contains(output, "resume") {
		t.Error("hint bar should show 'resume' for r key")
	}
}

// TestCursorLine: cursorLine accounts for date-group headers and grows with
// the cursor.
func TestCursorLine(t *testing.T) {
	now := time.Now()
	var entries []SessionEntry
	for i, a := range []string{"a1", "a2", "a3"} {
		entries = append(entries, SessionEntry{
			Session: sessions.Session{Agent: a, StartTime: now.Add(-time.Duration(i) * 2 * time.Minute)},
			Agent:   a, Project: "p" + a,
		})
	}
	m := NewSessionsModel("")
	m.loading = false
	m.entries, m.allEntries, m.showAll = entries, entries, true

	prev := -1
	for c := 0; c < 3; c++ {
		m.cursor = c
		cl := m.cursorLine()
		if cl <= prev {
			t.Errorf("cursor=%d: cursorLine %d should be > %d", c, cl, prev)
		}
		prev = cl
	}
	if lines := m.listLines(); len(lines) != 4 || lines[0].entry != -1 {
		t.Errorf("listLines() = %+v, want a group header then 3 rows", lines)
	}
}

// TestTotalsLine: the totals line counts the sessions shown.
func TestTotalsLine(t *testing.T) {
	entries := []SessionEntry{{
		Session:  sessions.Session{Agent: "go-engineer", StartTime: time.Now()},
		Agent:    "go-engineer",
		Project:  "agentctl",
		Duration: 5 * time.Minute,
		CostUSD:  0.10,
	}}
	m := NewSessionsModel("")
	m.loading = false
	m.entries, m.allEntries, m.showAll = entries, entries, true

	if plain := stripANSI(m.totalsLine()); !strings.Contains(plain, "1 sessions") {
		t.Errorf("totalsLine() = %q, want the session count", plain)
	}
}

// TestInfoLines: the preview/detail body names the session.
func TestInfoLines(t *testing.T) {
	sum := &sessions.Summary{TurnCount: 3, TotalInputTokens: 5000, TotalOutputTokens: 500, ModelName: "claude-3-opus"}
	entry := SessionEntry{
		Session:  sessions.Session{Agent: "go-engineer", StartTime: time.Now(), Project: "/Users/test/proj"},
		Summary:  sum,
		Agent:    "go-engineer",
		Project:  "proj",
		Duration: 3 * time.Minute,
	}
	m := NewSessionsModel("")
	m.loading = false
	m.entries, m.allEntries, m.showAll = []SessionEntry{entry}, []SessionEntry{entry}, true

	plain := stripANSI(strings.Join(m.infoLines(60), "\n"))
	for _, want := range []string{"Session Details", "go-engineer", "Token Usage"} {
		if !strings.Contains(plain, want) {
			t.Errorf("infoLines() missing %q", want)
		}
	}
}

// TestFrameFitsHeight: the model always draws exactly termHeight lines.
func TestFrameFitsHeight(t *testing.T) {
	m := NewSessionsModel("")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	sm := updated.(SessionsModel)
	if n := len(strings.Split(sm.View().Content, "\n")); n != 40 {
		t.Errorf("frame has %d lines, want 40", n)
	}
}
