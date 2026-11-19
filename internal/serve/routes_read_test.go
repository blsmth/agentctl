package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/phoneview"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
	"github.com/blsmth/agentctl/internal/state"
)

type fakeFleet struct {
	mu     sync.Mutex
	groups []fleetwatch.Group
	err    error
}

func (f *fakeFleet) Fleet() ([]fleetwatch.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.groups, f.err
}

func (f *fakeFleet) set(g ...fleetwatch.Group) {
	f.mu.Lock()
	f.groups = g
	f.mu.Unlock()
}

type fakePanes struct {
	text       string
	err        error
	cols, rows int
	got        struct {
		session, window string
		lines           int
	}
}

func (f *fakePanes) Size(string) (int, int, error) { return f.cols, f.rows, nil }

func (f *fakePanes) Capture(session, window string, lines int) (string, error) {
	f.got.session, f.got.window, f.got.lines = session, window, lines
	return f.text, f.err
}

type fakeAgents struct{ list []AgentInfo }

func (f fakeAgents) Agents([]string) ([]AgentInfo, error) { return f.list, nil }

func testGroup() fleetwatch.Group {
	row := fleet.Row{Session: "agentctl-proj", Project: "proj", AgentState: state.AgentState{
		Name: "go-engineer", WindowID: "@523", WindowIndex: 1, State: "blocked", LastText: "waiting on you",
		LastToolDetail: "go test ./...", ContextPct: 40, ModelName: "claude-x",
	}, Recap: fleet.RecapLine{Text: "Fixing the flaky checkout test.", Source: fleet.RecapSummary}}
	meta := fleet.Meta{Mode: "dispatch", PendingTool: "Bash", BlockedSince: time.Date(2026, 11, 16, 11, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 16, 11, 30, 0, 0, time.UTC), CostUSD: 1.5}
	return fleetwatch.Group{
		Session: "agentctl-proj", Project: "proj", Dir: "/code/proj", Rollup: "blocked",
		Windows: []fleet.Window{fleet.ToWindow(row, meta)},
		Rows:    []fleet.Row{row},
		Meta:    map[string]fleet.Meta{"@523": meta},
	}
}

type readEnv struct {
	*testServer
	fleet *fakeFleet
	panes *fakePanes
	tok   string
}

