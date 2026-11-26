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
// edit). The tick takes the jobs lock only without waiting (cron.TryLockJobs),
// to record a one-shot's outcome or prune a done one-shot.
//
// Every successful Create, Modify (with changes), SetEnabled and Delete
// appends a lifecycle entry (created, edited, paused, resumed, deleted) to
// runs.ndjson after the jobs file is committed, still under the jobs lock,
// carrying the job snapshot and who made the change (Store.Actor).
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
	"github.com/blsmth/agentctl/internal/prompttemplate"
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
	// LoadTemplate reads a template job's prompt template as a fire would,
	// from the job dir's project (shadowing the global templates). nil
	// means prompttemplate.Load.
	LoadTemplate func(name, dir string) (prompttemplate.Template, error)
	// Windows lists the live windows the jobs launched, for the running
	// flag. nil means none are running.
	Windows func(jobs []cron.Job) ([]cron.Window, error)
	// SchedulerInstalled reports whether the ticker is installed. nil means
	// the launchd plist check (SchedulerInstalled).
	SchedulerInstalled func() bool
	// Options supplies the values the pinned model, backend and account may
	// take (DefaultOptions in production). nil skips those checks.
	Options func() Options
	// Actor says who is making changes, for the lifecycle entries: the
	// caller's window for a CLI run from an agent window, "hub", "phone".
	// nil records cron.ActorCLI.
	Actor func() cron.Actor
}

func (s Store) actor() cron.Actor {
	if s.Actor == nil {
		return cron.Actor{Kind: cron.ActorCLI}
	}
	return s.Actor()
}

// recordLifecycle appends a lifecycle entry for job. The jobs file is
// already committed, so the change stands even when the append fails; the
// failure is not reported to the caller.
func (s Store) recordLifecycle(result string, job cron.Job, note string) {
	dir, err := s.StateDir()
	if err != nil {
		return
	}
	_ = cron.AppendRun(dir, cron.LifecycleEntry(s.now(), result, job, s.actor(), note))
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
	if job.PromptSourceKey != "" && !validPromptKey(job.PromptSourceKey) {
		add(cron.FieldPrompt, fmt.Sprintf("invalid prompt_source_key %q", job.PromptSourceKey))
	}
	if !has(cron.FieldSchedule) && !job.IsOnce() {
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
	agent := job.Agent
	if job.IsImprove() {
		// An improve job runs the fleet-improver unless it names an agent.
		agent = job.ImproveAgentName()
	}
	if dirOK && job.Template != "" && !has(cron.FieldTemplate) {
		t, ferr := s.resolveTemplate(job)
		switch {
		case ferr != nil:
			errs = append(errs, *ferr)
		case agent == "" && t.Agent == "":
			add(cron.FieldAgent, fmt.Sprintf("template %q sets no agent; pick an agent", job.Template))
		case agent == "":
			agent = t.Agent
		}
	}
	if dirOK && agent != "" && !has(cron.FieldAgent) && s.ValidateAgent != nil {
		if err := s.ValidateAgent(agent, job.Dir); err != nil {
			add(cron.FieldAgent, err.Error())
		}
	}
	return job, sortFields(errs)
}

// Template reads job's prompt template the way a fire does: from the job
// dir's project, shadowing the global templates.
func (s Store) Template(job cron.Job) (prompttemplate.Template, error) {
	load := s.LoadTemplate
	if load == nil {
		load = prompttemplate.Load
	}
	return load(job.Template, job.Dir)
}

// EffectiveAgent is the agent job runs: its own, else its template's (""
// when the template cannot be read).
func (s Store) EffectiveAgent(job cron.Job) string {
	if job.IsImprove() {
		return job.ImproveAgentName()
	}
	if job.Agent != "" || job.Template == "" {
		return job.Agent
	}
	t, err := s.Template(job)
	if err != nil {
		return ""
	}
	return t.Agent
}

// resolveTemplate is Template with the error as a template field error
// ("template X not found" for a missing file).
func (s Store) resolveTemplate(job cron.Job) (prompttemplate.Template, *cron.FieldError) {
	t, err := s.Template(job)
	if err == nil {
		return t, nil
	}
	return t, &cron.FieldError{Field: cron.FieldTemplate, Message: TemplateErrorText(job.Template, err)}
}

// TemplateErrorText is the reason a template job cannot run: "template X
// not found" when no file has the name, else the load error.
func TemplateErrorText(name string, err error) string {
	if errors.Is(err, prompttemplate.ErrNotFound) {
		return fmt.Sprintf("template %q not found", name)
	}
	return fmt.Sprintf("template %q: %v", name, err)
}

// templateErrors is the template check of a valid job for the list views:
// a template job whose template no longer resolves (deleted, or broken) is
// shown as invalid. A tick still tries it and records failed:template.
func (s Store) templateErrors(j cron.Job) []cron.FieldError {
	if j.Template == "" {
		return nil
	}
	if _, ferr := s.resolveTemplate(j); ferr != nil {
		return []cron.FieldError{*ferr}
	}
	return nil
}

// Check returns the problems of a valid job that need the outside world:
// the pinned model, backend and account against the option sets, and a
// template that no longer resolves. It is OverviewInput.Check.
func (s Store) Check(j cron.Job) []cron.FieldError {
	return append(s.optionErrors(j), s.templateErrors(j)...)
}

// atErrors rejects a pending one-shot whose time is not in the future. It
// runs only where the at time is being set (create, a changed at, a reset
// to pending), so an unrelated edit of a pending job in its last minute,
// or any edit of a done one, is not refused for it.
func (s Store) atErrors(job cron.Job) []cron.FieldError {
	if !job.IsOnce() || job.IsDone() {
		return nil
	}
	at, err := job.AtTime()
	if err != nil {
		return nil // reported by FieldErrors
	}
	if !at.After(s.now()) {
		return []cron.FieldError{{Field: cron.FieldAt, Message: fmt.Sprintf("at %s is in the past", at.In(time.Local).Format(cron.NextFireFormat))}}
	}
	return nil
}

// atChanged reports whether an update sets a one-shot's time: a new at, or
// a done one-shot reset to pending.
func atChanged(old, next cron.Job) bool {
	return next.IsOnce() && (strings.TrimSpace(old.At) != next.At || (old.IsDone() && !next.IsDone()))
}

// fieldOrder is the order field errors are reported in (the form order).
var fieldOrder = []string{
	cron.FieldName, cron.FieldDir, cron.FieldAgent, cron.FieldPrompt, cron.FieldTemplate, cron.FieldSchedule, cron.FieldAt,
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
	job.At = strings.TrimSpace(job.At)
	if at, err := job.AtTime(); job.At != "" && err == nil {
		job.At = cron.FormatAt(at)
	}
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
	// A new job (or a duplicate of a done one-shot) starts pending.
	job.ClearOutcome()
	if err := s.pinNewKey(&job, ""); err != nil {
		return cron.Job{}, err
	}
	job, errs := s.Validate(job, "")
	errs = sortFields(append(errs, s.atErrors(job)...))
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
		if err := s.commit(path, jobs, job.Name, true); err != nil {
			return err
		}
		s.recordLifecycle(cron.ResultCreated, job, "")
		return nil
	})
	return out, err
}

