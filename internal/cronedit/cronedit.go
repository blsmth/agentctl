// Package cronedit is the one editing path for `agentctl cron` jobs, shared
// by the CLI (`cron add/edit/pause/resume/rm`), the hub and `agentctl serve`.
//
// It owns validation (with per-field errors a form can show inline), the
// serialized read-modify-write of schedules.json, the fire-state rules of an
// edit, the live preview (schedule in words, next fires) and the job status
// the list views render. It never launches anything itself: run-now goes
// through a RunFunc (see run.go), which the production code implements as an
// `agentctl cron run --json` subprocess.
//
// Locking: every write takes cron.LockJobs (<stateDir>/cron/jobs.lock) around
// load+modify+save. A write that changes fire state (create, a schedule
// change, resume) also takes the tick lock, inside the jobs lock, and seeds
// last_fire before it saves the jobs file, so a tick never sees the new
// schedule with the old last_fire (which could fire for a time before the
// edit). The tick itself never takes the jobs lock.
package cronedit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/paths"
)

// ErrNotFound matches the error returned for a job name that does not exist.
var ErrNotFound = errors.New("cron job not found")

// ErrExists matches the validation error of a create whose name is taken.
var ErrExists = errors.New("cron job already exists")

// NotFoundError names the missing job.
type NotFoundError struct{ Name string }

func (e *NotFoundError) Error() string { return fmt.Sprintf("no cron job named %q", e.Name) }

// Is makes errors.Is(err, ErrNotFound) work.
func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// ValidationError carries every field problem of a rejected job.
type ValidationError struct {
	Fields []cron.FieldError
	exists bool
}

// Error joins the field messages.
func (e *ValidationError) Error() string {
	msgs := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		msgs[i] = f.Message
	}
	return strings.Join(msgs, "; ")
}

// Is makes errors.Is(err, ErrExists) work for a duplicate name.
func (e *ValidationError) Is(target error) bool { return target == ErrExists && e.exists }

// FieldsOf returns the field errors in err, or nil when it is not a
// ValidationError.
func FieldsOf(err error) []cron.FieldError {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve.Fields
	}
	return nil
}

// Store reads and writes the jobs. Every field is a seam; Default fills the
// production values.
type Store struct {
	JobsPath func() (string, error)
	StateDir func() (string, error)
	Now      func() time.Time
	Config   func() config.CronConfig
	// ValidateAgent reports an error unless the agent can be launched from
	// dir (dispatch.ValidateAgentName in production). nil skips the check;
	// it is injected because the dispatch package sits above this one.
	ValidateAgent func(name, dir string) error
	// ResolvePromptKey reads a prompt_key's text from the context store
	// (for display only). nil means the real store.
	ResolvePromptKey func(key string) (string, error)
	// Windows lists the live windows the jobs launched, for the running
	// flag. nil means none are running.
	Windows func(jobs []cron.Job) ([]cron.Window, error)
	// SchedulerInstalled reports whether the ticker is installed. nil means
	// the launchd plist check (SchedulerInstalled).
	SchedulerInstalled func() bool
	// Options supplies the values the pinned model, backend and account may
	// take (DefaultOptions in production). nil skips those checks.
	Options func() Options
}

// Default is the production store. validateAgent is
// dispatch.ValidateAgentName for callers that can import it.
func Default(validateAgent func(name, dir string) error) Store {
	return Store{
		JobsPath:      cron.JobsPath,
		StateDir:      cron.Dir,
		Now:           time.Now,
		Config:        func() config.CronConfig { return config.Load().Cron },
		ValidateAgent: validateAgent,
		Options:       DefaultOptions,
	}
}

func (s Store) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s Store) config() config.CronConfig {
	if s.Config == nil {
		return config.CronConfig{}
	}
	return s.Config()
}

// Load reads the jobs file (a missing file is no jobs). No lock: the file
// is replaced atomically.
func (s Store) Load() (*cron.Jobs, error) {
	path, err := s.JobsPath()
	if err != nil {
		return nil, err
	}
	return cron.LoadJobs(path)
}

// Get returns one job.
func (s Store) Get(name string) (cron.Job, error) {
	jobs, err := s.Load()
	if err != nil {
		return cron.Job{}, err
	}
	i := jobs.Find(name)
	if i < 0 {
		return cron.Job{}, &NotFoundError{Name: name}
	}
	return jobs.Jobs[i], nil
}

