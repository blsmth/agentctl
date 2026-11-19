package serve

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/panestate"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
)

const (
	idlePane    = "──── a ──\n❯\n──────\n"
	workingPane = "· Twisting… (10m 5s · ↓ 3k tokens)\n❯\n  esc to interrupt\n"
)

// syncPanes is a concurrency-safe PaneSource whose text can change between
// captures (onCapture runs after each capture).
type syncPanes struct {
	mu        sync.Mutex
	text      string
	err       error
	captures  int
	onCapture func(n int)

	mainPaneErr   error
	mainPaneCalls int
	capturedPanes []string
}

func (p *syncPanes) Capture(string, string, int) (string, error) {
	p.mu.Lock()
	p.captures++
	n, text, err, hook := p.captures, p.text, p.err, p.onCapture
	p.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return text, err
}

// testMainPane is the main pane syncPanes reports for every window.
const testMainPane = "%7"

// MainPane makes syncPanes the writes' MainPanes too.
func (p *syncPanes) MainPane(_, _, _, _ string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mainPaneCalls++
	return testMainPane, p.mainPaneErr
}

// CapturePane records the pane a write captured and returns the text.
func (p *syncPanes) CapturePane(paneID string, lines int) (string, error) {
	p.mu.Lock()
	p.capturedPanes = append(p.capturedPanes, paneID)
	p.mu.Unlock()
	return p.Capture("", "", lines)
}

func (p *syncPanes) set(text string) {
	p.mu.Lock()
	p.text = text
	p.mu.Unlock()
}

type sentKey struct {
	target WindowTarget
	key    string
}

type fakeKeys struct {
	mu     sync.Mutex
	sent   []sentKey
	inside atomic.Int32
	maxIn  atomic.Int32
	delay  time.Duration
	onSend func()
}

func (f *fakeKeys) SendKey(t WindowTarget, key string) error {
	n := f.inside.Add(1)
	defer f.inside.Add(-1)
	for {
		m := f.maxIn.Load()
		if n <= m || f.maxIn.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(f.delay)
	if f.onSend != nil {
		f.onSend()
	}
	f.mu.Lock()
	f.sent = append(f.sent, sentKey{t, key})
	f.mu.Unlock()
	return nil
}

func (f *fakeKeys) all() []sentKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentKey(nil), f.sent...)
}

type fakeInjector struct {
	mu   sync.Mutex
	got  []string
	err  error
	tgts []WindowTarget
}

func (f *fakeInjector) Inject(t WindowTarget, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, text)
	f.tgts = append(f.tgts, t)
	return nil
}

type fakeCloser struct {
	mu     sync.Mutex
	closed []WindowTarget
}

func (f *fakeCloser) CloseWindow(t WindowTarget) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, t)
	return nil
}

type fakeDispatcher struct {
	mu  sync.Mutex
	got []DispatchRequest
}

func (f *fakeDispatcher) Dispatch(req DispatchRequest) (DispatchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, req)
	return DispatchResult{WindowID: "@900", Session: req.Session, Branch: "agent/go-engineer/adhoc-1234"}, nil
}

type writeEnv struct {
	*testServer
	fleet  *fakeFleet
	panes  *syncPanes
	keys   *fakeKeys
	inj    *fakeInjector
	closer *fakeCloser
	disp   *fakeDispatcher
	sweep  *fakeSweeper
	tok    string
	dev    Device
}

// writeGroup is testGroup with a runtime dir and the window's state set.
func writeGroup(st string, done bool, name, mode string) fleetwatch.Group {
	row := fleet.Row{Session: "agentctl-proj", Project: "proj", AgentState: state.AgentState{
		Name: name, WindowID: "@523", WindowIndex: 1, State: st, Done: done,
	}}
	meta := fleet.Meta{WindowID: "@523", Mode: mode}
	return fleetwatch.Group{
		Session: "agentctl-proj", Key: "/code/proj", Project: "proj", Dir: "/code/proj", Rollup: st,
		RuntimeDir: "/code/proj/.agentctl",
		Windows:    []fleet.Window{fleet.ToWindow(row, meta)},
		Rows:       []fleet.Row{row},
		Meta:       map[string]fleet.Meta{"@523": meta},
	}
}

// piGroup is writeGroup for a pi window.
func piGroup(st string) fleetwatch.Group {
	g := writeGroup(st, false, "pi", "bare")
	g.Rows[0].Harness = "pi"
	return g
}

// TestTextToPi: pi's pane cannot be classified, so the recorded state
// decides reply and queue ("" counts as idle) and the inject skips the pane
// guard.
func TestTextToPi(t *testing.T) {
	tests := []struct {
		name, route, state string
		status             int
	}{
		{"reply idle", "reply", "idle", 200},
		{"reply empty state", "reply", "", 200},
		{"reply working", "reply", "working", 409},
		{"reply blocked", "reply", "blocked", 409},
		{"queue idle", "queue", "idle", 200},
		{"queue empty state", "queue", "", 200},
		{"queue working", "queue", "working", 200},
		{"queue blocked", "queue", "blocked", 409},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWriteEnv(t, true)
			e.fleet.set(piGroup(tt.state))
			e.panes.set("")
			w := e.post(winPath+"/"+tt.route, `{"text":"hi"}`)
			if w.Code != tt.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if got := len(e.inj.got) == 1; got != (tt.status == 200) {
				t.Errorf("injected %q", e.inj.got)
			}
			if tt.status == 200 && !(len(e.inj.tgts) == 1 && e.inj.tgts[0].Unclassified) {
				t.Error("target not marked Unclassified")
			}
		})
	}
}

