// Package cron implements `agentctl cron`: scheduled agent runs launched by a
// short-lived, flock-guarded `agentctl cron tick` that launchd runs every
// minute. See docs/design/cron.md.
//
// The package owns the jobs file, the per-job fire state, the run history and
// the tick's decisions (due fires, catch-up, overlap, max_concurrent, usage
// gate, timeouts). Every side effect a tick has on the outside world (tmux,
// dispatch, notifications) goes through Deps, so the decisions are testable
// without spawning anything.
package cron

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/agentname"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/launchopts"
	"github.com/blsmth/agentctl/internal/safefs"
)

// Accepted enum values. harness and mode ship now so the schema does not
// change when headless mode and other harnesses land.
const (
	HarnessClaude = "claude"
	ModeWindow    = "window"
	// ModeImprove runs a fleet improve run (internal/improve) instead of
	// dispatching a prompt: same launch path as `agentctl improve --fleet`.
	ModeImprove   = "improve"
	OverlapSkip   = "skip"
	OverlapAllow  = "allow"
	CatchupLatest = "latest"
	CatchupNone   = "none"
)

// SchedulesFile is the jobs file name inside the global config dir.
const SchedulesFile = "schedules.json"

// LaunchdLabel is the LaunchAgent label of the cron ticker.
const LaunchdLabel = "com.blsmth.agentctl.cron"

// ImproveAgent is the agent an improve job runs when it names none. It must
// equal improve.DefaultFleetAgent (a test in internal/cmd pins that).
const ImproveAgent = "fleet-improver"

// DefaultImproveSince is the evidence window of an improve job when its
// schedule gives no interval to derive one from.
const DefaultImproveSince = 24 * time.Hour

// maxImproveSince caps a derived evidence window.
const maxImproveSince = 30 * 24 * time.Hour

