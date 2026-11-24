// Package improverun records one improve session's trip through the agent
// history store: a snapshot of every store when the run starts, another when
// it ends (with the session's rationale in the commit body), and one summary
// notification when something changed.
//
// A run lives under <stateDir>/improve/<run-id>/: run.json (the ledger entry:
// stores, revisions, changed files), summary.md (written by the session: one
// line per change with what, why and evidence session IDs) and report.md (the
// published summary). Finish is idempotent and is reachable from several
// places (Stop, SessionEnd, the Gone reconcile and housekeeping), because a
// window can die at any point.
package improverun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/agenthistory"
	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/notify"
)

// EnvRun carries the run ID into the improve window.
const EnvRun = "AGENTCTL_IMPROVE_RUN"

// StaleAfter is how long an unfinished run may go before housekeeping closes
// it out (its window is presumed dead).
const StaleAfter = 2 * time.Hour

// maxRationale caps the summary text copied into a commit body.
const maxRationale = 8 << 10

// Seams for tests.
var (
	stateDir = config.StateDir
	now      = time.Now
	// notifyDesktop shows the desktop notification.
	notifyDesktop = notify.NotifyWithOptions
	// publishPhone publishes the report as an artifact, which serve pushes to
	// the phone (serve.push.artifacts).
	publishPhone = defaultPublish
)

// StoreRec is one history store's part of a run.
type StoreRec struct {
	Name string `json:"name"` // "agents", "config" or a project dir
	// Store is rebuilt from Name on every load, never read from run.json: the
	// file sits in an agent-writable dir and must not choose a git dir.
	Store    agenthistory.Store `json:"-"`
	StartRev string             `json:"start_rev,omitempty"`
	EndRev   string             `json:"end_rev,omitempty"`
}

// Run is the ledger entry of one improve run.
type Run struct {
	ID       string     `json:"id"`
	Agent    string     `json:"agent"`
	Fleet    bool       `json:"fleet,omitempty"`
	Started  time.Time  `json:"started"`
	Finished time.Time  `json:"finished,omitzero"`
	Stores   []StoreRec `json:"stores,omitempty"`
	// Files are the changed files (absolute paths), Revs the end revisions of
	// the stores that changed.
	Files []string `json:"files,omitempty"`
	Revs  []string `json:"revs,omitempty"`
	// Reverts holds one `agentctl agents revert` command per changed file.
	Reverts []string `json:"reverts,omitempty"`
}

// Dir is the run's directory.
func Dir(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
		return "", fmt.Errorf("invalid run id %q", id)
	}
	sd, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(sd, "improve", id), nil
}

// SummaryPath is where the session writes its rationale.
func SummaryPath(id string) (string, error) {
	d, err := Dir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "summary.md"), nil
}

// StartOptions describe a run being launched.
type StartOptions struct {
	ID    string
	Agent string
	Fleet bool
	// ProjectDirs are the work dirs whose project agent stores are included.
	ProjectDirs []string
}

// Start snapshots every history store (initialising them on demand) with the
// reason "improve <id> start" and records the run. A missing git or a failing
// store is logged and never blocks the launch.
func Start(o StartOptions) error {
	dir, err := Dir(o.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	r := Run{ID: o.ID, Agent: o.Agent, Fleet: o.Fleet, Started: now()}
	for _, ns := range stores(o.ProjectDirs) {
		_, err := ns.store.Snapshot("improve "+o.ID+" start", agenthistory.DefaultAuthor)
		switch {
		case errors.Is(err, agenthistory.ErrGitMissing):
			logging.Warn("improve: git not found on PATH; this run is not recorded in the history")
			r.Stores = nil
			return save(dir, r)
		case err != nil:
			logging.Log("improve: start snapshot %s: %v", ns.name, err)
			continue
		}
		r.Stores = append(r.Stores, StoreRec{Name: ns.name, Store: *ns.store, StartRev: ns.store.Head()})
	}
	return save(dir, r)
}

type namedStore struct {
	name  string
	store *agenthistory.Store
}

func stores(projectDirs []string) []namedStore {
	var out []namedStore
	if s, err := agenthistory.HomeStore(); err == nil {
		out = append(out, namedStore{agenthistory.NameHome, s})
	} else {
		logging.Log("improve: %v", err)
	}
	if s, err := agenthistory.ConfigStore(); err == nil {
		out = append(out, namedStore{agenthistory.NameConfig, s})
	} else {
		logging.Log("improve: %v", err)
	}
	for _, s := range agenthistory.ProjectStores(projectDirs) {
		out = append(out, namedStore{s.WorkTree, s})
	}
	return out
}

func save(dir string, r Run) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".run-*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name()) //nolint:errcheck
		return errors.Join(werr, cerr)
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, "run.json"))
}

