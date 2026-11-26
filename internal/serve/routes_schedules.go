package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/dispatchplan"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
)

// The schedules API: list, read, preview, create, update, pause, resume,
// run now and delete `agentctl cron` jobs through the shared editing core
// (internal/cronedit), the same path as the CLI and the hub.
//
// A job authorizes unlimited unattended runs, so every write is a Write
// route (paired device, serve.allow_write, CSRF header, rate limit, audit)
// like /v1/dispatch, and its inputs are held to the dispatch rules: a new or
// changed dir must be a fleet project's dir and the agent one of the agents
// discoverable there. Run now has plain `cron run` semantics (overlap,
// max_concurrent and the usage gate apply; there is no force).

const (
	// codeInvalid is a 422 whose body lists the field errors.
	codeInvalid = "invalid"
	// scheduleWriteDeadline bounds a run-now response (the subprocess may
	// start a session and a worktree).
	scheduleWriteDeadline = cronedit.RunTimeout + 15*time.Second
	// scheduleSlowDeadline bounds an update or resume: both take the cron
	// tick lock, which a slow launch holds for a while.
	scheduleSlowDeadline = 90 * time.Second
)

// scheduleIn is the body of create and update: the job, and optionally the
// schedule picker's model, which then replaces job.schedule.
type scheduleIn struct {
	Job    cron.Job                 `json:"job"`
	Simple *cronedit.SimpleSchedule `json:"simple,omitempty"`
}

// schedulePreviewIn is the body of POST /v1/schedules/preview. Original is
// the stored name when previewing an edit ("" for a new job).
type schedulePreviewIn struct {
	Job      cron.Job                 `json:"job"`
	Simple   *cronedit.SimpleSchedule `json:"simple,omitempty"`
	Original string                   `json:"original,omitempty"`
}

// invalidBody is a 422 response: the usual error fields plus every field
// problem.
type invalidBody struct {
	Error  string            `json:"error"`
	Code   string            `json:"code"`
	Fields []cron.FieldError `json:"fields"`
}

// scheduleOut is the response of a write that leaves a job behind: its card,
// plus what an update changed.
type scheduleOut struct {
	scheduleCardOut
	Changed  []string `json:"changed,omitempty"`
	Reseeded bool     `json:"reseeded,omitempty"`
}

// schedulesBody is GET /v1/schedules.
type schedulesBody struct {
	Generated string `json:"generated"`
	cronedit.Overview
}

func (s *Server) registerSchedules(rt *Router) {
	rt.Feature("schedules")
	rt.Device("GET /v1/schedules", s.handleSchedules)
	rt.Device("GET /v1/schedules/{name}", s.handleSchedule)
	rt.Write("POST /v1/schedules/preview", writeRoute(s, "schedule-preview", s.handleSchedulePreview))
	rt.Write("POST /v1/schedules", writeRoute(s, "schedule-create", s.handleScheduleCreate))
	rt.Write("PUT /v1/schedules/{name}", extendDeadline(scheduleSlowDeadline, writeRoute(s, "schedule-update", s.handleScheduleUpdate)))
	rt.Write("POST /v1/schedules/{name}", extendDeadline(scheduleSlowDeadline, writeRoute(s, "schedule-update", s.handleScheduleUpdate)))
	rt.Write("POST /v1/schedules/{name}/pause", writeRoute(s, "schedule-pause", s.handleSchedulePause))
	rt.Write("POST /v1/schedules/{name}/resume", extendDeadline(scheduleSlowDeadline, writeRoute(s, "schedule-resume", s.handleScheduleResume)))
	rt.Write("POST /v1/schedules/{name}/run", extendDeadline(scheduleWriteDeadline, writeRoute(s, "schedule-run", s.handleScheduleRun)))
	rt.Write("DELETE /v1/schedules/{name}", writeRoute(s, "schedule-delete", s.handleScheduleDelete))
	rt.Write("POST /v1/schedules/{name}/delete", writeRoute(s, "schedule-delete", s.handleScheduleDelete))
}

// scheduleActionFor names a /v1/schedules write route by path, for auditing
// requests rejected before routing.
func scheduleActionFor(path string) (string, bool) {
	p := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(p) < 2 || p[0] != "v1" || p[1] != "schedules" {
		return "", false
	}
	switch {
	case len(p) == 2:
		return "schedule-create", true
	case len(p) == 3 && p[2] == "preview":
		return "schedule-preview", true
	case len(p) == 3:
		return "schedule-update", true
	case len(p) == 4 && slices.Contains([]string{"pause", "resume", "run", "delete"}, p[3]):
		return "schedule-" + p[3], true
	}
	return "", false
}

