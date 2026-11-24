package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/transcript"
)

// claudeDir overrides the Claude Code config dirs for tests; "" means every
// configured account (claudehome.All).
var claudeDir string

// claudeDirs returns every Claude Code config dir to read transcripts from:
// one per configured account (plus the default dir), or the test override.
func claudeDirs() []string {
	if claudeDir != "" {
		return []string{claudeDir}
	}
	var out []string
	for _, a := range claudehome.All() {
		out = append(out, a.Dir)
	}
	return out
}

// ProjectsRoots returns the projects/ dir of every Claude Code config dir
// (one per configured account). Session IDs are unique across them.
func ProjectsRoots() []string {
	dirs := claudeDirs()
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, filepath.Join(d, "projects"))
	}
	return out
}

// Session represents a Claude Code conversation session.
type Session struct {
	ID        string
	Agent     string // agent name from agentSetting field
	Project   string // working directory (decoded from project dir name)
	StartTime time.Time
	EndTime   time.Time
	LogFile   string // path to the JSONL file
	// Harness is the agent program that wrote the session ("" = claude).
	Harness string
}

// Duration returns the session duration.
func (s Session) Duration() time.Duration {
	if s.EndTime.IsZero() {
		return time.Since(s.StartTime)
	}
	return s.EndTime.Sub(s.StartTime)
}

// Summary holds a parsed session summary for display.
type Summary struct {
	Session               Session
	Cwd                   string   // working directory from the first record carrying one
	UserMsgs              []string // user message previews
	TextBlocks            []string // assistant text blocks
	ToolCalls             []ToolCall
	FilesRead             []string
	FilesEdited           []string
	TotalInputTokens      int
	TotalOutputTokens     int
	CacheCreateTokens     int
	CacheReadTokens       int
	TurnCount             int
	ModelName             string       // last model seen (e.g. "claude-sonnet-4-20250514")
	GitBranch             string       // git branch from assistant entries
	LastInputTokens       int          // input_tokens from the last assistant turn (context window fill)
	LastCacheReadTokens   int          // cache_read_input_tokens from the last assistant turn
	LastCacheCreateTokens int          // cache_creation_input_tokens from the last assistant turn
	TurnMetrics           []TurnMetric // per-turn token data, ordered by turn number
}

// TurnMetric holds token usage for a single assistant turn.
type TurnMetric struct {
	Turn         int
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheCreate  int
	Timestamp    time.Time
	Model        string // per-request model; "" when the entry named none (use Summary.ModelName)
}

// ToolBreakdown returns a map of tool name → call count.
func (s *Summary) ToolBreakdown() map[string]int {
	counts := make(map[string]int)
	for _, tc := range s.ToolCalls {
		counts[tc.Name]++
	}
	return counts
}

// ToolCall is a tool invocation from a session.
type ToolCall struct {
	Name      string
	Timestamp time.Time
	Detail    string // e.g. file path, command, etc.
}

// FindSessionsForAgent scans all project directories for sessions that used
// the given agent name. Returns most recent first.
func FindSessionsForAgent(agentName string, limit int) ([]Session, error) {
	logging.Debug("sessions: scanning for agent %q (limit=%d)", agentName, limit)
	all, err := findAllSessions()
	if err != nil {
		logging.Error("sessions: FindSessionsForAgent %q: %v", agentName, err)
		return nil, err
	}

	sessions := all[agentName]
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})

	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}

	logging.Debug("sessions: found %d session(s) for agent %q", len(sessions), agentName)
	return sessions, nil
}

// ImprovePromptHeader is the first line of every `agentctl improve` prompt.
// Improve sessions run under the agent's own name, and no mode marker reaches
// their transcript, so the opening prompt is the only reliable tell.
const ImprovePromptHeader = "You are running a self-improvement review of your own agent definition."

// FleetImprovePromptHeader is the first line of an `agentctl improve --fleet`
// prompt (a review across every agent, run under the chosen agent's name).
const FleetImprovePromptHeader = "You are running a fleet-wide self-improvement review of every agent definition."

