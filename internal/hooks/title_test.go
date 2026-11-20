package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/llm"
	"github.com/blsmth/agentctl/internal/llmusage"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// titleFake is a counting completer.
type titleFake struct {
	mu      sync.Mutex
	calls   int
	out     string
	err     error
	prompts []string
	block   chan struct{} // when set, Complete waits on it
	hook    func()        // when set, runs inside Complete
	meta    llmusage.Meta
}

func (f *titleFake) Complete(ctx context.Context, p string) (string, error) {
	if f.block != nil {
		<-f.block
	}
	if f.hook != nil {
		f.hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.meta = llmusage.MetaFrom(ctx)
	f.prompts = append(f.prompts, p)
	return f.out, f.err
}

type titleEnv struct {
	fake      *titleFake
	built     atomic.Int32
	budget    atomic.Int32 // requests granted
	budgetCap int32
	keyErr    error
	settings  config.TitleSettings
	now       time.Time
	// recap is what the stubbed log read returns for every window.
	recap         string
	backfillTakes atomic.Int32
	refreshes     atomic.Int32
}

// setupTitle stubs every hook-title seam: settings, key check, budget,
// completer and clock. Nothing reaches the network or the keychain.
func setupTitle(t *testing.T) *titleEnv {
	t.Helper()
	setupWindowstate(t)
	e := &titleEnv{
		fake:      &titleFake{out: "Organizing your Photos"},
		budgetCap: 100,
		settings:  config.TitleSettings{Enabled: true, BackendName: "nous", Backend: config.BackendConfig{Kind: config.BackendKindNous}, Model: "m/m"},
		now:       time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC),
	}
	oldS, oldK, oldC, oldB, oldN := titleSettings, titleKeyAvailable, newTitleCompleter, titleTakeBudget, titleNow
	titleSettings = func() config.TitleSettings { return e.settings }
	titleKeyAvailable = func(config.TitleSettings) error { return e.keyErr }
	newTitleCompleter = func(config.TitleSettings) (llm.Completer, error) { e.built.Add(1); return e.fake, nil }
	titleTakeBudget = func(time.Time) bool {
		if e.budget.Load() >= e.budgetCap {
			return false
		}
		e.budget.Add(1)
		return true
	}
	titleNow = func() time.Time { return e.now }
	oldPR := titlePriceRefresh
	titlePriceRefresh = func(context.Context, config.TitleSettings) { e.refreshes.Add(1) }
	oldBB, oldR := titleTakeBackfillBudget, titleRecap
	titleTakeBackfillBudget = func(now time.Time) bool { e.backfillTakes.Add(1); return titleTakeBudget(now) }
	titleRecap = func(string) string { return e.recap }
	t.Cleanup(func() {
		titleSettings, titleKeyAvailable, newTitleCompleter, titleTakeBudget, titleNow = oldS, oldK, oldC, oldB, oldN
		titleTakeBackfillBudget, titleRecap, titlePriceRefresh = oldBB, oldR, oldPR
	})
	return e
}

func seedWindow(t *testing.T, id string, ws windowstate.WindowState) {
	t.Helper()
	if err := windowstate.Write(id, ws); err != nil {
		t.Fatal(err)
	}
}

func readTitle(t *testing.T, id string) windowstate.ShortTitle {
	t.Helper()
	ws, err := windowstate.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return ws.ShortTitle
}

