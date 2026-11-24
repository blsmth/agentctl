package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/tokens"
)

// SchemaVersion is the current metadata schema version.
const SchemaVersion = 1

// SessionMeta is the lightweight metadata extracted from a session and written
// as a .meta.json sidecar alongside Claude's JSONL logs. Each file is ~200 bytes
// and allows fast listing without a full JSONL parse.
type SessionMeta struct {
	SchemaVersion     int       `json:"v"`
	SessionID         string    `json:"session_id"`
	Agent             string    `json:"agent"`
	Project           string    `json:"project"`
	ParentProject     string    `json:"parent_project,omitempty"`
	StartTime         time.Time `json:"start_time"`
	EndTime           time.Time `json:"end_time"`
	Turns             int       `json:"turns"`
	InputTokens       int       `json:"input_tokens"`
	OutputTokens      int       `json:"output_tokens"`
	CacheReadTokens   int       `json:"cache_read_tokens"`
	CacheCreateTokens int       `json:"cache_create_tokens"`
	ModelName         string    `json:"model,omitempty"`
	// Backend is the config backend the session ran on ("" = Anthropic).
	Backend          string   `json:"backend,omitempty"`
	FirstUserMessage string   `json:"first_message"`
	FilesEdited      []string `json:"files_edited"`
	SourcePath       string   `json:"source_path"`
}

// QueryOpts filters the sessions returned by FindMirroredSessions.
type QueryOpts struct {
	ProjectDir string        // filter to this project (and its worktree children)
	Date       time.Time     // filter to this calendar date (local tz); zero = no date filter
	Agent      string        // filter to agent name; empty = all
	MaxAge     time.Duration // max age from now; zero = no age filter
}

// overrideSessionDataDir is used by tests to redirect session metadata writes.
var overrideSessionDataDir string

// SetSessionDataDirForTest overrides the session data directory. Only for use in tests.
func SetSessionDataDirForTest(dir string) {
	overrideSessionDataDir = dir
}

func sessionDataDir() string {
	if overrideSessionDataDir != "" {
		return overrideSessionDataDir
	}
	return paths.SessionDataDir()
}

// WriteMeta atomically writes meta to the session data directory using a
// temp-file-then-rename strategy to avoid partial reads from concurrent access.
func WriteMeta(meta SessionMeta) error {
	dir := filepath.Join(sessionDataDir(), encodeDirName(meta.Project))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("sessions: create meta dir %s: %w", dir, err)
	}

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("sessions: marshal meta: %w", err)
	}

	dst := filepath.Join(dir, meta.SessionID+".meta.json")

	// A unique temp file in the same dir so concurrent writers (detached
	// hook syncs, a backfill) never interleave bytes in a shared tmp path.
	f, err := os.CreateTemp(dir, meta.SessionID+".meta.json.*.tmp")
	if err != nil {
		return fmt.Errorf("sessions: create tmp meta: %w", err)
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, 0o644)
	}
	if werr != nil {
		os.Remove(tmp) //nolint:errcheck — best-effort cleanup
		return fmt.Errorf("sessions: write tmp meta: %w", werr)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp) //nolint:errcheck — best-effort cleanup
		return fmt.Errorf("sessions: rename meta: %w", err)
	}
	return nil
}

// ReadMeta reads and unmarshals a single .meta.json file.
func ReadMeta(metaPath string) (SessionMeta, error) {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return SessionMeta{}, fmt.Errorf("sessions: read meta %s: %w", metaPath, err)
	}
	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return SessionMeta{}, fmt.Errorf("sessions: unmarshal meta %s: %w", metaPath, err)
	}
	return meta, nil
}

// ExtractMeta takes a Session, calls ParseSummary(), and maps the result to a
// SessionMeta. The session's LogFile must be set.
func ExtractMeta(s Session) (SessionMeta, error) {
	sum, err := ParseSummary(s)
	if err != nil {
		return SessionMeta{}, fmt.Errorf("sessions: extract meta for %s: %w", s.LogFile, err)
	}

	agent := s.Agent
	if agent == "" {
		agent = "(unknown)"
	}

	// Truncate first user message to 200 chars.
	firstMsg := ""
	if len(sum.UserMsgs) > 0 {
		firstMsg = sum.UserMsgs[0]
		if len(firstMsg) > 200 {
			firstMsg = truncateBytes(firstMsg, 200)
		}
	}

	// Worktree detection: check for /.worktrees/ in real project path (test cases),
	// or -.worktrees- in lossy-decoded path (real usage via findAllSessions).
	project := s.Project
	parentProject := StripWorktree(project)

	return SessionMeta{
		SchemaVersion:     SchemaVersion,
		SessionID:         s.ID,
		Agent:             agent,
		Project:           project,
		ParentProject:     parentProject,
		StartTime:         s.StartTime,
		EndTime:           s.EndTime,
		Turns:             sum.TurnCount,
		InputTokens:       sum.TotalInputTokens,
		OutputTokens:      sum.TotalOutputTokens,
		CacheReadTokens:   sum.CacheReadTokens,
		CacheCreateTokens: sum.CacheCreateTokens,
		ModelName:         sum.ModelName,
		Backend:           RecordedBackend(s.ID),
		FirstUserMessage:  firstMsg,
		FilesEdited:       sum.FilesEdited,
		SourcePath:        s.LogFile,
	}, nil
}