// extendDeadline lifts the server's write deadline for a slow route.
func extendDeadline(d time.Duration, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d)); err != nil {
			logging.Debug("serve: extending write deadline: %v", err)
		}
		h(w, r)
	}
}

// scheduleStore is the editing core with serve's seams: agents are
// validated against agent discovery (what /v1/agents lists) and running
// windows come from the fleet.
func (s *Server) scheduleStore() cronedit.Store {
	st := s.schedules
	if st.ValidateAgent == nil {
		st.ValidateAgent = s.validateScheduleAgent
	}
	if st.Windows == nil {
		st.Windows = s.scheduleWindows
	}
	if st.Actor == nil {
		st.Actor = func() cron.Actor { return cron.Actor{Kind: cron.ActorPhone} }
	}
	return st
}

func (s *Server) validateScheduleAgent(name, dir string) error {
	agents, err := s.agents.Agents([]string{dir})
	if err != nil {
		return fmt.Errorf("discovering agents: %w", err)
	}
	if !slices.ContainsFunc(agents, func(a AgentInfo) bool { return a.Name == name }) {
		return fmt.Errorf("unknown agent %q for this project", name)
	}
	return nil
}

func (s *Server) scheduleWindows([]cron.Job) ([]cron.Window, error) {
	groups, err := s.fleet.Fleet()
	if err != nil {
		return nil, err
	}
	var out []cron.Window
	for _, g := range groups {
		out = append(out, cronedit.WindowsFromRows(g.Rows)...)
	}
	return out, nil
}

func (s *Server) handleSchedules(w http.ResponseWriter, _ *http.Request) {
	ov, err := s.scheduleStore().Overview()
	if err != nil {
		logging.Warn("serve: loading schedules: %v", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "loading schedules")
		return
	}
	cronedit.SortForDisplay(ov.Jobs)
	writeJSON(w, http.StatusOK, schedulesBody{Generated: s.now().UTC().Format(time.RFC3339), Overview: ov})
}

func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	d, err := s.scheduleStore().Detail(r.PathValue("name"))
	if err != nil {
		out := scheduleError(err)
		writeError(w, out.status, out.code, out.msg)
		return
	}
	writeJSON(w, http.StatusOK, s.scheduleCard(d))
}

// scheduleRunOut is a run entry plus the handle of the session it started
// (and its label; empty for a job's own runs, whose line shows the time).
type scheduleRunOut struct {
	cron.Entry
	Handle string `json:"handle,omitempty"`
	Label  string `json:"label,omitempty"`
}

// scheduleCardOut is a Detail whose runs carry handles.
type scheduleCardOut struct {
	cronedit.Detail
	Runs []scheduleRunOut `json:"runs"`
}

func (s *Server) scheduleCard(d cronedit.Detail) scheduleCardOut {
	return scheduleCardOut{Detail: d, Runs: s.runsWithHandles(d.Runs, d.Name)}
}

func (s *Server) runsWithHandles(runs []cron.Entry, job string) []scheduleRunOut {
	out := make([]scheduleRunOut, len(runs))
	var ix *fleet.Index
	for i, e := range runs {
		out[i] = scheduleRunOut{Entry: e}
		if e.SessionID == "" {
			continue
		}
		if ix == nil {
			ix = s.handles()
		}
		out[i].Handle = ix.HandleFor(e.SessionID)
		if sess, err := ix.Resolve(e.SessionID); err == nil {
			out[i].Label = fleet.Label(sess.Row, fleet.Scope{Job: job})
		}
	}
	return out
}

