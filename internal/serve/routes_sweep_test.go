package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/sweeprun"
)

type sweepCall struct {
	target sweeprun.Target
	dryRun bool
}

// fakeSweeper stands in for the sweep subprocess. block, when set, holds a
// run until it is closed.
type fakeSweeper struct {
	mu      sync.Mutex
	calls   []sweepCall
	preview sweeprun.Report
	apply   sweeprun.Report
	err     error
	started chan struct{}
	block   chan struct{}
}

func (f *fakeSweeper) run(_ context.Context, t sweeprun.Target, dryRun bool) (sweeprun.Report, error) {
	f.mu.Lock()
	f.calls = append(f.calls, sweepCall{t, dryRun})
	rep, err, block, started := f.apply, f.err, f.block, f.started
	if dryRun {
		rep = f.preview
	}
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		<-block
	}
	rep.Session, rep.DryRun = t.Session, dryRun
	return rep, err
}

func (f *fakeSweeper) all() []sweepCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sweepCall(nil), f.calls...)
}

func decodeSweep(t *testing.T, body []byte) sweepOut {
	t.Helper()
	var out sweepOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return out
}

func TestSweepPreviewAndApply(t *testing.T) {
	e := newWriteEnv(t, true)
	e.sweep.preview = sweeprun.Report{
		Closed:     []sweeprun.Row{{WindowID: "@1", Name: "a"}, {WindowID: "@2", Name: "b"}},
		Attention:  []sweeprun.Row{{WindowID: "@3", Name: "c"}},
		InProgress: []sweeprun.Row{},
	}
	e.sweep.apply = sweeprun.Report{Closed: []sweeprun.Row{{WindowID: "@1", Name: "a"}}}

	w := e.post("/v1/sweep/preview", `{"session":"agentctl-proj"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	out := decodeSweep(t, w.Body.Bytes())
	if out.Project != "proj" || out.Session != "agentctl-proj" || !out.DryRun || len(out.Closed) != 2 || len(out.Attention) != 1 {
		t.Errorf("preview = %+v", out)
	}

	w = e.post("/v1/sweep", `{"session":"agentctl-proj"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	out = decodeSweep(t, w.Body.Bytes())
	if out.DryRun || len(out.Closed) != 1 || out.Closed[0].Name != "a" {
		t.Errorf("apply = %+v", out)
	}

	calls := e.sweep.all()
	want := sweeprun.Target{Session: "agentctl-proj", RuntimeDir: "/code/proj/.agentctl", ProjectDir: "/code/proj"}
	if len(calls) != 2 || !calls[0].dryRun || calls[1].dryRun || calls[0].target != want || calls[1].target != want {
		t.Errorf("calls = %+v", calls)
	}
	lines := e.auditLines(t)
	if len(lines) != 2 || lines[0].Action != "sweep-preview" || lines[1].Action != "sweep" ||
		lines[1].Session != "agentctl-proj" || lines[1].Project != "proj" || lines[1].Result != "ok" {
		t.Errorf("audit = %+v", lines)
	}
}

func TestSweepRejectsUnknownSessions(t *testing.T) {
	hub := writeGroup("idle", false, "x", "bare")
	hub.Session, hub.Key = sweeprun.HubSession, "hub"
	noRuntime := writeGroup("idle", false, "x", "bare")
	noRuntime.Session, noRuntime.Key, noRuntime.RuntimeDir = "agentctl-bare", "bare", ""
	tests := []struct {
		name, body string
		status     int
		code       string
	}{
		{"not in the fleet", `{"session":"agentctl-other"}`, http.StatusNotFound, codeUnknownSession},
		{"tmux target spec", `{"session":"agentctl-proj:1"}`, http.StatusNotFound, codeUnknownSession},
		{"flag", `{"session":"--dry-run"}`, http.StatusNotFound, codeUnknownSession},
		{"hub session", `{"session":"` + sweeprun.HubSession + `"}`, http.StatusBadRequest, codeUnknownSession},
		{"no runtime dir", `{"session":"agentctl-bare"}`, http.StatusBadRequest, codeUnknownSession},
		{"empty", `{"session":""}`, http.StatusBadRequest, codeBadRequest},
		{"control char", `{"session":"a\u0007"}`, http.StatusBadRequest, codeBadRequest},
		{"too long", `{"session":"` + strings.Repeat("a", maxSessionBytes+1) + `"}`, http.StatusBadRequest, codeBadRequest},
		{"unknown field", `{"session":"agentctl-proj","dir":"/"}`, http.StatusBadRequest, codeBadRequest},
	}
	for _, path := range []string{"/v1/sweep/preview", "/v1/sweep"} {
		for _, tt := range tests {
			t.Run(path+" "+tt.name, func(t *testing.T) {
				e := newWriteEnv(t, true)
				e.fleet.set(writeGroup("idle", false, "x", "bare"), hub, noRuntime)
				w := e.post(path, tt.body)
				if w.Code != tt.status || errCode(t, w) != tt.code {
					t.Fatalf("status %d: %s", w.Code, w.Body)
				}
				if n := len(e.sweep.all()); n != 0 {
					t.Errorf("sweep ran %d times", n)
				}
			})
		}
	}
}

func TestSweepFailureReported(t *testing.T) {
	e := newWriteEnv(t, true)
	e.sweep.err = errors.New(`sweep agentctl-proj: no tmux session "agentctl-proj"`)
	w := e.post("/v1/sweep", `{"session":"agentctl-proj"}`)
	if w.Code != http.StatusBadGateway || errCode(t, w) != codeActionFailed || !strings.Contains(w.Body.String(), "no tmux session") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestSweepOneAtATimePerSession(t *testing.T) {
	e := newWriteEnv(t, true)
	e.sweep.block = make(chan struct{})
	e.sweep.started = make(chan struct{}, 1)
	done := make(chan int)
	go func() { done <- e.post("/v1/sweep", `{"session":"agentctl-proj"}`).Code }()
	<-e.sweep.started
	w := e.post("/v1/sweep/preview", `{"session":"agentctl-proj"}`)
	if w.Code != http.StatusConflict || errCode(t, w) != codeBusy {
		t.Errorf("concurrent sweep: %d %s", w.Code, w.Body)
	}
	close(e.sweep.block)
	if code := <-done; code != http.StatusOK {
		t.Errorf("first sweep: %d", code)
	}
	e.sweep.mu.Lock()
	e.sweep.block, e.sweep.started = nil, nil
	e.sweep.mu.Unlock()
	if w := e.post("/v1/sweep/preview", `{"session":"agentctl-proj"}`); w.Code != http.StatusOK {
		t.Errorf("after release: %d %s", w.Code, w.Body)
	}
}

// The real seam must never run in tests: sweeprun.Run refuses a test binary.
func TestSweepDefaultSeamRefusesTestBinary(t *testing.T) {
	e := newWriteEnvWith(t, true, func(o *Options) { o.Sweep = nil })
	w := e.post("/v1/sweep/preview", `{"session":"agentctl-proj"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

// deadlineRecorder records the write deadline a handler sets.
type deadlineRecorder struct {
	http.ResponseWriter
	deadline time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.deadline = t
	return nil
}

// A sweep can outlast the server's 30s WriteTimeout, so its routes extend it.
func TestSweepDeadlineExtendsWriteTimeout(t *testing.T) {
	rec := &deadlineRecorder{ResponseWriter: httptest.NewRecorder()}
	called := false
	sweepDeadline(func(http.ResponseWriter, *http.Request) { called = true })(rec, httptest.NewRequest("POST", "/v1/sweep", nil))
	if !called {
		t.Fatal("handler not called")
	}
	if until := time.Until(rec.deadline); until < sweeprun.Timeout {
		t.Errorf("write deadline %v from now, want at least %v", until, sweeprun.Timeout)
	}
}

func TestSweepHonorsIncludeBare(t *testing.T) {
	e := newWriteEnv(t, true)
	for _, body := range []string{`{"session":"agentctl-proj","include_bare":true}`, `{"session":"agentctl-proj","include_bare":false}`, `{"session":"agentctl-proj"}`} {
		for _, path := range []string{"/v1/sweep/preview", "/v1/sweep"} {
			if w := e.post(path, body); w.Code != http.StatusOK {
				t.Fatalf("%s %s: %d %s", path, body, w.Code, w.Body)
			}
		}
	}
	calls := e.sweep.all()
	if len(calls) != 6 {
		t.Fatalf("calls = %+v", calls)
	}
	for i, c := range calls {
		if want := i < 2; c.target.IncludeBare != want {
			t.Errorf("call %d IncludeBare = %v, want %v", i, c.target.IncludeBare, want)
		}
	}
}