// IsImproveSession reports whether the transcript at logPath is an
// `agentctl improve` session, by its first user message.
func IsImproveSession(logPath string) bool {
	if logPath == "" {
		return false
	}
	first := FirstHumanText(logPath)
	return strings.HasPrefix(first, ImprovePromptHeader) || strings.HasPrefix(first, FleetImprovePromptHeader)
}

// ImproveCostPrefix labels an improve session's agent in cost reports, so
// its spend is not counted under the agent it improved.
const ImproveCostPrefix = "improve:"

// FindWorkSessionsForAgent is FindSessionsForAgent without the agent's own
// improve sessions (see IsImproveSession), which would otherwise count as the
// agent's recent work. Returns most recent first.
func FindWorkSessionsForAgent(agentName string, limit int) ([]Session, error) {
	all, err := FindSessionsForAgent(agentName, 0)
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, s := range all {
		if IsImproveSession(s.LogFile) {
			continue
		}
		out = append(out, s)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// FindSessionsByAgent scans all JSONL files once and returns sessions grouped
// by agent name. Each agent's sessions are sorted most-recent-first.
func FindSessionsByAgent() (map[string][]Session, error) {
	result, err := findAllSessions()
	if err != nil {
		return nil, err
	}
	for agent := range result {
		ss := result[agent]
		sort.Slice(ss, func(i, j int) bool {
			return ss[i].StartTime.After(ss[j].StartTime)
		})
	}
	return result, nil
}

// FindRecentSessions returns sessions across all agents, sorted by start time
// (most recent first), filtered by max age and capped at limit.
func FindRecentSessions(limit int, maxAge time.Duration) ([]Session, error) {
	byAgent, err := findAllSessions()
	if err != nil {
		return nil, err
	}

	cutoff := time.Now().Add(-maxAge)
	var all []Session
	for _, ss := range byAgent {
		for _, s := range ss {
			if s.StartTime.After(cutoff) {
				all = append(all, s)
			}
		}
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].StartTime.After(all[j].StartTime)
	})

	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}

	return all, nil
}

// findAllSessions does a single pass over all JSONL files in every account's
// projects dir and buckets by agent.
func findAllSessions() (map[string][]Session, error) {
	result := make(map[string][]Session)
	for _, root := range ProjectsRoots() {
		if err := findSessionsIn(root, result); err != nil {
			return nil, err
		}
	}
	if root := piSessionsRoot(); root != "" {
		findPiSessionsIn(root, result)
	}
	return result, nil
}

// findSessionsIn adds the sessions under one projects dir to result.
func findSessionsIn(projectsDir string, result map[string][]Session) error {
	projectDirs, err := os.ReadDir(projectsDir)
	if err != nil {
		// A missing projects directory is a legitimate state (e.g. a fresh
		// Claude Code install with no sessions recorded yet, or a machine
		// where ~/.claude has never been created). Degrade to "no sessions"
		// rather than treating it as a hard error.
		if os.IsNotExist(err) {
			logging.Debug("sessions: no projects dir at %s, returning empty result", projectsDir)
			return nil
		}
		return fmt.Errorf("reading projects dir: %w", err)
	}

	for _, pd := range projectDirs {
		if !pd.IsDir() {
			continue
		}
		dirPath := filepath.Join(projectsDir, pd.Name())
		projectPath := decodeDirName(pd.Name())
		logging.Debug("sessions: scanning project dir %s", abbreviateHome(projectPath))

		entries, err := os.ReadDir(dirPath)
		if err != nil {
			logging.Debug("sessions: skip dir %s: %v", dirPath, err)
			continue
		}

		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}

			logFile := filepath.Join(dirPath, e.Name())
			agent := sessionAgent(logFile)
			if agent == "" {
				logging.Debug("sessions: no agent match for %s", logFile)
				continue
			}

			sessionID := strings.TrimSuffix(e.Name(), ".jsonl")
			start, end := sessionTimeRange(logFile)

			if start.IsZero() {
				info, err := e.Info()
				if err != nil {
					continue
				}
				start = info.ModTime()
			}

			result[agent] = append(result[agent], Session{
				ID:        sessionID,
				Agent:     agent,
				Project:   projectPath,
				StartTime: start,
				EndTime:   end,
				LogFile:   logFile,
			})
		}
	}

	return nil
}