func TestReplyClaudeUnknownPaneRefused(t *testing.T) {
	e := newWriteEnv(t, true)
	e.fleet.set(writeGroup("idle", false, "go-engineer", "dispatch"))
	e.panes.set("")
	if w := e.post(winPath+"/reply", `{"text":"hi"}`); w.Code != 409 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	e.assertNothingSent(t)
}

func newWriteEnv(t *testing.T, allowWrite bool) *writeEnv {
	t.Helper()
	return newWriteEnvWith(t, allowWrite, nil)
}

// newWriteEnvWith is newWriteEnv with a hook to adjust Options.
func newWriteEnvWith(t *testing.T, allowWrite bool, mutate func(*Options)) *writeEnv {
	t.Helper()
	fleet := &fakeFleet{}
	fleet.set(writeGroup("blocked", false, "go-engineer", "dispatch"))
	e := &writeEnv{
		fleet: fleet, panes: &syncPanes{text: dialogPane}, keys: &fakeKeys{},
		inj: &fakeInjector{}, closer: &fakeCloser{}, disp: &fakeDispatcher{}, sweep: &fakeSweeper{},
	}
	dir := t.TempDir()
	clock := newClock()
	ca, err := LoadOrCreateCA(dir, "testhost", nil, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Config: config.ServeConfig{AllowWrite: allowWrite}, Dir: dir, Version: "test", CA: ca, Now: clock.Now,
		Fleet: fleet, Panes: e.panes, Awake: &fakeHolder{},
		Agents: fakeAgents{list: []AgentInfo{{Name: "go-engineer", Scope: "global"}}},
		Keys:   e.keys, Injector: e.inj, Closer: e.closer, Dispatcher: e.disp,
		MainPanes:      e.panes,
		Sweep:          e.sweep.run,
		GroupOrderPath: func() (string, error) { return filepath.Join(dir, "group-order.json"), nil },
	}
	if mutate != nil {
		mutate(&opts)
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	// Tests that exercise the post-answer wait set their own.
	s.answerSettle = 20 * time.Millisecond
	t.Cleanup(s.Close)
	e.testServer = &testServer{Server: s, dir: dir, clock: clock}
	e.dev, e.tok = e.pairDevice(t, "phone")
	return e
}

func dialogHash(t *testing.T) string {
	t.Helper()
	d, ok := panestate.ParseDialog(dialogPane)
	if !ok {
		t.Fatal("fixture dialog does not parse")
	}
	return d.Hash
}

// post sends a write request with cookie, CSRF header and JSON body.
func (e *writeEnv) post(path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:51515"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(CSRFHeader, "1")
	r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: e.tok})
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	e.Handler().ServeHTTP(w, r)
	return w
}

func withoutHeader(k string) reqOpt { return func(r *http.Request) { r.Header.Del(k) } }

func (e *writeEnv) auditLines(t *testing.T) []auditEntry {
	t.Helper()
	f, err := os.Open(filepath.Join(e.dir, AuditFile))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []auditEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var a auditEntry
		dec := json.NewDecoder(strings.NewReader(sc.Text()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			t.Fatalf("audit line %q: %v", sc.Text(), err)
		}
		out = append(out, a)
	}
	return out
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var e apiError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body %q: %v", w.Body, err)
	}
	return e.Code
}

const winPath = "/v1/sessions/agentctl-proj/windows/@523"

// writeRoutes lists every write route with a valid body.
func writeRoutes(t *testing.T) []struct{ name, path, body string } {
	return []struct{ name, path, body string }{
		{"answer", winPath + "/answer", fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, dialogHash(t))},
		{"reply", winPath + "/reply", `{"text":"hi"}`},
		{"queue", winPath + "/queue", `{"text":"hi"}`},
		{"interrupt", winPath + "/interrupt", `{}`},
		{"close", winPath + "/close", `{}`},
		{"group-order", "/v1/group-order", `{"keys":["/code/proj"]}`},
		{"dispatch", "/v1/dispatch", `{"agent":"go-engineer","project_dir":"/code/proj","prompt":"p"}`},
		{"sweep-preview", "/v1/sweep/preview", `{"session":"agentctl-proj"}`},
		{"sweep", "/v1/sweep", `{"session":"agentctl-proj"}`},
		{"approval", "/v1/approvals/" + strings.Repeat("a", 32), `{"decision":"allow","payload_sha256":"x"}`},
	}
}

func (e *writeEnv) assertNothingSent(t *testing.T) {
	t.Helper()
	if n := len(e.keys.all()); n != 0 {
		t.Errorf("%d keys sent", n)
	}
	if len(e.inj.got) != 0 || len(e.closer.closed) != 0 || len(e.disp.got) != 0 || len(e.sweep.all()) != 0 {
		t.Errorf("action ran: inject %v close %v dispatch %v sweep %v", e.inj.got, e.closer.closed, e.disp.got, e.sweep.all())
	}
}