// A dispatched window: first title from its task, one refinement at its first
// Stop, then frozen for good.
func TestTitleDispatchedWindowTwoTitlesThenFrozen(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "idle", Task: "sort my photo library by year"})

	// SessionStart: first title from the task alone.
	runTitle(context.Background(), "@4", "", "")
	st := readTitle(t, "@4")
	if e.fake.calls != 1 || st.Title == "" || st.Final || !st.At.Equal(e.now) {
		t.Fatalf("after first title: calls=%d %+v", e.fake.calls, st)
	}
	if !strings.Contains(e.fake.prompts[0], "sort my photo library") || strings.Contains(e.fake.prompts[0], "first response") {
		t.Fatalf("first prompt = %q", e.fake.prompts[0])
	}
	// Its first prompt and a Stop-less event change nothing.
	runTitle(context.Background(), "@4", "", "go ahead")
	if e.fake.calls != 1 {
		t.Fatalf("calls = %d before the first Stop", e.fake.calls)
	}

	// The first Stop refines from the task plus the response.
	e.fake.out = "Sorting Photos By Year"
	runTitle(context.Background(), "@4", "I finished the 2019 folder.", "")
	st = readTitle(t, "@4")
	if e.fake.calls != 2 || st.Title != "Sorting Photos By Year" || !st.Final {
		t.Fatalf("after refinement: calls=%d %+v", e.fake.calls, st)
	}
	for _, s := range []string{"sort my photo library", "I finished the 2019 folder."} {
		if !strings.Contains(e.fake.prompts[1], s) {
			t.Errorf("refinement prompt missing %q:\n%s", s, e.fake.prompts[1])
		}
	}

	// Frozen: later Stops, prompts, SessionStarts and 10+ minutes pass with no
	// request. (A new away_summary in the log is no longer read at all.)
	e.now = e.now.Add(3 * time.Hour)
	runTitle(context.Background(), "@4", "Now on 2020.", "")
	runTitle(context.Background(), "@4", "", "next request")
	runTitle(context.Background(), "@4", "", "")
	if e.fake.calls != 2 || readTitle(t, "@4").Title != "Sorting Photos By Year" {
		t.Fatalf("frozen title changed: calls=%d %+v", e.fake.calls, readTitle(t, "@4"))
	}
}

// A bare window: first prompt titles it, the first Stop refines, then frozen.
func TestTitleBareWindowTwoTitlesThenFrozen(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "working"})
	runTitle(context.Background(), "@4", "", "")
	if e.fake.calls != 0 {
		t.Fatal("SessionStart on a bare window made a request")
	}
	runTitle(context.Background(), "@4", "", "sort my photo library by year")
	runTitle(context.Background(), "@4", "", "and keep the originals")
	if e.fake.calls != 1 || readTitle(t, "@4").Final {
		t.Fatalf("calls = %d %+v after prompts", e.fake.calls, readTitle(t, "@4"))
	}
	runTitle(context.Background(), "@4", "Sorted 2019.", "")
	if st := readTitle(t, "@4"); e.fake.calls != 2 || !st.Final {
		t.Fatalf("calls = %d %+v after first Stop", e.fake.calls, st)
	}
	if !strings.Contains(e.fake.prompts[1], "sort my photo library by year") || !strings.Contains(e.fake.prompts[1], "Sorted 2019.") {
		t.Fatalf("refinement prompt = %q", e.fake.prompts[1])
	}
	e.now = e.now.Add(time.Hour)
	runTitle(context.Background(), "@4", "More.", "another prompt")
	if e.fake.calls != 2 {
		t.Fatalf("calls = %d after freeze", e.fake.calls)
	}
}

// A failed refinement keeps the first title, does not freeze, and is retried
// at a later Stop once its backoff has passed.
func TestTitleFailedRefinementKeepsFirstTitle(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "working"})
	runTitle(context.Background(), "@4", "", "sort my photo library by year")
	first := readTitle(t, "@4").Title

	e.fake.err = errors.New("llm: nous returned HTTP 503")
	runTitle(context.Background(), "@4", "Sorted 2019.", "")
	st := readTitle(t, "@4")
	if st.Title != first || st.Final || st.Failures != 1 || !st.RetryAfter.After(e.now) {
		t.Fatalf("after failed refinement = %+v", st)
	}
	// Backoff holds off the next Stop.
	e.now = e.now.Add(time.Minute)
	runTitle(context.Background(), "@4", "Sorted 2020.", "")
	if e.fake.calls != 2 {
		t.Fatalf("calls = %d during backoff", e.fake.calls)
	}
	// After it, a later Stop retries and freezes on success.
	e.now = st.RetryAfter.Add(time.Second)
	e.fake.err = nil
	e.fake.out = "Sorting Photos By Year"
	runTitle(context.Background(), "@4", "Sorted 2021.", "")
	if st := readTitle(t, "@4"); st.Title != "Sorting Photos By Year" || !st.Final || st.Failures != 0 {
		t.Fatalf("after retry = %+v", st)
	}
}