// FindSessionsForProject returns sessions for a given project directory
// across every account, most recent first. It errors only when no account
// has the project dir.
func FindSessionsForProject(projectDir string, limit int) ([]Session, error) {
	dirName := claude.ProjectSlug(projectDir)
	var sessions []Session
	var firstErr error
	found := false
	for _, root := range ProjectsRoots() {
		dirPath := filepath.Join(root, dirName)
		ss, err := sessionsInProjectDir(dirPath, projectDir)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("reading project dir %s: %w", dirPath, err)
			}
			continue
		}
		found = true
		sessions = append(sessions, ss...)
	}
	if !found && firstErr != nil {
		return nil, firstErr
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})

	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}

	return sessions, nil
}

// sessionsInProjectDir lists the sessions in one project dir.
func sessionsInProjectDir(dirPath, projectDir string) ([]Session, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	var sessions []Session
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}

		sessionID := strings.TrimSuffix(e.Name(), ".jsonl")
		logFile := filepath.Join(dirPath, e.Name())

		info, err := e.Info()
		if err != nil {
			continue
		}

		start, end := sessionTimeRange(logFile)
		if start.IsZero() {
			start = info.ModTime()
		}

		agent := sessionAgent(logFile)

		sessions = append(sessions, Session{
			ID:        sessionID,
			Agent:     agent,
			Project:   projectDir,
			StartTime: start,
			EndTime:   end,
			LogFile:   logFile,
		})
	}
	return sessions, nil
}

