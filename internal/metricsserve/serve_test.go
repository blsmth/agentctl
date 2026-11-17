package metricsserve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/blsmth/agentctl/internal/metrics"
	"github.com/blsmth/agentctl/internal/metrics/events"
	"github.com/blsmth/agentctl/internal/metricsproc"
	"github.com/blsmth/agentctl/internal/selfexec"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// sample returns the value of the metric in family name whose labels include
// all of want, summed (counters/gauges) or the histogram sample count.
func sample(t *testing.T, reg *prometheus.Registry, name string, want map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var total float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if !hasLabels(m, want) {
				continue
			}
			switch {
			case m.Counter != nil:
				total += m.GetCounter().GetValue()
			case m.Gauge != nil:
				total += m.GetGauge().GetValue()
			case m.Histogram != nil:
				total += float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return total
}

func hasLabels(m *dto.Metric, want map[string]string) bool {
	got := map[string]string{}
	for _, l := range m.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

type step struct {
	win      string
	from, to state.AgentStatus
	tool     string
}

// The serve feeder, fed the events for a Transition sequence, must yield the
// same counters as recording those Transitions directly (the expectations in
// internal/metrics/transitions_test.go).
func TestFeederMatchesDirectTransitions(t *testing.T) {
	W, I, B, G := state.StatusWorking, state.StatusIdle, state.StatusBlocked, state.StatusGone
	seq := []step{
		{"@1", I, W, ""}, {"@1", W, B, "Bash"}, {"@1", B, W, ""}, {"@1", W, I, ""},
		{"@2", I, W, ""}, {"@2", W, G, ""}, // crash
		{"@1", I, G, ""},
	}

	direct := prometheus.NewRegistry()
	dt := metrics.NewTransitionTracker(direct)
	for _, s := range seq {
		dt.RecordTransition(state.Transition{Agent: "go-engineer", Project: "proj", WindowID: s.win, From: s.from, To: s.to, LastToolName: s.tool})
	}

	served := prometheus.NewRegistry()
	srv := NewServer(served, time.Now())
	rt := filepath.Join(t.TempDir(), "proj", ".agentctl")
	srv.SetProjects([]Project{{Name: "proj", RuntimeDir: rt}})
	base := time.Now()
	for i, s := range seq {
		if err := events.Append(rt, events.Event{
			TS: base.Add(time.Duration(i) * time.Millisecond), Project: filepath.Dir(rt), WindowID: s.win,
			Agent: "go-engineer", From: s.from.String(), To: s.to.String(), Tool: s.tool,
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv.Step()

	for _, fam := range []string{
		"agentctl_agents_spawned_total", "agentctl_sessions_completed_total", "agentctl_state_transitions_total",
		"agentctl_tool_uses_total", "agentctl_failures_total", "agentctl_agents_active",
		"agentctl_session_duration_seconds", "agentctl_working_duration_seconds", "agentctl_blocked_duration_seconds",
	} {
		want := sample(t, direct, fam, nil)
		got := sample(t, served, fam, map[string]string{"project": "proj"})
		if got != want {
			t.Errorf("%s = %v, want %v", fam, got, want)
		}
	}
	if got := sample(t, served, "agentctl_failures_total", nil); got != 1 {
		t.Errorf("failures = %v, want 1", got)
	}
}

// Events present before the serve started are not replayed.
func TestServeDoesNotReplayHistory(t *testing.T) {
	rt := filepath.Join(t.TempDir(), "proj", ".agentctl")
	if err := events.Append(rt, events.Event{Project: filepath.Dir(rt), WindowID: "@1", Agent: "a", From: "idle", To: "working"}); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	srv := NewServer(reg, time.Now())
	srv.SetProjects([]Project{{Name: "proj", RuntimeDir: rt}})
	srv.Step()
	if got := sample(t, reg, "agentctl_state_transitions_total", nil); got != 0 {
		t.Fatalf("history replayed: %v", got)
	}
}

// A project that appears after startup is read from the start, but only for
// events at or after the serve's start.
func TestLateProjectKeepsFirstEvents(t *testing.T) {
	reg := prometheus.NewRegistry()
	started := time.Now()
	srv := NewServer(reg, started)
	srv.SetProjects(nil)

	rt := filepath.Join(t.TempDir(), "late", ".agentctl")
	events.Append(rt, events.Event{TS: started.Add(-time.Hour), Project: filepath.Dir(rt), WindowID: "@1", Agent: "a", From: "idle", To: "working"})  //nolint:errcheck
	events.Append(rt, events.Event{TS: started.Add(time.Second), Project: filepath.Dir(rt), WindowID: "@1", Agent: "a", From: "working", To: "idle"}) //nolint:errcheck
	srv.SetProjects([]Project{{Name: "late", RuntimeDir: rt}})
	srv.Step()
	if got := sample(t, reg, "agentctl_state_transitions_total", map[string]string{"project": "late"}); got != 1 {
		t.Fatalf("transitions = %v, want 1", got)
	}
}

func writeWindow(t *testing.T, rt, id string, ws windowstate.WindowState) {
	t.Helper()
	dir := windowstate.WindowsDirFor(rt)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(ws)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeLog(t *testing.T, dir string, in, out int) string {
	t.Helper()
	p := filepath.Join(dir, "s.jsonl")
	line := fmt.Sprintf(`{"type":"assistant","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":%d,"output_tokens":%d},"content":[{"type":"tool_use","name":"Bash"}]}}`+"\n", in, out)
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Two projects, one registry and one serve: transition and usage series are
// split by the project label.
func TestTwoProjectsOneRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	srv := NewServer(reg, time.Now())

	rtA := filepath.Join(t.TempDir(), "alpha", ".agentctl")
	rtB := filepath.Join(t.TempDir(), "beta", ".agentctl")
	srv.SetProjects([]Project{{Name: "alpha", RuntimeDir: rtA}, {Name: "beta", RuntimeDir: rtB}})

	writeWindow(t, rtA, "@1", windowstate.WindowState{Instance: "go-engineer:1", AgentName: "go-engineer", LogPath: writeLog(t, t.TempDir(), 100, 10)})
	writeWindow(t, rtB, "@1", windowstate.WindowState{Instance: "go-engineer:1", AgentName: "go-engineer", LogPath: writeLog(t, t.TempDir(), 7, 3)})
	// Untracked windows contribute no usage.
	writeWindow(t, rtB, "@2", windowstate.WindowState{Instance: "shell", LogPath: writeLog(t, t.TempDir(), 999, 999)})

	for _, rt := range []string{rtA, rtB} {
		events.Append(rt, events.Event{Project: filepath.Dir(rt), WindowID: "@1", Agent: "go-engineer", From: "idle", To: "working"}) //nolint:errcheck
	}
	srv.Step()

	for _, c := range []struct {
		project string
		input   float64
	}{{"alpha", 100}, {"beta", 7}} {
		got := sample(t, reg, "agentctl_tokens_total", map[string]string{"project": c.project, "direction": "input", "cache_type": "none"})
		if got != c.input {
			t.Errorf("%s input tokens = %v, want %v", c.project, got, c.input)
		}
		if got := sample(t, reg, "agentctl_tool_use_total", map[string]string{"project": c.project, "tool": "Bash"}); got != 1 {
			t.Errorf("%s tool uses = %v, want 1", c.project, got)
		}
		if got := sample(t, reg, "agentctl_state_transitions_total", map[string]string{"project": c.project, "to_state": "working"}); got != 1 {
			t.Errorf("%s transitions = %v, want 1", c.project, got)
		}
	}
	if got := sample(t, reg, "agentctl_cost_estimate_dollars", map[string]string{"project": "alpha"}); got <= 0 {
		t.Errorf("alpha cost = %v, want > 0", got)
	}

	// Window gone from windowstate: its stream is released.
	if err := os.Remove(filepath.Join(windowstate.WindowsDirFor(rtB), "@1.json")); err != nil {
		t.Fatal(err)
	}
	srv.Step()
	if srv.usage.Len() != 1 {
		t.Errorf("usage streams = %d, want 1", srv.usage.Len())
	}
}

// One serve per machine: the second Acquire loses, and Running sees the first.
func TestSingleServePerMachine(t *testing.T) {
	dir := t.TempDir()
	restore := metricsproc.SetSeamsForTest(func() (string, error) { return dir, nil }, nil, nil)
	defer restore()

	rel, ok, err := metricsproc.Acquire()
	if err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	if _, ok, err := metricsproc.Acquire(); err != nil || ok {
		t.Fatalf("second acquire: ok=%v err=%v, want not acquired", ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, metricsproc.PidName)); err != nil {
		t.Errorf("pidfile missing: %v", err)
	}
	rel()
	rel2, ok, _ := metricsproc.Acquire()
	if !ok {
		t.Fatal("acquire after release failed")
	}
	rel2()
}

// The serve exits once there have been no agentctl sessions for the idle
// period, and keeps running while projects exist.
func TestLoopExitsWhenIdle(t *testing.T) {
	reg := prometheus.NewRegistry()
	srv := NewServer(reg, time.Now())
	calls := 0
	opts := Options{
		Poll: time.Millisecond,
		Idle: 20 * time.Millisecond,
		Discover: func() ([]Project, error) {
			calls++
			return nil, nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- loop(context.Background(), srv, opts, selfexec.Identity{}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not exit when idle")
	}
	if calls == 0 {
		t.Error("discover never ran")
	}
}

// A project missing from one discovery pass (e.g. a failed tmux query) and
// then returning must not re-read its events or logs: counters stay put.
func TestProjectDropAndReturnDoesNotDoubleCount(t *testing.T) {
	reg := prometheus.NewRegistry()
	started := time.Now()
	srv := NewServer(reg, started)
	srv.SetProjects(nil) // first pass: later projects count as "late" and replay since start

	rt := filepath.Join(t.TempDir(), "proj", ".agentctl")
	proj := []Project{{Name: "proj", RuntimeDir: rt}}
	writeWindow(t, rt, "@1", windowstate.WindowState{Instance: "go-engineer:1", AgentName: "go-engineer", LogPath: writeLog(t, t.TempDir(), 100, 10)})
	events.Append(rt, events.Event{TS: started.Add(time.Second), Project: filepath.Dir(rt), WindowID: "@1", Agent: "go-engineer", From: "idle", To: "working", Tool: "Bash"}) //nolint:errcheck

	srv.SetProjects(proj)
	srv.Step()

	counters := func() [3]float64 {
		return [3]float64{
			sample(t, reg, "agentctl_state_transitions_total", map[string]string{"project": "proj"}),
			sample(t, reg, "agentctl_tokens_total", map[string]string{"project": "proj", "direction": "input"}),
			sample(t, reg, "agentctl_tool_use_total", map[string]string{"project": "proj", "tool": "Bash"}),
		}
	}
	before := counters()
	if before[0] != 1 || before[1] != 100 || before[2] != 1 {
		t.Fatalf("baseline counters = %v", before)
	}

	srv.SetProjects(nil) // dropped for one pass
	srv.Step()
	srv.SetProjects(proj) // and back
	srv.Step()
	srv.Step()

	if after := counters(); after != before {
		t.Fatalf("counters after drop+return = %v, want %v (double counted)", after, before)
	}
	if srv.usage.Len() != 1 {
		t.Errorf("usage streams = %d, want 1", srv.usage.Len())
	}

	// New events after the return are still picked up.
	events.Append(rt, events.Event{TS: started.Add(2 * time.Second), Project: filepath.Dir(rt), WindowID: "@1", Agent: "go-engineer", From: "working", To: "idle"}) //nolint:errcheck
	srv.Step()
	if got := sample(t, reg, "agentctl_state_transitions_total", map[string]string{"project": "proj"}); got != 2 {
		t.Errorf("transitions after return = %v, want 2", got)
	}
}

// After a long absence the project's state expires, and its return does not
// replay events from the serve start.
func TestProjectExpiresAfterLongAbsence(t *testing.T) {
	reg := prometheus.NewRegistry()
	started := time.Now()
	srv := NewServer(reg, started)
	srv.SetProjectsAt(nil, started)
	rt := filepath.Join(t.TempDir(), "proj", ".agentctl")
	proj := []Project{{Name: "proj", RuntimeDir: rt}}
	events.Append(rt, events.Event{TS: started.Add(time.Second), Project: filepath.Dir(rt), WindowID: "@1", Agent: "a", From: "idle", To: "working"}) //nolint:errcheck
	srv.SetProjectsAt(proj, started)
	srv.Step()

	srv.SetProjectsAt(nil, started.Add(absenceExpiry/2))
	if len(srv.tailers) != 1 {
		t.Fatalf("tailer dropped before expiry")
	}
	srv.SetProjectsAt(nil, started.Add(absenceExpiry+time.Minute))
	if len(srv.tailers) != 0 {
		t.Fatalf("tailer kept past expiry")
	}
	srv.SetProjectsAt(proj, started.Add(absenceExpiry+2*time.Minute))
	srv.Step()
	if got := sample(t, reg, "agentctl_state_transitions_total", nil); got != 1 {
		t.Fatalf("transitions = %v, want 1 (no replay)", got)
	}
}

// A serve starting while agents are already running seeds the active gauge
// from their windowstate records.
func TestSeedActiveGaugeFromWindowState(t *testing.T) {
	reg := prometheus.NewRegistry()
	srv := NewServer(reg, time.Now())
	rt := filepath.Join(t.TempDir(), "proj", ".agentctl")
	writeWindow(t, rt, "@1", windowstate.WindowState{Instance: "go-engineer:1", AgentName: "go-engineer", State: "working"})
	writeWindow(t, rt, "@2", windowstate.WindowState{Instance: "go-engineer:2", AgentName: "go-engineer", State: "blocked"})
	writeWindow(t, rt, "@3", windowstate.WindowState{Instance: "shell"})
	srv.SetProjects([]Project{{Name: "proj", RuntimeDir: rt}})
	if got := sample(t, reg, "agentctl_agents_active", map[string]string{"state": "working"}); got != 1 {
		t.Errorf("working = %v, want 1", got)
	}
	if got := sample(t, reg, "agentctl_agents_active", map[string]string{"state": "blocked"}); got != 1 {
		t.Errorf("blocked = %v, want 1", got)
	}
	// A later real transition moves the seeded window, not double counts it.
	events.Append(rt, events.Event{Project: filepath.Dir(rt), WindowID: "@1", Agent: "go-engineer", From: "working", To: "idle"}) //nolint:errcheck
	srv.Step()
	if got := sample(t, reg, "agentctl_agents_active", map[string]string{"state": "working"}); got != 0 {
		t.Errorf("working after idle = %v, want 0", got)
	}
}