func TestWriteRoutesForbiddenWhenWriteDisabled(t *testing.T) {
	for _, rt := range writeRoutes(t) {
		t.Run(rt.name, func(t *testing.T) {
			e := newWriteEnv(t, false)
			w := e.post(rt.path, rt.body)
			if w.Code != http.StatusForbidden || errCode(t, w) != codeWriteDisabled {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			e.assertNothingSent(t)
			lines := e.auditLines(t)
			if len(lines) != 1 || lines[0].Result != codeWriteDisabled || lines[0].Action != rt.name || lines[0].DeviceID != e.dev.ID {
				t.Errorf("audit = %+v", lines)
			}
		})
	}
}

func TestWriteRoutesRequireCSRF(t *testing.T) {
	for _, rt := range writeRoutes(t) {
		t.Run(rt.name, func(t *testing.T) {
			e := newWriteEnv(t, true)
			w := e.post(rt.path, rt.body, withoutHeader(CSRFHeader))
			if w.Code != http.StatusForbidden || errCode(t, w) != codeCSRF {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			e.assertNothingSent(t)
			lines := e.auditLines(t)
			if len(lines) != 1 || lines[0].Result != codeCSRF || lines[0].Action != rt.name || lines[0].DeviceName != "phone" {
				t.Errorf("audit = %+v", lines)
			}
		})
	}
}

func TestWriteRoutesRequireDevice(t *testing.T) {
	for _, rt := range writeRoutes(t) {
		t.Run(rt.name, func(t *testing.T) {
			e := newWriteEnv(t, true)
			e.tok = "bogus"
			if w := e.post(rt.path, rt.body); w.Code != http.StatusUnauthorized {
				t.Fatalf("status %d", w.Code)
			}
			e.assertNothingSent(t)
		})
	}
}

func TestWriteFeatureAdvertisedOnlyWhenAllowed(t *testing.T) {
	for _, allow := range []bool{false, true} {
		e := newWriteEnv(t, allow)
		for _, name := range []string{"write", "queue"} {
			if has := slices.Contains(e.router.Features(), name); has != allow {
				t.Errorf("allow_write=%v: %s feature = %v", allow, name, has)
			}
		}
	}
}

func TestWriteBodyValidation(t *testing.T) {
	tests := []struct {
		name, path, body string
		opts             []reqOpt
		status           int
		code             string
	}{
		{"unknown field", winPath + "/interrupt", `{"force":true}`, nil, 400, codeBadRequest},
		{"unknown field answer", winPath + "/answer", `{"key":"1","dialog_hash":"x","extra":1}`, nil, 400, codeBadRequest},
		{"trailing data", winPath + "/interrupt", `{}{}`, nil, 400, codeBadRequest},
		{"not json", winPath + "/interrupt", `nope`, nil, 400, codeBadRequest},
		{"empty body", winPath + "/interrupt", ``, nil, 400, codeBadRequest},
		{"too large", winPath + "/reply", `{"text":"` + strings.Repeat("a", maxWriteBody) + `"}`, nil, 413, codeTooLarge},
		{"wrong content type", winPath + "/interrupt", `{}`, []reqOpt{withHeader("Content-Type", "text/plain")}, 415, codeUnsupportedMedia},
		{"bad answer key", winPath + "/answer", `{"key":"y","dialog_hash":"` + strings.Repeat("a", 64) + `"}`, nil, 400, codeBadRequest},
		{"bad hash", winPath + "/answer", `{"key":"1","dialog_hash":"abc"}`, nil, 400, codeBadRequest},
		{"reply control chars", winPath + "/reply", `{"text":"hi\u001b[A"}`, nil, 400, codeBadRequest},
		{"reply empty", winPath + "/reply", `{"text":"  "}`, nil, 400, codeBadRequest},
		{"unknown window", "/v1/sessions/agentctl-proj/windows/@999/interrupt", `{}`, nil, 404, codeNotFound},
		{"unknown session", "/v1/sessions/other/windows/@523/interrupt", `{}`, nil, 404, codeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWriteEnv(t, true)
			w := e.post(tt.path, tt.body, tt.opts...)
			if w.Code != tt.status || errCode(t, w) != tt.code {
				t.Fatalf("status %d: %s, want %d %s", w.Code, w.Body, tt.status, tt.code)
			}
			e.assertNothingSent(t)
			if lines := e.auditLines(t); len(lines) != 1 || lines[0].Result != tt.code {
				t.Errorf("audit = %+v", lines)
			}
		})
	}
}

const multiSelectPane = "────────────────────────────────────────────────────────────────────────────────\n" +
	"←  ☐ Next  ✔ Submit  →\n\nWhich threads?\n\n" +
	"❯ 1. [ ] Merge\n         desc\n  2. [ ] Fix\n         desc\n  3. [ ] Type something\n     Submit\n" +
	"────────────────────────────────────────────────────────────────────────────────\n" +
	"  4. Chat about this\n\nEnter to select · ↑/↓ to navigate · Esc to cancel\n"

// In a multi-select a number key toggles one checkbox (dialog stays up); esc
// dismisses; "submit" ticks the differences, moves to the Submit row, Enter.
func TestAnswerMultiSelect(t *testing.T) {
	d, ok := panestate.ParseDialog(multiSelectPane)
	if !ok || !d.Multi || d.Submit == nil {
		t.Fatalf("fixture: ok=%v multi=%v submit=%v", ok, d.Multi, d.Submit)
	}
	tests := []struct {
		name string
		body string
		hash string
		code int
		keys []string
	}{
		{"toggle", `{"key":"2","dialog_hash":%q}`, d.Hash, 200, []string{"2"}},
		{"toggle unknown option", `{"key":"9","dialog_hash":%q}`, d.Hash, 409, nil},
		{"esc", `{"key":"esc","dialog_hash":%q}`, d.Hash, 200, []string{"Escape"}},
		{"stale hash", `{"key":"1","dialog_hash":%q}`, strings.Repeat("0", 64), 409, nil},
		{"submit", `{"key":"submit","select":["1","2"],"dialog_hash":%q}`, d.Hash, 200,
			[]string{"1", "2", "Down", "Down", "Down", "Enter"}},
		{"submit nothing ticked", `{"key":"submit","dialog_hash":%q}`, d.Hash, 200,
			[]string{"Down", "Down", "Down", "Enter"}},
		{"submit unknown option", `{"key":"submit","select":["8"],"dialog_hash":%q}`, d.Hash, 409, nil},
		{"submit stale", `{"key":"submit","select":["1"],"dialog_hash":%q}`, strings.Repeat("0", 64), 409, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWriteEnv(t, true)
			e.panes.set(multiSelectPane)
			w := e.post(winPath+"/answer", fmt.Sprintf(tt.body, tt.hash))
			var got []string
			for _, k := range e.keys.all() {
				got = append(got, k.key)
			}
			if w.Code != tt.code || !reflect.DeepEqual(got, tt.keys) {
				t.Errorf("status %d sent %v, want %d %v: %s", w.Code, got, tt.code, tt.keys, w.Body)
			}
		})
	}
}

