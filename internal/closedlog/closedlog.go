// Package closedlog is the machine-wide ledger of closed agent windows: one
// NDJSON line per tracked window that reconcile.Finish handled, in
// <stateDir>/closed/closed-<YYYY-MM>.ndjson. It is what lets `agentctl status
// list --closed` and the hub's Closed section show (and resume) sessions whose
// tmux window is gone. Session teardown never reaches Finish, so windows that
// live on in restore.json are not in it.
//
// It is a leaf: callers build the Record. State paths are resolved per call,
// never at package init.
package closedlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/safefs"
)

// MaxAge is how long a monthly file is kept.
const MaxAge = 90 * 24 * time.Hour

// MaxPrompt bounds the stored first prompt, in runes.
const MaxPrompt = 300

// maxLine bounds one ledger line when reading.
const maxLine = 64 << 10

// Close reasons.
const (
	// ReasonClosed is a window closed on its own (the user, the agent
	// exiting, sweep, ...).
	ReasonClosed = "closed"
	// ReasonCascade is a child closed because its dispatcher closed
	// (close_children).
	ReasonCascade = "cascade"
)

// Seams; tests replace them. Resolved per call, never at package init.
var (
	dirFn  = func() (string, error) { d, err := config.StateDir(); return filepath.Join(d, "closed"), err }
	rootFn = config.StateDir
	now    = time.Now
)