func newReadEnv(t *testing.T) *readEnv {
	t.Helper()
	fleet := &fakeFleet{}
	fleet.set(testGroup())
	panes := &fakePanes{}
	dir := t.TempDir()
	clock := newClock()
	ca, err := LoadOrCreateCA(dir, "testhost", nil, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{
		Config: config.ServeConfig{}, Dir: dir, Version: "test", CA: ca, Now: clock.Now,
		Fleet: fleet, Panes: panes,
		Agents:    fakeAgents{list: []AgentInfo{{Name: "zed", Scope: "global"}, {Name: "alpha", Description: "d", Scope: "project"}}},
		Awake:     &fakeHolder{},
		PingEvery: 20 * time.Millisecond,
		BusOptions: fleetwatch.Options{
			Debounce: 10 * time.Millisecond, Rediscover: 20 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ts := &testServer{Server: s, dir: dir, clock: clock}
	_, tok := ts.pairDevice(t, "phone")
	return &readEnv{testServer: ts, fleet: fleet, panes: panes, tok: tok}
}

func (e *readEnv) get(path string) *httptest.ResponseRecorder {
	return do(e.Handler(), "GET", path, withCookie(e.tok))
}

func TestFleetJSONShape(t *testing.T) {
	e := newReadEnv(t)
	w := e.get("/v1/fleet")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got struct {
		Generated string `json:"generated"`
		Groups    []struct {
			Session string `json:"session"`
			Project string `json:"project"`
			Dir     string `json:"dir"`
			Rollup  string `json:"rollup"`
			Windows []map[string]any
		} `json:"groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339, got.Generated); err != nil {
		t.Errorf("generated = %q", got.Generated)
	}
	if len(got.Groups) != 1 {
		t.Fatalf("groups = %d", len(got.Groups))
	}
	g := got.Groups[0]
	if g.Session != "agentctl-proj" || g.Project != "proj" || g.Dir != "/code/proj" || g.Rollup != "blocked" {
		t.Errorf("group = %+v", g)
	}
	win := g.Windows[0]
	for k, want := range map[string]any{
		"window_id": "@523", "agent": "go-engineer", "state": "blocked", "mode": "dispatch",
		"activity": "go test ./...", "pending_tool": "Bash", "blocked_since": "2026-11-16T11:00:00Z",
		"context_pct": float64(40), "session": "agentctl-proj", "project": "proj",
			"recap": "Fixing the flaky checkout test.", "recap_source": "summary",
	} {
		if win[k] != want {
			t.Errorf("window[%q] = %v, want %v", k, win[k], want)
		}
	}
}

func TestFleetEmptyIsArray(t *testing.T) {
	e := newReadEnv(t)
	e.fleet.set()
	if body := e.get("/v1/fleet").Body.String(); !strings.Contains(body, `"groups":[]`) {
		t.Errorf("body = %s", body)
	}
}

func TestReadRequiresDevice(t *testing.T) {
	e := newReadEnv(t)
	for _, p := range []string{"/v1/fleet", "/v1/agents", "/v1/projects", "/v1/events", "/v1/sessions/agentctl-proj/windows/@523"} {
		if w := do(e.Handler(), "GET", p); w.Code != 401 {
			t.Errorf("%s without cookie = %d", p, w.Code)
		}
	}
}

func TestWindowEndpoint(t *testing.T) {
	e := newReadEnv(t)
	tests := []struct {
		name, path string
		code       int
	}{
		{"at-sign", "/v1/sessions/agentctl-proj/windows/@523", 200},
		{"bare number", "/v1/sessions/agentctl-proj/windows/523", 200},
		{"escaped", "/v1/sessions/agentctl-proj/windows/%40523", 200},
		{"unknown window", "/v1/sessions/agentctl-proj/windows/@999", 404},
		{"unknown session", "/v1/sessions/nope/windows/@523", 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := e.get(tt.path)
			if w.Code != tt.code {
				t.Fatalf("status %d, want %d: %s", w.Code, tt.code, w.Body)
			}
			if tt.code == 404 {
				var ae apiError
				_ = json.Unmarshal(w.Body.Bytes(), &ae)
				if ae.Code != "not_found" {
					t.Errorf("error = %+v", ae)
				}
				return
			}
			var body struct {
				WindowID string `json:"window_id"`
				Detail   struct {
					Summary      string  `json:"summary"`
					LastText     string  `json:"last_activity_text"`
					LastActivity string  `json:"last_activity"`
					PendingTool  string  `json:"pending_tool"`
					BlockedSince string  `json:"blocked_since"`
					CostUSD      float64 `json:"cost_usd"`
				} `json:"detail"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			d := body.Detail
			if body.WindowID != "@523" || d.Summary != "Fixing the flaky checkout test." || d.LastText != "waiting on you" || d.PendingTool != "Bash" ||
				d.BlockedSince != "2026-11-16T11:00:00Z" || d.LastActivity != "2026-11-16T11:30:00Z" || d.CostUSD != 1.5 {
				t.Errorf("body = %+v", body)
			}
		})
	}
}

const dialogPane = "\x1b[2m● Bash(go test ./...)\x1b[0m\n" +
	"╭────────────────────────────────────────────────╮\n" +
	"│ Bash command                                   │\n" +
	"│   go test ./...                                │\n" +
	"│ Do you want to proceed?                        │\n" +
	"│ \x1b[36m❯ 1. Yes\x1b[0m                                       │\n" +
	"│   2. No, and tell Claude what to do differently│\n" +
	"╰────────────────────────────────────────────────╯\n"

func TestPaneEndpoint(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		verdict     string
		wantDialog  bool
		wantOptions int
	}{
		{"dialog", dialogPane, "awaiting_input", true, 2},
		{"idle", "──── a ──\n❯\n──────\n", "idle", false, 0},
		{"working", "· Twisting… (10m 5s · ↓ 3k tokens)\n❯\n  esc to interrupt\n", "working", false, 0},
		{"unknown", "hello\n", "unknown", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newReadEnv(t)
			e.panes.text = tt.text
			w := e.get("/v1/sessions/agentctl-proj/windows/523/pane?lines=50")
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if e.panes.got.session != "agentctl-proj" || e.panes.got.window != "@523" || e.panes.got.lines != 50 {
				t.Errorf("capture args = %+v", e.panes.got)
			}
			var body struct {
				Text    string `json:"text"`
				Verdict string `json:"verdict"`
				Dialog  *struct {
					Text    string `json:"text"`
					Hash    string `json:"hash"`
					Options []struct{ Key, Label string }
				} `json:"dialog"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Text != tt.text {
				t.Error("text must be returned verbatim, ANSI included")
			}
			if body.Verdict != tt.verdict {
				t.Errorf("verdict = %q, want %q", body.Verdict, tt.verdict)
			}
			if (body.Dialog != nil) != tt.wantDialog {
				t.Fatalf("dialog = %+v", body.Dialog)
			}
			if body.Dialog != nil {
				if len(body.Dialog.Options) != tt.wantOptions || body.Dialog.Options[0].Key != "1" || len(body.Dialog.Hash) != 64 {
					t.Errorf("dialog = %+v", body.Dialog)
				}
			}
		})
	}
}

func TestPaneEndpointErrors(t *testing.T) {
	e := newReadEnv(t)
	if w := e.get("/v1/sessions/agentctl-proj/windows/@523/pane?lines=abc"); w.Code != 400 {
		t.Errorf("bad lines = %d", w.Code)
	}
	if w := e.get("/v1/sessions/agentctl-proj/windows/@9/pane"); w.Code != 404 {
		t.Errorf("unknown window = %d", w.Code)
	}
	e.get("/v1/sessions/agentctl-proj/windows/@523/pane?lines=999999")
	if e.panes.got.lines != maxPaneLines {
		t.Errorf("lines not clamped: %d", e.panes.got.lines)
	}
	e.panes.err = errors.New("gone")
	if w := e.get("/v1/sessions/agentctl-proj/windows/@523/pane"); w.Code != http.StatusBadGateway {
		t.Errorf("capture failure = %d", w.Code)
	}
}

func TestAgentsAndProjects(t *testing.T) {
	e := newReadEnv(t)
	var agents struct {
		Agents []AgentInfo `json:"agents"`
	}
	if err := json.Unmarshal(e.get("/v1/agents").Body.Bytes(), &agents); err != nil {
		t.Fatal(err)
	}
	if len(agents.Agents) != 2 || agents.Agents[0].Name != "alpha" || agents.Agents[0].Scope != "project" {
		t.Errorf("agents = %+v", agents.Agents)
	}
	var projects struct {
		Projects []projectInfo `json:"projects"`
	}
	if err := json.Unmarshal(e.get("/v1/projects").Body.Bytes(), &projects); err != nil {
		t.Fatal(err)
	}
	want := projectInfo{Name: "proj", Dir: "/code/proj", Session: "agentctl-proj"}
	if len(projects.Projects) != 1 || projects.Projects[0] != want {
		t.Errorf("projects = %+v", projects.Projects)
	}
}

func TestHealthzFeatures(t *testing.T) {
	e := newReadEnv(t)
	var h healthz
	if err := json.Unmarshal(do(e.Handler(), "GET", "/healthz").Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	have := strings.Join(h.Features, ",")
	if !strings.Contains(have, "fleet") || !strings.Contains(have, "events") {
		t.Errorf("features = %v", h.Features)
	}
}

// sseReader reads events from a live SSE response.
type sseReader struct {
	sc *bufio.Scanner
}

type sseEvent struct{ name, data string }

func (r *sseReader) next(t *testing.T) (sseEvent, bool) {
	t.Helper()
	var ev sseEvent
	for r.sc.Scan() {
		line := r.sc.Text()
		switch {
		case line == "":
			if ev.name != "" {
				return ev, true
			}
		case strings.HasPrefix(line, ": "):
			return sseEvent{name: "comment", data: line[2:]}, true
		case strings.HasPrefix(line, "event: "):
			ev.name = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			ev.data = line[len("data: "):]
		}
	}
	return ev, false
}

func TestSSEInitialFleetUpdateGoneAndPing(t *testing.T) {
	e := newReadEnv(t)
	e.Start(context.Background())

	srv := httptest.NewServer(e.Handler())
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/events", nil)
	req.AddCookie(&http.Cookie{Name: DeviceCookie, Value: e.tok})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	r := &sseReader{sc: bufio.NewScanner(resp.Body)}

	nextNamed := func(name string) sseEvent {
		t.Helper()
		for i := 0; i < 200; i++ {
			ev, ok := r.next(t)
			if !ok {
				t.Fatalf("stream ended waiting for %q", name)
			}
			if ev.name == name {
				return ev
			}
		}
		t.Fatalf("no %q event", name)
		return sseEvent{}
	}

	first := nextNamed("fleet")
	if !strings.Contains(first.data, `"window_id":"@523"`) {
		t.Errorf("initial fleet = %s", first.data)
	}
	nextNamed("comment") // ping

	// Working: a rendered field changes -> window event.
	g := testGroup()
	g.Windows[0].State = "working"
	g.Windows[0].Activity = "Editing y.go"
	e.fleet.set(g)
	ev := nextNamed("window")
	if !strings.Contains(ev.data, `"state":"working"`) || !strings.Contains(ev.data, "Editing y.go") {
		t.Errorf("window event = %s", ev.data)
	}

	// Window disappears -> gone (after the confirmation refresh).
	e.fleet.set(fleetwatch.Group{Session: "agentctl-proj", Project: "proj", Dir: "/code/proj"})
	gone := nextNamed("gone")
	if !strings.Contains(gone.data, `"window_id":"@523"`) || !strings.Contains(gone.data, `"session":"agentctl-proj"`) {
		t.Errorf("gone event = %s", gone.data)
	}

	cancel() // client disconnect ends the handler
}

func TestSSEEndsOnServerClose(t *testing.T) {
	e := newReadEnv(t)
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/events") // no cookie
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/events", nil)
	req.AddCookie(&http.Cookie{Name: DeviceCookie, Value: e.tok})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := &sseReader{sc: bufio.NewScanner(resp.Body)}
	if ev, ok := r.next(t); !ok || ev.name != "fleet" {
		t.Fatalf("first event = %+v", ev)
	}
	e.Close()
	done := make(chan struct{})
	go func() {
		for {
			if _, ok := r.next(t); !ok {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream still open after Close")
	}
}

func TestPaneEndpointSize(t *testing.T) {
	e := newReadEnv(t)
	e.panes.text = "hello\n"
	e.panes.cols, e.panes.rows = 180, 50
	w := e.get("/v1/sessions/agentctl-proj/windows/523/pane")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var body struct{ Cols, Rows int }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Cols != 180 || body.Rows != 50 {
		t.Errorf("size = %dx%d, want 180x50", body.Cols, body.Rows)
	}
}

// The pane endpoint and the answer guard both go through buildPaneBody, so
// the same capture must yield the same dialog hash on both paths.
func TestPaneAndAnswerHashSameDialog(t *testing.T) {
	e := newReadEnv(t)
	e.panes.text = dialogPane
	w := e.get("/v1/sessions/agentctl-proj/windows/523/pane")
	var viaPane struct {
		Dialog struct{ Hash string } `json:"dialog"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &viaPane); err != nil {
		t.Fatal(err)
	}
	guard := buildPaneBody(e.panes.text)
	if guard.Dialog == nil || guard.Dialog.Hash == "" || guard.Dialog.Hash != viaPane.Dialog.Hash {
		t.Errorf("guard hash %+v != pane hash %q", guard.Dialog, viaPane.Dialog.Hash)
	}
}

func TestPaneEndpointMarksPhoneView(t *testing.T) {
	e := newReadEnv(t)
	e.term.markers = t.TempDir()
	if phoneview.Active(e.term.markers, "@523") {
		t.Fatal("marker before any pane request")
	}
	if w := e.get("/v1/sessions/agentctl-proj/windows/@9/pane"); w.Code != 404 {
		t.Fatalf("unknown window = %d", w.Code)
	}
	if phoneview.Active(e.term.markers, "@9") {
		t.Fatal("marker written for a window that does not exist")
	}
	if w := e.get("/v1/sessions/agentctl-proj/windows/@523/pane"); w.Code != 200 {
		t.Fatalf("pane = %d", w.Code)
	}
	if !phoneview.Active(e.term.markers, "@523") {
		t.Fatal("pane request wrote no marker")
	}
	// Phone vanished: the marker ages out on its own.
	old := time.Now().Add(-2 * phoneview.PaneTTL)
	entries, _ := os.ReadDir(filepath.Join(e.term.markers, "@523"))
	if len(entries) != 1 {
		t.Fatalf("markers = %d, want 1", len(entries))
	}
	if err := os.Chtimes(filepath.Join(e.term.markers, "@523", entries[0].Name()), old, old); err != nil {
		t.Fatal(err)
	}
	if phoneview.Active(e.term.markers, "@523") {
		t.Fatal("stale marker still active")
	}
}