// Validate normalizes job and checks everything a save checks: the job's
// own rules (cron.Job.FieldErrors), cron.min_interval, that dir is an
// existing directory and that the agent exists. original is the stored name
// for an update ("" for a create); the name cannot change on update, since
// jitter, fire state and run history key on it. It does not check that a
// new name is free (Create and Preview do, against the file).
func (s Store) Validate(job cron.Job, original string) (cron.Job, []cron.FieldError) {
	job = normalize(job)
	if original != "" && job.Name == "" {
		job.Name = original
	}
	errs := job.FieldErrors()
	has := func(field string) bool {
		return slices.ContainsFunc(errs, func(e cron.FieldError) bool { return e.Field == field })
	}
	add := func(field, msg string) {
		errs = append(errs, cron.FieldError{Field: field, Message: msg})
	}
	if original != "" && job.Name != original {
		add(cron.FieldName, fmt.Sprintf("the name cannot be changed (it was %q): fire state and run history are keyed on it; duplicate the job instead", original))
	}
	if job.PromptKey != "" && !validPromptKey(job.PromptKey) {
		add(cron.FieldPrompt, fmt.Sprintf("invalid prompt_key %q", job.PromptKey))
	}
	if !has(cron.FieldSchedule) {
		sched, _ := cron.ParseSchedule(job.Schedule)
		if err := cron.CheckMinInterval(sched, s.now(), s.config().EffectiveMinInterval()); err != nil {
			add(cron.FieldSchedule, err.Error())
		}
	}
	for _, e := range s.optionErrors(job) {
		if !has(e.Field) {
			errs = append(errs, e)
		}
	}
	dirOK := false
	if !has(cron.FieldDir) {
		if fi, err := os.Stat(job.Dir); err != nil || !fi.IsDir() {
			add(cron.FieldDir, fmt.Sprintf("%s is not a directory", job.Dir))
		} else {
			dirOK = true
		}
	}
	if dirOK && !has(cron.FieldAgent) && s.ValidateAgent != nil {
		if err := s.ValidateAgent(job.Agent, job.Dir); err != nil {
			add(cron.FieldAgent, err.Error())
		}
	}
	return job, sortFields(errs)
}

// fieldOrder is the order field errors are reported in (the form order).
var fieldOrder = []string{
	cron.FieldName, cron.FieldDir, cron.FieldAgent, cron.FieldPrompt, cron.FieldSchedule,
	cron.FieldHarness, cron.FieldMode, cron.FieldModel, cron.FieldEffort, cron.FieldBackend, cron.FieldAccount,
	cron.FieldTimeout, cron.FieldOverlap, cron.FieldCatchup, cron.FieldSkipIfUsageAbove,
}

func sortFields(errs []cron.FieldError) []cron.FieldError {
	out := slices.Clone(errs)
	slices.SortStableFunc(out, func(a, b cron.FieldError) int {
		return rankIn(fieldOrder, a.Field) - rankIn(fieldOrder, b.Field)
	})
	return out
}

// rankIn is v's index in order, or len(order) when absent.
func rankIn(order []string, v string) int {
	if i := slices.Index(order, v); i >= 0 {
		return i
	}
	return len(order)
}

// validPromptKey is the context store's key rule (a file name component).
func validPromptKey(k string) bool {
	return k != "" && !strings.HasPrefix(k, ".") && !strings.ContainsAny(k, "/\\\x00")
}

// normalize fills the defaults, expands ~ in dir, and makes the worktree
// and enabled flags explicit so the stored file says what runs.
func normalize(job cron.Job) cron.Job {
	job = clone(job)
	job.Schedule = strings.TrimSpace(job.Schedule)
	if job.Dir != "" {
		job.Dir = paths.ExpandHome(strings.TrimSpace(job.Dir))
		if filepath.IsAbs(job.Dir) {
			job.Dir = filepath.Clean(job.Dir)
		}
	}
	job.Normalize()
	wt, en := job.UseWorktree(), job.IsEnabled()
	job.Worktree, job.Enabled = &wt, &en
	return job
}

// clone deep-copies the pointer fields.
func clone(j cron.Job) cron.Job {
	if j.Worktree != nil {
		v := *j.Worktree
		j.Worktree = &v
	}
	if j.Enabled != nil {
		v := *j.Enabled
		j.Enabled = &v
	}
	if j.SkipIfUsageAbove != nil {
		v := *j.SkipIfUsageAbove
		j.SkipIfUsageAbove = &v
	}
	return j
}

// Create validates and appends a new job and seeds its fire state to now,
// so it starts from the next scheduled time. Unset worktree and enabled
// default to true; Duplicate is a Create with a new name (and, from the UI,
// enabled false).
func (s Store) Create(job cron.Job) (cron.Job, error) {
	job, errs := s.Validate(job, "")
	if len(errs) > 0 {
		return cron.Job{}, &ValidationError{Fields: errs}
	}
	var out cron.Job
	err := s.withJobs(func(path string, jobs *cron.Jobs) error {
		if jobs.Find(job.Name) >= 0 {
			return &ValidationError{exists: true, Fields: []cron.FieldError{{
				Field: cron.FieldName, Message: fmt.Sprintf("a cron job named %q already exists", job.Name),
			}}}
		}
		jobs.Jobs = append(jobs.Jobs, job)
		out = job
		return s.commit(path, jobs, job.Name)
	})
	return out, err
}

// Updated reports what an update changed.
type Updated struct {
	Job cron.Job `json:"job"`
	// Changed lists the JSON names of the fields that differ from the
	// stored job, in form order.
	Changed []string `json:"changed"`
	// Reseeded is set when the fire state was re-seeded to now (the
	// schedule changed, or the job went from paused to enabled), so no
	// fire scheduled before the edit runs.
	Reseeded bool `json:"reseeded"`
}