// scheduleChecks are serve's extra rules on top of the core's: the prompt
// is held to the dispatch prompt rules (no control characters, at most
// maxPromptRunes), a prompt_key must resolve, and a dir that is new or
// changed must be a fleet project's dir (the project picker's list). A
// template is checked by the core (it must resolve from the job dir).
func (s *Server) scheduleChecks(job cron.Job, original string) []cron.FieldError {
	var errs []cron.FieldError
	add := func(field, msg string) { errs = append(errs, cron.FieldError{Field: field, Message: msg}) }
	if job.Prompt != "" {
		switch {
		case utf8.RuneCountInString(job.Prompt) > maxPromptRunes:
			add(cron.FieldPrompt, fmt.Sprintf("prompt exceeds %d characters", maxPromptRunes))
		case !utf8.ValidString(job.Prompt):
			add(cron.FieldPrompt, "prompt must be valid UTF-8")
		case hasPromptControl(job.Prompt):
			add(cron.FieldPrompt, "prompt contains control characters")
		case strings.TrimSpace(job.Prompt) == "":
			add(cron.FieldPrompt, "prompt is required")
		}
	}
	if job.PromptKey != "" && job.Prompt == "" {
		if _, err := s.scheduleStore().PromptKeyText(job.PromptKey); err != nil {
			add(cron.FieldPrompt, fmt.Sprintf("context key %q cannot be read: %v", job.PromptKey, err))
		}
	}
	dir := filepath.Clean(job.Dir)
	if job.Dir != "" && filepath.IsAbs(dir) && !s.storedDir(original, dir) && !s.isProjectDir(dir) {
		add(cron.FieldDir, "pick a project from the project list (a running agentctl session's directory)")
	}
	return errs
}

func hasPromptControl(s string) bool {
	for _, c := range s {
		if c != '\n' && c != '\t' && (unicode.IsControl(c) || c == '\u2028' || c == '\u2029') {
			return true
		}
	}
	return false
}

// storedDir reports whether dir is the stored dir of the job being edited.
func (s *Server) storedDir(original, dir string) bool {
	if original == "" {
		return false
	}
	j, err := s.scheduleStore().Get(original)
	return err == nil && filepath.Clean(j.Dir) == dir
}

func (s *Server) isProjectDir(dir string) bool {
	groups, err := s.fleet.Fleet()
	if err != nil {
		return false
	}
	return slices.ContainsFunc(groups, func(g fleetwatch.Group) bool { return g.Dir != "" && filepath.Clean(g.Dir) == dir })
}

// scheduleError maps a core error to a response.
func scheduleError(err error) outcome {
	if fields := cronedit.FieldsOf(err); fields != nil {
		return invalid(fields)
	}
	if errors.Is(err, cronedit.ErrNotFound) {
		return failure(http.StatusNotFound, codeNotFound, err.Error())
	}
	logging.Warn("serve: schedules: %v", err)
	return failure(http.StatusInternalServerError, codeInternal, "saving schedules")
}

func invalid(fields []cron.FieldError) outcome {
	msgs := make([]string, len(fields))
	for i, f := range fields {
		msgs[i] = f.Message
	}
	return outcome{status: http.StatusUnprocessableEntity, code: codeInvalid,
		body: invalidBody{Error: strings.Join(msgs, "; "), Code: codeInvalid, Fields: fields}}
}

// auditJob fills the audit fields for a job (never the prompt).
func auditJob(a *auditEntry, job cron.Job) {
	a.Schedule = clip(job.Name)
	a.Agent = clip(job.Agent)
	if job.Dir != "" {
		a.Project = clip(filepath.Base(job.Dir))
	}
}

// previewOut is the preview plus the permission mode a fire would launch
// with, so the review sheet states what the job will really run under.
type previewOut struct {
	cronedit.Preview
	PermissionMode string `json:"permission_mode,omitempty"`
}

func (s *Server) handleSchedulePreview(_ *http.Request, in schedulePreviewIn, a *auditEntry) outcome {
	auditJob(a, in.Job)
	p := s.scheduleStore().Preview(in.Job, in.Original, in.Simple)
	if extra := s.scheduleChecks(p.Job, in.Original); len(extra) > 0 {
		p.Errors = append(p.Errors, extra...)
		p.Valid = false
	}
	// A cron fire is a worker launch: the agent's configured mode, else auto.
	return success(previewOut{Preview: p, PermissionMode: dispatchplan.PermissionMode(true, s.agentPermission(s.scheduleStore().EffectiveAgent(p.Job)), "")})
}

// checkSchedule runs the core validation plus serve's checks, so a save
// reports every problem at once.
func (s *Server) checkSchedule(job cron.Job, simple *cronedit.SimpleSchedule, original string) (cron.Job, outcome, bool) {
	p := s.scheduleStore().Preview(job, original, simple)
	errs := append(p.Errors, s.scheduleChecks(p.Job, original)...)
	if len(errs) > 0 {
		return cron.Job{}, invalid(errs), false
	}
	return p.Job, outcome{}, true
}