// A hook-title that fires on /clear does nothing; SessionStart resets, and
// the next prompt titles fresh. Resume keeps the title.
func TestTitleClearResetsAndResumeDoesNot(t *testing.T) {
	for _, tt := range []struct {
		source    string
		wantReset bool
	}{{"clear", true}, {"resume", false}, {"compact", false}, {"startup", false}} {
		t.Run(tt.source, func(t *testing.T) {
			e := setupTitle(t)
			t.Setenv("AGENTCTL", "1")
			t.Setenv("AGENTCTL_WINDOW_ID", "@9")
			t.Setenv("TMUX_PANE", "")
			t.Setenv("AGENTCTL_SESSION_ID", "")
			t.Setenv("CLAUDE_ENV_FILE", filepath.Join(t.TempDir(), "env"))
			seedWindow(t, "@9", windowstate.WindowState{
				State: "idle", FirstPrompt: "old work",
				ShortTitle: windowstate.ShortTitle{Title: "Old Work", Final: true, InputHash: "h"},
			})
			payload := fmt.Sprintf(`{"session_id":"s2","cwd":%q,"source":%q}`, t.TempDir(), tt.source)
			runHookCaptured(t, nil, payload, SessionStart)
			runHookCaptured(t, nil, payload, Title)
			ws := readWS(t, "@9")
			if tt.wantReset {
				if ws.FirstPrompt != "" || ws.ShortTitle != (windowstate.ShortTitle{}) {
					t.Fatalf("not reset: %+v", ws)
				}
				runTitle(context.Background(), "@9", "", "new work entirely")
				if e.fake.calls != 1 || readTitle(t, "@9").Title == "" || readTitle(t, "@9").Final {
					t.Fatalf("no fresh title: calls=%d %+v", e.fake.calls, readTitle(t, "@9"))
				}
				return
			}
			if ws.FirstPrompt != "old work" || ws.ShortTitle.Title != "Old Work" || !ws.ShortTitle.Final || e.fake.calls != 0 {
				t.Fatalf("reset on %s: %+v calls=%d", tt.source, ws, e.fake.calls)
			}
		})
	}
}

// Old records with summary_hash still load.
func TestTitleLoadsRecordWithSummaryHash(t *testing.T) {
	setupWindowstate(t)
	raw := `{"state":"idle","short_title":{"title":"Old","at":"2026-11-19T12:00:00Z","input_hash":"h","summary_hash":"s"}}`
	dir := t.TempDir()
	windowstate.SetDirForTest(dir)
	if err := os.WriteFile(filepath.Join(dir, "@4.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := readTitle(t, "@4"); st.Title != "Old" || st.Final {
		t.Fatalf("title = %+v", st)
	}
}

// Only the Stop payload's response counts; a recorded LastText does not make
// a prompt event a refinement.
func TestTitleIgnoresRecordedLastText(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "working", Task: "sort the photos", LastText: "Refactored the sort comparator."})
	runTitle(context.Background(), "@4", "", "")
	if e.fake.calls != 1 || strings.Contains(e.fake.prompts[0], "Refactored the sort comparator.") {
		t.Fatalf("calls = %d prompts = %q", e.fake.calls, e.fake.prompts)
	}
}

