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
	Account  string `json:"account,omitempty"`
	// Missed counts older fires a catch-up superseded.
	Missed int    `json:"missed,omitempty"`
	Note   string `json:"note,omitempty"`
}

// IsAttempt reports whether the entry is a launch attempt (fired or failed),
// as opposed to a skip or a timeout.
func (e Entry) IsAttempt() bool { return e.Result == ResultFired || IsFailed(e.Result) }

// runsMaxBytes bounds runs.ndjson; past it the oldest half is dropped.
const runsMaxBytes = 2 << 20

// AppendRun appends e to <dir>/runs.ndjson.
func AppendRun(dir string, e Entry) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, runsFile)
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
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

// trimRuns keeps the newest half of an oversized runs file. Callers hold the
// tick lock, so no append races the rewrite.
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

// LastByJob returns each job's most recent entry.
func LastByJob(entries []Entry) map[string]Entry {
	m := map[string]Entry{}
	for _, e := range entries {
		m[e.Job] = e
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
