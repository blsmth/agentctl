package cronedit

import (
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/launchd"
)

// RecentRuns is how many runs a list row carries; DetailRuns how many the
// job card does.
const (
	RecentRuns = 5
	DetailRuns = 20
)

// RunningWindow is a live, not-done window a job launched.
type RunningWindow struct {
	WindowID string `json:"window_id"`
	Session  string `json:"session"`
}

// Status is one job as the list views show it.
type Status struct {
	Name string   `json:"name"`
	Job  cron.Job `json:"job"`
	// Enabled is the job's enabled flag (false = paused).
	Enabled bool `json:"enabled"`
	// Invalid is the job's first validation problem ("" when it is valid);
	// a tick skips an invalid job.
	Invalid string `json:"invalid,omitempty"`
	// ScheduleError is set when the schedule does not parse.
	ScheduleError string `json:"schedule_error,omitempty"`
	// Words is the schedule in words ("" when Describe has none).
	Words string `json:"words,omitempty"`
	// Simple is the picker model of the schedule (nil = Custom).
	Simple *SimpleSchedule `json:"simple,omitempty"`
	// NextFire is the next fire with jitter; nil when paused, invalid or
	// never firing.
	NextFire *time.Time `json:"next_fire,omitempty"`
	// Running is set while a window the job launched is live and not done.
	Running bool            `json:"running"`
	Windows []RunningWindow `json:"windows"`
	// Last is the newest run entry (any result); FailedLast is set when it
	// is a failed launch.
	Last       *cron.Entry `json:"last,omitempty"`
	FailedLast bool        `json:"failed_last"`
	// Recent are the newest RecentRuns entries, newest first.
	Recent []cron.Entry `json:"recent"`
}

// Throttles are the global cron limits the list shows.
type Throttles struct {
	MinInterval   string                     `json:"min_interval"`
	MaxConcurrent int                        `json:"max_concurrent"`
	CatchupWindow string                     `json:"catchup_window"`
	UsageGate     config.CronUsageThresholds `json:"skip_if_usage_above"`
}

// Overview is the whole list: global state plus one Status per job, in file
// order (SortForDisplay orders them for the UI).
type Overview struct {
	// Enabled is cron.enabled (false = the kill switch is on).
	Enabled bool `json:"enabled"`
	// SchedulerInstalled is false when the ticker is not installed
	// ("run agentctl cron install").
	SchedulerInstalled bool      `json:"scheduler_installed"`
	Throttles          Throttles `json:"throttles"`
	// Active counts live, not-done scheduled windows (max_concurrent's
	// count).
	Active int      `json:"active"`
	Jobs   []Status `json:"jobs"`
}

// OverviewInput is what BuildOverview needs, gathered by the caller.
type OverviewInput struct {
	Now                time.Time
	Config             config.CronConfig
	Jobs               []cron.Job
	Runs               []cron.Entry // oldest first, as cron.ReadRuns
	Windows            []cron.Window
	SchedulerInstalled bool
	// Check returns extra problems of a valid job (the pinned model, backend
	// and account against the real option sets); nil skips it.
	Check func(cron.Job) []cron.FieldError
}

// BuildOverview computes the list. It is pure.
func BuildOverview(in OverviewInput) Overview {
	th := in.Config.EffectiveUsageThresholds()
	o := Overview{
		Enabled:            in.Config.EnabledOrDefault(),
		SchedulerInstalled: in.SchedulerInstalled,
		Throttles: Throttles{
			MinInterval:   shortDuration(in.Config.EffectiveMinInterval()),
			MaxConcurrent: in.Config.EffectiveMaxConcurrent(),
			CatchupWindow: shortDuration(in.Config.EffectiveCatchupWindow()),
			UsageGate:     th,
		},
		Jobs: make([]Status, 0, len(in.Jobs)),
	}
	byJob := map[string][]cron.Entry{}
	for _, e := range in.Runs {
		byJob[e.Job] = append(byJob[e.Job], e)
	}
	for _, w := range in.Windows {
		if !w.Done {
			o.Active++
		}
	}
	for _, j := range in.Jobs {
		o.Jobs = append(o.Jobs, buildStatus(j, in.Now, byJob[j.Name], in.Windows, in.Check))
	}
	return o
}

func buildStatus(j cron.Job, now time.Time, runs []cron.Entry, windows []cron.Window, check func(cron.Job) []cron.FieldError) Status {
	st := Status{Name: j.Name, Job: j, Enabled: j.IsEnabled(), Words: cron.Describe(j.Schedule), Windows: []RunningWindow{}}
	if err := j.Validate(); err != nil {
		st.Invalid = err.Error()
	} else if check != nil {
		if errs := check(normalize(j)); len(errs) > 0 {
			st.Invalid = errs[0].Message
		}
	}
	if ss, ok := ParseSimple(j.Schedule); ok {
		st.Simple = &ss
	}
	if next, err := NextFire(j, now); err != nil {
		st.ScheduleError = err.Error()
	} else if !next.IsZero() {
		st.NextFire = &next
	}
	for _, w := range windows {
		if w.Job == j.Name && !w.Done {
			st.Running = true
			st.Windows = append(st.Windows, RunningWindow{WindowID: w.ID, Session: w.Session})
		}
	}
	st.Recent = newest(runs, RecentRuns)
	if len(runs) > 0 {
		last := runs[len(runs)-1]
		st.Last = &last
		st.FailedLast = cron.IsFailed(last.Result)
	}
	return st
}

