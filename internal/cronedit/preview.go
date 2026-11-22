package cronedit

import (
	"fmt"
	"time"

	"github.com/blsmth/agentctl/internal/cron"
)

// PreviewFires is how many upcoming fires a preview lists.
const PreviewFires = 3

// Preview is the live preview a form shows under the schedule, and the
// review step's facts: the schedule in words, the next fires (jitter
// included, local time) and every validation problem.
type Preview struct {
	// Job is the job as it would be saved (normalized).
	Job cron.Job `json:"job"`
	// Schedule is the cron expression (from the picker when one was given).
	Schedule string `json:"schedule"`
	// Words is cron.Describe of the schedule; "" when it has no wording,
	// in which case the expression is the description.
	Words string `json:"words,omitempty"`
	// Simple is the picker model of the schedule, nil when the picker
	// cannot show it exactly (Custom mode).
	Simple *SimpleSchedule `json:"simple,omitempty"`
	// NextFires are the next PreviewFires fire times and NextFiresText the
	// same in cron.NextFireFormat ("Mon 23 Nov 07:31"), local time.
	NextFires     []time.Time `json:"next_fires"`
	NextFiresText []string    `json:"next_fires_text"`
	// TimeZone is the local zone abbreviation the times are in.
	TimeZone string `json:"time_zone"`
	// Errors lists every field problem; Valid is len(Errors) == 0.
	Errors []cron.FieldError `json:"errors"`
	Valid  bool              `json:"valid"`
	// Changed lists the fields an update would change (original set).
	Changed []string `json:"changed,omitempty"`
	// Diff is the same as Changed with the old and new values (an update).
	Diff []FieldChange `json:"diff,omitempty"`
	// Warnings are non-blocking problems (a model outside a list that
	// cannot be complete); they never make the preview invalid.
	Warnings []cron.FieldError `json:"warnings"`
}

// ApplySimple sets job.Schedule from a picker model. A model that does not
// render is a schedule field error.
func ApplySimple(job cron.Job, s *SimpleSchedule) (cron.Job, *cron.FieldError) {
	if s == nil {
		return job, nil
	}
	spec, err := s.Spec()
	if err != nil {
		return job, &cron.FieldError{Field: cron.FieldSchedule, Message: err.Error()}
	}
	job.Schedule = spec
	return job, nil
}

// Preview validates job as Create (original "") or Update (original = the
// stored name) would, without saving, and computes the schedule facts.
// simple, when set, replaces the job's schedule.
func (s Store) Preview(job cron.Job, original string, simple *SimpleSchedule) Preview {
	var extra []cron.FieldError
	job, ferr := ApplySimple(job, simple)
	if ferr != nil {
		extra = append(extra, *ferr)
	}
	norm, errs := s.Validate(job, original)
	errs = append(errs, extra...)
	var changed []string
	var diff []FieldChange
	switch jobs, err := s.Load(); {
	case err != nil:
		errs = append(errs, cron.FieldError{Field: cron.FieldName, Message: err.Error()})
	case original == "":
		if jobs.Find(norm.Name) >= 0 {
			errs = append(errs, cron.FieldError{Field: cron.FieldName, Message: fmt.Sprintf("a cron job named %q already exists", norm.Name)})
		}
	default:
		if i := jobs.Find(original); i < 0 {
			errs = append(errs, cron.FieldError{Field: cron.FieldName, Message: (&NotFoundError{Name: original}).Error()})
		} else {
			changed = ChangedFields(jobs.Jobs[i], norm)
			diff = Diff(jobs.Jobs[i], norm)
		}
	}
	now := s.now()
	p := Preview{Job: norm, Schedule: norm.Schedule, Words: cron.Describe(norm.Schedule), Errors: sortFields(errs), Changed: changed, Diff: diff, Warnings: s.Warnings(norm)}
	if ss, ok := ParseSimple(norm.Schedule); ok {
		p.Simple = &ss
	}
	p.NextFires = NextFires(norm.Schedule, norm.Name, now, PreviewFires)
	p.NextFiresText = make([]string, len(p.NextFires))
	for i, t := range p.NextFires {
		p.NextFiresText[i] = t.Format(cron.NextFireFormat)
	}
	p.TimeZone, _ = now.In(time.Local).Zone()
	if p.Errors == nil {
		p.Errors = []cron.FieldError{}
	}
	p.Valid = len(p.Errors) == 0
	return p
}

// NextFires returns up to n upcoming fires of spec for the job name
// (jitter included), or nil for an invalid or never-firing schedule.
func NextFires(spec, name string, now time.Time, n int) []time.Time {
	sched, err := cron.ParseSchedule(spec)
	if err != nil {
		return []time.Time{}
	}
	jitter := cron.Jitter(name)
	out := make([]time.Time, 0, n)
	t := now
	for len(out) < n {
		next := cron.NextFire(sched, t, jitter)
		if next.IsZero() {
			break
		}
		out = append(out, next)
		t = next
	}
	return out
}

// NextFire is the job's next fire (jitter included). It returns the zero
// time for a paused job or one that never fires, and the parse error for an
// invalid schedule.
func NextFire(job cron.Job, now time.Time) (time.Time, error) {
	sched, err := cron.ParseSchedule(job.Schedule)
	if err != nil {
		return time.Time{}, err
	}
	if !job.IsEnabled() {
		return time.Time{}, nil
	}
	return cron.NextFire(sched, now, cron.Jitter(job.Name)), nil
}
