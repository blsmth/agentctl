package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// followProject is a temp project runtime dir with one agent window: its
// windowstate record, transcript and per-session agentctl log.
type followProject struct {
	runtimeDir string
	logPath    string
	sessionLog string
}

// newFollowProject writes a project whose window windowID runs session sid
// and has said each of texts (one assistant text entry each).
func newFollowProject(t *testing.T, windowID, sid, title string, texts ...string) followProject {
	t.Helper()
	rt := filepath.Join(t.TempDir(), ".agentctl")
	wdir := windowstate.WindowsDirFor(rt)
	sdir := filepath.Join(rt, "logs", "sessions")
	for _, d := range []string{wdir, sdir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var jsonl strings.Builder
	for _, txt := range texts {
		jsonl.WriteString(`{"type":"assistant","message":{"model":"claude-x","content":[{"type":"text","text":"` + txt + `"}]}}` + "\n")
	}
	logPath := writeTestLog(t, t.TempDir(), sid+".jsonl", jsonl.String())
	rec := `{"state":"working","session_id":"` + sid + `","log_path":"` + logPath + `","short_title":{"title":"` + title + `"}}`
	if err := os.WriteFile(filepath.Join(wdir, windowID+".json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionLog := writeTestLog(t, sdir, sid+".log", "")
	return followProject{runtimeDir: rt, logPath: logPath, sessionLog: sessionLog}
}

// fakeFollow is a scripted selection source and tmux window table.
type fakeFollow struct {
	sel     HubSelection
	err     error
	windows map[string]string // "session|windowID" -> window name
}

func (f *fakeFollow) model() HudModel {
	m := newHudModel("")
	m.follow = hudFollow{
		enabled:    true,
		read:       func() (HubSelection, error) { return f.sel, f.err },
		windowName: func(session, id string) string { return f.windows[session+"|"+id] },
	}
	return m
}

func entryTexts(m HudModel) []string {
	var out []string
	for _, e := range m.logEntries {
		out = append(out, e.FullText)
	}
	return out
}

func TestFollowHudRetargetResetsState(t *testing.T) {
	a := newFollowProject(t, "@1", "sess-a", "Fix the parser", "alpha one", "alpha two")
	b := newFollowProject(t, "@1", "sess-b", "Write docs", "bravo")
	f := &fakeFollow{
		sel:     HubSelection{Session: "agentctl-a", WindowID: "@1", RuntimeDir: a.runtimeDir, Name: "go-engineer"},
		windows: map[string]string{"agentctl-a|@1": "go-engineer", "agentctl-b|@1": "● writer"},
	}
	m := f.model()
	m.poll()

	if got := entryTexts(m); len(got) != 2 || got[0] != "alpha one" {
		t.Fatalf("entries after following A = %v", got)
	}
	if m.agentName != "go-engineer" || m.title != "Fix the parser" || m.state != "working" {
		t.Errorf("A header: name=%q title=%q state=%q", m.agentName, m.title, m.state)
	}
	if m.agentctlLogPath != a.sessionLog {
		t.Errorf("A agentctl log = %q, want %q (target's runtime dir)", m.agentctlLogPath, a.sessionLog)
	}

	// Per-target UI state the retarget must drop.
	m.streamCursor = 1
	m.expandedEntries[1] = true
	m.streamAtBottom = false
	m.agentLogAtBottom = false
	m.scrollTop = 1
	m.done = true

	// Same window ID, other project: still a new target.
	f.sel = HubSelection{Session: "agentctl-b", WindowID: "@1", RuntimeDir: b.runtimeDir}
	m.poll()

	if got := entryTexts(m); len(got) != 1 || got[0] != "bravo" {
		t.Errorf("entries after following B = %v, want only B's", got)
	}
	if m.logPath != b.logPath {
		t.Errorf("logPath = %q, want %q", m.logPath, b.logPath)
	}
	if m.streamCursor != -1 || len(m.expandedEntries) != 0 || !m.streamAtBottom || !m.agentLogAtBottom || m.scrollTop != 0 {
		t.Errorf("UI state not reset: cursor=%d expanded=%v atBottom=%v/%v scroll=%d",
			m.streamCursor, m.expandedEntries, m.streamAtBottom, m.agentLogAtBottom, m.scrollTop)
	}
	if m.done {
		t.Error("done carried over from A")
	}
	if m.agentName != "writer" {
		t.Errorf("agentName = %q, want the base name from B's window", m.agentName)
	}
	if m.title != "Write docs" {
		t.Errorf("title = %q", m.title)
	}
	if m.agentctlSessionID != "sess-b" || m.agentctlLogPath != b.sessionLog {
		t.Errorf("agentctl log = %q (session %q), want %q", m.agentctlLogPath, m.agentctlSessionID, b.sessionLog)
	}
	if want := filepath.Join(b.runtimeDir, "logs", "agentctl.log"); m.sysLogPath != want {
		t.Errorf("sysLogPath = %q, want %q", m.sysLogPath, want)
	}

	// An unchanged selection keeps state (no re-read from the start).
	m.streamCursor = 0
	m.poll()
	if m.streamCursor != 0 || len(m.logEntries) != 1 {
		t.Errorf("unchanged selection reset state: cursor=%d entries=%d", m.streamCursor, len(m.logEntries))
	}
}

func TestFollowHudPlaceholder(t *testing.T) {
	a := newFollowProject(t, "@1", "sess-a", "", "alpha")
	valid := HubSelection{Session: "agentctl-a", WindowID: "@1", RuntimeDir: a.runtimeDir}
	tests := []struct {
		name string
		sel  HubSelection
		err  error
		live bool // the window exists in tmux
		want string
	}{
		{name: "no selection", live: true, want: "Select an agent in the hub"},
		{name: "header row (session only)", sel: HubSelection{Session: "agentctl-a"}, live: true, want: "Select an agent in the hub"},
		{name: "no runtime dir", sel: HubSelection{Session: "agentctl-a", WindowID: "@1"}, live: true, want: "Select an agent in the hub"},
		{name: "corrupt selection file", sel: valid, err: errors.New("parsing"), live: true, want: "Hub selection unavailable"},
		{name: "window disappeared", sel: valid, want: "Window closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeFollow{sel: tt.sel, err: tt.err, windows: map[string]string{}}
			if tt.live {
				f.windows["agentctl-a|@1"] = "go-engineer"
			}
			m := f.model()
			m.poll()
			m.poll() // a vanished window needs two consecutive misses
			if !m.followIdle() {
				t.Fatalf("followIdle = false, target %q", m.windowID)
			}
			if len(m.logEntries) != 0 || m.logPath != "" {
				t.Errorf("placeholder has a log: %q entries=%d", m.logPath, len(m.logEntries))
			}
			for _, hint := range []string{m.emptyStreamHint(), m.emptyAgentLogsHint(), m.emptySysLogsHint()} {
				if !strings.HasPrefix(hint, tt.want) {
					t.Errorf("hint = %q, want prefix %q", hint, tt.want)
				}
			}
			if strings.Contains(m.renderHeader(60), "Idle") {
				t.Errorf("placeholder header shows a state badge: %q", m.renderHeader(60))
			}
		})
	}
}

func TestFollowHudGoneWindowStaysPlaceholderUntilNewSelection(t *testing.T) {
	a := newFollowProject(t, "@1", "sess-a", "", "alpha")
	b := newFollowProject(t, "@2", "sess-b", "", "bravo")
	f := &fakeFollow{
		sel:     HubSelection{Session: "s", WindowID: "@1", RuntimeDir: a.runtimeDir},
		windows: map[string]string{"s|@1": "one", "s|@2": "two"},
	}
	m := f.model()
	m.poll()
	if m.windowID != "@1" {
		t.Fatalf("target = %q", m.windowID)
	}
	delete(f.windows, "s|@1")
	for i := 0; i < followAliveEvery*2; i++ {
		m.poll()
	}
	if !m.followIdle() || !m.follow.gone {
		t.Fatalf("vanished window still followed: target=%q gone=%v", m.windowID, m.follow.gone)
	}
	f.sel = HubSelection{Session: "s", WindowID: "@2", RuntimeDir: b.runtimeDir}
	m.poll()
	if m.windowID != "@2" || m.follow.gone {
		t.Errorf("new selection not followed: target=%q gone=%v", m.windowID, m.follow.gone)
	}
	if got := entryTexts(m); len(got) != 1 || got[0] != "bravo" {
		t.Errorf("entries = %v", got)
	}
}

func TestFollowHudSkipsAgentNameScan(t *testing.T) {
	rt := filepath.Join(t.TempDir(), ".agentctl")
	if err := os.MkdirAll(windowstate.WindowsDirFor(rt), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fakeFollow{
		sel:     HubSelection{Session: "s", WindowID: "@5", RuntimeDir: rt, Name: "go-engineer"},
		windows: map[string]string{"s|@5": "go-engineer"},
	}
	m := f.model()
	for i := 0; i < 15; i++ {
		m.poll()
	}
	if m.logPath != "" {
		t.Errorf("logPath = %q: follow mode must not scan for logs by agent name", m.logPath)
	}
	if m.agentctlLogPath != filepath.Join(rt, "logs", "agentctl.log") || !m.agentctlLogIsGlobal {
		t.Errorf("agentctl log = %q global=%v, want the target project's agentctl.log", m.agentctlLogPath, m.agentctlLogIsGlobal)
	}
}

func TestFollowStartOffset(t *testing.T) {
	dir := t.TempDir()
	small := writeTestLog(t, dir, "small.jsonl", "{}\n")
	big := filepath.Join(dir, "big.jsonl")
	if err := os.WriteFile(big, make([]byte, followTailBytes+100), 0o600); err != nil {
		t.Fatal(err)
	}
	follow := (&fakeFollow{}).model()
	window := newHudModel("@1")
	tests := []struct {
		name string
		m    HudModel
		path string
		want int64
	}{
		{"follow, small log", follow, small, 0},
		{"follow, big log", follow, big, 100},
		{"follow, missing log", follow, filepath.Join(dir, "nope"), 0},
		{"per-window reads from the start", window, big, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.followStartOffset(tt.path); got != tt.want {
				t.Errorf("offset = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestReadHubSelection(t *testing.T) {
	dir := t.TempDir()
	want := HubSelection{Session: "s", WindowID: "@3", RuntimeDir: "/p/.agentctl", Name: "n", Title: "t"}
	good := filepath.Join(dir, "good.json")
	if err := writeHubSelection(good, want); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(good); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", fi.Mode(), err)
	}
	corrupt := writeTestLog(t, dir, "corrupt.json", "{not json")

	tests := []struct {
		name    string
		path    string
		want    HubSelection
		wantErr bool
	}{
		{"round trip", good, want, false},
		{"missing", filepath.Join(dir, "missing.json"), HubSelection{}, false},
		{"corrupt", corrupt, HubSelection{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readHubSelection(tt.path)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("got %+v, %v; want %+v, err %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestFleetHubSelection(t *testing.T) {
	g := fleet.Group{
		Session:    "agentctl-p",
		RuntimeDir: "/p/.agentctl",
		Rows: []fleet.Row{
			{Session: "agentctl-p", AgentState: state.AgentState{Name: "go-engineer~2", WindowID: "@1"}, Recap: fleet.RecapLine{Title: "Fix it"}},
			{Session: "agentctl-p", AgentState: state.AgentState{Name: "shell", WindowID: "@2"}, Shell: true},
		},
	}
	noDir := g
	noDir.RuntimeDir = ""
	tests := []struct {
		name     string
		groups   []fleet.Group
		session  string
		windowID string
		header   bool
		want     HubSelection
	}{
		{"agent row", []fleet.Group{g}, "agentctl-p", "@1", false,
			HubSelection{Session: "agentctl-p", WindowID: "@1", RuntimeDir: "/p/.agentctl", Name: "go-engineer~2", Title: "Fix it"}},
		{"shell row", []fleet.Group{g}, "agentctl-p", "@2", false, HubSelection{}},
		{"header", []fleet.Group{g}, "agentctl-p", "", true, HubSelection{}},
		{"nothing", []fleet.Group{g}, "", "", false, HubSelection{}},
		{"row gone", []fleet.Group{g}, "agentctl-p", "@9", false, HubSelection{}},
		{"no runtime dir", []fleet.Group{noDir}, "agentctl-p", "@1", false, HubSelection{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newFleetModel()
			m.groups = tt.groups
			m.selSession, m.selWindowID, m.selIsHeader = tt.session, tt.windowID, tt.header
			if got := m.hubSelection(); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFleetPublishSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub-selection.json")
	orig := hubSelectionPath
	hubSelectionPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { hubSelectionPath = orig })

	g := fleet.Group{Session: "s", RuntimeDir: "/p/.agentctl", Rows: []fleet.Row{
		{Session: "s", AgentState: state.AgentState{Name: "a", WindowID: "@1"}},
		{Session: "s", AgentState: state.AgentState{Name: "b", WindowID: "@2"}},
	}}
	m := newFleetModel()
	m.groups = []fleet.Group{g}
	m.selSession, m.selWindowID = "s", "@1"

	m.publishSelection()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("published before the list loaded: %v", err)
	}

	m.loaded = true
	m.publishSelection()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("published with the activity pane closed: %v", err)
	}

	m.activity.pane = "%9"
	m.publishSelection()
	if got, _ := readHubSelection(path); got.WindowID != "@1" {
		t.Fatalf("published %+v", got)
	}

	// Unchanged selection: no rewrite (a removed file stays removed).
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	m.publishSelection()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("unchanged selection rewritten: %v", err)
	}

	m.selWindowID = "@2"
	m.publishSelection()
	if got, _ := readHubSelection(path); got.WindowID != "@2" {
		t.Errorf("after move published %+v", got)
	}

	m.selWindowID, m.selIsHeader = "", true
	m.publishSelection()
	if got, err := readHubSelection(path); err != nil || got != (HubSelection{}) {
		t.Errorf("header selection published %+v, %v; want the zero selection", got, err)
	}
}

func TestFollowHudSingleLivenessMissKeepsTarget(t *testing.T) {
	a := newFollowProject(t, "@1", "sess-a", "", "alpha")
	f := &fakeFollow{
		sel:     HubSelection{Session: "s", WindowID: "@1", RuntimeDir: a.runtimeDir},
		windows: map[string]string{"s|@1": "one"},
	}
	m := f.model()
	m.poll()
	if m.windowID != "@1" {
		t.Fatalf("target = %q", m.windowID)
	}

	// One failed lookup (a transient tmux error), then the window answers.
	delete(f.windows, "s|@1")
	for i := 0; i < followAliveEvery && m.follow.misses == 0; i++ {
		m.poll()
	}
	if m.follow.misses != 1 || m.followIdle() {
		t.Fatalf("after one miss: misses=%d idle=%v", m.follow.misses, m.followIdle())
	}
	f.windows["s|@1"] = "one"
	m.poll()
	if m.followIdle() || m.follow.misses != 0 {
		t.Fatalf("recovered window dropped: idle=%v misses=%d", m.followIdle(), m.follow.misses)
	}
	if got := entryTexts(m); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("entries reset by a transient miss: %v", got)
	}

	// Two consecutive misses drop it.
	delete(f.windows, "s|@1")
	for i := 0; i < followAliveEvery*2 && !m.followIdle(); i++ {
		m.poll()
	}
	if !m.followIdle() || !m.follow.gone {
		t.Errorf("window gone for two checks still followed: target=%q", m.windowID)
	}
}

func TestFollowHudRetargetsOnClear(t *testing.T) {
	a := newFollowProject(t, "@1", "sess-a", "", "before clear")
	f := &fakeFollow{
		sel:     HubSelection{Session: "s", WindowID: "@1", RuntimeDir: a.runtimeDir},
		windows: map[string]string{"s|@1": "one"},
	}
	m := f.model()
	m.poll()
	if got := entryTexts(m); len(got) != 1 || got[0] != "before clear" {
		t.Fatalf("entries = %v", got)
	}
	if m.agentctlSessionID != "sess-a" {
		t.Fatalf("session id = %q", m.agentctlSessionID)
	}
	m.streamCursor = 0
	m.expandedEntries[0] = true

	// /clear: the same window now records a new session and transcript.
	newLog := writeTestLog(t, t.TempDir(), "sess-c.jsonl",
		`{"type":"assistant","message":{"model":"claude-x","content":[{"type":"text","text":"after clear"}]}}`+"\n")
	writeTestLog(t, filepath.Join(a.runtimeDir, "logs", "sessions"), "sess-c.log", "")
	rec := `{"state":"working","session_id":"sess-c","log_path":"` + newLog + `"}`
	if err := os.WriteFile(filepath.Join(windowstate.WindowsDirFor(a.runtimeDir), "@1.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	m.poll()
	if m.logPath != newLog {
		t.Errorf("logPath = %q, want %q", m.logPath, newLog)
	}
	if got := entryTexts(m); len(got) != 1 || got[0] != "after clear" {
		t.Errorf("entries after /clear = %v", got)
	}
	if m.agentctlSessionID != "sess-c" {
		t.Errorf("agentctl session id = %q, want sess-c", m.agentctlSessionID)
	}
	if m.streamCursor != -1 || len(m.expandedEntries) != 0 {
		t.Errorf("cursor/expansion leaked across /clear: %d %v", m.streamCursor, m.expandedEntries)
	}
}

func TestTailLogSanitizesText(t *testing.T) {
	jsonl := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi \u001b[31mred\u001b[0m\u0007 there"}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"text","text":"\u001b]0;evil\u0007ok\r\nnext"}]}}` + "\n"
	p := writeTestLog(t, t.TempDir(), "s.jsonl", jsonl)
	res, err := TailLog(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 2 {
		t.Fatalf("entries = %d", len(res.Entries))
	}
	want := []string{"hi red there", "ok\nnext"}
	for i, e := range res.Entries {
		if e.FullText != want[i] {
			t.Errorf("entry %d FullText = %q, want %q", i, e.FullText, want[i])
		}
		if strings.ContainsAny(e.Summary, "\x1b\x07") {
			t.Errorf("entry %d summary keeps control chars: %q", i, e.Summary)
		}
	}
}
