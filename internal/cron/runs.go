package cron

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Run results recorded in runs.ndjson.
const (
	ResultFired              = "fired"
	ResultSkippedOverlap     = "skipped:overlap"
	ResultSkippedUsage       = "skipped:usage"
	ResultSkippedCatchup     = "skipped:catchup"
	ResultSkippedConcurrency = "skipped:concurrency"
	ResultTimeout            = "timeout"
	resultFailedPrefix       = "failed:"
)

// Lifecycle results: a change to the job definition rather than a run.
// Each carries the job snapshot (Entry.Def) and who made it (Entry.By), so
// a job's definition survives its deletion. A one-shot's completion needs
// no entry of its own: its fired/failed/skipped:catchup entry is the
// record, and the job keeps the outcome until it is pruned.
const (
	ResultCreated = "created"
	ResultEdited  = "edited"
	ResultPaused  = "paused"
	ResultResumed = "resumed"
	ResultDeleted = "deleted"
	// ResultPruned is a done one-shot the tick removed PruneDoneAfter
	// after it completed.
	ResultPruned = "pruned"
)

// IsLifecycle reports whether result is a lifecycle result.
func IsLifecycle(result string) bool {
	switch result {
	case ResultCreated, ResultEdited, ResultPaused, ResultResumed, ResultDeleted, ResultPruned:
		return true
	}
	return false
}

// Failed returns the failed:<reason> result.
func Failed(reason string) string { return resultFailedPrefix + reason }

// IsFailed reports whether result is a failed:<reason>.
func IsFailed(result string) bool { return strings.HasPrefix(result, resultFailedPrefix) }

// Entry is one runs.ndjson line: a decision a tick (or `cron run`) made.
type Entry struct {
	Time      time.Time `json:"ts"`
	Job       string    `json:"job"`
	Scheduled time.Time `json:"scheduled,omitzero"`
	Result    string    `json:"result"`
	// Manual marks a `cron run` fire.
	Manual   bool   `json:"manual,omitempty"`
	WindowID string `json:"window,omitempty"`
	Session  string `json:"session,omitempty"`
	// SessionID is the agent session the run started; it outlives the
	// window and identifies a closed run's transcript. Absent on entries
	// recorded before it was tracked.
	SessionID string `json:"session_id,omitempty"`
	Account   string `json:"account,omitempty"`
	// Missed counts older fires a catch-up superseded.
	Missed int    `json:"missed,omitempty"`
	Note   string `json:"note,omitempty"`
	// By and Def are set on lifecycle entries only.
	By  *Actor       `json:"by,omitempty"`
	Def *JobSnapshot `json:"def,omitempty"`
}

// IsAttempt reports whether the entry is a launch attempt (fired or failed),
// as opposed to a skip, a timeout or a lifecycle entry.
func (e Entry) IsAttempt() bool { return e.Result == ResultFired || IsFailed(e.Result) }

// IsLifecycle reports whether the entry records a definition change.
func (e Entry) IsLifecycle() bool { return IsLifecycle(e.Result) }

// Actor kinds: who made a lifecycle change.
const (
	ActorCLI    = "cli"    // a CLI run that is not in an agent window
	ActorWindow = "window" // a CLI run from an agentctl window (an agent)
	ActorHub    = "hub"
	ActorPhone  = "phone"
	ActorTick   = "tick"
)

// Actor is who made a lifecycle change: the kind, and for ActorWindow the
// caller's window and agent session (as spawned_by records them).
type Actor struct {
	Kind      string `json:"kind"`
	WindowID  string `json:"window,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// String is "window @12 (session 1a2b3c4d)", "hub", "cli".
func (a Actor) String() string {
	if a.Kind == "" {
		return ActorCLI
	}
	s := a.Kind
	if a.WindowID != "" {
		s += " " + a.WindowID
	}
	if a.SessionID != "" {
		sid := a.SessionID
		if len(sid) > 8 {
			sid = sid[:8]
		}
		s += " (session " + sid + ")"
	}
	return s
}

// promptExcerptRunes bounds JobSnapshot.Prompt.
const promptExcerptRunes = 200

// JobSnapshot is a job's definition as a lifecycle entry records it. The
// prompt is an excerpt (inline prompts) or the key/template name, never
// the full text.
type JobSnapshot struct {
	Name         string    `json:"name"`
	Schedule     string    `json:"schedule,omitempty"`
	At           string    `json:"at,omitempty"`
	State        string    `json:"state,omitempty"`
	CompletedAt  time.Time `json:"completed_at,omitzero"`
	Result       string    `json:"result,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	Dir          string    `json:"dir,omitempty"`
	Account      string    `json:"account,omitempty"`
	Mode         string    `json:"mode,omitempty"`
	Model        string    `json:"model,omitempty"`
	Enabled      bool      `json:"enabled"`
	PromptSource string    `json:"prompt_source"`
	Prompt       string    `json:"prompt,omitempty"`
}

