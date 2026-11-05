package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/registry"
)

// Filter narrows the records returned by Query. Zero-value fields (empty
// string, zero time, false, zero int) impose no filter.
type Filter struct {
	Session    string    // exact session ID -- restricts the scan to that one file
	Agent      string    // exact agent name
	Window     string    // exact tmux window ID (e.g. "@5")
	Tool       string    // exact tool name (e.g. "Bash", "Edit")
	Project    string    // exact project basename (see Record.Project)
	Event      string    // exact event name (e.g. audit.EventToolDenied)
	FailedOnly bool      // only records with OK == false
	DeniedOnly bool      // only records with Event == audit.EventToolDenied
	Since      time.Time // only records at or after this time
	Limit      int       // keep at most the Limit most recent matches (0 = unlimited)
}

// Query reads matching records from <runtimeDir>/audit/ -- the current
// project only. When Filter.Session is set, only that session's file is
// read; otherwise every *.jsonl file in the audit directory is scanned.
//
// Lines that fail to parse as JSON, or that parse but lack a "v" (schema
// version) field -- i.e. lines written by the old pre-audit-package writer --
// are skipped rather than causing an error, so a directory mixing old and
// new schema lines still reads cleanly.
//
// Results are sorted by timestamp ascending. If Filter.Limit > 0, only the
// most recent Limit matches are returned.
func Query(f Filter) ([]Record, error) {
	dir := paths.AuditDir()
	if dir == "" {
		return nil, nil
	}

	files, err := auditFiles(dir, f.Session)
	if err != nil {
		return nil, err
	}

	out := collectRecords(files, f)
	return finalizeRecords(out, f), nil
}

// QueryDirs is Query over several project audit directories at once, for
// --all-projects. Unlike Query, a directory that can't be read (permission
// error, etc.) is skipped rather than failing the whole call -- one bad
// project must not blank the rest of the fleet's audit trail.
//
// Results are merged, sorted by timestamp ascending, and Filter.Limit is
// applied across the merged set (not per directory).
func QueryDirs(dirs []string, f Filter) ([]Record, error) {
	var out []Record
	for _, dir := range dirs {
		files, err := auditFiles(dir, f.Session)
		if err != nil {
			continue // best-effort across projects
		}
		out = append(out, collectRecords(files, f)...)
	}
	return finalizeRecords(out, f), nil
}

// collectRecords reads and filters every record across files.
func collectRecords(files []string, f Filter) []Record {
	var out []Record
	for _, path := range files {
		recs, err := readRecordsFile(path)
		if err != nil {
			continue // best-effort: skip unreadable files
		}
		for _, rec := range recs {
			if matches(rec, f) {
				out = append(out, rec)
			}
		}
	}
	return out
}

// finalizeRecords sorts recs by timestamp ascending and applies f.Limit.
func finalizeRecords(recs []Record, f Filter) []Record {
	sort.Slice(recs, func(i, j int) bool { return recs[i].Timestamp < recs[j].Timestamp })
	if f.Limit > 0 && len(recs) > f.Limit {
		recs = recs[len(recs)-f.Limit:]
	}
	return recs
}

// AllProjectAuditDirs enumerates the audit directory of every project known
// to the session registry (~/.local/state/agentctl/registry/*.json, see
// internal/registry), for use with QueryDirs and --all-projects.
//
// Known and documented limitation: this covers REGISTERED sessions only. The
// registry only ever holds entries for projects that have run an agentctl
// session since the registry was introduced, and entries are pruned when
// their session goes away -- so a project dispatched to once and never
// registered, or one whose session has since ended, will not appear here even
// though its audit files still exist on disk. This is deliberate: globbing
// the filesystem for ".agentctl/audit" directories would also rediscover
// stale, no-longer-active umbrella directories (see the audit.Record doc
// comment on Project) as if they were live projects.
//
// The current project's audit dir (paths.AuditDir) is always included, first,
// regardless of registration; the registered dirs follow in sorted order.
//
// A malformed registry entry file is skipped, not fatal (see registry.List).
// A registered project whose audit directory does not exist yet simply
// contributes no records.
func AllProjectAuditDirs() ([]string, error) {
	entries, err := registry.List()
	if err != nil {
		return nil, fmt.Errorf("audit: listing registry: %w", err)
	}

	seen := make(map[string]bool, len(entries)+1)
	var dirs []string
	// Seed with the current project's own audit dir so --all-projects is
	// always a superset of the default query, even when the current project
	// has no live registry entry (entries are pruned when a session dies).
	// It stays first; seen dedupes it if the project is also registered.
	if cur := paths.AuditDir(); cur != "" {
		seen[cur] = true
		dirs = append(dirs, cur)
	}
	var registered []string
	for _, e := range entries {
		if e.RuntimeDir == "" {
			continue
		}
		dir := filepath.Join(e.RuntimeDir, "audit")
		if seen[dir] {
			continue
		}
		seen[dir] = true
		registered = append(registered, dir)
	}
	sort.Strings(registered)
	return append(dirs, registered...), nil
}

// auditFiles returns the .jsonl file paths to scan: just session's file if
// session is non-empty (and it exists), otherwise every *.jsonl file in dir.
func auditFiles(dir, session string) ([]string, error) {
	if session != "" {
		path := filepath.Join(dir, session+".jsonl")
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("audit: stat %s: %w", path, err)
		}
		return []string{path}, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("audit: read dir %s: %w", dir, err)
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	return files, nil
}

// readRecordsFile parses every line of path as a Record, skipping lines that
// are malformed JSON or that lack a valid schema version (old-writer lines).
func readRecordsFile(path string) ([]Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var recs []Record
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // malformed line -- skip rather than error
		}
		if rec.SchemaVersion < 1 {
			continue // no "v" field -- pre-audit-package line, skip
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

// matches reports whether rec satisfies every non-zero field of f.
func matches(rec Record, f Filter) bool {
	if f.Agent != "" && rec.Agent != f.Agent {
		return false
	}
	if f.Window != "" && rec.Window != f.Window {
		return false
	}
	if f.Tool != "" && rec.Tool != f.Tool {
		return false
	}
	if f.Project != "" && rec.Project != f.Project {
		return false
	}
	if f.Event != "" && rec.Event != f.Event {
		return false
	}
	if f.FailedOnly && rec.OK {
		return false
	}
	if f.DeniedOnly && rec.Event != EventToolDenied {
		return false
	}
	if !f.Since.IsZero() {
		ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
		if err != nil || ts.Before(f.Since) {
			return false
		}
	}
	return true
}
