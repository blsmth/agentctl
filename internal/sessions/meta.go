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
	FirstUserMessage  string    `json:"first_message"`
	FilesEdited       []string  `json:"files_edited"`
	SourcePath        string    `json:"source_path"`
}

// QueryOpts filters the sessions returned by FindMirroredSessions.
type QueryOpts struct {
	ProjectDir string        // filter to this project (and its worktree children)
	Date       time.Time     // filter to this calendar date (local tz); zero = no date filter
	Agent      string        // filter to agent name; empty = all
	MaxAge     time.Duration // max age from now; zero = no age filter
}

// modelPricing holds per-token prices in USD per 1M tokens.
type modelPricing struct {
	Input      float64
	Output     float64
	CacheWrite float64
	CacheRead  float64
}

// pricingTable maps model name substring to pricing. Entries are checked in order;
// put more-specific substrings (e.g. "claude-opus") before broader ones.
var pricingTable = []struct {
	substr  string
	pricing modelPricing
}{
	{"claude-opus", modelPricing{Input: 15.0, Output: 75.0, CacheWrite: 18.75, CacheRead: 1.50}},
	{"claude-sonnet", modelPricing{Input: 3.0, Output: 15.0, CacheWrite: 3.75, CacheRead: 0.30}},
	{"claude-haiku", modelPricing{Input: 0.25, Output: 1.25, CacheWrite: 0.30, CacheRead: 0.03}},
}

var defaultPricing = modelPricing{Input: 3.0, Output: 15.0, CacheWrite: 3.75, CacheRead: 0.30}

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

	tmp := filepath.Join(dir, meta.SessionID+".meta.json.tmp")
	dst := filepath.Join(dir, meta.SessionID+".meta.json")

	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("sessions: write tmp meta: %w", err)
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
	parentProject := project
	if idx := strings.Index(project, "/.worktrees/"); idx >= 0 {
		parentProject = project[:idx]
	} else if idx := strings.Index(project, "-.worktrees-"); idx >= 0 {
		parentProject = project[:idx]
	}

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

// EstimateCost estimates the USD cost of a session based on token usage.
// The pricing table is keyed by model name substring. Unknown models default
// to Sonnet pricing.
func EstimateCost(meta SessionMeta) float64 {
	p := pricingForModel(meta.ModelName)

	// InputTokens is raw (uncached) input only; cache tokens are tracked separately.
	return (float64(meta.InputTokens)*p.Input +
		float64(meta.OutputTokens)*p.Output +
		float64(meta.CacheCreateTokens)*p.CacheWrite +
		float64(meta.CacheReadTokens)*p.CacheRead) / 1_000_000
}

func pricingForModel(model string) modelPricing {
	model = strings.ToLower(model)
	for _, entry := range pricingTable {
		if strings.Contains(model, entry.substr) {
			return entry.pricing
		}
	}
	return defaultPricing
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
