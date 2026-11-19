package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// jsonl encodes records as JSONL lines.
func jsonl(t *testing.T, recs ...map[string]any) string {
	t.Helper()
	var b strings.Builder
	for _, r := range recs {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

func awaySummary(content string) map[string]any {
	return map[string]any{"type": "system", "subtype": "away_summary", "content": content, "uuid": "u"}
}

func aiTitle(title string) map[string]any {
	return map[string]any{"type": "ai-title", "aiTitle": title, "sessionId": "s"}
}

func prLink(n int, url string) map[string]any {
	return map[string]any{"type": "pr-link", "prNumber": n, "prUrl": url, "sessionId": "s"}
}

func assistant(text string) map[string]any {
	return map[string]any{"type": "assistant", "message": map[string]any{"content": text}}
}

// writeLog writes content to a fresh log file and returns its path.
func writeLog(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRecapChain(t *testing.T) {
	activity := Row{AgentState: state.AgentState{LastToolDetail: "Editing a.go"}}
	ticket := Row{AgentState: state.AgentState{TicketRef: "60", TicketTitle: "Mobile app", LastToolDetail: "Editing a.go"}}
	adhoc := Row{AgentState: state.AgentState{Task: "Fix the flaky test", LastToolDetail: "Editing a.go"}}
	tests := []struct {
		name string
		row  Row
		info LogInfo
		want RecapLine
	}{
		{"summary beats everything", ticket, LogInfo{Recap: "We are building X.", AITitle: "T"}, RecapLine{Text: "We are building X.", Source: RecapSummary}},
		{"ticket task", ticket, LogInfo{AITitle: "T"}, RecapLine{Text: "#60 Mobile app", Source: RecapTask}},
		{"ticket ref with hash", Row{AgentState: state.AgentState{TicketRef: "#7", TicketTitle: "x"}}, LogInfo{}, RecapLine{Text: "#7 x", Source: RecapTask}},
		{"ticket int only", Row{AgentState: state.AgentState{Ticket: 9, TicketTitle: "x"}}, LogInfo{}, RecapLine{Text: "#9 x", Source: RecapTask}},
		{"ad-hoc task", adhoc, LogInfo{AITitle: "T"}, RecapLine{Text: "Fix the flaky test", Source: RecapTask}},
		{"ai title", activity, LogInfo{AITitle: "Local network"}, RecapLine{Text: "Local network", Source: RecapTitle}},
		{"activity fallback", activity, LogInfo{}, RecapLine{Text: "Editing a.go", Source: RecapActivity}},
		{"nothing", Row{}, LogInfo{}, RecapLine{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Recap(tt.row, tt.info); got != tt.want {
				t.Errorf("Recap = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadLogInfo(t *testing.T) {
	const pr = "https://github.com/o/r/pull/60"
	tests := []struct {
		name    string
		content string
		want    LogInfo
	}{
		{"empty", "", LogInfo{}},
		{"no matching records", jsonl(t, assistant("hi")), LogInfo{}},
		{
			"suffix stripped and newest wins",
			jsonl(t, awaySummary("old recap (disable recaps in /config)"), assistant("x"),
				awaySummary("We are building the phone web app.\n (disable recaps in /config)")),
			LogInfo{Recap: "We are building the phone web app."},
		},
		{
			"recap without suffix",
			jsonl(t, awaySummary("Plain recap")),
			LogInfo{Recap: "Plain recap"},
		},
		{
			"pr link and title",
			jsonl(t, aiTitle("Old title"), prLink(59, "u59"), aiTitle("New title"), prLink(60, pr)),
			LogInfo{AITitle: "New title", PRNumber: 60, PRURL: pr},
		},
		{
			"partial trailing line ignored",
			jsonl(t, aiTitle("Done")) + `{"type":"ai-title","aiTitle":"Half`,
			LogInfo{AITitle: "Done"},
		},
		{
			"corrupt line skipped",
			jsonl(t, aiTitle("Good")) + `{"type":"ai-title" broken` + "\n",
			LogInfo{AITitle: "Good"},
		},
		{
			"away_summary text in other records is not a recap",
			jsonl(t, assistant(`talking about "away_summary" records`)),
			LogInfo{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReadLogInfo(writeLog(t, tt.content)); got != tt.want {
				t.Errorf("ReadLogInfo = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadLogInfo_MissingLog(t *testing.T) {
	for _, p := range []string{"", filepath.Join(t.TempDir(), "nope.jsonl"), t.TempDir()} {
		if got := ReadLogInfo(p); got != (LogInfo{}) {
			t.Errorf("ReadLogInfo(%q) = %+v, want zero", p, got)
		}
	}
}

func TestReadLogInfo_LargeLogReadsTailAndHead(t *testing.T) {
	filler := assistant(strings.Repeat("x", 1000))
	var b strings.Builder
	b.WriteString(jsonl(t, aiTitle("Head title"), awaySummary("old recap, outside the tail")))
	for b.Len() < logTailBytes+200<<10 {
		b.WriteString(jsonl(t, filler))
	}
	b.WriteString(jsonl(t, prLink(7, "u7")))
	got := ReadLogInfo(writeLog(t, b.String()))
	want := LogInfo{AITitle: "Head title", PRNumber: 7, PRURL: "u7"}
	if got != want {
		t.Errorf("ReadLogInfo = %+v, want %+v (recap outside the tail must not be found)", got, want)
	}
}

func TestReadLogInfo_CacheInvalidation(t *testing.T) {
	p := writeLog(t, jsonl(t, awaySummary("first"), aiTitle("T")))
	if got := ReadLogInfo(p).Recap; got != "first" {
		t.Fatalf("Recap = %q, want first", got)
	}

	// Unchanged size+mtime: served from the cache even if the bytes changed
	// underneath (same length, mtime restored).
	fi, _ := os.Stat(p)
	same := jsonl(t, awaySummary("FIRST"), aiTitle("T"))
	if err := os.WriteFile(p, []byte(same), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := ReadLogInfo(p).Recap; got != "first" {
		t.Errorf("unchanged size+mtime should hit the cache, got %q", got)
	}

	// Appended records: newer ones override, older cached fields survive.
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(jsonl(t, prLink(60, "u60")) + `{"type":"system","subtype":"away_su`)
	_ = f.Close()
	got := ReadLogInfo(p)
	if got.Recap != "first" || got.AITitle != "T" || got.PRNumber != 60 {
		t.Errorf("after append = %+v, want cached recap/title plus the new PR", got)
	}

	// Completing the partial line is picked up on the next read.
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`mmary","content":"second (disable recaps in /config)"}` + "\n")
	_ = f.Close()
	if got := ReadLogInfo(p).Recap; got != "second" {
		t.Errorf("completed partial line: Recap = %q, want second", got)
	}

	// Truncated/rewritten: re-read from scratch.
	if err := os.WriteFile(p, []byte(jsonl(t, aiTitle("Only"))), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadLogInfo(p); got != (LogInfo{AITitle: "Only"}) {
		t.Errorf("after rewrite = %+v, want only the new title", got)
	}

	// Removed: zero, and the cache entry is dropped.
	_ = os.Remove(p)
	if got := ReadLogInfo(p); got != (LogInfo{}) {
		t.Errorf("after removal = %+v, want zero", got)
	}
	logCacheMu.Lock()
	_, cached := logCache[p]
	logCacheMu.Unlock()
	if cached {
		t.Error("removed log should be evicted from the cache")
	}
}

func TestTagLogs(t *testing.T) {
	p := writeLog(t, jsonl(t, awaySummary("Recap here"), prLink(60, "https://x/60")))
	rows := []Row{
		{AgentState: state.AgentState{LogPath: p}},
		{AgentState: state.AgentState{LastText: "hi"}},
	}
	TagLogs(rows)
	if rows[0].Recap != (RecapLine{Text: "Recap here", Source: RecapSummary}) || rows[0].PRNumber != 60 || rows[0].PRURL != "https://x/60" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Recap != (RecapLine{Text: "hi", Source: RecapActivity}) || rows[1].PRURL != "" {
		t.Errorf("row 1 = %+v", rows[1])
	}
	if w := ToWindow(rows[0], Meta{}); w.PRURL != "https://x/60" {
		t.Errorf("ToWindow PRURL = %q", w.PRURL)
	}
}

func TestTagMetaStateSince(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	blocked := since.Add(time.Minute)
	rows := []Row{
		{AgentState: state.AgentState{WindowID: "@1", State: "working"}},
		{AgentState: state.AgentState{WindowID: "@2", State: "blocked"}},
		{AgentState: state.AgentState{WindowID: "@3", State: "blocked"}},
		{AgentState: state.AgentState{WindowID: "@4", State: "idle"}},
	}
	TagMeta(rows, map[string]Meta{
		"@1": {StateSince: since, BlockedSince: blocked},
		"@2": {StateSince: since, BlockedSince: blocked},
		"@3": {StateSince: since},
	})
	want := []time.Time{since, blocked, since, {}}
	for i, w := range want {
		if !rows[i].StateSince.Equal(w) {
			t.Errorf("row %d StateSince = %v, want %v", i, rows[i].StateSince, w)
		}
	}
}

func TestBaseName(t *testing.T) {
	tests := []struct {
		name, agent, inst, want string
	}{
		{"agent name wins", "go-engineer", "go-engineer:3", "go-engineer"},
		{"colon suffix", "", "go-engineer:3", "go-engineer"},
		{"tilde suffix", "", "improve~12", "improve"},
		{"no suffix", "", "claude", "claude"},
		{"non-numeric suffix kept", "", "a:b", "a:b"},
		{"trailing colon kept", "", "a:", "a:"},
		{"leading colon kept", "", ":3", ":3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Row{AgentState: state.AgentState{Name: tt.inst, AgentName: tt.agent}}
			if got := BaseName(r); got != tt.want {
				t.Errorf("BaseName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCompactAge(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{0, "0s"},
		{45 * time.Second, "45s"},
		{4*time.Minute + 59*time.Second, "4m"},
		{59 * time.Minute, "59m"},
		{2 * time.Hour, "2h"},
		{47 * time.Hour, "47h"},
		{72 * time.Hour, "3d"},
	}
	for _, tt := range tests {
		if got := CompactAge(tt.d); got != tt.want {
			t.Errorf("CompactAge(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestTagLogsTitles(t *testing.T) {
	const summary = "We reorganized the photo library into albums by year and started deduplicating."
	const task = "Please go through every photo in the library and sort them into albums by year"
	rows := []Row{
		{AgentState: state.AgentState{LogPath: writeLog(t, jsonl(t, awaySummary(summary))), ShortTitle: "Organizing your Photos"}},
		{AgentState: state.AgentState{Task: task, ShortTitle: "Sorting Photos by Year"}},
		// A ticket recap keeps its "#N title"...
		{AgentState: state.AgentState{TicketRef: "60", TicketTitle: "A long ticket title that stays as it is", ShortTitle: "X"}},
		// ...and so do AI titles.
		{AgentState: state.AgentState{LogPath: writeLog(t, jsonl(t, aiTitle("Some AI title"))), ShortTitle: "X"}},
		// A bare window with only an activity line shows its generated title.
		{AgentState: state.AgentState{LastText: "activity text", ShortTitle: "Fixing Hub Sort"}},
		// A ticket window whose recap is an away_summary does get the title.
		{AgentState: state.AgentState{TicketRef: "61", TicketTitle: "Ticket", LogPath: writeLog(t, jsonl(t, awaySummary(summary))), ShortTitle: "Fixing Ticket 61"}},
		// No generated title: the row shows the recap text.
		{AgentState: state.AgentState{LogPath: writeLog(t, jsonl(t, awaySummary(summary)))}},
	}
	TagLogs(rows)
	want := []string{"Organizing your Photos", "Sorting Photos by Year", "", "", "Fixing Hub Sort", "Fixing Ticket 61", ""}
	for i, w := range want {
		got := rows[i].Recap
		if got.Title != w {
			t.Errorf("row %d Title = %q, want %q (recap %+v)", i, got.Title, w, got)
		}
		if w == "" && got.Short() != got.Text {
			t.Errorf("row %d Short = %q, want the recap text", i, got.Short())
		}
		if w != "" && got.Short() != w {
			t.Errorf("row %d Short = %q, want %q", i, got.Short(), w)
		}
	}
	if rows[0].Recap.Text != summary {
		t.Errorf("full recap lost: %q", rows[0].Recap.Text)
	}
	w := ToWindow(rows[0], Meta{})
	if w.Title != "Organizing your Photos" || w.Recap != summary {
		t.Errorf("ToWindow title=%q recap=%q", w.Title, w.Recap)
	}
	if w := ToWindow(rows[2], Meta{}); w.Title != "" {
		t.Errorf("ticket row title = %q", w.Title)
	}
}

func TestAgentFromWindowCarriesShortTitle(t *testing.T) {
	recs := map[string]windowstate.WindowState{
		"@3": {State: "idle", ShortTitle: windowstate.ShortTitle{Title: "Organizing your Photos", InputHash: "h"}},
	}
	a := agentFromWindow(1, tmux.WindowInfo{ID: "@3"}, "x", recs)
	if a.ShortTitle != "Organizing your Photos" {
		t.Fatalf("ShortTitle = %q", a.ShortTitle)
	}
}

func TestTagLogsTitleStandsInForEmptyRecap(t *testing.T) {
	rows := []Row{
		{AgentState: state.AgentState{ShortTitle: "Organizing your Photos"}},
		{AgentState: state.AgentState{}},
	}
	TagLogs(rows)
	if got := rows[0].Recap; got.Short() != "Organizing your Photos" || got.Source != RecapTitle {
		t.Errorf("recap = %+v", got)
	}
	if got := rows[1].Recap; got.Source != RecapNone {
		t.Errorf("recap = %+v", got)
	}
}