// Toggling a box on and straight back off lands on an earlier hash; the
// second toggle must not be refused as an already-answered dialog.
func TestAnswerMultiSelectToggleTwice(t *testing.T) {
	d, _ := panestate.ParseDialog(multiSelectPane)
	e := newWriteEnv(t, true)
	e.panes.set(multiSelectPane)
	for i := 0; i < 2; i++ {
		if w := e.post(winPath+"/answer", fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, d.Hash)); w.Code != 200 {
			t.Fatalf("toggle %d: %d %s", i, w.Code, w.Body)
		}
	}
}

// The review page ends a multi-question flow; its Submit answers choice is an
// ordinary single-select answer (key 1).
func TestAnswerReviewPage(t *testing.T) {
	const review = "────────────────────────────────────────────────────────────────────────────────\n" +
		"←  ☒ Pick  ☒ Mode  ✔ Submit  →\nReview your answers\n ● Pick\n   → B\n\n❯ 1. Submit answers\n  2. Cancel\n"
	d, ok := panestate.ParseDialog(review)
	if !ok {
		t.Fatal("not parsed")
	}
	e := newWriteEnv(t, true)
	e.panes.set(review)
	if w := e.post(winPath+"/answer", fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, d.Hash)); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if ks := e.keys.all(); len(ks) != 1 || ks[0].key != "1" {
		t.Errorf("sent %v", ks)
	}
}

func TestAnswer(t *testing.T) {
	hash := dialogHash(t)
	tests := []struct {
		name    string
		pane    string
		body    string
		status  int
		code    string
		wantKey string
	}{
		{"happy path", dialogPane, fmt.Sprintf(`{"key":"2","dialog_hash":%q}`, hash), 200, "", "2"},
		{"esc", dialogPane, fmt.Sprintf(`{"key":"esc","dialog_hash":%q}`, hash), 200, "", "Escape"},
		{"stale hash", dialogPane, fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, strings.Repeat("0", 64)), 409, codeDialogChanged, ""},
		{"key not in options", dialogPane, fmt.Sprintf(`{"key":"3","dialog_hash":%q}`, hash), 409, codeDialogChanged, ""},
		{"no longer awaiting input", idlePane, fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, hash), 409, codeDialogChanged, ""},
		{"working", workingPane, fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, hash), 409, codeDialogChanged, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWriteEnv(t, true)
			e.panes.set(tt.pane)
			w := e.post(winPath+"/answer", tt.body)
			if w.Code != tt.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			sent := e.keys.all()
			if tt.wantKey == "" {
				if errCode(t, w) != tt.code || len(sent) != 0 {
					t.Fatalf("code %s, sent %v", errCode(t, w), sent)
				}
			} else {
				want := WindowTarget{Session: "agentctl-proj", RuntimeDir: "/code/proj/.agentctl", WindowID: "@523", Pane: testMainPane}
				if len(sent) != 1 || sent[0].key != tt.wantKey || sent[0].target != want {
					t.Fatalf("sent %+v", sent)
				}
			}
			// A sent key is preceded by an attempt line; a refusal has none.
			wantResults := []string{tt.code}
			if tt.code == "" {
				wantResults = []string{auditAttemptResult, "ok"}
			}
			lines := e.auditLines(t)
			if len(lines) != len(wantResults) {
				t.Fatalf("audit = %+v", lines)
			}
			for i, l := range lines {
				if l.Result != wantResults[i] || l.Window != "@523" || l.Session != "agentctl-proj" {
					t.Errorf("audit line %d = %+v", i, l)
				}
			}
		})
	}
}

// The pane is re-captured immediately before sending: a dialog that changes
// after the phone loaded it is caught even when the earlier view was valid.
func TestAnswerRecapturesBeforeSending(t *testing.T) {
	e := newWriteEnv(t, true)
	e.panes.set(idlePane) // the dialog the phone saw is gone by now
	w := e.post(winPath+"/answer", fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, dialogHash(t)))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d", w.Code)
	}
	if e.panes.captures == 0 {
		t.Error("pane was not captured")
	}
}

