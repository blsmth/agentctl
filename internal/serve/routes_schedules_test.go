package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
)

// fakeRunner is the run-now seam: it records calls and returns a canned
// entry. No test reaches cronedit.RunSubprocess.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	entry cron.Entry
	err   error
}

func (f *fakeRunner) run(_ context.Context, name string) (cron.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	e := f.entry
	e.Job = name
	return e, f.err
}

type scheduleEnv struct {
	*writeEnv
	store    *cronedit.Store
	runner   *fakeRunner
	projDir  string
	stateDir string
}

// newScheduleEnv is a write env whose fleet project lives in a real temp
// dir, with the schedules store over temp files and a fake runner.
func newScheduleEnv(t *testing.T, allowWrite bool) *scheduleEnv {
	t.Helper()
	root := t.TempDir()
	se := &scheduleEnv{runner: &fakeRunner{}, projDir: t.TempDir(), stateDir: filepath.Join(root, "state", "cron")}
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.Local)
	se.store = &cronedit.Store{
		JobsPath:           func() (string, error) { return filepath.Join(root, "config", cron.SchedulesFile), nil },
		StateDir:           func() (string, error) { return se.stateDir, nil },
		Now:                func() time.Time { return now },
		Config:             func() config.CronConfig { return config.CronConfig{} },
		ResolvePromptKey:   func(key string) (string, error) { return "", errors.New("not set") },
		SchedulerInstalled: func() bool { return false },
	}
	se.writeEnv = newWriteEnvWith(t, allowWrite, func(o *Options) {
		o.Schedules = se.store
		o.RunSchedule = se.runner.run
	})
	g := writeGroup("working", false, "go-engineer", "dispatch")
	g.Dir, g.Key = se.projDir, se.projDir
	se.fleet.set(g)
	return se
}

func (se *scheduleEnv) jobBody(name string) string {
	j := map[string]any{"name": name, "schedule": "30 7 * * 1-5", "dir": se.projDir, "agent": "go-engineer", "prompt": "Triage new issues."}
	data, _ := json.Marshal(map[string]any{"job": j})
	return string(data)
}

func (se *scheduleEnv) send(method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:51515"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(CSRFHeader, "1")
	r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: se.tok})
	w := httptest.NewRecorder()
	se.Handler().ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %q: %v", w.Body, err)
	}
	return v
}

func TestSchedulesListEmpty(t *testing.T) {
	se := newScheduleEnv(t, false)
	w := do(se.Handler(), "GET", "/v1/schedules", withCookie(se.tok))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := decode[map[string]any](t, w)
	jobs, ok := got["jobs"].([]any)
	if !ok || len(jobs) != 0 || got["scheduler_installed"] != false || got["enabled"] != true {
		t.Errorf("body = %v", got)
	}
	th := got["throttles"].(map[string]any)
	if th["min_interval"] != "15m" || th["max_concurrent"] != float64(2) {
		t.Errorf("throttles = %v", th)
	}
	if w := do(se.Handler(), "GET", "/v1/schedules"); w.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: status %d", w.Code)
	}
}

func TestSchedulesWriteGating(t *testing.T) {
	se := newScheduleEnv(t, false)
	if w := se.send("POST", "/v1/schedules", se.jobBody("triage")); w.Code != http.StatusForbidden || errCode(t, w) != codeWriteDisabled {
		t.Errorf("allow_write off: %d %s", w.Code, w.Body)
	}
	se = newScheduleEnv(t, true)
	w := se.post("/v1/schedules", se.jobBody("triage"), withoutHeader(CSRFHeader))
	if w.Code != http.StatusForbidden || errCode(t, w) != codeCSRF {
		t.Errorf("no CSRF: %d %s", w.Code, w.Body)
	}
	lines := se.auditLines(t)
	if len(lines) != 1 || lines[0].Action != "schedule-create" || lines[0].Result != codeCSRF {
		t.Errorf("audit = %+v", lines)
	}
	if jobs, _ := se.store.Load(); len(jobs.Jobs) != 0 {
		t.Errorf("a rejected write saved a job: %+v", jobs.Jobs)
	}
}