// ParseSummary reads a session log and extracts a summary.
func ParseSummary(s Session) (*Summary, error) {
	logging.Debug("sessions: parsing summary for agent=%q project=%s", s.Agent, abbreviateHome(s.Project))
	f, err := os.Open(s.LogFile)
	if err != nil {
		logging.Error("sessions: ParseSummary open %s: %v", s.LogFile, err)
		return nil, err
	}
	defer f.Close()

	sum := &Summary{Session: s}
	filesRead := make(map[string]bool)
	filesEdited := make(map[string]bool)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line

	// Claude Code writes one JSONL line per content block of an API response,
	// each repeating the response's message.id and usage. Usage and turns are
	// counted once per message id; the value is the TurnMetrics index (-1 when
	// the first line carried no usage).
	seenMsg := make(map[string]int)

	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entry = transcript.Normalize(entry)

		if sum.Cwd == "" {
			sum.Cwd, _ = entry["cwd"].(string)
		}

		entryType, _ := entry["type"].(string)

		switch entryType {
		case "user":
			msg, _ := entry["message"].(map[string]any)
			content, _ := msg["content"].([]any)
			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				if b["type"] == "text" {
					text, _ := b["text"].(string)
					if text != "" {
						preview := text
						if len(preview) > 120 {
							preview = truncateBytes(preview, 120) + "…"
						}
						sum.UserMsgs = append(sum.UserMsgs, preview)
					}
				}
			}

		case "assistant":
			msg, _ := entry["message"].(map[string]any)
			content, _ := msg["content"].([]any)
			ts := parseTimestamp(entry)
			msgID, _ := msg["id"].(string)
			metricIdx, dup := -1, false
			if msgID != "" {
				metricIdx, dup = seenMsg[msgID]
			}
			if !dup {
				sum.TurnCount++
			}
			turnModel := ""
			if m, ok := msg["model"].(string); ok && isRealModelID(m) {
				sum.ModelName = m
				turnModel = m
			}
			if branch, ok := entry["gitBranch"].(string); ok && branch != "" && sum.GitBranch == "" {
				sum.GitBranch = branch
			}
			if usage, ok := msg["usage"].(map[string]any); ok {
				tm := TurnMetric{
					Turn:         sum.TurnCount,
					InputTokens:  int(logstream.JSONFloat(usage, "input_tokens")),
					OutputTokens: int(logstream.JSONFloat(usage, "output_tokens")),
					CacheRead:    int(logstream.JSONFloat(usage, "cache_read_input_tokens")),
					CacheCreate:  int(logstream.JSONFloat(usage, "cache_creation_input_tokens")),
					Timestamp:    ts,
					Model:        turnModel,
				}
				switch {
				case dup && metricIdx >= 0:
					// Repeat of a message already counted: keep the larger
					// figure per field (streamed output_tokens only grows).
					cur := &sum.TurnMetrics[metricIdx]
					cur.InputTokens = max(cur.InputTokens, tm.InputTokens)
					cur.OutputTokens = max(cur.OutputTokens, tm.OutputTokens)
					cur.CacheRead = max(cur.CacheRead, tm.CacheRead)
					cur.CacheCreate = max(cur.CacheCreate, tm.CacheCreate)
					if cur.Model == "" {
						cur.Model = tm.Model
					}
				default:
					sum.TurnMetrics = append(sum.TurnMetrics, tm)
					if msgID != "" {
						seenMsg[msgID] = len(sum.TurnMetrics) - 1
					}
				}
			} else if msgID != "" && !dup {
				seenMsg[msgID] = -1
			}

			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}

				switch b["type"] {
				case "text":
					text, _ := b["text"].(string)
					if text != "" {
						preview := text
						if len(preview) > 200 {
							preview = truncateBytes(preview, 200) + "…"
						}
						sum.TextBlocks = append(sum.TextBlocks, preview)
					}

				case "tool_use":
					toolName, _ := b["name"].(string)
					input, _ := b["input"].(map[string]any)
					detail := extractToolDetail(toolName, input)

					sum.ToolCalls = append(sum.ToolCalls, ToolCall{
						Name:      toolName,
						Timestamp: ts,
						Detail:    detail,
					})

					switch toolName {
					case "Read":
						if fp, ok := input["file_path"].(string); ok {
							filesRead[fp] = true
						}
					case "Edit", "Write":
						if fp, ok := input["file_path"].(string); ok {
							filesEdited[fp] = true
						}
					}
				}
			}
		}
	}

	for f := range filesRead {
		sum.FilesRead = append(sum.FilesRead, f)
	}
	sort.Strings(sum.FilesRead)

	for f := range filesEdited {
		sum.FilesEdited = append(sum.FilesEdited, f)
	}
	sort.Strings(sum.FilesEdited)

	// TotalInputTokens tracks raw (uncached) input only; cache tokens are
	// accumulated separately in CacheCreateTokens / CacheReadTokens.
	for _, tm := range sum.TurnMetrics {
		sum.TotalInputTokens += tm.InputTokens
		sum.TotalOutputTokens += tm.OutputTokens
		sum.CacheCreateTokens += tm.CacheCreate
		sum.CacheReadTokens += tm.CacheRead
	}
	// Last* fields reflect the final turn.
	if n := len(sum.TurnMetrics); n > 0 {
		last := sum.TurnMetrics[n-1]
		sum.LastInputTokens = last.InputTokens
		sum.LastCacheReadTokens = last.CacheRead
		sum.LastCacheCreateTokens = last.CacheCreate
	}

	if err := scanner.Err(); err != nil {
		logging.Error("sessions: ParseSummary scan error for %s: %v", s.LogFile, err)
		return sum, err
	}
	logging.Debug("sessions: parsed summary for %q — %d turns, %d tool calls", s.Agent, sum.TurnCount, len(sum.ToolCalls))
	return sum, nil
}

// isRealModelID reports whether m names a model: not empty, not Claude Code's
// "<synthetic>" placeholder, and not a bare family alias ("sonnet") that some
// entries carry instead of a model id.
func isRealModelID(m string) bool {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "", "<synthetic>", "opus", "sonnet", "haiku", "fable":
		return false
	}
	return true
}

// sessionAgent reads the first entries to extract the agentSetting field.
// Claude Code may emit ~20 lines before agentSetting, so we scan up to 30.
func sessionAgent(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for i := 0; i < 30 && scanner.Scan(); i++ {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entry = transcript.Normalize(entry)
		if agent, ok := entry["agentSetting"].(string); ok {
			return agent
		}
	}
	return ""
}