// SnapshotOf is j's lifecycle snapshot.
func SnapshotOf(j Job) JobSnapshot {
	s := JobSnapshot{
		Name: j.Name, Schedule: strings.TrimSpace(j.Schedule), At: strings.TrimSpace(j.At), State: j.State,
		CompletedAt: j.CompletedAt, Result: j.Result,
		Agent: j.Agent, Dir: j.Dir, Account: j.Account, Mode: j.Mode, Model: j.Model,
		Enabled: j.IsEnabled(), PromptSource: j.PromptSource(),
	}
	switch {
	case j.Template != "":
		s.Prompt = j.Template
	case j.PromptKey != "":
		s.Prompt = j.PromptKey
	default:
		s.Prompt = excerpt(j.Prompt, promptExcerptRunes)
	}
	return s
}

// Job rebuilds the parts of a job the snapshot holds (for display).
func (s JobSnapshot) Job() Job {
	en := s.Enabled
	j := Job{Name: s.Name, Schedule: s.Schedule, At: s.At, State: s.State, CompletedAt: s.CompletedAt, Result: s.Result, Agent: s.Agent, Dir: s.Dir,
		Account: s.Account, Mode: s.Mode, Model: s.Model, Enabled: &en}
	switch s.PromptSource {
	case FieldTemplate:
		j.Template = s.Prompt
	case "prompt_key":
		j.PromptKey = s.Prompt
	default:
		j.Prompt = s.Prompt
	}
	return j
}

// excerpt collapses whitespace and keeps the first n runes, with "…" when
// cut.
func excerpt(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// LifecycleEntry builds the lifecycle entry for job.
func LifecycleEntry(now time.Time, result string, job Job, by Actor, note string) Entry {
	snap := SnapshotOf(job)
	if by.Kind == "" {
		by.Kind = ActorCLI
	}
	return Entry{Time: now, Job: job.Name, Result: result, Account: job.Account, Note: note, By: &by, Def: &snap}
}

// runsMaxBytes bounds runs.ndjson; past it the oldest half is dropped.
const runsMaxBytes = 2 << 20

// AppendRun appends e to <dir>/runs.ndjson. It holds <dir>/runs.lock (a
// leaf lock: nothing else is taken while it is held) around the append and
// any trim, so the tick, `cron run` and the editors (cronedit, which holds
// only the jobs lock) never lose a line to a concurrent trim.
func AppendRun(dir string, e Entry) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, runsFile)
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	unlock, _, err := flockIn(dir, runsLock, true)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	_, werr := f.Write(append(data, '\n'))
	fi, serr := f.Stat()
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("writing %s: %w", path, werr)
	}
	if serr == nil && fi.Size() > runsMaxBytes {
		trimRuns(path)
	}
	return nil
}

// trimRuns keeps the newest half of an oversized runs file. The caller
// (AppendRun) holds runs.lock, so no append races the rewrite.
func trimRuns(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	cut := bytes.IndexByte(data[len(data)/2:], '\n')
	if cut < 0 {
		return
	}
	tmp := path + ".trim"
	if err := os.WriteFile(tmp, data[len(data)/2+cut+1:], 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// ReadRuns returns the entries in <dir>/runs.ndjson, oldest first, filtered
// to job when it is non-empty. Unparseable lines are skipped.
func ReadRuns(dir, job string) ([]Entry, error) {
	f, err := os.Open(filepath.Join(dir, runsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if job == "" || e.Job == job {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// LastByJob returns each job's most recent run entry. Lifecycle entries
// are not runs and are ignored.
func LastByJob(entries []Entry) map[string]Entry {
	m := map[string]Entry{}
	for _, e := range entries {
		if e.IsLifecycle() {
			continue
		}
		m[e.Job] = e
	}
	return m
}

// RunsOnly drops lifecycle entries, keeping the order.
func RunsOnly(entries []Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if !e.IsLifecycle() {
			out = append(out, e)
		}
	}
	return out
}

// LatestSnapshots returns each job's newest lifecycle entry that carries a
// snapshot (the last known definition, and whether it was deleted).
func LatestSnapshots(entries []Entry) map[string]Entry {
	m := map[string]Entry{}
	for _, e := range entries {
		if e.IsLifecycle() && e.Def != nil {
			m[e.Job] = e
		}
	}
	return m
}

// FailingJobs returns the jobs whose last n launch attempts all failed.
func FailingJobs(entries []Entry, n int) []string {
	attempts := map[string][]Entry{}
	var order []string
	for _, e := range entries {
		if !e.IsAttempt() {
			continue
		}
		if _, seen := attempts[e.Job]; !seen {
			order = append(order, e.Job)
		}
		attempts[e.Job] = append(attempts[e.Job], e)
	}
	var out []string
	for _, job := range order {
		a := attempts[job]
		if len(a) < n {
			continue
		}
		all := true
		for _, e := range a[len(a)-n:] {
			if !IsFailed(e.Result) {
				all = false
				break
			}
		}
		if all {
			out = append(out, job)
		}
	}
	return out
}