func TestSchedulesCreateEditDelete(t *testing.T) {
	se := newScheduleEnv(t, true)

	// Validation errors come back per field.
	bad := `{"job":{"name":"../x","schedule":"* * * * *","dir":"/nowhere","agent":"nobody","prompt":"hi\u001b[2J"}}`
	w := se.send("POST", "/v1/schedules", bad)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid create: %d %s", w.Code, w.Body)
	}
	inv := decode[invalidBody](t, w)
	var got []string
	for _, f := range inv.Fields {
		got = append(got, f.Field)
	}
	if inv.Code != codeInvalid || strings.Join(got, ",") != "name,dir,schedule,prompt,dir" {
		t.Errorf("fields = %v (%+v)", got, inv)
	}

	// A dir outside the project list is refused even when it exists.
	other := t.TempDir()
	body := strings.Replace(se.jobBody("triage"), se.projDir, other, 1)
	if w := se.send("POST", "/v1/schedules", body); w.Code != 422 || !strings.Contains(w.Body.String(), "project list") {
		t.Errorf("unknown project dir: %d %s", w.Code, w.Body)
	}

	// Create through the picker model.
	simple := `{"job":{"name":"triage","dir":"` + se.projDir + `","agent":"go-engineer","prompt":"Triage."},"simple":{"kind":"days","minute":15,"hour":8,"days":[5,1]}}`
	w = se.send("POST", "/v1/schedules", simple)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	out := decode[scheduleOut](t, w)
	if out.Job.Schedule != "15 8 * * 1,5" || out.Simple == nil || out.Simple.Kind != cronedit.KindDays || out.NextFire == nil || !out.Enabled {
		t.Errorf("created = %+v", out)
	}
	if w := se.send("POST", "/v1/schedules", simple); w.Code != 422 || !strings.Contains(w.Body.String(), "already exists") {
		t.Errorf("duplicate: %d %s", w.Code, w.Body)
	}

	// Read it back.
	w = do(se.Handler(), "GET", "/v1/schedules/triage", withCookie(se.tok))
	if d := decode[cronedit.Detail](t, w); w.Code != 200 || d.Name != "triage" || d.Runs == nil {
		t.Errorf("get: %d %+v", w.Code, d)
	}
	if w := do(se.Handler(), "GET", "/v1/schedules/nope", withCookie(se.tok)); w.Code != 404 {
		t.Errorf("get missing: %d", w.Code)
	}

	// Update: the name is immutable, a prompt edit keeps fire state.
	if w := se.send("PUT", "/v1/schedules/triage", se.jobBody("renamed")); w.Code != 422 || !strings.Contains(w.Body.String(), "cannot be changed") {
		t.Errorf("rename: %d %s", w.Code, w.Body)
	}
	if err := cron.Seed(se.stateDir, "triage", time.Date(2026, 11, 16, 8, 15, 0, 0, time.Local)); err != nil {
		t.Fatal(err)
	}
	edit := `{"job":{"schedule":"15 8 * * 1,5","dir":"` + se.projDir + `","agent":"go-engineer","prompt":"Triage, again."}}`
	w = se.send("POST", "/v1/schedules/triage", edit)
	if w.Code != 200 {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	if out := decode[scheduleOut](t, w); out.Reseeded || strings.Join(out.Changed, ",") != "prompt" || out.Job.Prompt != "Triage, again." {
		t.Errorf("update = %+v", out)
	}
	if st, _ := cron.LoadState(se.stateDir); !st.Jobs["triage"].LastFire.Equal(time.Date(2026, 11, 16, 8, 15, 0, 0, time.Local)) {
		t.Errorf("prompt edit moved last_fire: %+v", st.Jobs)
	}
	if w := se.send("PUT", "/v1/schedules/nope", se.jobBody("nope")); w.Code != 404 {
		t.Errorf("update missing: %d %s", w.Code, w.Body)
	}

	// Pause keeps the job paused across an edit that omits enabled.
	if w := se.send("POST", "/v1/schedules/triage/pause", `{}`); w.Code != 200 || decode[scheduleOut](t, w).Enabled {
		t.Errorf("pause: %d %s", w.Code, w.Body)
	}
	if w := se.send("PUT", "/v1/schedules/triage", edit); w.Code != 200 || decode[scheduleOut](t, w).Enabled {
		t.Errorf("edit resumed a paused job: %s", w.Body)
	}
	if w := se.send("POST", "/v1/schedules/triage/resume", `{}`); w.Code != 200 || !decode[scheduleOut](t, w).Enabled {
		t.Errorf("resume: %d %s", w.Code, w.Body)
	}

	// The list carries it.
	w = do(se.Handler(), "GET", "/v1/schedules", withCookie(se.tok))
	if ov := decode[schedulesBody](t, w); len(ov.Jobs) != 1 || ov.Jobs[0].Words != "Mon, Fri 08:15" {
		t.Errorf("list = %+v", ov)
	}

	// Delete (both spellings), then 404.
	if w := se.send("DELETE", "/v1/schedules/triage", `{}`); w.Code != 200 {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if w := se.send("POST", "/v1/schedules/triage/delete", `{}`); w.Code != 404 {
		t.Errorf("delete again: %d %s", w.Code, w.Body)
	}

	// Every write was audited, with the job but never the prompt.
	actions := map[string]int{}
	for _, a := range se.auditLines(t) {
		actions[a.Action+":"+a.Result]++
		if a.Action == "schedule-create" && a.Result == "ok" && (a.Schedule != "triage" || a.Agent != "go-engineer" || a.BodySHA256 == "") {
			t.Errorf("create audit = %+v", a)
		}
	}
	for _, want := range []string{"schedule-create:invalid", "schedule-create:attempt", "schedule-create:ok", "schedule-update:invalid",
		"schedule-update:ok", "schedule-update:not_found", "schedule-pause:ok", "schedule-resume:ok", "schedule-delete:ok", "schedule-delete:not_found"} {
		if actions[want] == 0 {
			t.Errorf("no audit line %s in %v", want, actions)
		}
	}
}

func TestSchedulesPreview(t *testing.T) {
	se := newScheduleEnv(t, true)
	body := `{"job":{"name":"triage","dir":"` + se.projDir + `","agent":"go-engineer","prompt":"Triage."},"simple":{"kind":"weekdays","minute":30,"hour":7}}`
	w := se.send("POST", "/v1/schedules/preview", body)
	if w.Code != 200 {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	p := decode[cronedit.Preview](t, w)
	if !p.Valid || p.Words != "weekdays 07:30" || len(p.NextFires) != 3 || len(p.NextFiresText) != 3 || p.Schedule != "30 7 * * 1-5" {
		t.Errorf("preview = %+v", p)
	}
	if jobs, _ := se.store.Load(); len(jobs.Jobs) != 0 {
		t.Error("preview saved a job")
	}
	w = se.send("POST", "/v1/schedules/preview", `{"job":{"name":"x","schedule":"nope","dir":"`+se.projDir+`","agent":"ghost","prompt_key":"missing"}}`)
	p = decode[cronedit.Preview](t, w)
	if w.Code != 200 || p.Valid || len(p.Errors) < 2 {
		t.Errorf("invalid preview: %d %+v", w.Code, p)
	}
	if !strings.Contains(w.Body.String(), `context key \"missing\" cannot be read`) {
		t.Errorf("prompt key error missing: %s", w.Body)
	}
}

func TestSchedulesRunNow(t *testing.T) {
	se := newScheduleEnv(t, true)
	if w := se.send("POST", "/v1/schedules", se.jobBody("triage")); w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}

	se.runner.entry = cron.Entry{Result: cron.ResultSkippedUsage, Note: "five_hour usage 82% >= 80%"}
	w := se.send("POST", "/v1/schedules/triage/run", `{}`)
	if w.Code != 200 {
		t.Fatalf("run: %d %s", w.Code, w.Body)
	}
	out := decode[runOut](t, w)
	if out.Fired || out.Result != cron.ResultSkippedUsage || out.Reason != "five_hour usage 82% >= 80%" || out.Name != "triage" {
		t.Errorf("skipped run = %+v", out)
	}

	se.runner.entry = cron.Entry{Result: cron.ResultFired, WindowID: "@9", Session: "agentctl-proj"}
	if out := decode[runOut](t, se.send("POST", "/v1/schedules/triage/run", `{}`)); !out.Fired || out.WindowID != "@9" {
		t.Errorf("fired run = %+v", out)
	}

	se.runner.err = errors.New("exec failed")
	if w := se.send("POST", "/v1/schedules/triage/run", `{}`); w.Code != http.StatusBadGateway {
		t.Errorf("runner error: %d %s", w.Code, w.Body)
	}
	if w := se.send("POST", "/v1/schedules/nope/run", `{}`); w.Code != 404 {
		t.Errorf("missing job: %d", w.Code)
	}
	if w := se.send("POST", "/v1/schedules/triage/run", `{"force":true}`); w.Code != 400 {
		t.Errorf("force accepted: %d %s", w.Code, w.Body)
	}
	if len(se.runner.calls) != 3 {
		t.Errorf("runner calls = %v", se.runner.calls)
	}
}

func TestSchedulesRunningFromFleet(t *testing.T) {
	se := newScheduleEnv(t, true)
	if w := se.send("POST", "/v1/schedules", se.jobBody("triage")); w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	g := writeGroup("working", false, "go-engineer", "dispatch")
	g.Dir = se.projDir
	g.Rows = append(g.Rows, fleet.Row{Session: g.Session, AgentState: state.AgentState{WindowID: "@31", ScheduledBy: "triage"}})
	se.fleet.set(g)
	ov := decode[schedulesBody](t, do(se.Handler(), "GET", "/v1/schedules", withCookie(se.tok)))
	if len(ov.Jobs) != 1 || !ov.Jobs[0].Running || ov.Active != 1 || ov.Jobs[0].Windows[0].WindowID != "@31" {
		t.Errorf("list = %+v", ov)
	}
}

func TestScheduleActionFor(t *testing.T) {
	for path, want := range map[string]string{
		"/v1/schedules": "schedule-create", "/v1/schedules/preview": "schedule-preview", "/v1/schedules/x": "schedule-update",
		"/v1/schedules/x/run": "schedule-run", "/v1/schedules/x/delete": "schedule-delete", "/v1/schedules/x/other": "",
	} {
		got, _, _, _ := writeActionFor(path)
		if got != want {
			t.Errorf("writeActionFor(%q) = %q, want %q", path, got, want)
		}
	}
}

// Schedule-changing updates and resume wait on the cron tick lock, which a
// slow launch holds, so they extend the write deadline like run-now.
func TestScheduleSlowRoutesExtendWriteDeadline(t *testing.T) {
	se := newScheduleEnv(t, true)
	if w := se.post("/v1/schedules", se.jobBody("triage")); w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	tests := []struct {
		name, method, path, body string
		want                     time.Duration
	}{
		{"put", "PUT", "/v1/schedules/triage", se.jobBody("triage"), scheduleSlowDeadline},
		{"post update", "POST", "/v1/schedules/triage", se.jobBody("triage"), scheduleSlowDeadline},
		{"resume", "POST", "/v1/schedules/triage/resume", "{}", scheduleSlowDeadline},
		{"run", "POST", "/v1/schedules/triage/run", "{}", scheduleWriteDeadline},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			r.RemoteAddr = "192.168.1.20:51515"
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set(CSRFHeader, "1")
			r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: se.tok})
			rec := &deadlineRecorder{ResponseWriter: httptest.NewRecorder()}
			se.Handler().ServeHTTP(rec, r)
			if until := time.Until(rec.deadline); until < tt.want-5*time.Second {
				t.Errorf("write deadline %v from now, want about %v", until, tt.want)
			}
		})
	}
}