// pinNewKey copies a prompt_key that job names newly (not the stored job's
// own, kept) into its prompt, so no new job reads the context store at a
// fire. Only a legacy job keeps an unpinned key, until an edit pins it. The
// CLI pins before calling (with the text the approval popup showed); this
// covers the hub, the phone and duplicates of a legacy job.
func (s Store) pinNewKey(job *cron.Job, kept string) error {
	key := job.PromptKey
	if key == "" || key == kept || job.Prompt != "" || job.Template != "" {
		return nil
	}
	text, err := s.PromptKeyText(key)
	switch {
	case errors.Is(err, ErrNotFound):
		err = errors.New("not found in the context store")
	case err == nil && strings.TrimSpace(text) == "":
		err = errors.New("the key is empty")
	}
	if err != nil {
		return &ValidationError{Fields: []cron.FieldError{{Field: cron.FieldPrompt,
			Message: fmt.Sprintf("context key %q cannot be read to copy into the job: %v", key, err)}}}
	}
	job.PinKey(key, text)
	return nil
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
// an edit never resumes a paused job by omission. A one-shot keeps its
// outcome (state, result, ...) unless the at time changes, since the
// forms do not send it.
func (s Store) Update(name string, job cron.Job) (Updated, error) {
	return s.Modify(name, func(cur *cron.Job) error {
		enabled := cur.Enabled
		prev := *cur
		*cur = clone(job)
		if cur.Enabled == nil {
			cur.Enabled = enabled
		}
		if cur.IsOnce() && cur.State == "" && strings.TrimSpace(cur.At) == strings.TrimSpace(prev.At) {
			cur.State, cur.CompletedAt, cur.Result = prev.State, prev.CompletedAt, prev.Result
			cur.RunWindow, cur.RunSession, cur.RunSessionID = prev.RunWindow, prev.RunSession, prev.RunSessionID
		}
		return nil
	})
}

// Modify applies fn to a copy of the named job, validates the result and
// saves it, all under the jobs lock. Fire state is kept unless the schedule
// or a one-shot's at changed or the job was resumed, in which case
// last_fire is re-seeded to now. A new at resets a done one-shot to
// pending (and must be in the future). Run history is kept; an edit that
// changes something appends an edited entry. An error from fn aborts the
// edit.
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
		if err := s.pinNewKey(&cur, old.PromptKey); err != nil {
			return err
		}
		if strings.TrimSpace(cur.At) != strings.TrimSpace(old.At) || !cur.IsOnce() {
			cur.ClearOutcome()
		}
		next, errs := s.Validate(cur, name)
		if atChanged(old, next) {
			errs = sortFields(append(errs, s.atErrors(next)...))
		}
		if len(errs) > 0 {
			return &ValidationError{Fields: errs}
		}
		if !old.IsEnabled() && next.IsEnabled() && next.IsDone() {
			return cron.OnceDoneError(next)
		}
		reseed := strings.TrimSpace(old.Schedule) != next.Schedule || atChanged(old, next) || (!old.IsEnabled() && next.IsEnabled())
		jobs.Jobs[i] = next
		res = Updated{Job: next, Changed: ChangedFields(old, next), Reseeded: reseed}
		seed := ""
		if reseed {
			seed = name
		}
		// A new at (or a switch to a schedule) starts the one-shot over; any
		// other reseed keeps a recorded outcome.
		if err := s.commit(path, jobs, seed, atChanged(old, next) || !next.IsOnce()); err != nil {
			return err
		}
		if len(res.Changed) > 0 {
			s.recordLifecycle(cron.ResultEdited, next, "changed "+strings.Join(res.Changed, ", "))
		}
		return nil
	})
	return res, err
}