// Two phones (or a double tap) answering the same dialog: exactly one key is
// sent. The fake pane never redraws: Claude Code redraws some time after
// send-keys returns, so a fake that cleared the dialog inside SendKey would
// hide the race where every later request re-captures the dialog the first
// one already answered and its key lands in the input box.
func TestConcurrentAnswersSerialized(t *testing.T) {
	e := newWriteEnv(t, true)
	e.keys.delay = 20 * time.Millisecond
	body := fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, dialogHash(t))

	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = e.post(winPath+"/answer", body).Code
		}(i)
	}
	wg.Wait()

	ok := 0
	for _, c := range codes {
		switch c {
		case 200:
			ok++
		case 409:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if ok != 1 {
		t.Errorf("%d answers succeeded, want exactly 1 (codes %v)", ok, codes)
	}
	if n := len(e.keys.all()); n != 1 {
		t.Errorf("%d keys sent, want 1", n)
	}
	if m := e.keys.maxIn.Load(); m > 1 {
		t.Errorf("%d sends overlapped", m)
	}
}

func TestReply(t *testing.T) {
	tests := []struct {
		name     string
		state    string
		pane     string
		text     string
		injErr   error
		status   int
		code     string
		injected bool
	}{
		{"idle", "idle", idlePane, "carry on", nil, 200, "", true},
		{"multi-line", "idle", idlePane, "a\nb\tc", nil, 200, "", true},
		{"working window", "working", idlePane, "hi", nil, 409, codeNotIdle, false},
		{"blocked window", "blocked", dialogPane, "hi", nil, 409, codeNotIdle, false},
		{"record idle but pane busy", "idle", workingPane, "hi", nil, 409, codeNotIdle, false},
		{"record idle but pane dialog", "idle", dialogPane, "hi", nil, 409, codeNotIdle, false},
		{"dialog appears during inject", "idle", idlePane, "hi", fmt.Errorf("x: %w", tmux.ErrDialogBlocking), 409, codeNotIdle, false},
		{"too long", "idle", idlePane, strings.Repeat("é", maxReplyRunes+1), nil, 413, codeTooLarge, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWriteEnv(t, true)
			e.fleet.set(writeGroup(tt.state, false, "go-engineer", "dispatch"))
			e.panes.set(tt.pane)
			e.inj.err = tt.injErr
			body, _ := json.Marshal(replyIn{Text: tt.text})
			w := e.post(winPath+"/reply", string(body))
			if w.Code != tt.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if tt.code != "" && errCode(t, w) != tt.code {
				t.Errorf("code = %s, want %s", errCode(t, w), tt.code)
			}
			if got := len(e.inj.got) == 1 && e.inj.got[0] == tt.text; got != tt.injected {
				t.Errorf("injected %q, want injected=%v", e.inj.got, tt.injected)
			}
		})
	}
	t.Run("4000 chars accepted", func(t *testing.T) {
		e := newWriteEnv(t, true)
		e.fleet.set(writeGroup("idle", false, "go-engineer", "dispatch"))
		e.panes.set(idlePane)
		body, _ := json.Marshal(replyIn{Text: strings.Repeat("é", maxReplyRunes)})
		if w := e.post(winPath+"/reply", string(body)); w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	})
}

func TestQueue(t *testing.T) {
	tests := []struct {
		name     string
		state    string
		pane     string
		text     string
		injErr   error
		status   int
		code     string
		injected bool
	}{
		{"working", "working", workingPane, "also run the linter", nil, 200, "", true},
		{"turn ended before the send", "idle", idlePane, "hi", nil, 200, "", true},
		{"record working, pane idle", "working", idlePane, "hi", nil, 200, "", true},
		{"blocked window", "blocked", dialogPane, "hi", nil, 409, codeNotIdle, false},
		{"blocked record, busy pane", "blocked", workingPane, "hi", nil, 409, codeNotIdle, false},
		{"record working but pane dialog", "working", dialogPane, "hi", nil, 409, codeNotIdle, false},
		{"unrecognised pane", "working", "", "hi", nil, 409, codeNotIdle, false},
		{"dialog appears during inject", "working", workingPane, "hi", fmt.Errorf("x: %w", tmux.ErrDialogBlocking), 409, codeNotIdle, false},
		{"slash command", "working", workingPane, "/permissions", nil, 400, codeReplyIsCommand, false},
		{"bash mode", "working", workingPane, "  !rm -rf /", nil, 400, codeReplyIsCommand, false},
		{"too long", "working", workingPane, strings.Repeat("é", maxReplyRunes+1), nil, 413, codeTooLarge, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWriteEnv(t, true)
			e.fleet.set(writeGroup(tt.state, false, "go-engineer", "dispatch"))
			e.panes.set(tt.pane)
			e.inj.err = tt.injErr
			body, _ := json.Marshal(replyIn{Text: tt.text})
			w := e.post(winPath+"/queue", string(body))
			if w.Code != tt.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if tt.code != "" && errCode(t, w) != tt.code {
				t.Errorf("code = %s, want %s", errCode(t, w), tt.code)
			}
			if got := len(e.inj.got) == 1 && e.inj.got[0] == tt.text; got != tt.injected {
				t.Errorf("injected %q, want injected=%v", e.inj.got, tt.injected)
			}
			if len(e.keys.all()) != 0 {
				t.Errorf("keys sent: %+v", e.keys.all())
			}
			lines := e.auditLines(t)
			if len(lines) == 0 || lines[len(lines)-1].Action != "queue" {
				t.Errorf("audit = %+v, want a queue line", lines)
			}
		})
	}
}