// Record is one closed window.
type Record struct {
	ClosedAt time.Time `json:"closed_at"`
	WindowID string    `json:"window_id"`
	// Session is the window's home tmux session (agentctl-<project>).
	Session    string `json:"session,omitempty"`
	ProjectDir string `json:"project_dir,omitempty"`
	RuntimeDir string `json:"runtime_dir,omitempty"`
	// WorkDir is where the agent ran (its worktree, else the project dir),
	// which is where a resume must start for the harness to find the
	// transcript.
	WorkDir     string `json:"work_dir,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Instance    string `json:"instance,omitempty"`
	Harness     string `json:"harness,omitempty"`
	Account     string `json:"account,omitempty"`
	Backend     string `json:"backend,omitempty"`
	Model       string `json:"model,omitempty"`
	Mode        string `json:"mode,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	LogPath     string `json:"log_path,omitempty"`
	ShortTitle  string `json:"short_title,omitempty"`
	TitleKind   string `json:"short_title_kind,omitempty"`
	Ticket      string `json:"ticket,omitempty"`
	TicketTitle string `json:"ticket_title,omitempty"`
	FirstPrompt string `json:"first_prompt,omitempty"`
	// State is the record's last state (working, blocked, idle).
	State     string `json:"state,omitempty"`
	Done      bool   `json:"done,omitempty"`
	TurnCount int    `json:"turn_count,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Worktree  string `json:"worktree,omitempty"`
	PRNumber  int    `json:"pr_number,omitempty"`
	PRURL     string `json:"pr_url,omitempty"`
	SpawnedBy string `json:"spawned_by,omitempty"`
	// SpawnedBySession is the dispatcher's session UUID.
	SpawnedBySession string `json:"spawned_by_session,omitempty"`
	ScheduledBy      string `json:"scheduled_by,omitempty"`
	// Reason is ReasonClosed or ReasonCascade; ClosedBy is the dispatcher
	// window whose close cascaded to this one.
	Reason   string `json:"reason,omitempty"`
	ClosedBy string `json:"closed_by,omitempty"`
}

// Key identifies a close: the same window of the same session running the
// same agent session. Load keeps one record per key, so a duplicated append
// shows once. Records with no session ID also key on the close second, since
// tmux recycles window IDs.
func (r Record) Key() string {
	k := r.Session + "|" + r.WindowID + "|" + r.SessionID + "|" + r.LogPath
	if r.SessionID == "" {
		k += "|" + strconv.FormatInt(r.ClosedAt.Unix(), 10)
	}
	return k
}

// Title is the record's display title: the short title, the ticket title,
// the first prompt's first line, else the instance.
func (r Record) Title() string {
	for _, s := range []string{r.ShortTitle, r.TicketTitle, firstLine(r.FirstPrompt), r.Instance, r.Agent, r.WindowID} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// Project is the project's display name (its directory basename).
func (r Record) Project() string {
	if r.ProjectDir == "" {
		return strings.TrimPrefix(r.Session, "agentctl-")
	}
	return filepath.Base(r.ProjectDir)
}

// FinalState is "done" for a finished window, else its last state.
func (r Record) FinalState() string {
	if r.Done {
		return "done"
	}
	if r.State == "" {
		return "idle"
	}
	return r.State
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// Truncate shortens s to at most n runes, appending "…" when cut.
func Truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// FileName is the ledger file holding t.
func FileName(t time.Time) string {
	return "closed-" + t.UTC().Format("2006-01") + ".ndjson"
}

// Append writes r (ClosedAt defaults to now, FirstPrompt is truncated to
// MaxPrompt) to the ledger with one O_APPEND write under an exclusive flock,
// so concurrent closers never interleave. Errors are returned for logging;
// callers never fail on them.
func Append(r Record) error {
	if r.ClosedAt.IsZero() {
		r.ClosedAt = now()
	}
	r.ClosedAt = r.ClosedAt.UTC()
	r.FirstPrompt = Truncate(r.FirstPrompt, MaxPrompt)
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
	f, err := safefs.OpenNoFollow(root, filepath.Join(dir, FileName(r.ClosedAt)), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening closed ledger: %w", err)
	}
	defer f.Close() //nolint:errcheck
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking closed ledger: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("writing closed ledger: %w", err)
	}
	return nil
}

// files lists the ledger files that can hold records at or after since,
// oldest first.
func files(since time.Time) ([]string, error) {
	dir, err := dirFn()
	if err != nil {
		return nil, err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "closed-*.ndjson"))
	sort.Strings(matches)
	floor := FileName(since)
	out := matches[:0]
	for _, m := range matches {
		if filepath.Base(m) >= floor {
			out = append(out, m)
		}
	}
	return out, nil
}

// Load returns the records closed at or after since, newest first, one per
// Key. A missing ledger is no records; malformed lines are skipped.
func Load(since time.Time) ([]Record, error) {
	paths, err := files(since)
	if err != nil {
		return nil, err
	}
	return loadFiles(paths, since), nil
}

func loadFiles(paths []string, since time.Time) []Record {
	var all []Record
	for _, p := range paths {
		for _, r := range readFile(p) {
			if !r.ClosedAt.Before(since) {
				all = append(all, r)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].ClosedAt.After(all[j].ClosedAt) })
	seen := make(map[string]bool, len(all))
	out := all[:0]
	for _, r := range all {
		if k := r.Key(); !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
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
		if json.Unmarshal([]byte(line), &r) == nil && !r.ClosedAt.IsZero() && r.WindowID != "" {
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
	matches, _ := filepath.Glob(filepath.Join(dir, "closed-*.ndjson"))
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

// Cache re-reads the ledger only when its files change (name, size or
// modification time), for readers that poll, like the hub. Safe for
// concurrent use.
type Cache struct {
	mu     sync.Mutex
	loaded bool
	sig    string
	floor  string
	recs   []Record
}

// Load is Load(since) served from the cache while the files are unchanged.
func (c *Cache) Load(since time.Time) ([]Record, error) {
	paths, err := files(since)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d;", filepath.Base(p), fi.Size(), fi.ModTime().UnixNano())
	}
	sig := b.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	floor := FileName(since)
	if !c.loaded || sig != c.sig || floor != c.floor {
		// Read from the month floor, filter by since below: the cache must
		// not depend on the exact since, which moves every poll.
		c.recs = loadFiles(paths, monthStart(since))
		c.sig, c.floor, c.loaded = sig, floor, true
	}
	out := make([]Record, 0, len(c.recs))
	for _, r := range c.recs {
		if !r.ClosedAt.Before(since) {
			out = append(out, r)
		}
	}
	return out, nil
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Ago renders how long before at t was, for "closed 2h ago" style labels:
// "just now", "5m ago", "2h ago", "3d ago".
func Ago(at, t time.Time) string {
	d := at.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h ago"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
}