// newest returns up to n entries, newest first.
func newest(runs []cron.Entry, n int) []cron.Entry {
	out := make([]cron.Entry, 0, min(n, len(runs)))
	for i := len(runs) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, runs[i])
	}
	return out
}

// SortForDisplay orders statuses the way the phone and hub list them:
// failed last run first, then running, then by next fire (soonest first),
// paused jobs last; ties by name.
func SortForDisplay(sts []Status) {
	group := func(s Status) int {
		switch {
		case !s.Enabled:
			return 3
		case s.FailedLast:
			return 0
		case s.Running:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(sts, func(a, b Status) int {
		if d := group(a) - group(b); d != 0 {
			return d
		}
		switch {
		case a.NextFire != nil && b.NextFire != nil && !a.NextFire.Equal(*b.NextFire):
			return a.NextFire.Compare(*b.NextFire)
		case a.NextFire != nil && b.NextFire == nil:
			return -1
		case a.NextFire == nil && b.NextFire != nil:
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// Overview loads the jobs, run history, windows and config and builds the
// list. Zero jobs and a missing schedules.json are an empty list.
func (s Store) Overview() (Overview, error) {
	jobs, err := s.Load()
	if err != nil {
		return Overview{}, err
	}
	in := OverviewInput{Now: s.now(), Config: s.config(), Jobs: jobs.Jobs, SchedulerInstalled: s.schedulerInstalled(), Check: s.optionErrors}
	if dir, err := s.StateDir(); err == nil {
		in.Runs, _ = cron.ReadRuns(dir, "")
	}
	in.Windows = s.windows(jobs.Jobs)
	return BuildOverview(in), nil
}

func (s Store) windows(jobs []cron.Job) []cron.Window {
	if s.Windows == nil || len(jobs) == 0 {
		return nil
	}
	ws, _ := s.Windows(jobs)
	return ws
}

func (s Store) schedulerInstalled() bool {
	if s.SchedulerInstalled == nil {
		return SchedulerInstalled()
	}
	return s.SchedulerInstalled()
}

// Detail is the job card: the status, the next PreviewFires fires, the last
// DetailRuns runs (newest first) and, for a prompt_key job, the key's
// current text (read-only).
type Detail struct {
	Status
	// NextFires are the next PreviewFires fires (jitter included) and
	// NextFiresText the same in cron.NextFireFormat, local time; empty for a
	// paused or invalid job.
	NextFires     []time.Time  `json:"next_fires"`
	NextFiresText []string     `json:"next_fires_text"`
	Runs          []cron.Entry `json:"runs"`
	// PromptResolved is the prompt_key's text now; PromptKeyError says why
	// it could not be read (fires fail until it can).
	PromptResolved string `json:"prompt_resolved,omitempty"`
	PromptKeyError string `json:"prompt_key_error,omitempty"`
}

// Detail returns one job's card.
func (s Store) Detail(name string) (Detail, error) {
	job, err := s.Get(name)
	if err != nil {
		return Detail{}, err
	}
	var runs []cron.Entry
	if dir, err := s.StateDir(); err == nil {
		runs, _ = cron.ReadRuns(dir, name)
	}
	now := s.now()
	d := Detail{
		Status:        buildStatus(job, now, runs, s.windows([]cron.Job{job}), s.optionErrors),
		Runs:          newest(runs, DetailRuns),
		NextFires:     []time.Time{},
		NextFiresText: []string{},
	}
	if job.IsEnabled() {
		d.NextFires = NextFires(job.Schedule, job.Name, now, PreviewFires)
		for _, t := range d.NextFires {
			d.NextFiresText = append(d.NextFiresText, t.In(time.Local).Format(cron.NextFireFormat))
		}
	}
	if job.PromptKey != "" {
		d.PromptResolved, err = s.PromptKeyText(job.PromptKey)
		if err != nil {
			d.PromptKeyError = err.Error()
		}
	}
	return d, nil
}

// PromptKeyText reads a prompt_key's current text (ResolvePromptKey unless
// the seam is set).
func (s Store) PromptKeyText(key string) (string, error) {
	if s.ResolvePromptKey != nil {
		return s.ResolvePromptKey(key)
	}
	return ResolvePromptKey(key)
}

// ResolvePromptKey reads a context store key's text. The store must be
// initialized (ctxstore.Init, done by main).
func ResolvePromptKey(key string) (string, error) {
	if !validPromptKey(key) {
		return "", &ValidationError{Fields: []cron.FieldError{{Field: cron.FieldPrompt, Message: "invalid prompt_key " + key}}}
	}
	e, ok, err := ctxstore.Get(key)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", &NotFoundError{Name: key}
	}
	return e.Content, nil
}

// WindowsFromRows lists the cron-launched windows among fleet rows (the
// hub's and serve's view of the live windows), for Store.Windows.
func WindowsFromRows(rows []fleet.Row) []cron.Window {
	var out []cron.Window
	for _, r := range rows {
		if r.ScheduledBy == "" || r.Shell {
			continue
		}
		out = append(out, cron.Window{ID: r.WindowID, Job: r.ScheduledBy, Session: r.Session, Done: r.Done, ScheduledAt: r.ScheduledAt})
	}
	return out
}

// SchedulerInstalled reports whether the ticker's LaunchAgent plist is on
// disk. Off macOS it cannot tell and reports true.
func SchedulerInstalled() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return true
	}
	_, err = os.Stat(launchd.PlistPath(home, cron.LaunchdLabel))
	return err == nil
}

// shortDuration drops Duration.String's zero tails: 15m, 6h, 1h30m.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
