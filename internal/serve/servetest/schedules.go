package servetest

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/prompttemplate"
)

// schedules is the harness's cron world: a jobs file and run history in the
// harness's own dir, a stubbed scheduler/config, and a canned run-now.
// Nothing here reaches the real schedules.json or spawns a subprocess.
type schedules struct {
	root string

	mu        sync.Mutex
	installed bool
	disabled  bool
	runResult cron.Entry
	runs      []string
}

func newSchedules(root string) *schedules {
	s := &schedules{root: root, installed: true}
	_ = os.MkdirAll(s.projectDir(), 0o755)
	return s
}

// projectDir is a real directory a test's fleet group can use as its dir,
// so a job saved against it passes the "dir exists" check.
func (s *schedules) projectDir() string { return filepath.Join(s.root, "sched-project") }

func (s *schedules) jobsPath() string {
	return filepath.Join(s.root, "cron-config", cron.SchedulesFile)
}
func (s *schedules) stateDir() string { return filepath.Join(s.root, "cron-state") }

func (s *schedules) store() *cronedit.Store {
	return &cronedit.Store{
		JobsPath: func() (string, error) { return s.jobsPath(), nil },
		StateDir: func() (string, error) { return s.stateDir(), nil },
		Now:      time.Now,
		Config: func() config.CronConfig {
			s.mu.Lock()
			defer s.mu.Unlock()
			c := config.CronConfig{}
			if s.disabled {
				off := false
				c.Enabled = &off
			}
			return c
		},
		ResolvePromptKey: func(string) (string, error) { return "", os.ErrNotExist },
		SchedulerInstalled: func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.installed
		},
	}
}

func (s *schedules) run(_ context.Context, name string) (cron.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, name)
	e := s.runResult
	if e.Result == "" {
		e.Result = cron.ResultFired
		e.WindowID = "@99"
		e.Session = "agentctl-shop"
	}
	e.Job, e.Manual, e.Time = name, true, time.Now()
	return e, nil
}

// SchedulesSpec is the body of PUT /__test/schedules: the world the cron
// API sees. Omitted fields reset to the default (installed, enabled).
type SchedulesSpec struct {
	Jobs []cron.Job   `json:"jobs"`
	Runs []cron.Entry `json:"runs"`
	// NotInstalled reports the ticker as missing.
	NotInstalled bool `json:"not_installed"`
	// CronDisabled sets cron.enabled false.
	CronDisabled bool `json:"cron_disabled"`
	// Run is what run-now returns (default: fired in @99).
	Run *cron.Entry `json:"run"`
	// Templates are written as the project's prompt templates (replacing
	// any from an earlier seed), so GET /v1/templates?dir=<project> and a
	// template job's checks see them.
	Templates []prompttemplate.Template `json:"templates"`
}

func (s *schedules) set(spec SchedulesSpec) error {
	s.mu.Lock()
	s.installed, s.disabled, s.runs = !spec.NotInstalled, spec.CronDisabled, nil
	s.runResult = cron.Entry{}
	if spec.Run != nil {
		s.runResult = *spec.Run
	}
	s.mu.Unlock()
	_ = os.RemoveAll(s.stateDir())
	_ = os.RemoveAll(filepath.Dir(s.jobsPath()))
	if err := os.MkdirAll(filepath.Dir(s.jobsPath()), 0o700); err != nil {
		return err
	}
	if err := cron.SaveJobs(s.jobsPath(), &cron.Jobs{Jobs: spec.Jobs}); err != nil {
		return err
	}
	for _, e := range spec.Runs {
		if err := cron.AppendRun(s.stateDir(), e); err != nil {
			return err
		}
	}
	_ = os.RemoveAll(prompttemplate.ProjectDir(s.projectDir()))
	for _, t := range spec.Templates {
		if _, err := prompttemplate.Save(t, prompttemplate.ScopeProject, s.projectDir()); err != nil {
			return err
		}
	}
	return nil
}

func (s *schedules) handleSet(h *Harness, w http.ResponseWriter, r *http.Request) {
	var spec SchedulesSpec
	if !decode(w, r, &spec) {
		return
	}
	if err := h.scheds.set(spec); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": s.projectDir()})
}

func (s *schedules) handleRuns(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"runs": append([]string{}, s.runs...), "dir": s.projectDir()})
}

// SeedSchedules replaces the cron jobs and runs the API sees (the devserver
// seeds demo jobs this way).
func (h *Harness) SeedSchedules(spec SchedulesSpec) error { return h.scheds.set(spec) }
