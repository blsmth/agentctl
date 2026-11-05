package sessions

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/logging"
)

// searchAllTimeMaxAge stands in for "no --since filter": FindRecentSessions
// requires a maxAge, so a filter this large is effectively unlimited without
// needing a second discovery code path for the unfiltered case.
const searchAllTimeMaxAge = 100 * 365 * 24 * time.Hour

// snippetRadius is how many bytes of surrounding text (on each side of a
// match) are kept in a Hit's Text field. It bounds how much of a large
// tool_result blob a single hit carries around.
const snippetRadius = 160

// SearchOptions configures Search.
type SearchOptions struct {
	Query         string        // required
	Role          string        // "user", "assistant", or "any" (default "any")
	Tool          string        // also match tool_use inputs for this tool name
	IncludeOutput bool          // also match tool_result content
	Agent         string        // filter to this agent
	Project       string        // filter to this project dir ("." = cwd); empty = all projects
	Since         time.Duration // 0 = no time filter
	Context       int           // surrounding messages to capture around each hit
	Regex         bool          // treat Query as a regexp instead of a substring
	Limit         int           // max hits returned; <= 0 defaults to 50
}

// Hit is a single search match, attributed back to its session.
type Hit struct {
	SessionID     string
	Agent         string
	Project       string
	Timestamp     time.Time
	Role          string // "user" or "assistant"
	Text          string // a window of text around the match
	MatchStart    int    // byte offset of the match start within Text
	MatchEnd      int    // byte offset of the match end within Text
	LogFile       string
	ContextBefore []string // formatted "role: text" lines preceding the hit
	ContextAfter  []string // formatted "role: text" lines following the hit
}

// record is one piece of searchable text extracted from a JSONL entry.
type record struct {
	role      string // "user" or "assistant"
	source    string // "message", "tool_use", or "tool_result"
	toolName  string // set when source == "tool_use"
	text      string
	timestamp time.Time
}

// ParseSince parses a --since duration flag. It accepts Go duration strings
// ("24h", "30m") plus a "Nd" day suffix ("7d", "30d") that time.ParseDuration
// does not support natively. An empty string means "no filter" (zero
// duration).
func ParseSince(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		numPart := strings.TrimSuffix(s, "d")
		if n, err := strconv.ParseFloat(numPart, 64); err == nil {
			return time.Duration(n * float64(24*time.Hour)), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --since value %q: want a duration like \"24h\", \"7d\", \"30d\"", s)
	}
	return d, nil
}

// Search scans candidate session transcripts concurrently for text matching
// opts.Query, applying role/tool/output/agent/project/since filters, and
// returns the most recent matches up to opts.Limit.
func Search(opts SearchOptions) ([]Hit, error) {
	if strings.TrimSpace(opts.Query) == "" {
		return nil, fmt.Errorf("search query must not be empty")
	}
	if opts.Limit <= 0 {
		opts.Limit = 50
	}
	if opts.Role == "" {
		opts.Role = "any"
	}

	m, err := newMatcher(opts.Query, opts.Regex)
	if err != nil {
		return nil, fmt.Errorf("compiling search pattern: %w", err)
	}

	candidates, err := gatherCandidateSessions(opts)
	if err != nil {
		return nil, fmt.Errorf("finding candidate sessions: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	if workers > len(candidates) {
		workers = len(candidates)
	}

	jobs := make(chan Session)
	results := make(chan []Hit, len(candidates))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				hits, err := searchSession(s, m, opts)
				if err != nil {
					logging.Warn("sessions search: %s: %v", s.LogFile, err)
				}
				if len(hits) > 0 {
					results <- hits
				}
			}
		}()
	}
	go func() {
		for _, s := range candidates {
			jobs <- s
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var all []Hit
	for hits := range results {
		all = append(all, hits...)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.After(all[j].Timestamp)
	})
	if len(all) > opts.Limit {
		all = all[:opts.Limit]
	}
	return all, nil
}

// gatherCandidateSessions resolves the set of sessions to search, applying
// project/agent/since filters before any transcript content is read.
func gatherCandidateSessions(opts SearchOptions) ([]Session, error) {
	var sess []Session
	if opts.Project != "" {
		dir := resolveProjectDir(opts.Project)
		s, err := FindSessionsForProject(dir, 0)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			return nil, err
		}
		sess = s
	} else {
		s, err := FindRecentSessions(0, searchAllTimeMaxAge)
		if err != nil {
			return nil, err
		}
		sess = s
	}
	return filterSessions(sess, opts), nil
}