// Job is one scheduled run. Optional enum fields are normalized by
// Normalize; read Worktree and Enabled through UseWorktree and IsEnabled.
type Job struct {
	Name string `json:"name"`
	// Schedule is the cron expression of a recurring job; empty for a
	// one-shot.
	Schedule string `json:"schedule"`
	// At makes the job a one-shot: the RFC3339 time it fires once at
	// (jitter included, like a scheduled fire). Exactly one of Schedule and
	// At is set. See IsOnce and the one-shot fields below.
	At    string `json:"at,omitempty"`
	Dir   string `json:"dir"`
	Agent string `json:"agent"`
	// Exactly one of Prompt, PromptKey (context store) and Template (a
	// prompt template, internal/prompttemplate). --prompt-file is copied
	// into Prompt at add time; PromptKey and Template are read at every
	// fire, Template from Dir's project (shadowing the global templates).
	// With a Template, Agent, Model and Effort may be empty: the
	// template's defaults fill them, and a job field always wins.
	//
	// In ModeImprove no prompt is needed: at most one of the three adds a
	// theme (prompt, prompt_key) or a template body to the improve prompt,
	// and Agent defaults to the fleet-improver agent.
	Prompt    string `json:"prompt,omitempty"`
	PromptKey string `json:"prompt_key,omitempty"`
	Template  string `json:"template,omitempty"`
	Harness   string `json:"harness"`
	Mode      string `json:"mode"`
	// Worktree runs each fire in its own worktree (default true).
	Worktree *bool  `json:"worktree,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Backend  string `json:"backend,omitempty"`
	Account  string `json:"account,omitempty"`
	Overlap  string `json:"overlap"`
	Catchup  string `json:"catchup"`
	// Timeout is a Go duration; empty means none.
	Timeout          string                      `json:"timeout,omitempty"`
	SkipIfUsageAbove *config.CronUsageThresholds `json:"skip_if_usage_above,omitempty"`
	Enabled          *bool                       `json:"enabled,omitempty"`

	// One-shot outcome, written by the tick (or `cron run`) after the one
	// launch attempt, or when the time is missed: State is "" while
	// pending, then StateCompleted or StateExpired. A done one-shot never
	// fires again and is pruned PruneDoneAfter after CompletedAt. The
	// rest copy the run entry.
	State        string    `json:"state,omitempty"`
	CompletedAt  time.Time `json:"completed_at,omitzero"`
	Result       string    `json:"result,omitempty"`
	RunWindow    string    `json:"run_window,omitempty"`
	RunSession   string    `json:"run_session,omitempty"`
	RunSessionID string    `json:"run_session_id,omitempty"`
}

// One-shot states (Job.State).
const (
	StateCompleted = "completed"
	StateExpired   = "expired"
)

// PruneDoneAfter is how long a completed or expired one-shot stays in the
// jobs file before the tick prunes it.
const PruneDoneAfter = 30 * 24 * time.Hour

// IsOnce reports whether the job is a one-shot (At set).
func (j Job) IsOnce() bool { return strings.TrimSpace(j.At) != "" }

// IsDone reports whether a one-shot has completed or expired.
func (j Job) IsDone() bool { return j.State == StateCompleted || j.State == StateExpired }

// AtTime parses At.
func (j Job) AtTime() (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(j.At))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid at %q: want an RFC3339 time such as 2026-12-01T09:00:00-05:00", j.At)
	}
	return t, nil
}

// ClearOutcome resets a one-shot to pending.
func (j *Job) ClearOutcome() {
	j.State, j.CompletedAt, j.Result, j.RunWindow, j.RunSession, j.RunSessionID = "", time.Time{}, "", "", "", ""
}

// atLayouts are the local-time forms ParseAt accepts besides RFC3339.
var atLayouts = []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05"}

// ParseAt parses a `--at` value: "YYYY-MM-DD HH:MM" in local time, or an
// RFC3339 timestamp. A local wall-clock time that does not exist (the hour
// a DST spring-forward skips) is rejected; one that occurs twice (a
// fall-back) is the first occurrence, as Go resolves it, and the preview
// shows the offset. It does not check that the time is in the future.
func ParseAt(s string) (time.Time, error) {
	return parseAtIn(s, time.Local)
}

func parseAtIn(s string, loc *time.Location) (time.Time, error) {
	v := strings.TrimSpace(s)
	if v == "" {
		return time.Time{}, fmt.Errorf("--at needs a time: YYYY-MM-DD HH:MM (local time) or RFC3339")
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	for _, layout := range atLayouts {
		t, err := time.ParseInLocation(layout, v, loc)
		if err != nil {
			continue
		}
		want, _ := time.Parse(layout, v) // the wall clock as written
		if t.Hour() != want.Hour() || t.Minute() != want.Minute() || t.Day() != want.Day() {
			return time.Time{}, fmt.Errorf("--at %q does not exist in local time (a daylight saving change skips it); pick another time", s)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--at %q: want YYYY-MM-DD HH:MM (local time) or an RFC3339 time", s)
}

// FormatAt is the stored form of an at time (RFC3339, local offset).
func FormatAt(t time.Time) string { return t.In(time.Local).Format(time.RFC3339) }

// IsImprove reports whether the job runs a fleet improve run.
func (j Job) IsImprove() bool { return j.Mode == ModeImprove }

// ImproveSince is the evidence window of an improve fire at t: the gap between
// the next two scheduled fires (a daily job reviews 24h, a weekly one 7d),
// DefaultImproveSince when the schedule has fewer than two.
func (j Job) ImproveSince(t time.Time) time.Duration {
	sched, err := ParseSchedule(j.Schedule)
	if err != nil {
		return DefaultImproveSince
	}
	first := sched.Next(t)
	if first.IsZero() {
		return DefaultImproveSince
	}
	second := sched.Next(first)
	if second.IsZero() {
		return DefaultImproveSince
	}
	d := second.Sub(first)
	switch {
	case d < time.Hour:
		return time.Hour
	case d > maxImproveSince:
		return maxImproveSince
	}
	return d
}

// ImproveAgentName is the agent an improve job runs: its own, else ImproveAgent.
func (j Job) ImproveAgentName() string {
	if strings.TrimSpace(j.Agent) != "" {
		return j.Agent
	}
	return ImproveAgent
}

// ImproveSummary describes what an improve job does at t, the way the
// approval popup, `cron list` and the cards show it instead of a prompt:
// "Fleet improve, last 24h, agent fleet-improver, model opus".
func (j Job) ImproveSummary(t time.Time) string {
	model := j.Model
	if model == "" {
		model = "default"
	}
	return fmt.Sprintf("Fleet improve, last %s, agent %s, model %s", ShortDuration(j.ImproveSince(t)), j.ImproveAgentName(), model)
}

// ShortDuration renders d as days when it is a whole number of days, else as
// hours and minutes ("7d", "24h" is shown as "24h", "90m").
func ShortDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

// UseWorktree reports whether fires get their own worktree (default true).
func (j Job) UseWorktree() bool { return j.Worktree == nil || *j.Worktree }

// IsEnabled reports whether the job is active (default true).
func (j Job) IsEnabled() bool { return j.Enabled == nil || *j.Enabled }

// TimeoutDuration returns the parsed timeout, or 0 for none.
func (j Job) TimeoutDuration() time.Duration {
	if j.Timeout == "" {
		return 0
	}
	d, err := time.ParseDuration(j.Timeout)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// UsageThresholds returns the job's usage gate: its own non-zero fields over
// the global defaults.
func (j Job) UsageThresholds(global config.CronUsageThresholds) config.CronUsageThresholds {
	t := global
	if j.SkipIfUsageAbove != nil {
		if j.SkipIfUsageAbove.FiveHour > 0 {
			t.FiveHour = j.SkipIfUsageAbove.FiveHour
		}
		if j.SkipIfUsageAbove.SevenDay > 0 {
			t.SevenDay = j.SkipIfUsageAbove.SevenDay
		}
	}
	return t
}

// Normalize fills the defaults for unset enum fields.
func (j *Job) Normalize() {
	if j.Harness == "" {
		j.Harness = HarnessClaude
	}
	if j.Mode == "" {
		j.Mode = ModeWindow
	}
	if j.Overlap == "" {
		j.Overlap = OverlapSkip
	}
	if j.Catchup == "" {
		j.Catchup = CatchupLatest
	}
}

var (
	jobNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,47}$`)
	// backendRE and accountRE match config map keys; both end up in env
	// and log lines, never a shell.
	backendRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// Job field names, as in the JSON, used by FieldError.