// extractToolDetail pulls the most useful field from a tool call's input.
func extractToolDetail(toolName string, input map[string]any) string {
	switch toolName {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			if len(cmd) > 80 {
				cmd = truncateBytes(cmd, 80) + "…"
			}
			return cmd
		}
		if desc, ok := input["description"].(string); ok {
			return desc
		}
	case "Read":
		if fp, ok := input["file_path"].(string); ok {
			return abbreviateHome(fp)
		}
	case "Write":
		if fp, ok := input["file_path"].(string); ok {
			return abbreviateHome(fp)
		}
	case "Edit":
		if fp, ok := input["file_path"].(string); ok {
			return abbreviateHome(fp)
		}
	case "Glob":
		if p, ok := input["pattern"].(string); ok {
			return p
		}
	case "Grep":
		if p, ok := input["pattern"].(string); ok {
			return p
		}
	case "Agent":
		if desc, ok := input["description"].(string); ok {
			return desc
		}
	case "TaskCreate", "TaskUpdate":
		if subj, ok := input["subject"].(string); ok {
			return subj
		}
	}
	return ""
}

// sessionTimeRange reads the first and last timestamps from a JSONL file.
// It reads the first few lines for the start time and seeks to the end for
// the last timestamp, avoiding a full scan of potentially large files.
func sessionTimeRange(path string) (start, end time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	// Read start timestamp from the first few lines.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for i := 0; i < 20 && scanner.Scan(); i++ {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entry = transcript.Normalize(entry)
		ts := parseTimestamp(entry)
		if !ts.IsZero() {
			start = ts
			break
		}
	}

	// Read end timestamp from the tail of the file.
	end = lastTimestamp(f)
	if end.IsZero() {
		end = start
	}
	return
}

// lastTimestamp reads the last ~64KB of a file and returns the latest timestamp found.
func lastTimestamp(f *os.File) time.Time {
	const tailSize = 64 * 1024

	info, err := f.Stat()
	if err != nil {
		return time.Time{}
	}

	offset := info.Size() - tailSize
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return time.Time{}
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	// If we seeked mid-line, skip the partial first line.
	if offset > 0 {
		scanner.Scan()
	}

	var last time.Time
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entry = transcript.Normalize(entry)
		ts := parseTimestamp(entry)
		if !ts.IsZero() {
			last = ts
		}
	}
	return last
}

func parseTimestamp(entry map[string]any) time.Time {
	if ts, ok := entry["timestamp"].(string); ok {
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err == nil {
			return t
		}
		t, err = time.Parse("2006-01-02T15:04:05Z", ts)
		if err == nil {
			return t
		}
	}
	return time.Time{}
}

// encodeDirName converts a path to Claude's project directory naming.
func encodeDirName(dir string) string {
	return strings.TrimPrefix(claude.ProjectSlug(dir), "-")
}

// decodeDirName converts Claude's project directory name back to a path.
func decodeDirName(name string) string {
	// The encoding replaces / with - and strips the leading /.
	// We can't perfectly reverse this (ambiguous), but for display purposes
	// we restore the leading / and leave the rest as-is.
	return "/" + name
}

// NormalizeProjectName extracts a clean, human-readable project name from a
// session's Project field (which is the output of decodeDirName and therefore
// has the form "/" + encoded-dir-name, e.g. "/Users-alice-src-agentctl").
//
// The encoding replaces every "/" with "-", so we can't fully reverse it,
// but we can extract the last "-"-separated segment as a best-effort basename.
// Worktree paths (which contain "--worktrees-" from the encoding of
// "/.worktrees/") are stripped before the basename is taken, so worktree
// sessions display their parent project name.
//
// Examples:
//
//	"/Users-alice-src-agentctl"                                 → "agentctl"
//	"/Users-alice-src-webapp"                                   → "webapp"
//	"/Users-alice-src-agentctl--worktrees-go-engineer-issue-42" → "agentctl"
func NormalizeProjectName(project string) string {
	// Strip the leading "/" added by decodeDirName.
	encoded := strings.TrimPrefix(project, "/")
	if encoded == "" {
		return project
	}

	// Worktree paths encode "/.worktrees/" as "--worktrees-" (the dot becomes
	// a hyphen, and both surrounding slashes also become hyphens). Strip
	// everything from that marker onwards so we recover the parent project.
	// pi keeps the dot ("-.worktrees-"), so both forms are recognised.
	for _, marker := range []string{"--worktrees-", "-.worktrees-"} {
		if idx := strings.Index(encoded, marker); idx > 0 {
			encoded = encoded[:idx]
			break
		}
	}

	// The last "-"-separated segment is the project basename.
	if i := strings.LastIndex(encoded, "-"); i >= 0 {
		if name := encoded[i+1:]; name != "" {
			return name
		}
	}

	return encoded
}