func TestTitleFailureBacksOff(t *testing.T) {
	e := setupTitle(t)
	e.fake.err = errors.New("llm: nous returned HTTP 503")
	seedWindow(t, "@4", windowstate.WindowState{Task: "sort the photos"})
	runTitle(context.Background(), "@4", "", "")
	st := readTitle(t, "@4")
	if st.Title != "" || st.Failures != 1 || !st.RetryAfter.After(e.now) {
		t.Fatalf("after failure = %+v", st)
	}
	// Every Stop during the backoff stays off the network.
	e.now = e.now.Add(time.Minute)
	runTitle(context.Background(), "@4", "new text", "")
	if e.fake.calls != 1 {
		t.Fatalf("calls = %d during backoff", e.fake.calls)
	}
	// After it, a retry that succeeds clears the backoff.
	e.now = st.RetryAfter.Add(time.Second)
	e.fake.err = nil
	runTitle(context.Background(), "@4", "new text", "")
	if st := readTitle(t, "@4"); st.Title != "Organizing your Photos" || st.Failures != 0 || !st.RetryAfter.IsZero() {
		t.Fatalf("after retry = %+v", st)
	}
}

func TestTitleBlankReplyIsAFailure(t *testing.T) {
	e := setupTitle(t)
	e.fake.out = "  \"...\" "
	seedWindow(t, "@4", windowstate.WindowState{Task: "sort the photos"})
	runTitle(context.Background(), "@4", "", "")
	if st := readTitle(t, "@4"); st.Title != "" || st.Failures != 1 {
		t.Fatalf("blank reply = %+v", st)
	}
}

// Off, no backend, or no key: no request, no write.
func TestTitleDisabledWritesNothing(t *testing.T) {
	tests := []struct {
		name string
		set  func(e *titleEnv)
	}{
		{"titles off", func(e *titleEnv) { e.settings = config.TitleSettings{} }},
		{"no key", func(e *titleEnv) { e.keyErr = errors.New("no API key stored for backend \"nous\"") }},
		{"key error from the request", func(e *titleEnv) { e.fake.err = fmt.Errorf("%w: keychain", llm.ErrNoKey) }},
		{"hourly cap spent", func(e *titleEnv) { e.budgetCap = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setupTitle(t)
			tt.set(e)
			seedWindow(t, "@4", windowstate.WindowState{State: "idle", Task: "sort the photos"})
			before, _ := windowstate.Read("@4")
			runTitle(context.Background(), "@4", "", "")
			after, _ := windowstate.Read("@4")
			if after.ShortTitle != (windowstate.ShortTitle{}) {
				t.Fatalf("title state written: %+v", after.ShortTitle)
			}
			if tt.name == "titles off" || tt.name == "no key" {
				if !after.UpdatedAt.Equal(before.UpdatedAt) {
					t.Fatal("windowstate was rewritten")
				}
				if e.built.Load() != 0 || e.budget.Load() != 0 {
					t.Fatalf("completer built %d, budget taken %d", e.built.Load(), e.budget.Load())
				}
			}
		})
	}
}

// A double-registered hook fires twice at once: one request, one write.
func TestTitleConcurrentFiresSpendOneRequest(t *testing.T) {
	e := setupTitle(t)
	e.fake.block = make(chan struct{})
	seedWindow(t, "@4", windowstate.WindowState{Task: "sort the photos"})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runTitle(context.Background(), "@4", "", "")
		}()
	}
	// Let the claimed run finish; the others must already have bailed.
	time.AfterFunc(50*time.Millisecond, func() { close(e.fake.block) })
	wg.Wait()
	if e.fake.calls != 1 || e.budget.Load() != 1 {
		t.Fatalf("calls = %d budget = %d, want 1 each", e.fake.calls, e.budget.Load())
	}
	if st := readTitle(t, "@4"); st.Title != "Organizing your Photos" {
		t.Fatalf("title = %+v", st)
	}
}

func TestTitleHookReadsPayload(t *testing.T) {
	e := setupTitle(t)
	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", "@9")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	seedWindow(t, "@9", windowstate.WindowState{State: "idle", Task: "fix hub sort"})
	stdin := `{"session_id":"s","last_assistant_message":"Wrote the hub sort fix."}`
	runHookCaptured(t, nil, stdin, Title)
	if e.fake.calls != 1 || !strings.Contains(e.fake.prompts[0], "Wrote the hub sort fix.") {
		t.Fatalf("calls = %d prompts = %q", e.fake.calls, e.fake.prompts)
	}
	if st := readTitle(t, "@9"); st.Title == "" {
		t.Fatal("no title written")
	}
}