const (
	FieldName             = "name"
	FieldSchedule         = "schedule"
	FieldAt               = "at"
	FieldDir              = "dir"
	FieldAgent            = "agent"
	FieldPrompt           = "prompt"
	FieldTemplate         = "template"
	FieldHarness          = "harness"
	FieldMode             = "mode"
	FieldOverlap          = "overlap"
	FieldCatchup          = "catchup"
	FieldModel            = "model"
	FieldEffort           = "effort"
	FieldBackend          = "backend"
	FieldAccount          = "account"
	FieldTimeout          = "timeout"
	FieldSkipIfUsageAbove = "skip_if_usage_above"
)

// FieldError is one validation problem, tied to the job field (its JSON
// name) a form shows it under.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Message }

// Validate checks a normalized job and returns its first problem. It does
// not check min_interval (see CheckMinInterval) or that the agent exists
// (the caller discovers agents).
func (j Job) Validate() error {
	if errs := j.FieldErrors(); len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// FieldErrors checks a normalized job and returns every problem, in field
// order (the order Validate reports them).
func (j Job) FieldErrors() []FieldError {
	var errs []FieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if strings.TrimSpace(j.Name) == "" {
		add(FieldName, "give the job a name")
	} else if !jobNameRE.MatchString(j.Name) {
		add(FieldName, "invalid job name %q: use letters, digits, - and _ (max 48, starting with a letter or digit)", j.Name)
	}
	switch {
	case j.IsOnce() && strings.TrimSpace(j.Schedule) != "":
		add(FieldSchedule, "use a schedule or a one-shot at time, not both")
	case j.IsOnce():
		if _, err := j.AtTime(); err != nil {
			add(FieldAt, "%s", err.Error())
		}
	default:
		if _, err := ParseSchedule(j.Schedule); err != nil {
			add(FieldSchedule, "%s", err.Error())
		}
	}
	switch j.State {
	case "", StateCompleted, StateExpired:
		if j.State != "" && !j.IsOnce() {
			add(FieldSchedule, "state %q applies only to a one-shot (at) job", j.State)
		}
	default:
		add(FieldSchedule, "state %q: want %s or %s", j.State, StateCompleted, StateExpired)
	}
	switch {
	case j.Dir == "":
		add(FieldDir, "dir is required")
	case !filepath.IsAbs(j.Dir):
		add(FieldDir, "dir %q must be an absolute path", j.Dir)
	}
	improve := j.IsImprove()
	switch {
	case improve && strings.TrimSpace(j.Agent) == "":
		// The fleet-improver agent runs it (checked at fire time).
	case improve && !agentname.ValidLaunch(j.Agent):
		add(FieldAgent, "invalid agent name %q", j.Agent)
	case strings.TrimSpace(j.Agent) == "" && j.Template != "":
		// The template names the agent (checked against the file by
		// cronedit, and again at fire time).
	case strings.TrimSpace(j.Agent) == "":
		add(FieldAgent, "pick an agent")
	case !agentname.ValidLaunch(j.Agent):
		add(FieldAgent, "invalid agent name %q", j.Agent)
	}
	switch n := countNonEmpty(j.Prompt, j.PromptKey, j.Template); {
	case n == 0 && improve:
		// An improve run needs no prompt.
	case n == 0:
		add(FieldPrompt, "write a prompt or pick a template")
	case n > 1 && j.Template != "":
		add(FieldTemplate, "use a template or a prompt, not both")
	case n > 1:
		add(FieldPrompt, "use a prompt or a prompt key, not both")
	case j.Template != "" && !agentname.Valid(j.Template):
		add(FieldTemplate, "invalid template name %q", j.Template)
	}
	if j.Harness != HarnessClaude {
		add(FieldHarness, "harness %q is not supported yet (only %q)", j.Harness, HarnessClaude)
	}
	if !slices.Contains([]string{ModeWindow, ModeImprove}, j.Mode) {
		add(FieldMode, "mode %q: want %s or %s", j.Mode, ModeWindow, ModeImprove)
	}
	if !slices.Contains([]string{OverlapSkip, OverlapAllow}, j.Overlap) {
		add(FieldOverlap, "overlap %q: want skip or allow", j.Overlap)
	} else if improve && j.Overlap == OverlapAllow {
		add(FieldOverlap, "an improve job cannot overlap itself: runs share the history stores, so the first end snapshot would take in the other run's edits")
	}
	if !slices.Contains([]string{CatchupLatest, CatchupNone}, j.Catchup) {
		add(FieldCatchup, "catchup %q: want latest or none", j.Catchup)
	}
	if j.Model != "" && !launchopts.ValidModel(j.Model) {
		add(FieldModel, "invalid model %q", j.Model)
	}
	if j.Effort != "" {
		if err := launchopts.ValidateEffort(j.Effort); err != nil {
			add(FieldEffort, "%s", err.Error())
		}
	}
	if j.Backend != "" && !backendRE.MatchString(j.Backend) {
		add(FieldBackend, "invalid backend %q", j.Backend)
	}
	if j.Account != "" && !backendRE.MatchString(j.Account) {
		add(FieldAccount, "invalid account %q", j.Account)
	}
	if j.Timeout != "" {
		if d, err := time.ParseDuration(j.Timeout); err != nil || d <= 0 {
			add(FieldTimeout, "invalid timeout %q: want a positive Go duration such as 45m", j.Timeout)
		}
	}
	if t := j.SkipIfUsageAbove; t != nil {
		for _, v := range []float64{t.FiveHour, t.SevenDay} {
			if v < 0 || v > 100 {
				add(FieldSkipIfUsageAbove, "skip_if_usage_above values must be between 0 and 100, got %v", v)
				break
			}
		}
	}
	return errs
}

// PromptSource names where a job's prompt comes from: "template",
// "prompt_key" or "prompt".
func (j Job) PromptSource() string {
	switch {
	case j.Template != "":
		return FieldTemplate
	case j.PromptKey != "":
		return "prompt_key"
	}
	return FieldPrompt
}

func countNonEmpty(vals ...string) int {
	n := 0
	for _, v := range vals {
		if v != "" {
			n++
		}
	}
	return n
}

// Jobs is the on-disk shape of schedules.json.
type Jobs struct {
	Jobs []Job `json:"jobs"`
}

// Find returns the index of the named job, or -1.
func (f *Jobs) Find(name string) int {
	return slices.IndexFunc(f.Jobs, func(j Job) bool { return j.Name == name })
}

// JobsPath returns <GlobalDir>/schedules.json. Jobs are global only: nothing
// in a project-local .agentctl/ can define one.
func JobsPath() (string, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SchedulesFile), nil
}

// LoadJobs reads the jobs file. A missing file is an empty list; a corrupt
// one is an error so a write never clobbers a file mid-edit. Jobs are
// normalized; invalid ones are returned as-is for the caller to report.
func LoadJobs(path string) (*Jobs, error) {
	f := &Jobs{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	for i := range f.Jobs {
		f.Jobs[i].Normalize()
	}
	return f, nil
}

// SaveJobs writes the jobs file atomically (temp + rename), mode 0600.
func SaveJobs(path string, f *Jobs) error {
	if f.Jobs == nil {
		f.Jobs = []Job{}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding jobs: %w", err)
	}
	data = append(data, '\n')
	if err := safefs.ReplaceFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