// SetEnabled pauses or resumes a job. Resuming re-seeds last_fire, so fires
// missed while paused are not caught up (a one-shot whose time passed while
// paused expires). A done one-shot cannot be resumed: cron edit --at
// schedules it again. The job is not re-validated, so a broken job can
// still be paused.
func (s Store) SetEnabled(name string, enabled bool) (cron.Job, error) {
	var out cron.Job
	err := s.withJobs(func(path string, jobs *cron.Jobs) error {
		i := jobs.Find(name)
		if i < 0 {
			return &NotFoundError{Name: name}
		}
		if enabled && jobs.Jobs[i].IsDone() {
			return cron.OnceDoneError(jobs.Jobs[i])
		}
		was := jobs.Jobs[i].IsEnabled()
		v := enabled
		jobs.Jobs[i].Enabled = &v
		out = jobs.Jobs[i]
		seed := ""
		if enabled {
			seed = name
		}
		if err := s.commit(path, jobs, seed, false); err != nil {
			return err
		}
		if was != enabled {
			result := cron.ResultPaused
			if enabled {
				result = cron.ResultResumed
			}
			s.recordLifecycle(result, out, "")
		}
		return nil
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
		gone := jobs.Jobs[i]
		jobs.Jobs = append(jobs.Jobs[:i], jobs.Jobs[i+1:]...)
		if err := s.commit(path, jobs, "", false); err != nil {
			return err
		}
		s.recordLifecycle(cron.ResultDeleted, gone, "")
		return nil
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
// last_fire. fresh also drops the job's one-shot outcome in state.json (a
// create, or a new at time); a plain resume keeps it.
func (s Store) commit(path string, jobs *cron.Jobs, seed string, fresh bool) error {
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
	seedFn := cron.SeedLocked
	if fresh {
		seedFn = cron.SeedFreshLocked
	}
	if err := seedFn(dir, seed, s.now()); err != nil {
		return fmt.Errorf("seeding state for %s: %w", seed, err)
	}
	return cron.SaveJobs(path, jobs)
}

// ChangedFields lists the JSON names of the fields that differ between two
// jobs, in form order. Unset worktree/enabled compare as their defaults.
func ChangedFields(a, b cron.Job) []string {
	am, bm := jobMap(normalize(a)), jobMap(normalize(b))
	// A one-shot's outcome is bookkeeping: "state" stands for all of it.
	for _, k := range []string{"completed_at", "result", "run_window", "run_session", "run_session_id"} {
		delete(am, k)
		delete(bm, k)
	}
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
	order := append(slices.Clone(fieldOrder), "prompt_key", "prompt_source_key", "worktree", "enabled", "state")
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