func TestTitleHookOutsideAgentctlIsNoop(t *testing.T) {
	e := setupTitle(t)
	t.Setenv("AGENTCTL", "")
	runHookCaptured(t, nil, `{"last_assistant_message":"x"}`, Title)
	if e.built.Load() != 0 {
		t.Fatal("completer built outside an agentctl session")
	}
}

// hook-title rides along async on Stop, UserPromptSubmit and SessionStart,
// after the existing (sync) handler, with its own timeout.
func TestBuildHooksConfigRegistersTitle(t *testing.T) {
	cfg := BuildHooksConfig("agentctl")
	want := map[string]string{"Stop": "hook-check-done", "UserPromptSubmit": "hook-user-prompt", "SessionStart": "hook-session-start"}
	for event, first := range want {
		entries := cfg[event].([]interface{})
		if len(entries) != 1 {
			t.Fatalf("%s: %d entries", event, len(entries))
		}
		hooks := entries[0].(map[string]interface{})["hooks"].([]interface{})
		if len(hooks) != 2 {
			t.Fatalf("%s: %d hooks", event, len(hooks))
		}
		h0, h1 := hooks[0].(map[string]interface{}), hooks[1].(map[string]interface{})
		if h0["command"] != "agentctl "+first || h0["async"] != nil {
			t.Errorf("%s first hook = %v, want sync %s", event, h0, first)
		}
		if h1["command"] != "agentctl hook-title" || h1["async"] != true || h1["timeout"] != titleHookTimeoutSeconds {
			t.Errorf("%s title hook = %v", event, h1)
		}
	}
	for event, v := range cfg {
		if _, ok := want[event]; ok {
			continue
		}
		if s := fmt.Sprint(v); strings.Contains(s, "hook-title") {
			t.Errorf("%s registers hook-title", event)
		}
	}
}

func TestTitleSkipsWhileOutOfCredits(t *testing.T) {
	e := setupTitle(t)
	fresh := false
	old := titleNoCredits
	titleNoCredits = func(string, time.Time) (backendauth.CreditState, bool) { return backendauth.CreditState{}, fresh }
	t.Cleanup(func() { titleNoCredits = old })

	seedWindow(t, "@4", windowstate.WindowState{State: "idle", Task: "sort my photo library by year"})
	fresh = true
	runTitle(context.Background(), "@4", "done", "")
	if e.built.Load() != 0 || e.budget.Load() != 0 {
		t.Fatalf("fresh marker: built=%d budget=%d, want no request", e.built.Load(), e.budget.Load())
	}
	if st := readTitle(t, "@4"); st.Failures != 0 || !st.RetryAfter.IsZero() {
		t.Errorf("skip must leave no backoff: %+v", st)
	}

	// Marker expired: one probe goes through.
	fresh = false
	runTitle(context.Background(), "@4", "done", "")
	if e.built.Load() != 1 || e.budget.Load() != 1 {
		t.Fatalf("probe: built=%d budget=%d, want 1/1", e.built.Load(), e.budget.Load())
	}
}

func TestTitleNoCreditsErrorLeavesNoBackoff(t *testing.T) {
	e := setupTitle(t)
	e.fake.err = fmt.Errorf("llm: nous returned HTTP 402: %w", backendauth.ErrNoCredits)
	seedWindow(t, "@4", windowstate.WindowState{State: "idle", Task: "sort my photo library by year"})
	runTitle(context.Background(), "@4", "done", "")
	if st := readTitle(t, "@4"); st.Failures != 0 || !st.RetryAfter.IsZero() {
		t.Errorf("no-credits must not count as a failure: %+v", st)
	}
}

func TestTitleFreeModelIgnoresNoCreditsMarker(t *testing.T) {
	e := setupTitle(t)
	e.settings.Model = "meituan/longcat-2.5:free"
	old := titleNoCredits
	titleNoCredits = func(string, time.Time) (backendauth.CreditState, bool) { return backendauth.CreditState{}, true }
	t.Cleanup(func() { titleNoCredits = old })
	seedWindow(t, "@4", windowstate.WindowState{State: "idle", Task: "sort my photo library by year"})
	runTitle(context.Background(), "@4", "done", "")
	if e.built.Load() != 1 {
		t.Fatalf("built=%d, want a request for a :free model", e.built.Load())
	}
}