// TestReplyStillRefusesWorking pins that queueing is opt-in: the reply route
// keeps refusing a working agent even though queue accepts it.
func TestReplyStillRefusesWorking(t *testing.T) {
	e := newWriteEnv(t, true)
	e.fleet.set(writeGroup("working", false, "go-engineer", "dispatch"))
	e.panes.set(workingPane)
	if w := e.post(winPath+"/reply", `{"text":"hi"}`); w.Code != http.StatusConflict || errCode(t, w) != codeNotIdle {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	e.assertNothingSent(t)
}

// queueInjector distinguishes queued messages from replies (Queuer).
type queueInjector struct {
	fakeInjector
	queued []string
}

func (q *queueInjector) Queue(_ WindowTarget, text string) error {
	q.queued = append(q.queued, text)
	return nil
}

func TestQueueUsesQueuer(t *testing.T) {
	q := &queueInjector{}
	s := &Server{injector: q}
	if err := s.inject(WindowTarget{}, "a", true); err != nil {
		t.Fatal(err)
	}
	if err := s.inject(WindowTarget{}, "b", false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(q.queued, []string{"a"}) || !slices.Equal(q.got, []string{"b"}) {
		t.Errorf("queued %q injected %q", q.queued, q.got)
	}
}

func TestInterruptAnyState(t *testing.T) {
	for _, st := range []string{"working", "blocked", "idle"} {
		t.Run(st, func(t *testing.T) {
			e := newWriteEnv(t, true)
			e.fleet.set(writeGroup(st, false, "go-engineer", "dispatch"))
			if w := e.post(winPath+"/interrupt", `{}`); w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if sent := e.keys.all(); len(sent) != 1 || sent[0].key != "Escape" || sent[0].target.WindowID != "@523" {
				t.Errorf("sent %+v", sent)
			}
		})
	}
}

func TestClose(t *testing.T) {
	tests := []struct {
		name   string
		group  fleetwatch.Group
		pane   string
		status int
	}{
		{"done", writeGroup("idle", true, "go-engineer", "dispatch"), idlePane, 200},
		{"idle not done", writeGroup("idle", false, "go-engineer", "adhoc"), idlePane, 200},
		{"idle bare", writeGroup("idle", false, "go-engineer", "bare"), idlePane, 200},
		{"working", writeGroup("working", false, "go-engineer", "dispatch"), workingPane, 409},
		{"record idle but pane busy", writeGroup("idle", true, "go-engineer", "dispatch"), workingPane, 409},
		{"blocked", writeGroup("blocked", false, "go-engineer", "dispatch"), dialogPane, 409},
		{"shell", writeGroup("idle", true, "shell", ""), idlePane, 409},
		{"improve", writeGroup("idle", true, "improve~1", "dispatch"), idlePane, 409},
		{"untracked", func() fleetwatch.Group {
			g := writeGroup("idle", true, "go-engineer", "dispatch")
			g.Meta = map[string]fleet.Meta{}
			return g
		}(), idlePane, 409},
		{"hub", func() fleetwatch.Group {
			g := writeGroup("idle", true, "go-engineer", "dispatch")
			g.Session = tmux.HubSessionName
			for i := range g.Windows {
				g.Windows[i].Session = tmux.HubSessionName
			}
			return g
		}(), idlePane, 409},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWriteEnv(t, true)
			e.fleet.set(tt.group)
			e.panes.set(tt.pane)
			w := e.post("/v1/sessions/"+tt.group.Session+"/windows/@523/close", `{}`)
			if w.Code != tt.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if tt.status == 200 {
				if len(e.closer.closed) != 1 || e.closer.closed[0].WindowID != "@523" {
					t.Errorf("closed %+v", e.closer.closed)
				}
				return
			}
			if errCode(t, w) != codeNotClosable || len(e.closer.closed) != 0 {
				t.Errorf("code %s closed %+v", errCode(t, w), e.closer.closed)
			}
		})
	}
}

func TestDispatch(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"unknown project dir", `{"agent":"go-engineer","project_dir":"/code/other","prompt":"p"}`, 400, codeUnknownProject},
		{"project dir not exact", `{"agent":"go-engineer","project_dir":"/code/proj/","prompt":"p"}`, 400, codeUnknownProject},
		{"relative project dir", `{"agent":"go-engineer","project_dir":"proj","prompt":"p"}`, 400, codeUnknownProject},
		{"unknown agent", `{"agent":"rust-engineer","project_dir":"/code/proj","prompt":"p"}`, 400, codeUnknownAgent},
		{"built-in agent not listed", `{"agent":"general-purpose","project_dir":"/code/proj","prompt":"p"}`, 400, codeUnknownAgent},
		{"agent with shell chars", `{"agent":"go-engineer; touch /tmp/x","project_dir":"/code/proj","prompt":"p"}`, 400, codeUnknownAgent},
		{"empty prompt", `{"agent":"go-engineer","project_dir":"/code/proj","prompt":""}`, 400, codeBadRequest},
		{"control chars in prompt", `{"agent":"go-engineer","project_dir":"/code/proj","prompt":"a\u0000b"}`, 400, codeBadRequest},
		{"bad model", `{"agent":"go-engineer","project_dir":"/code/proj","prompt":"p","model":"x; rm"}`, 400, codeBadRequest},
		{"unknown field", `{"agent":"go-engineer","project_dir":"/code/proj","prompt":"p","dir":"/"}`, 400, codeBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWriteEnv(t, true)
			w := e.post("/v1/dispatch", tt.body)
			if w.Code != tt.status || errCode(t, w) != tt.code {
				t.Fatalf("status %d: %s, want %d %s", w.Code, w.Body, tt.status, tt.code)
			}
			if len(e.disp.got) != 0 {
				t.Errorf("dispatched %+v", e.disp.got)
			}
		})
	}

	t.Run("happy path", func(t *testing.T) {
		e := newWriteEnv(t, true)
		w := e.post("/v1/dispatch", `{"agent":"go-engineer","project_dir":"/code/proj","prompt":"fix it\nplease","model":"opus"}`)
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		var got map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["window_id"] != "@900" || got["session"] != "agentctl-proj" || got["branch"] != "agent/go-engineer/adhoc-1234" || len(got) != 3 {
			t.Errorf("body = %v", got)
		}
		want := DispatchRequest{Agent: "go-engineer", Prompt: "fix it\nplease", Model: "opus", Dir: "/code/proj", Session: "agentctl-proj", RuntimeDir: "/code/proj/.agentctl"}
		if len(e.disp.got) != 1 || e.disp.got[0] != want {
			t.Errorf("dispatch = %+v", e.disp.got)
		}
		lines := e.auditLines(t)
		if len(lines) != 2 {
			t.Fatalf("audit = %+v", lines)
		}
		if a := lines[0]; a.Result != auditAttemptResult || a.Session != "agentctl-proj" || a.Agent != "go-engineer" || a.Project != "proj" {
			t.Errorf("attempt line = %+v", a)
		}
		if a := lines[1]; a.Result != "ok" || a.Window != "@900" || a.Session != "agentctl-proj" || a.Agent != "go-engineer" || a.Project != "proj" {
			t.Errorf("result line = %+v", a)
		}
	})
}