// SetClaudeDirForTest overrides the base Claude directory. Only for use in tests.
func SetClaudeDirForTest(dir string) {
	claudeDir = dir
}

func abbreviateHome(path string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// FindLatestLog returns the most recently modified JSONL log file for agentName
// under every account's Claude projects directory (see ProjectsRoots).
// notBefore excludes files whose birth time predates notBefore by more than 30s.
// excludePaths is a set of file paths to skip (e.g. already claimed by another agent).
func FindLatestLog(agentName string, notBefore time.Time, excludePaths map[string]bool) string {
	// Improvement sessions use the base agent name as their agentSetting.
	agentName = strings.TrimPrefix(agentName, "improve~")
	var newest latestLog
	for _, root := range ProjectsRoots() {
		newest.scanRoot(root, agentName, notBefore, excludePaths)
	}
	return newest.path
}

// FindLatestLogIn is the testable core of FindLatestLog. It searches for the
// most recently modified JSONL log file for agentName under projectsDir,
// filtering by notBefore and excludePaths.
func FindLatestLogIn(projectsDir, agentName string, notBefore time.Time, excludePaths map[string]bool) string {
	var newest latestLog
	newest.scanRoot(projectsDir, agentName, notBefore, excludePaths)
	return newest.path
}

// FindLatestLogInProject is FindLatestLogIn scoped to a single project
// directory (e.g. ~/.claude/projects/-Users-me-code-foo) rather than every
// project under the projects root. Used for windows with no recorded session
// ID, where scanning other projects would link another project's log.
func FindLatestLogInProject(projectDir, agentName string, notBefore time.Time, excludePaths map[string]bool) string {
	var newest latestLog
	newest.scan(projectDir, agentName, notBefore, excludePaths)
	return newest.path
}

// latestLog accumulates the newest matching log across one or more dirs.
type latestLog struct {
	modTime time.Time
	path    string
}

// scanRoot scans every project dir under one projects root.
func (l *latestLog) scanRoot(projectsDir, agentName string, notBefore time.Time, excludePaths map[string]bool) {
	dirs, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, d := range dirs {
		if d.IsDir() {
			l.scan(filepath.Join(projectsDir, d.Name()), agentName, notBefore, excludePaths)
		}
	}
}

func (l *latestLog) scan(dirPath, agentName string, notBefore time.Time, excludePaths map[string]bool) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > time.Hour {
			continue
		}
		if !info.ModTime().After(l.modTime) {
			continue
		}
		fp := filepath.Join(dirPath, e.Name())
		if excludePaths[fp] {
			continue
		}
		if sessionAgent(fp) != agentName {
			continue
		}
		if !notBefore.IsZero() {
			birth := fileBirthTime(fp)
			if !birth.IsZero() && birth.Before(notBefore.Add(-30*time.Second)) {
				continue
			}
		}
		l.modTime = info.ModTime()
		l.path = fp
	}
}