func readWS(t *testing.T, id string) windowstate.WindowState {
	t.Helper()
	ws, err := windowstate.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestTitleBareWindowTitlesFromFirstPrompt(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "working"})
	runTitle(context.Background(), "@4", "", "sort my photo library by year")
	if e.fake.calls != 1 || !strings.Contains(e.fake.prompts[0], "sort my photo library by year") {
		t.Fatalf("calls = %d prompts = %q", e.fake.calls, e.fake.prompts)
	}
	if st := readTitle(t, "@4"); st.Title == "" {
		t.Fatal("no title written")
	}
	if ws := readWS(t, "@4"); ws.FirstPrompt != "sort my photo library by year" || ws.Task != "" {
		t.Fatalf("first_prompt = %q task = %q", ws.FirstPrompt, ws.Task)
	}
}

func TestTitleHookReadsPromptPayload(t *testing.T) {
	e := setupTitle(t)
	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", "@9")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	seedWindow(t, "@9", windowstate.WindowState{State: "working"})
	runHookCaptured(t, nil, `{"hook_event_name":"UserPromptSubmit","prompt":"fix the hub sort order"}`, Title)
	if e.fake.calls != 1 || readTitle(t, "@9").Title == "" {
		t.Fatalf("calls = %d", e.fake.calls)
	}
}

func TestTitleIgnoresNonRequests(t *testing.T) {
	for name, prompt := range map[string]string{
		"slash command": "/compact keep the plan",
		"empty":         "",
		"bare slash":    "/clear",
		"whitespace":    " \n\t ",
		"notice only":   "agentctl notice: @5 (go-engineer) finished its turn and is idle.\nagentctl notice: @6 is blocked.",
		"task notif":    "<task-notification>\n<task-id>x</task-id>\n</task-notification>",
		"reminder":      "<system-reminder>\nstuff\n</system-reminder>\n",
	} {
		t.Run(name, func(t *testing.T) {
			e := setupTitle(t)
			seedWindow(t, "@4", windowstate.WindowState{State: "working"})
			runTitle(context.Background(), "@4", "", prompt)
			if e.fake.calls != 0 || e.built.Load() != 0 {
				t.Fatalf("request made: calls = %d", e.fake.calls)
			}
			if ws := readWS(t, "@4"); ws.FirstPrompt != "" || ws.ShortTitle.InputHash != "" {
				t.Fatalf("state recorded: %+v", ws)
			}
		})
	}
}

func TestTitleStripsInjectedPromptParts(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "working"})
	runTitle(context.Background(), "@4", "", "agentctl notice: @5 is idle.\n<system-reminder>x</system-reminder>\nrename the flag")
	if got := readWS(t, "@4").FirstPrompt; got != "rename the flag" || e.fake.calls != 1 {
		t.Fatalf("first_prompt = %q calls = %d", got, e.fake.calls)
	}
}

func TestTitleDispatchedWindowIgnoresPrompt(t *testing.T) {
	for name, ws := range map[string]windowstate.WindowState{
		"task":   {State: "working", Task: "sort my photo library by year"},
		"ticket": {State: "working", TicketTitle: "Fix the thing"},
	} {
		t.Run(name, func(t *testing.T) {
			e := setupTitle(t)
			seedWindow(t, "@4", ws)
			runTitle(context.Background(), "@4", "", "a different request")
			if got := readWS(t, "@4").FirstPrompt; got != "" {
				t.Fatalf("first_prompt = %q", got)
			}
			if e.fake.calls != 1 || strings.Contains(e.fake.prompts[0], "a different request") {
				t.Fatalf("calls = %d prompts = %q", e.fake.calls, e.fake.prompts)
			}
		})
	}
}