// Load reads a run record.
func Load(id string) (Run, error) {
	dir, err := Dir(id)
	if err != nil {
		return Run{}, err
	}
	return load(dir)
}

func load(dir string) (Run, error) {
	data, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return Run{}, err
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil {
		return Run{}, fmt.Errorf("parsing %s: %w", dir, err)
	}
	return r, nil
}

func lockRun(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close() //nolint:errcheck
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
		f.Close()                                   //nolint:errcheck
	}, nil
}

// Finish takes the end snapshot of run id, attributing the session's rationale
// to it, records the changes and, when there are any, sends the summary
// notifications. It is idempotent: the first call does the work, later ones
// return at once. An unknown run is not an error (nothing to do).
func Finish(id string) error {
	if id == "" {
		return nil
	}
	dir, err := Dir(id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "run.json")); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	unlock, err := lockRun(dir)
	if err != nil {
		return fmt.Errorf("locking run %s: %w", id, err)
	}
	defer unlock()
	r, err := load(dir)
	if err != nil {
		return err
	}
	if !r.Finished.IsZero() {
		return nil
	}
	rationale := readRationale(dir)
	for i := range r.Stores {
		rec := &r.Stores[i]
		st, err := agenthistory.StoreByName(rec.Name)
		if err != nil {
			logging.Log("improve: store %q: %v", rec.Name, err)
			continue
		}
		rec.Store = *st
		if _, err := st.SnapshotBody("improve "+id, rationale, agenthistory.DefaultAuthor); err != nil {
			logging.Log("improve: end snapshot %s: %v", rec.Name, err)
		}
		rec.EndRev = st.Head()
		if rec.StartRev == "" || rec.EndRev == "" || rec.StartRev == rec.EndRev {
			continue
		}
		files, err := st.ChangedFiles(rec.StartRev, rec.EndRev)
		if err != nil {
			logging.Log("improve: listing changes %s: %v", rec.Name, err)
			continue
		}
		r.Revs = append(r.Revs, rec.EndRev)
		for _, f := range files {
			r.Files = append(r.Files, filepath.Join(st.WorkTree, f))
			r.Reverts = append(r.Reverts, revertCommand(rec, f))
		}
	}
	r.Finished = now()
	if err := save(dir, r); err != nil {
		return err
	}
	if len(r.Files) == 0 {
		logging.Log("improve: run %s finished with no changes", id)
		return nil
	}
	announce(dir, r, rationale)
	return nil
}

func readRationale(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "summary.md"))
	if err != nil {
		return ""
	}
	if len(data) > maxRationale {
		data = append(data[:maxRationale:maxRationale], []byte("\n[truncated]")...)
	}
	return strings.TrimSpace(string(data))
}

// revertCommand is the command that undoes file (relative to the store's work
// tree) back to its state at the start of the run.
func revertCommand(rec *StoreRec, file string) string {
	cmd := "agentctl agents revert"
	switch rec.Name {
	case agenthistory.NameHome:
	default:
		cmd += " --store " + shellWord(rec.Name)
	}
	return fmt.Sprintf("%s %s %s", cmd, shellWord(file), rec.StartRev[:min(8, len(rec.StartRev))])
}

