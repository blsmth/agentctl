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
	"github.com/blsmth/agentctl/internal/shorttitle"
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
}

func (f *titleFake) Complete(_ context.Context, p string) (string, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
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
	t.Cleanup(func() {
		titleSettings, titleKeyAvailable, newTitleCompleter, titleTakeBudget, titleNow = oldS, oldK, oldC, oldB, oldN
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

func logWithSummary(t *testing.T, summary string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	line := fmt.Sprintf(`{"type":"system","subtype":"away_summary","content":%q,"uuid":"u"}`+"\n", summary)
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTitleWritesAndThrottles(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "idle", Task: "sort my photo library by year"})
	log := logWithSummary(t, "Sorted 2019 into albums; next is 2020.")

	runTitle(context.Background(), "@4", log, "I finished the 2019 folder.")
	st := readTitle(t, "@4")
	if st.Title != "Organizing your Photos" || !st.At.Equal(e.now) || st.SummaryHash != shorttitle.Hash("Sorted 2019 into albums; next is 2020.") {
		t.Fatalf("title state = %+v", st)
	}
	p := e.fake.prompts[0]
	for _, s := range []string{"sort my photo library", "Sorted 2019 into albums", "I finished the 2019 folder."} {
		if !strings.Contains(p, s) {
			t.Errorf("prompt missing %q:\n%s", s, p)
		}
	}

	// Same input again: nothing to do.
	runTitle(context.Background(), "@4", log, "I finished the 2019 folder.")
	// New last message within the interval: throttled.
	e.now = e.now.Add(time.Minute)
	runTitle(context.Background(), "@4", log, "Now working on 2020.")
	if e.fake.calls != 1 {
		t.Fatalf("calls = %d, want 1 (same input, then throttled)", e.fake.calls)
	}
	// A new away_summary beats the throttle.
	runTitle(context.Background(), "@4", logWithSummary(t, "Sorted 2020 as well."), "")
	if e.fake.calls != 2 {
		t.Fatalf("calls = %d, want 2 after a new away_summary", e.fake.calls)
	}
	// After the interval a new message retitles.
	e.now = e.now.Add(shorttitle.RetitleInterval)
	runTitle(context.Background(), "@4", log, "Moving on to tagging.")
	if e.fake.calls != 3 {
		t.Fatalf("calls = %d, want 3 after the interval", e.fake.calls)
	}
}

func TestTitleUsesRecordedLastText(t *testing.T) {
	e := setupTitle(t)
	seedWindow(t, "@4", windowstate.WindowState{State: "working", LastText: "Refactored the sort comparator."})
	runTitle(context.Background(), "@4", "", "")
	if e.fake.calls != 1 || !strings.Contains(e.fake.prompts[0], "Refactored the sort comparator.") {
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
	runTitle(context.Background(), "@4", "", "new text")
	if e.fake.calls != 1 {
		t.Fatalf("calls = %d during backoff", e.fake.calls)
	}
	// After it, a retry that succeeds clears the backoff.
	e.now = st.RetryAfter.Add(time.Second)
	e.fake.err = nil
	runTitle(context.Background(), "@4", "", "new text")
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
	seedWindow(t, "@9", windowstate.WindowState{State: "idle"})
	stdin := fmt.Sprintf(`{"session_id":"s","transcript_path":%q,"last_assistant_message":"Wrote the hub sort fix."}`,
		logWithSummary(t, "Fixed the hub sort order."))
	runHookCaptured(t, nil, stdin, Title)
	if e.fake.calls != 1 || !strings.Contains(e.fake.prompts[0], "Wrote the hub sort fix.") || !strings.Contains(e.fake.prompts[0], "Fixed the hub sort order.") {
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
	runTitle(context.Background(), "@4", "", "done")
	if e.built.Load() != 0 || e.budget.Load() != 0 {
		t.Fatalf("fresh marker: built=%d budget=%d, want no request", e.built.Load(), e.budget.Load())
	}
	if st := readTitle(t, "@4"); st.Failures != 0 || !st.RetryAfter.IsZero() {
		t.Errorf("skip must leave no backoff: %+v", st)
	}

	// Marker expired: one probe goes through.
	fresh = false
	runTitle(context.Background(), "@4", "", "done")
	if e.built.Load() != 1 || e.budget.Load() != 1 {
		t.Fatalf("probe: built=%d budget=%d, want 1/1", e.built.Load(), e.budget.Load())
	}
}

func TestTitleNoCreditsErrorLeavesNoBackoff(t *testing.T) {
	e := setupTitle(t)
	e.fake.err = fmt.Errorf("llm: nous returned HTTP 402: %w", backendauth.ErrNoCredits)
	seedWindow(t, "@4", windowstate.WindowState{State: "idle", Task: "sort my photo library by year"})
	runTitle(context.Background(), "@4", "", "done")
	if st := readTitle(t, "@4"); st.Failures != 0 || !st.RetryAfter.IsZero() {
		t.Errorf("no-credits must not count as a failure: %+v", st)
	}
}