// FindLogBySessionIDIn returns the JSONL log for sessionID by globbing
// <projectsDir>/*/<sessionID>.jsonl (projectsDir is normally
// ~/.claude/projects). Session UUIDs are unique, so anything other than
// exactly one match is treated as "unknown".
func FindLogBySessionIDIn(projectsDir, sessionID string) string {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\*?[`) {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(projectsDir, "*", sessionID+".jsonl"))
	if err != nil || len(matches) != 1 {
		return ""
	}
	return matches[0]
}

// FindLogBySessionID returns the JSONL log for sessionID, searching every
// account's projects dir (see ProjectsRoots). "" when not found or ambiguous.
func FindLogBySessionID(sessionID string) string {
	found := ""
	for _, root := range ProjectsRoots() {
		if p := FindLogBySessionIDIn(root, sessionID); p != "" {
			if found != "" {
				return ""
			}
			found = p
		}
	}
	return found
}

// ReadStartTime reads the first timestamp from a JSONL log file without a full
// parse — only the first 20 lines are scanned. Returns zero time if not found.
func ReadStartTime(logPath string) time.Time {
	f, err := os.Open(logPath)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 64*1024)
	for i := 0; i < 20; i++ {
		// line is nil for an over-long (skipped) line; that still counts
		// toward the 20-line budget but must not end the scan.
		line, err := readBoundedLine(r, startTimeMaxLine)
		if line != nil {
			var entry map[string]any
			if json.Unmarshal(line, &entry) == nil {
				if ts, ok := entry["timestamp"].(string); ok {
					if t, perr := time.Parse(time.RFC3339Nano, ts); perr == nil {
						return t
					}
					if t, perr := time.Parse("2006-01-02T15:04:05Z", ts); perr == nil {
						return t
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	return time.Time{}
}

// startTimeMaxLine bounds how much of a single line ReadStartTime buffers.
// The first line of a Claude log (e.g. one carrying a large attachment) can be
// far longer than a bufio.Scanner default; lines past the cap are skipped
// rather than ending the scan.
const startTimeMaxLine = 1024 * 1024

// readBoundedLine reads one line from r, returning at most max bytes of it
// (without the trailing newline). A line longer than max is consumed in full
// but reported as nil. err is io.EOF (or another read error) when the input
// ended; a final unterminated line is still returned alongside the error.
func readBoundedLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > max {
				tooLong = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if tooLong {
			return nil, err
		}
		if len(buf) == 0 && err != nil {
			return nil, err
		}
		return bytes.TrimRight(buf, "\r\n"), err
	}
}

// truncateBytes returns s cut to at most max bytes without splitting a UTF-8
// rune.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// ReadFirstUserMessage reads the first user text message from a JSONL log
// without a full parse — scans up to 50 lines and returns the first non-empty
// text block found in a user message, truncated to 200 chars.
func ReadFirstUserMessage(logPath string) string {
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for i := 0; i < 50 && scanner.Scan(); i++ {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entry = transcript.Normalize(entry)
		if t, _ := entry["type"].(string); t != "user" {
			continue
		}
		msg, _ := entry["message"].(map[string]any)
		content, _ := msg["content"].([]any)
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "text" {
				text, _ := b["text"].(string)
				if text != "" {
					if len(text) > 200 {
						text = truncateBytes(text, 200)
					}
					return text
				}
			}
		}
	}
	return ""
}

// FirstHumanText returns the first text a human typed in the session log at
// logPath, or "" when there is none within the first 300 lines. Claude Code
// writes a typed prompt as a plain-string content (not a block list), and the
// log is also full of synthetic user entries (tool results, slash-command
// echoes, system reminders, interrupts) that are not what the person said.
func FirstHumanText(logPath string) string {
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for i := 0; i < 300 && scanner.Scan(); i++ {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entry = transcript.Normalize(entry)
		if t, _ := entry["type"].(string); t != "user" {
			continue
		}
		if meta, _ := entry["isMeta"].(bool); meta {
			continue
		}
		msg, _ := entry["message"].(map[string]any)
		switch c := msg["content"].(type) {
		case string:
			if text, ok := humanText(c); ok {
				return text
			}
		case []any:
			for _, block := range c {
				b, ok := block.(map[string]any)
				if !ok || b["type"] != "text" {
					continue
				}
				s, _ := b["text"].(string)
				if text, ok := humanText(s); ok {
					return text
				}
			}
		}
	}
	return ""
}

// humanText reports whether s looks like text a person typed rather than a
// synthetic wrapper (<system-reminder>, <command-name>, interrupt markers).
func humanText(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "<") || strings.HasPrefix(s, "[Request interrupted") ||
		strings.HasPrefix(s, "Caveat:") {
		return "", false
	}
	return s, true
}