func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/-+=:@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// HasSummary reports whether the session has written its rationale: the
// signal that it considers the run done.
func HasSummary(id string) bool {
	p, err := SummaryPath(id)
	if err != nil {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Size() > 0
}

// Title is the notification title.
func (r Run) Title() string {
	n := len(r.Files)
	noun := "changes"
	if n == 1 {
		noun = "change"
	}
	if r.Fleet {
		return fmt.Sprintf("Fleet improve: %d %s", n, noun)
	}
	return fmt.Sprintf("Improve %s: %d %s", r.Agent, n, noun)
}

// announce sends one desktop notification and publishes one report for the
// phone. Failures are logged only.
func announce(dir string, r Run, rationale string) {
	report := renderReport(r, rationale)
	path := filepath.Join(dir, "report.md")
	if err := os.WriteFile(path, []byte(report), 0o600); err != nil {
		logging.Log("improve: writing report: %v", err)
	} else if err := publishPhone(path, r); err != nil {
		logging.Log("improve: publishing report: %v", err)
	}
	notifyDesktop(notify.Options{
		Title:   r.Title(),
		Message: notificationBody(r),
		Env:     map[string]string{"AGENTCTL_NOTIFICATION_TYPE": "improve_summary", "AGENTCTL_AGENT_NAME": r.Agent},
	})
}

func notificationBody(r Run) string {
	const maxListed = 5
	var b strings.Builder
	for i, f := range r.Files {
		if i == maxListed {
			fmt.Fprintf(&b, "…and %d more\n", len(r.Files)-maxListed)
			break
		}
		b.WriteString(displayPath(f) + "\n")
	}
	if len(r.Reverts) > 0 {
		b.WriteString("Undo: " + r.Reverts[0] + "\n")
	}
	b.WriteString("All runs: agentctl improve runs")
	return b.String()
}

func displayPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

func renderReport(r Run, rationale string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nRun `%s`, started %s.\n\n", r.Title(), r.ID, r.Started.Format("2006-01-02 15:04"))
	if rationale != "" {
		b.WriteString("## Rationale\n\n" + rationale + "\n\n")
	}
	b.WriteString("## Changed files\n\n")
	for i, f := range r.Files {
		fmt.Fprintf(&b, "- `%s`\n  undo: `%s`\n", displayPath(f), r.Reverts[i])
	}
	b.WriteString("\nHistory revisions: ")
	for i, rev := range r.Revs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("`" + rev[:min(8, len(rev))] + "`")
	}
	b.WriteString("\n\nAll runs: `agentctl improve runs`\n")
	return b.String()
}

func defaultPublish(path string, r Run) error {
	_, err := artifacts.New().Add(artifacts.AddOptions{
		Source: artifacts.Source{Path: path},
		Title:  r.Title(),
		Kind:   artifacts.KindMarkdown,
		Origin: artifacts.Origin{Agent: r.Agent},
	})
	return err
}

// List returns the recorded runs started at or after since, newest first.
func List(since time.Time) ([]Run, error) {
	sd, err := stateDir()
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(filepath.Join(sd, "improve"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var runs []Run
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		r, err := load(filepath.Join(sd, "improve", e.Name()))
		if err != nil || r.Started.Before(since) {
			continue
		}
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Started.After(runs[j].Started) })
	return runs, nil
}

// Active reports whether a run started less than StaleAfter ago has not
// finished. Housekeeping leaves the history stores alone meanwhile, so the
// end snapshot (and its rationale) owns the run's edits.
func Active() bool {
	runs, err := List(now().Add(-StaleAfter))
	if err != nil {
		return false
	}
	for _, r := range runs {
		if r.Finished.IsZero() {
			return true
		}
	}
	return false
}

// FinishStale finishes every run older than StaleAfter that never finished
// (its window died without a hook reaching Finish and the run ID was lost).
func FinishStale() {
	runs, err := List(time.Time{})
	if err != nil {
		return
	}
	for _, r := range runs {
		if r.Finished.IsZero() && now().Sub(r.Started) >= StaleAfter {
			if err := Finish(r.ID); err != nil {
				logging.Log("improve: finishing stale run %s: %v", r.ID, err)
			}
		}
	}
}
