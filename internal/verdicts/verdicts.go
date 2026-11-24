// Package verdicts is the machine-wide ledger of PR review verdicts: one
// NDJSON line per `REVIEW PR #N — VERDICT: ...` message a reviewer session
// sends, in <stateDir>/verdicts/verdicts-<YYYY-MM>.ndjson. The PostToolUse
// hook appends (hooks.recordVerdict) and `agentctl audit verdicts` reads.
//
// It is a leaf: callers build the Record. State paths are resolved per call,
// never at package init.
package verdicts

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/safefs"
)

// MaxAge is how long a monthly file is kept.
const MaxAge = 90 * 24 * time.Hour

const maxLine = 64 << 10

// Verdict values a reviewer may send.
const (
	Merge             = "MERGE"
	MergeWithFollowup = "MERGE-WITH-FOLLOWUPS"
	Blocked           = "BLOCKED"
	NeedsBrendan      = "NEEDS-BRENDAN"
)

// Seams; tests replace them. Resolved per call, never at package init.
var (
	dirFn  = func() (string, error) { d, err := config.StateDir(); return filepath.Join(d, "verdicts"), err }
	rootFn = config.StateDir
	now    = time.Now
)

// Record is one review verdict. Author fields are empty when the author
// window could not be resolved locally.
type Record struct {
	TS        time.Time `json:"ts"`
	PR        int       `json:"pr"`
	Repo      string    `json:"repo,omitempty"`
	Verdict   string    `json:"verdict"`
	Rounds    int       `json:"rounds,omitempty"`
	Reviewer  string    `json:"reviewer_window,omitempty"`
	RevAgent  string    `json:"reviewer_agent,omitempty"`
	RevSess   string    `json:"reviewer_session_id,omitempty"`
	Author    string    `json:"author_window,omitempty"`
	AuthAgent string    `json:"author_agent,omitempty"`
	AuthSess  string    `json:"author_session_id,omitempty"`
	Branch    string    `json:"branch,omitempty"`
}

var (
	verdictRe = regexp.MustCompile(`^\s*REVIEW PR #(\d+)\s*[—–-]+\s*VERDICT:\s*(MERGE-WITH-FOLLOWUPS|MERGE|BLOCKED|NEEDS-BRENDAN)`)
	roundsRe  = regexp.MustCompile(`(?i)\brounds?:\s*(\d+)|\bround\s+(\d+)\b|\b(\d+)(?:st|nd|rd|th)\s+round\b`)
)

// Parse extracts the PR number, verdict and stated review round from a
// message body. ok is false for any other message, including one that merely
// quotes a verdict: the template starts the message with the verdict line.
// The cheap prefix check comes first: this runs on every SendMessage.
func Parse(body string) (pr int, verdict string, rounds int, ok bool) {
	if !strings.HasPrefix(strings.TrimSpace(body), "REVIEW PR #") {
		return 0, "", 0, false
	}
	m := verdictRe.FindStringSubmatch(body)
	if m == nil {
		return 0, "", 0, false
	}
	pr, err := strconv.Atoi(m[1])
	if err != nil || pr <= 0 {
		return 0, "", 0, false
	}
	if r := roundsRe.FindStringSubmatch(body); r != nil {
		rounds, _ = strconv.Atoi(r[1] + r[2] + r[3])
	}
	return pr, m[2], rounds, true
}

// FileName is the ledger file holding t.
func FileName(t time.Time) string {
	return "verdicts-" + t.UTC().Format("2006-01") + ".ndjson"
}

// Append writes r (TS defaults to now) with one O_APPEND write under an
// exclusive flock. Errors are returned for logging; callers never fail on
// them.
func Append(r Record) error {
	if r.TS.IsZero() {
		r.TS = now()
	}
	r.TS = r.TS.UTC()
	root, err := rootFn()
	if err != nil {
		return err
	}
	dir, err := dirFn()
	if err != nil {
		return err
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := safefs.OpenNoFollow(root, filepath.Join(dir, FileName(r.TS)), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening verdict ledger: %w", err)
	}
	defer f.Close() //nolint:errcheck
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking verdict ledger: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("writing verdict ledger: %w", err)
	}
	return nil
}

// Load returns the records at or after since, newest first. A missing ledger
// is no records; malformed lines are skipped.
func Load(since time.Time) ([]Record, error) {
	dir, err := dirFn()
	if err != nil {
		return nil, err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "verdicts-*.ndjson"))
	sort.Strings(matches)
	floor := FileName(since)
	var out []Record
	for _, m := range matches {
		if filepath.Base(m) < floor {
			continue
		}
		for _, r := range readFile(m) {
			if !r.TS.Before(since) {
				out = append(out, r)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.After(out[j].TS) })
	return out, nil
}

func readFile(path string) []Record {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close() //nolint:errcheck
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), maxLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if json.Unmarshal([]byte(line), &r) == nil && !r.TS.IsZero() && r.PR > 0 && r.Verdict != "" {
			out = append(out, r)
		}
	}
	return out
}

// Prune removes ledger files not modified for maxAge and reports how many.
func Prune(at time.Time, maxAge time.Duration) (int, error) {
	dir, err := dirFn()
	if err != nil {
		return 0, err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "verdicts-*.ndjson"))
	n := 0
	for _, m := range matches {
		fi, err := os.Lstat(m)
		if err != nil || !fi.Mode().IsRegular() || at.Sub(fi.ModTime()) <= maxAge {
			continue
		}
		if os.Remove(m) == nil {
			n++
		}
	}
	return n, nil
}

// AgentStat is the verdict tally for one author agent.
type AgentStat struct {
	Agent   string  `json:"agent"`
	Total   int     `json:"total"`
	Blocked int     `json:"blocked"`
	Rate    float64 `json:"blocked_rate"`
}

// Unknown is the AgentStat name for verdicts whose author was not resolved.
const Unknown = "(unknown)"

// ByAuthor tallies recs per author agent, most verdicts first.
// A reviewer session's repeated verdicts on one PR count once: the latest.
func ByAuthor(recs []Record) []AgentStat {
	m := map[string]*AgentStat{}
	for _, r := range dedupe(recs) {
		a := r.AuthAgent
		if a == "" {
			a = Unknown
		}
		s := m[a]
		if s == nil {
			s = &AgentStat{Agent: a}
			m[a] = s
		}
		s.Total++
		if r.Verdict == Blocked {
			s.Blocked++
		}
	}
	out := make([]AgentStat, 0, len(m))
	for _, s := range m {
		s.Rate = float64(s.Blocked) / float64(s.Total)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Agent < out[j].Agent
	})
	return out
}

// dedupe keeps the latest record per (PR, reviewer session). Records
// without a reviewer session ID cannot be matched and are all kept.
func dedupe(recs []Record) []Record {
	type key struct {
		pr   int
		sess string
	}
	latest := map[key]int{}
	for i, r := range recs {
		if r.RevSess == "" {
			continue
		}
		k := key{r.PR, r.RevSess}
		if j, ok := latest[k]; !ok || !r.TS.Before(recs[j].TS) {
			latest[k] = i
		}
	}
	out := make([]Record, 0, len(recs))
	for i, r := range recs {
		if r.RevSess != "" && latest[key{r.PR, r.RevSess}] != i {
			continue
		}
		out = append(out, r)
	}
	return out
}