// Update replaces the named job with job, atomically. The name cannot
// change (an empty name keeps it). A nil enabled keeps the stored value, so
// an edit never resumes a paused job by omission.
func (s Store) Update(name string, job cron.Job) (Updated, error) {
	return s.Modify(name, func(cur *cron.Job) error {
		enabled := cur.Enabled
		*cur = clone(job)
		if cur.Enabled == nil {
			cur.Enabled = enabled
		}
		return nil
	})
}

// Modify applies fn to a copy of the named job, validates the result and
// saves it, all under the jobs lock. Fire state is kept unless the schedule
// changed or the job was resumed, in which case last_fire is re-seeded to
// now. Run history is never touched. An error from fn aborts the edit.
func (s Store) Modify(name string, fn func(*cron.Job) error) (Updated, error) {
	var res Updated
	err := s.withJobs(func(path string, jobs *cron.Jobs) error {
		i := jobs.Find(name)
		if i < 0 {
			return &NotFoundError{Name: name}
		}
		old := normalize(jobs.Jobs[i])
		cur := clone(old)
		if err := fn(&cur); err != nil {
			return err
		}
		next, errs := s.Validate(cur, name)
		if len(errs) > 0 {
			return &ValidationError{Fields: errs}
		}
		reseed := strings.TrimSpace(old.Schedule) != next.Schedule || (!old.IsEnabled() && next.IsEnabled())
		jobs.Jobs[i] = next
		res = Updated{Job: next, Changed: ChangedFields(old, next), Reseeded: reseed}
		seed := ""
		if reseed {
			seed = name
		}
		return s.commit(path, jobs, seed)
	})
	return res, err
}

// SetEnabled pauses or resumes a job. Resuming re-seeds last_fire, so fires
// missed while paused are not caught up. The job is not re-validated, so a
// broken job can still be paused.
func (s Store) SetEnabled(name string, enabled bool) (cron.Job, error) {
	var out cron.Job
	err := s.withJobs(func(path string, jobs *cron.Jobs) error {
		i := jobs.Find(name)
		if i < 0 {
			return &NotFoundError{Name: name}
		}
		v := enabled
		jobs.Jobs[i].Enabled = &v
		out = jobs.Jobs[i]
		seed := ""
		if enabled {
			seed = name
		}
		return s.commit(path, jobs, seed)
	})
	return out, err
}

// Delete removes a job. Its fire state is dropped by the next tick; its run
// history stays, and a window it launched is left alone.
func (s Store) Delete(name string) error {
	return s.withJobs(func(path string, jobs *cron.Jobs) error {
		i := jobs.Find(name)
		if i < 0 {
			return &NotFoundError{Name: name}
		}
		jobs.Jobs = append(jobs.Jobs[:i], jobs.Jobs[i+1:]...)
		return s.commit(path, jobs, "")
	})
}

// withJobs runs fn on the loaded jobs under the jobs lock.
func (s Store) withJobs(fn func(path string, jobs *cron.Jobs) error) error {
	path, err := s.JobsPath()
	if err != nil {
		return err
	}
	dir, err := s.StateDir()
	if err != nil {
		return err
	}
	unlock, err := cron.LockJobs(dir)
	if err != nil {
		return err
	}
	defer unlock()
	jobs, err := cron.LoadJobs(path)
	if err != nil {
		return err
	}
	return fn(path, jobs)
}

// commit saves the jobs file. When seed names a job, its last_fire is set
// to now first, under the tick lock, so a tick sees either the old jobs or
// the new jobs with the new last_fire, never the new schedule with the old
// last_fire.
func (s Store) commit(path string, jobs *cron.Jobs, seed string) error {
	if seed == "" {
		return cron.SaveJobs(path, jobs)
	}
	dir, err := s.StateDir()
	if err != nil {
		return err
	}
	unlock, _, err := cron.Lock(dir, true)
	if err != nil {
		return err
	}
	defer unlock()
	if err := cron.SeedLocked(dir, seed, s.now()); err != nil {
		return fmt.Errorf("seeding state for %s: %w", seed, err)
	}
	return cron.SaveJobs(path, jobs)
}

// ChangedFields lists the JSON names of the fields that differ between two
// jobs, in form order. Unset worktree/enabled compare as their defaults.
func ChangedFields(a, b cron.Job) []string {
	am, bm := jobMap(normalize(a)), jobMap(normalize(b))
	var out []string
	for k, v := range am {
		if string(bm[k]) != string(v) {
			out = append(out, k)
		}
	}
	for k := range bm {
		if _, ok := am[k]; !ok {
			out = append(out, k)
		}
	}
	order := append(slices.Clone(fieldOrder), "prompt_key", "worktree", "enabled")
	slices.SortFunc(out, func(x, y string) int {
		if d := rankIn(order, x) - rankIn(order, y); d != 0 {
			return d
		}
		return strings.Compare(x, y)
	})
	return out
}

func jobMap(j cron.Job) map[string]json.RawMessage {
	data, _ := json.Marshal(j)
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(data, &m)
	return m
}