func TestWriteRateLimit(t *testing.T) {
	e := newWriteEnv(t, true)
	for i := range writesPerMinute {
		if w := e.post(winPath+"/interrupt", `{}`); w.Code != 200 {
			t.Fatalf("request %d: status %d", i, w.Code)
		}
	}
	w := e.post(winPath+"/interrupt", `{}`)
	if w.Code != http.StatusTooManyRequests || errCode(t, w) != codeRateLimited {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if ra := w.Header().Get("Retry-After"); ra != "2" {
		t.Errorf("Retry-After = %q, want 2", ra)
	}
	if n := len(e.keys.all()); n != writesPerMinute {
		t.Errorf("%d keys sent, want %d", n, writesPerMinute)
	}

	// A different device has its own bucket.
	_, tok2 := e.pairDevice(t, "tablet")
	tok1 := e.tok
	e.tok = tok2
	if w := e.post(winPath+"/interrupt", `{}`); w.Code != 200 {
		t.Errorf("second device: status %d", w.Code)
	}
	e.tok = tok1
	// Tokens refill with time.
	e.clock.Advance(2 * time.Second)
	if w := e.post(winPath+"/interrupt", `{}`); w.Code != 200 {
		t.Errorf("after refill: status %d", w.Code)
	}

	lines := e.auditLines(t)
	limited := 0
	for _, l := range lines {
		if l.Result == codeRateLimited {
			limited++
		}
	}
	if limited != 1 {
		t.Errorf("%d rate_limited audit lines, want 1", limited)
	}
}

// Audit lines record the body's hash, never its text, for successes and
// rejections alike; the file is 0600.
func TestAuditNoPlaintext(t *testing.T) {
	e := newWriteEnv(t, true)
	e.fleet.set(writeGroup("idle", false, "go-engineer", "dispatch"))
	e.panes.set(idlePane)
	const secret = "SECRET-REPLY-TEXT"
	okBody := `{"text":"` + secret + `"}`
	if w := e.post(winPath+"/reply", okBody); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	e.fleet.set(writeGroup("working", false, "go-engineer", "dispatch"))
	if w := e.post(winPath+"/reply", okBody); w.Code != 409 {
		t.Fatalf("status %d", w.Code)
	}
	const secretPrompt = "SECRET-PROMPT-TEXT"
	if w := e.post("/v1/dispatch", `{"agent":"go-engineer","project_dir":"/code/proj","prompt":"`+secretPrompt+`"}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	raw, err := os.ReadFile(filepath.Join(e.dir, AuditFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), secretPrompt) {
		t.Fatalf("audit log contains request text:\n%s", raw)
	}
	fi, err := os.Stat(filepath.Join(e.dir, AuditFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("audit mode = %v, want 0600", fi.Mode().Perm())
	}

	sum := sha256.Sum256([]byte(okBody))
	want := hex.EncodeToString(sum[:])
	lines := e.auditLines(t)
	// An attempt line precedes every action that acts; a refusal has none.
	if len(lines) != 5 {
		t.Fatalf("audit = %+v", lines)
	}
	for i, wantResult := range []string{auditAttemptResult, "ok", codeNotIdle, auditAttemptResult, "ok"} {
		l := lines[i]
		if l.Result != wantResult || l.DeviceID != e.dev.ID || l.DeviceName != "phone" || l.RemoteIP != "192.168.1.20" || l.Time == "" {
			t.Errorf("line %d = %+v", i, l)
		}
	}
	for _, l := range lines[:3] {
		if l.Action != "reply" || l.BodySHA256 != want {
			t.Errorf("reply line = %+v", l)
		}
	}
	for _, l := range lines[3:] {
		if l.Action != "dispatch" || len(l.BodySHA256) != 64 || l.Agent != "go-engineer" || l.Project != "proj" {
			t.Errorf("dispatch line = %+v", l)
		}
	}
}

func TestWriteActionFor(t *testing.T) {
	tests := []struct {
		path                    string
		action, session, window string
		ok                      bool
	}{
		{"/v1/dispatch", "dispatch", "", "", true},
		{"/v1/sessions/s/windows/@1/answer", "answer", "s", "@1", true},
		{"/v1/sessions/s/windows/@1/close", "close", "s", "@1", true},
		{"/v1/sessions/s/windows/@1/pane", "", "", "", false},
		{"/v1/push/subscribe", "", "", "", false},
		{"/v1/approvals/abc", "approval", "", "", true},
		{"/v1/approvals", "", "", "", false},
		{"/v1/group-order", "group-order", "", "", true},
	}
	for _, tt := range tests {
		a, s, w, ok := writeActionFor(tt.path)
		if a != tt.action || s != tt.session || w != tt.window || ok != tt.ok {
			t.Errorf("writeActionFor(%q) = %q %q %q %v", tt.path, a, s, w, ok)
		}
	}
}

func TestWithSessionRejectsBadTargets(t *testing.T) {
	for _, tc := range []struct{ session, dir string }{
		{"", "/p/.agentctl"},
		{"a:b", "/p/.agentctl"},
		{"agentctl-p", "relative/.agentctl"},
		{"agentctl-p", "/p/other"},
	} {
		called := false
		err := withSession(tc.session, tc.dir, func() error { called = true; return nil })
		if err == nil || called {
			t.Errorf("withSession(%q, %q): err=%v called=%v", tc.session, tc.dir, err, called)
		}
	}
}

// An answered dialog's hash is refused for answeredTTL, then accepted again
// (the agent may legitimately ask the same thing later).
func TestAnswerRepeatRefusedUntilTTL(t *testing.T) {
	e := newWriteEnv(t, true)
	body := fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, dialogHash(t))
	if w := e.post(winPath+"/answer", body); w.Code != 200 {
		t.Fatalf("first answer = %d %s", w.Code, w.Body)
	}
	w := e.post(winPath+"/answer", body)
	if w.Code != http.StatusConflict || errCode(t, w) != codeAlreadyAnswered {
		t.Fatalf("repeat = %d %s, want 409 %s", w.Code, w.Body, codeAlreadyAnswered)
	}
	e.clock.Advance(answeredTTL)
	if w := e.post(winPath+"/answer", body); w.Code != 200 {
		t.Fatalf("after TTL = %d %s", w.Code, w.Body)
	}
	if n := len(e.keys.all()); n != 2 {
		t.Errorf("%d keys sent, want 2", n)
	}
}

// The answer holds the window lock until the answered dialog leaves the
// pane (bounded by answerSettle), so the next request sees the redraw.
func TestAnswerWaitsForRedraw(t *testing.T) {
	e := newWriteEnv(t, true)
	e.answerSettle = 5 * time.Second
	const redraw = 150 * time.Millisecond
	e.keys.onSend = func() { time.AfterFunc(redraw, func() { e.panes.set(idlePane) }) }
	start := time.Now()
	if w := e.post(winPath+"/answer", fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, dialogHash(t))); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if d := time.Since(start); d < redraw || d > e.answerSettle {
		t.Errorf("answer returned after %v, want after the %v redraw and well before the %v cap", d, redraw, e.answerSettle)
	}
}

// Regression: writes captured the window's active pane but typed into its
// main pane. Every capture and keystroke now targets the main pane.
func TestWritesTargetMainPane(t *testing.T) {
	t.Run("answer", func(t *testing.T) {
		e := newWriteEnv(t, true)
		if w := e.post(winPath+"/answer", fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, dialogHash(t))); w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		if sent := e.keys.all(); len(sent) != 1 || sent[0].target.Pane != testMainPane {
			t.Errorf("sent %+v", sent)
		}
		assertCapturedMainPane(t, e)
	})
	t.Run("reply", func(t *testing.T) {
		e := newWriteEnv(t, true)
		e.fleet.set(writeGroup("idle", false, "go-engineer", "dispatch"))
		e.panes.set(idlePane)
		if w := e.post(winPath+"/reply", `{"text":"hi"}`); w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		if len(e.inj.tgts) != 1 || e.inj.tgts[0].Pane != testMainPane {
			t.Errorf("injected into %+v", e.inj.tgts)
		}
		assertCapturedMainPane(t, e)
	})
	t.Run("close", func(t *testing.T) {
		e := newWriteEnv(t, true)
		e.fleet.set(writeGroup("idle", true, "go-engineer", "dispatch"))
		e.panes.set(idlePane)
		if w := e.post(winPath+"/close", `{}`); w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		assertCapturedMainPane(t, e)
	})
	t.Run("interrupt", func(t *testing.T) {
		e := newWriteEnv(t, true)
		if w := e.post(winPath+"/interrupt", `{}`); w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		if sent := e.keys.all(); len(sent) != 1 || sent[0].target.Pane != testMainPane {
			t.Errorf("sent %+v", sent)
		}
	})
	t.Run("no main pane", func(t *testing.T) {
		e := newWriteEnv(t, true)
		e.panes.mainPaneErr = fmt.Errorf("no main pane")
		w := e.post(winPath+"/answer", fmt.Sprintf(`{"key":"1","dialog_hash":%q}`, dialogHash(t)))
		if w.Code != http.StatusBadGateway || errCode(t, w) != codePaneUnavailable {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		e.assertNothingSent(t)
	})
}

func assertCapturedMainPane(t *testing.T, e *writeEnv) {
	t.Helper()
	e.panes.mu.Lock()
	defer e.panes.mu.Unlock()
	if len(e.panes.capturedPanes) == 0 {
		t.Fatal("no main-pane capture")
	}
	for _, p := range e.panes.capturedPanes {
		if p != testMainPane {
			t.Errorf("captured pane %q, want %q", p, testMainPane)
		}
	}
}

// A reply starting with "!" would run a shell command (Claude Code bash
// mode, no permission dialog), "/" a slash command, "#" a memory write.
func TestReplyRejectsCommandPrefixes(t *testing.T) {
	for _, text := range []string{
		"!rm -rf ~",
		"/permissions",
		"/add-dir /",
		"#remember this",
		"   !ls",
		"\n\t/hooks",
		"！", // fullwidth is not a mode prefix; accepted below
	} {
		t.Run(text, func(t *testing.T) {
			e := newWriteEnv(t, true)
			e.fleet.set(writeGroup("idle", false, "go-engineer", "dispatch"))
			e.panes.set(idlePane)
			body, _ := json.Marshal(replyIn{Text: text})
			w := e.post(winPath+"/reply", string(body))
			if text == "！" {
				if w.Code != 200 {
					t.Fatalf("status %d: %s", w.Code, w.Body)
				}
				return
			}
			if w.Code != http.StatusBadRequest || errCode(t, w) != codeReplyIsCommand {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			e.assertNothingSent(t)
			if lines := e.auditLines(t); len(lines) != 1 || lines[0].Result != codeReplyIsCommand {
				t.Errorf("audit = %+v", lines)
			}
		})
	}
	t.Run("prefix later in the text is fine", func(t *testing.T) {
		e := newWriteEnv(t, true)
		e.fleet.set(writeGroup("idle", false, "go-engineer", "dispatch"))
		e.panes.set(idlePane)
		if w := e.post(winPath+"/reply", `{"text":"run /tmp/x.sh! #1"}`); w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	})
}