// FindMirroredSessions scans the session data directory and returns session
// metadata matching the given query options, sorted by StartTime ascending.
func FindMirroredSessions(opts QueryOpts) ([]SessionMeta, error) {
	dataDir := sessionDataDir()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("sessions: read session data dir: %w", err)
	}

	var results []SessionMeta
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projDir := filepath.Join(dataDir, entry.Name())
		metas, err := readMetasFromDir(projDir)
		if err != nil {
			continue
		}
		for _, meta := range metas {
			if !matchesOpts(meta, opts) {
				continue
			}
			results = append(results, meta)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].StartTime.Before(results[j].StartTime)
	})
	return results, nil
}

// readMetasFromDir reads all .meta.json files from a single project directory.
func readMetasFromDir(dir string) ([]SessionMeta, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var metas []SessionMeta
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".meta.json") {
			continue
		}
		meta, err := ReadMeta(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // skip malformed files
		}
		metas = append(metas, meta)
	}
	return metas, nil
}

// matchesOpts checks whether a SessionMeta satisfies all non-zero QueryOpts filters.
func matchesOpts(meta SessionMeta, opts QueryOpts) bool {
	if opts.ProjectDir != "" {
		if meta.Project != opts.ProjectDir && meta.ParentProject != opts.ProjectDir {
			return false
		}
	}
	if !opts.Date.IsZero() {
		y, m, d := opts.Date.In(time.Local).Date()
		sy, sm, sd := meta.StartTime.In(time.Local).Date()
		if y != sy || m != sm || d != sd {
			return false
		}
	}
	if opts.Agent != "" && meta.Agent != opts.Agent {
		return false
	}
	if opts.MaxAge > 0 && time.Since(meta.StartTime) > opts.MaxAge {
		return false
	}
	return true
}

// EstimateCost estimates the USD cost of a session from its sidecar totals.
// The sidecar has no per-request data, so this prices the aggregates and
// cannot apply per-request tiers (Haiku 5.5 long-context); prefer
// CostOfSummary when the JSONL is parsed. ok is false when the model has no
// price.
func EstimateCost(meta SessionMeta) (usd float64, ok bool) {
	m := tokens.Lookup(meta.ModelName)
	if !m.HasPricing() {
		return 0, false
	}
	// InputTokens is raw (uncached) input only; cache tokens are tracked separately.
	return m.Pricing.Cost(tokens.Usage{
		Input: meta.InputTokens, Output: meta.OutputTokens,
		CacheRead: meta.CacheReadTokens, CacheCreate: meta.CacheCreateTokens,
	}), true
}

// DiskUsage returns the total bytes and file count in the session data directory.
func DiskUsage() (totalBytes int64, fileCount int, err error) {
	dataDir := sessionDataDir()
	err = filepath.Walk(dataDir, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		if !info.IsDir() {
			totalBytes += info.Size()
			fileCount++
		}
		return nil
	})
	return totalBytes, fileCount, err
}

// Purge removes .meta.json files older than maxAge and removes empty project
// subdirectories. Files modified within the last hour are skipped as a safety
// guard against deleting data another instance is currently writing.
func Purge(maxAge time.Duration) (int, error) {
	dataDir := sessionDataDir()
	now := time.Now()
	const safetyWindow = time.Hour

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("sessions: purge read dir: %w", err)
	}

	var removed int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projDir := filepath.Join(dataDir, entry.Name())
		fileEntries, err := os.ReadDir(projDir)
		if err != nil {
			continue
		}

		for _, fe := range fileEntries {
			if !strings.HasSuffix(fe.Name(), ".meta.json") {
				continue
			}
			path := filepath.Join(projDir, fe.Name())
			info, err := fe.Info()
			if err != nil {
				continue
			}
			// Safety: never purge recently-modified files (another writer may still be active).
			if now.Sub(info.ModTime()) < safetyWindow {
				continue
			}
			meta, err := ReadMeta(path)
			if err != nil {
				continue
			}
			if now.Sub(meta.StartTime) > maxAge {
				if removeErr := os.Remove(path); removeErr == nil {
					removed++
				}
			}
		}

		// Clean up empty project directories.
		remaining, _ := os.ReadDir(projDir)
		if len(remaining) == 0 {
			os.Remove(projDir) //nolint:errcheck
		}
	}

	return removed, nil
}

// StripWorktree cuts a worktree suffix from a project path ("/.worktrees/")
// or a lossy-decoded Claude projects slug ("-.worktrees-", or "--worktrees-" when the dot was also encoded), returning the
// parent project. Paths outside a worktree are returned unchanged.
func StripWorktree(project string) string {
	if idx := strings.Index(project, "/.worktrees/"); idx >= 0 {
		return project[:idx]
	}
	for _, m := range []string{"-.worktrees-", "--worktrees-"} {
		if idx := strings.Index(project, m); idx >= 0 {
			return project[:idx]
		}
	}
	return project
}

// ProjectName returns the short parent-project name for display: worktrees
// fold into their parent and only the base name is kept. cwd (the real path
// from the transcript) is preferred; slug is the fallback.
//
// Limitation of the slug fallback: the slug encodes path separators and
// hyphens identically, so a hyphenated repo name ("local-infra") cannot be
// told from nested directories; the last hyphen-separated segment is used
// ("infra"). That is why cwd comes first.
func ProjectName(cwd, slug string) string {
	if cwd != "" {
		return filepath.Base(StripWorktree(filepath.Clean(cwd)))
	}
	p := strings.TrimRight(StripWorktree(slug), "-/")
	if strings.Contains(p, "/") {
		return filepath.Base(p)
	}
	if i := strings.LastIndex(p, "-"); i >= 0 {
		return p[i+1:]
	}
	return p
}
