// Package watcher provides a log-content matching framework for the monitor.
// It scans Claude Code JSONL session logs incrementally, extracts assistant
// text content blocks, and fires registered callbacks when patterns match.
package watcher

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"sync"

	"github.com/blsmth/agentctl/internal/logging"
)

// scannerBufPool pools 512 KB scanner buffers to reduce per-scan allocations.
// Stores *[]byte so that sync.Pool doesn't copy the slice header on every Put.
var scannerBufPool = sync.Pool{
	New: func() any { b := make([]byte, 0, 512*1024); return &b },
}

// ContentMatch holds context about a matched rule.
type ContentMatch struct {
	Rule        string   // rule name
	Agent       string   // agent base name
	Instance    string   // instance name
	WindowID    string   // tmux window ID
	MatchedText string   // the full text block that matched
	Submatches  []string // regexp submatches (index 0 = full match, 1+ = groups)
	LogPath     string   // which log file produced the match
}

// ContentRule defines a pattern to watch for in agent log output.
type ContentRule struct {
	Name    string                   // rule identifier, e.g. "agent-signal"
	Pattern *regexp.Regexp           // pattern to match against assistant text content blocks
	Action  func(match ContentMatch) // callback fired on each match
}

// ContentWatcher scans JSONL logs incrementally for registered patterns.
// Cursor positions are tracked per log file so content is never re-scanned.
// ContentWatcher is not safe for concurrent use from multiple goroutines.
type ContentWatcher struct {
	rules   []ContentRule
	cursors map[string]int64 // logPath -> last byte offset scanned
}

// New returns an empty ContentWatcher ready for rule registration.
func New() *ContentWatcher {
	return &ContentWatcher{
		cursors: make(map[string]int64),
	}
}

// Register adds a rule to the watcher. Rules are evaluated in registration
// order on every Scan call.
func (w *ContentWatcher) Register(rule ContentRule) {
	w.rules = append(w.rules, rule)
}

// ResetCursor discards the cursor for logPath so the next Scan starts from
// the beginning of the file. Call this whenever the active log file changes
// (e.g. after a /clear or monitor restart discovers a new session file).
func (w *ContentWatcher) ResetCursor(logPath string) {
	delete(w.cursors, logPath)
}

// Scan reads new content from logPath since the last scan, parses JSONL
// entries, extracts assistant text blocks, and fires matching rule callbacks.
// agent, instance, and windowID are metadata passed through to ContentMatch.
// Scan is a no-op when logPath is empty or the file cannot be opened.
func (w *ContentWatcher) Scan(logPath, agent, instance, windowID string) {
	if logPath == "" || len(w.rules) == 0 {
		return
	}

	f, err := os.Open(logPath)
	if err != nil {
		return
	}
	defer f.Close()

	// Seek to the last scanned position so we only read new bytes.
	cursor := w.cursors[logPath]
	if cursor > 0 {
		if _, err := f.Seek(cursor, io.SeekStart); err != nil {
			return
		}
	}

	bufPtr := scannerBufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	defer func() { *bufPtr = buf[:0]; scannerBufPool.Put(bufPtr) }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(buf, 2*1024*1024)

	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}

		entryType, _ := entry["type"].(string)
		if entryType != "assistant" {
			continue
		}

		msg, _ := entry["message"].(map[string]any)
		content, _ := msg["content"].([]any)

		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if blockType, _ := b["type"].(string); blockType != "text" {
				continue
			}
			text, _ := b["text"].(string)
			if text == "" {
				continue
			}

			// Run every registered rule against this text block.
			for _, rule := range w.rules {
				submatches := rule.Pattern.FindStringSubmatch(text)
				if submatches == nil {
					continue
				}
				logging.Log("watcher: rule %q matched in %s (instance=%s)", rule.Name, logPath, instance)
				rule.Action(ContentMatch{
					Rule:        rule.Name,
					Agent:       agent,
					Instance:    instance,
					WindowID:    windowID,
					MatchedText: text,
					Submatches:  submatches,
					LogPath:     logPath,
				})
			}
		}
	}

	// Update cursor to current file position after scanning.
	pos, err := f.Seek(0, io.SeekCurrent)
	if err == nil {
		w.cursors[logPath] = pos
	}
}