// resolveProjectDir resolves the --project flag value to an absolute path,
// treating "." as the current working directory.
func resolveProjectDir(p string) string {
	if p == "." {
		if cwd, err := os.Getwd(); err == nil {
			p = cwd
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// filterSessions applies the agent and since filters. Project filtering is
// already handled by gatherCandidateSessions choosing the right discovery
// call.
func filterSessions(sess []Session, opts SearchOptions) []Session {
	var cutoff time.Time
	if opts.Since > 0 {
		cutoff = time.Now().Add(-opts.Since)
	}
	out := make([]Session, 0, len(sess))
	for _, s := range sess {
		if opts.Agent != "" && s.Agent != opts.Agent {
			continue
		}
		if !cutoff.IsZero() {
			last := s.EndTime
			if last.IsZero() {
				last = s.StartTime
			}
			if last.Before(cutoff) {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// searchSession extracts every searchable record from a session's transcript
// and returns a Hit for each eligible record that matches.
func searchSession(s Session, m *matcher, opts SearchOptions) ([]Hit, error) {
	records, err := extractRecords(s.LogFile)
	if err != nil && len(records) == 0 {
		return nil, err
	}

	var hits []Hit
	for i, rec := range records {
		if !eligible(rec, opts) {
			continue
		}
		start, end, ok := m.FindIndex(rec.text)
		if !ok {
			continue
		}
		snippet, relStart, relEnd := windowSnippet(rec.text, start, end, snippetRadius)
		hits = append(hits, Hit{
			SessionID:     s.ID,
			Agent:         s.Agent,
			Project:       s.Project,
			Timestamp:     rec.timestamp,
			Role:          rec.role,
			Text:          snippet,
			MatchStart:    relStart,
			MatchEnd:      relEnd,
			LogFile:       s.LogFile,
			ContextBefore: contextSlice(records, i, -opts.Context),
			ContextAfter:  contextSlice(records, i, opts.Context),
		})
	}
	return hits, err
}

// eligible reports whether a record is in scope for matching under opts,
// applying the role filter uniformly and gating tool_use/tool_result records
// behind their respective flags.
func eligible(rec record, opts SearchOptions) bool {
	if opts.Role != "any" && rec.role != opts.Role {
		return false
	}
	switch rec.source {
	case "message":
		return true
	case "tool_use":
		return opts.Tool != "" && strings.EqualFold(rec.toolName, opts.Tool)
	case "tool_result":
		return opts.IncludeOutput
	default:
		return false
	}
}

// contextSlice returns up to |n| formatted context lines around records[idx].
// n < 0 looks backward, n > 0 looks forward, n == 0 returns nil.
func contextSlice(records []record, idx, n int) []string {
	if n == 0 {
		return nil
	}
	var out []string
	if n < 0 {
		start := idx + n
		if start < 0 {
			start = 0
		}
		for i := start; i < idx; i++ {
			out = append(out, formatContextLine(records[i]))
		}
		return out
	}
	end := idx + n
	if end >= len(records) {
		end = len(records) - 1
	}
	for i := idx + 1; i <= end; i++ {
		out = append(out, formatContextLine(records[i]))
	}
	return out
}

func formatContextLine(r record) string {
	text := r.text
	const maxLen = 200
	if len(text) > maxLen {
		text = text[:maxLen] + "…"
	}
	return r.role + ": " + text
}

// windowSnippet returns a slice of text around [start, end) padded by radius
// bytes on each side, snapped to valid UTF-8 rune boundaries, along with the
// match offsets relative to the returned snippet.
func windowSnippet(text string, start, end, radius int) (snippet string, relStart, relEnd int) {
	winStart := start - radius
	if winStart < 0 {
		winStart = 0
	}
	winStart = snapBackward(text, winStart)

	winEnd := end + radius
	if winEnd > len(text) {
		winEnd = len(text)
	}
	winEnd = snapForward(text, winEnd)

	snippet = text[winStart:winEnd]
	relStart = start - winStart
	relEnd = end - winStart
	return snippet, relStart, relEnd
}

// snapBackward moves i left until it sits on a UTF-8 rune boundary.
func snapBackward(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// snapForward moves i right until it sits on a UTF-8 rune boundary.
func snapForward(s string, i int) int {
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// matcher performs either a case-insensitive substring search or a
// case-insensitive regexp search, depending on how it was constructed.
type matcher struct {
	substr string // lowercased, set when re == nil
	re     *regexp.Regexp
}

func newMatcher(query string, isRegex bool) (*matcher, error) {
	if isRegex {
		re, err := regexp.Compile("(?i)" + query)
		if err != nil {
			return nil, err
		}
		return &matcher{re: re}, nil
	}
	return &matcher{substr: strings.ToLower(query)}, nil
}

// FindIndex returns the byte offsets of the first match in text.
func (m *matcher) FindIndex(text string) (start, end int, ok bool) {
	if m.re != nil {
		loc := m.re.FindStringIndex(text)
		if loc == nil {
			return 0, 0, false
		}
		return loc[0], loc[1], true
	}
	if m.substr == "" {
		return 0, 0, false
	}
	idx := strings.Index(strings.ToLower(text), m.substr)
	if idx < 0 {
		return 0, 0, false
	}
	return idx, idx + len(m.substr), true
}

// extractRecords reads a session transcript and extracts every searchable
// record from it. Unlike ParseSummary and friends, this uses a bufio.Reader
// with ReadString rather than a bufio.Scanner: a Scanner with a bounded
// buffer returns bufio.ErrTooLong and stops scanning for good on a single
// oversized line (e.g. a tool_result carrying a large file), silently
// dropping the rest of the transcript from the search. ReadString has no such
// cap, so an oversized line is read in full and the scan continues.
func extractRecords(path string) ([]record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 64*1024)
	var records []record
	for {
		line, err := reader.ReadString('\n')
		trimmed := strings.TrimRight(line, "\n")
		if trimmed != "" {
			records = append(records, parseLineRecords(trimmed)...)
		}
		if err != nil {
			if err == io.EOF {
				return records, nil
			}
			return records, err
		}
	}
}

// parseLineRecords decodes a single JSONL line into zero or more searchable
// records. Lines that fail to unmarshal are skipped, matching the existing
// convention in ParseSummary.
func parseLineRecords(line string) []record {
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return nil
	}

	entryType, _ := entry["type"].(string)
	ts := parseTimestamp(entry)

	switch entryType {
	case "user":
		return extractUserRecords(entry, ts)
	case "assistant":
		return extractAssistantRecords(entry, ts)
	default:
		return nil
	}
}

// extractUserRecords handles both shapes of message.content: a plain string
// (the common shape for real human prompts) and an array of content blocks
// (text and tool_result). ParseSummary only handles the array shape; this
// deliberately does not repeat that gap.
func extractUserRecords(entry map[string]any, ts time.Time) []record {
	msg, _ := entry["message"].(map[string]any)
	if msg == nil {
		return nil
	}

	switch content := msg["content"].(type) {
	case string:
		if content == "" {
			return nil
		}
		return []record{{role: "user", source: "message", text: content, timestamp: ts}}
	case []any:
		var recs []record
		for _, blockAny := range content {
			block, ok := blockAny.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "text":
				if text, _ := block["text"].(string); text != "" {
					recs = append(recs, record{role: "user", source: "message", text: text, timestamp: ts})
				}
			case "tool_result":
				if text := extractToolResultText(block); text != "" {
					recs = append(recs, record{role: "user", source: "tool_result", text: text, timestamp: ts})
				}
			}
		}
		return recs
	default:
		return nil
	}
}

// extractAssistantRecords handles message.content for assistant entries:
// text blocks, and tool_use blocks (whose full JSON-encoded input becomes
// searchable text, since callers may search for anything inside it, not just
// the file_path/command fields extractToolDetail picks out for display).
func extractAssistantRecords(entry map[string]any, ts time.Time) []record {
	msg, _ := entry["message"].(map[string]any)
	if msg == nil {
		return nil
	}

	switch content := msg["content"].(type) {
	case string:
		if content == "" {
			return nil
		}
		return []record{{role: "assistant", source: "message", text: content, timestamp: ts}}
	case []any:
		var recs []record
		for _, blockAny := range content {
			block, ok := blockAny.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "text":
				if text, _ := block["text"].(string); text != "" {
					recs = append(recs, record{role: "assistant", source: "message", text: text, timestamp: ts})
				}
			case "tool_use":
				toolName, _ := block["name"].(string)
				var inputText string
				if input, ok := block["input"].(map[string]any); ok {
					if b, err := json.Marshal(input); err == nil {
						inputText = string(b)
					}
				}
				recs = append(recs, record{role: "assistant", source: "tool_use", toolName: toolName, text: inputText, timestamp: ts})
			}
		}
		return recs
	default:
		return nil
	}
}

// extractToolResultText pulls the text content out of a tool_result block,
// which may carry its content as a plain string or as an array of blocks
// (usually just {"type":"text","text":"..."}).
func extractToolResultText(block map[string]any) string {
	switch content := block["content"].(type) {
	case string:
		return content
	case []any:
		var parts []string
		for _, blockAny := range content {
			b, ok := blockAny.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "text" {
				if t, _ := b["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}