func TestTitleFirstPromptNotOverwritten(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "working"})
	runTitle(context.Background(), "@4", "", "first request")
	e.now = e.now.Add(time.Hour)
	runTitle(context.Background(), "@4", "", "second request")
	if got := readWS(t, "@4").FirstPrompt; got != "first request" {
		t.Fatalf("first_prompt = %q", got)
	}
	if last := e.fake.prompts[len(e.fake.prompts)-1]; strings.Contains(last, "second request") {
		t.Fatalf("later prompt used as input: %q", last)
	}
}

func TestTitleFirstPromptTrimmed(t *testing.T) {
	setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "working"})
	runTitle(context.Background(), "@4", "", strings.Repeat("é", 800))
	if n := len([]rune(readWS(t, "@4").FirstPrompt)); n != maxFirstPromptRunes {
		t.Fatalf("runes = %d", n)
	}
}

func TestTitleSessionStartThenPromptStillTitles(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "idle"})
	runTitle(context.Background(), "@4", "", "")
	if e.fake.calls != 0 {
		t.Fatal("SessionStart on an empty window made a request")
	}
	if st := readTitle(t, "@4"); st.InputHash != "" || st.RetryAfter != (time.Time{}) || st.Failures != 0 {
		t.Fatalf("SessionStart burned the slot: %+v", st)
	}
	runTitle(context.Background(), "@4", "", "organize my photos")
	if e.fake.calls != 1 || readTitle(t, "@4").Title == "" {
		t.Fatalf("calls = %d", e.fake.calls)
	}
}

func TestTitleAbsolutePathPromptCounts(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "working"})
	runTitle(context.Background(), "@4", "", "/Users/me/x.go fails to build")
	if e.fake.calls != 1 {
		t.Fatalf("calls = %d", e.fake.calls)
	}
}

// A request in flight across /clear must not write the old work's title over
// the reset record.
func TestTitleInFlightRequestDroppedAfterClear(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "working", FirstPrompt: "old work"})
	e.fake.hook = func() {
		if err := windowstate.Update("@4", resetTitleOnClear); err != nil {
			t.Error(err)
		}
	}
	runTitle(context.Background(), "@4", "Did the old work.", "")
	ws := readWS(t, "@4")
	if ws.ShortTitle.Title != "" || ws.ShortTitle.Final || ws.FirstPrompt != "" {
		t.Fatalf("stale title written after clear: %+v", ws)
	}
}

// Every title request is attributed in the usage ledger: feature, window and
// the project (worktree paths fold into their repo).
func TestTitleRequestCarriesLedgerMeta(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "idle", Task: "sort photos", WorkDir: "/src/agentctl/.worktrees/agent-x-issue-3"})
	runTitle(context.Background(), "@4", "", "")
	want := llmusage.Meta{Feature: "titles", Window: "@4", Project: "agentctl"}
	if e.fake.calls != 1 || e.fake.meta != want {
		t.Fatalf("calls=%d meta=%+v, want %+v", e.fake.calls, e.fake.meta, want)
	}
}

func TestProjectName(t *testing.T) {
	for in, want := range map[string]string{
		"":                                 "",
		"/src/agentctl":                    "agentctl",
		"/src/agentctl/.worktrees/agent-1": "agentctl",
		"/src/agentctl/sub/dir":            "dir",
	} {
		if got := projectName(in); got != want {
			t.Errorf("projectName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The price cache is kept fresh whenever hook-title runs with titles on,
// including runs that find nothing to title; never when titles are off.
func TestTitleKeepsPriceCacheFresh(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "idle", Task: "sort photos"})
	runTitle(context.Background(), "@4", "", "")
	runTitle(context.Background(), "@4", "Done.", "") // refinement, then frozen
	runTitle(context.Background(), "@4", "More.", "") // frozen: nothing to title
	if got := e.refreshes.Load(); got != 3 {
		t.Errorf("refresh checks = %d, want 3", got)
	}
	e.settings.Enabled = false
	runTitle(context.Background(), "@4", "", "")
	if got := e.refreshes.Load(); got != 3 {
		t.Errorf("refreshed with titles off (%d)", got)
	}
}