func (s *Server) handleScheduleCreate(_ *http.Request, in scheduleIn, a *auditEntry) outcome {
	auditJob(a, in.Job)
	job, out, ok := s.checkSchedule(in.Job, in.Simple, "")
	if !ok {
		return out
	}
	st := s.scheduleStore()
	s.auditAttempt(*a)
	job, err := st.Create(job)
	if err != nil {
		return scheduleError(err)
	}
	return s.scheduleResult(job.Name, nil, false)
}

// handleScheduleUpdate replaces the whole job: clients send every field
// (an omitted worktree means the default, true). Only an omitted enabled
// keeps the stored value.
func (s *Server) handleScheduleUpdate(r *http.Request, in scheduleIn, a *auditEntry) outcome {
	name := r.PathValue("name")
	auditJob(a, in.Job)
	a.Schedule = clip(name)
	if _, err := s.scheduleStore().Get(name); err != nil {
		return scheduleError(err)
	}
	job, out, ok := s.checkSchedule(in.Job, in.Simple, name)
	if !ok {
		return out
	}
	if in.Job.Enabled == nil {
		job.Enabled = nil // keep the stored enabled flag
	}
	s.auditAttempt(*a)
	res, err := s.scheduleStore().Update(name, job)
	if err != nil {
		return scheduleError(err)
	}
	return s.scheduleResult(name, res.Changed, res.Reseeded)
}

func (s *Server) handleSchedulePause(r *http.Request, _ emptyIn, a *auditEntry) outcome {
	return s.setScheduleEnabled(r.PathValue("name"), false, a)
}

func (s *Server) handleScheduleResume(r *http.Request, _ emptyIn, a *auditEntry) outcome {
	return s.setScheduleEnabled(r.PathValue("name"), true, a)
}

func (s *Server) setScheduleEnabled(name string, enabled bool, a *auditEntry) outcome {
	a.Schedule = clip(name)
	st := s.scheduleStore()
	job, err := st.Get(name)
	if err != nil {
		return scheduleError(err)
	}
	auditJob(a, job)
	s.auditAttempt(*a)
	if _, err := st.SetEnabled(name, enabled); err != nil {
		return scheduleError(err)
	}
	return s.scheduleResult(name, nil, enabled)
}

func (s *Server) handleScheduleDelete(r *http.Request, _ emptyIn, a *auditEntry) outcome {
	name := r.PathValue("name")
	a.Schedule = clip(name)
	st := s.scheduleStore()
	job, err := st.Get(name)
	if err != nil {
		return scheduleError(err)
	}
	auditJob(a, job)
	s.auditAttempt(*a)
	if err := st.Delete(name); err != nil {
		return scheduleError(err)
	}
	return success(okBody)
}

// runOut is the run-now response.
type runOut struct {
	cronedit.RunOutcome
	Name string `json:"name"`
}

// handleScheduleRun fires a job now with plain `cron run` semantics,
// through the RunFunc seam (an `agentctl cron run --json` subprocess). A
// skip (overlap, max_concurrent, usage gate) or a failed launch is a 200
// with fired false and the reason.
func (s *Server) handleScheduleRun(r *http.Request, _ emptyIn, a *auditEntry) outcome {
	name := r.PathValue("name")
	a.Schedule = clip(name)
	job, err := s.scheduleStore().Get(name)
	if err != nil {
		return scheduleError(err)
	}
	auditJob(a, job)
	if err := job.Validate(); err != nil {
		return invalid(job.FieldErrors())
	}
	release, ok := s.scheduleRuns.acquire(name)
	if !ok {
		return failure(http.StatusConflict, codeBusy, "a run of this job is already starting")
	}
	defer release()
	s.auditAttempt(*a)
	// Not the request context: a launch cut short by a dropped connection
	// would be half done. RunFunc bounds the run.
	entry, err := s.runSchedule(context.Background(), name)
	if err != nil {
		logging.Warn("serve: running schedule %s: %v", name, err)
		return failure(http.StatusBadGateway, codeActionFailed, err.Error())
	}
	if entry.WindowID != "" {
		a.Window = clip(entry.WindowID)
		a.Session = clip(entry.Session)
		s.bus.Refresh()
	}
	return success(runOut{RunOutcome: cronedit.OutcomeOf(entry), Name: name})
}

// scheduleResult is the card of a job after a write.
func (s *Server) scheduleResult(name string, changed []string, reseeded bool) outcome {
	d, err := s.scheduleStore().Detail(name)
	if err != nil {
		return scheduleError(err)
	}
	return success(scheduleOut{scheduleCardOut: s.scheduleCard(d), Changed: changed, Reseeded: reseeded})
}
