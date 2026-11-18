package fleet

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// LogInfo is what the hub reads out of a Claude Code session JSONL: the
// newest recap, AI title and PR link. Every field is optional.
type LogInfo struct {
	Recap    string // newest system/away_summary content, recap suffix stripped
	AITitle  string // newest ai-title record's aiTitle
	PRNumber int    // newest pr-link record's prNumber (0 = none)
	PRURL    string // newest pr-link record's prUrl
}

// Byte budgets for reading a log. The hub polls every second, so a log is
// never parsed whole: only its tail (and, when the tail has no ai-title,
// its head) is scanned.
const (
	logTailBytes = 512 << 10
	logHeadBytes = 64 << 10
)

// recapSuffix is the hint Claude Code appends to every away_summary.
const recapSuffix = "(disable recaps in /config)"

// Substrings that cheaply pre-filter lines before any JSON decoding.
var (
	markAwaySummary = []byte(`"away_summary"`)
	markAITitle     = []byte(`"ai-title"`)
	markPRLink      = []byte(`"pr-link"`)
)

// logRecord is the union of the JSONL record fields LogInfo reads.
type logRecord struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	Content  any    `json:"content"`
	AITitle  string `json:"aiTitle"`
	PRNumber int    `json:"prNumber"`
	PRURL    string `json:"prUrl"`
}

// logCacheEntry is one log's cached LogInfo. offset is how far the log was
// consumed (just past the last complete line), so a log that only grew is
// scanned from there rather than re-read.
type logCacheEntry struct {
	size   int64
	mtime  time.Time
	offset int64
	info   LogInfo
}

var (
	logCacheMu sync.Mutex
	logCache   = map[string]logCacheEntry{}
)

// ReadLogInfo returns the LogInfo for the JSONL at path, cached on the file's
// size and mtime. An unchanged log costs one stat; a log that grew is scanned
// only over the appended bytes (newer records override cached ones); anything
// else (shrunk, replaced, first sight) re-reads the tail. A missing or
// unreadable log yields the zero LogInfo.
func ReadLogInfo(path string) LogInfo {
	if path == "" {
		return LogInfo{}
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		forgetLog(path)
		return LogInfo{}
	}
	logCacheMu.Lock()
	prev, ok := logCache[path]
	logCacheMu.Unlock()
	if ok && prev.size == fi.Size() && prev.mtime.Equal(fi.ModTime()) {
		return prev.info
	}

	f, err := os.Open(path)
	if err != nil {
		return LogInfo{}
	}
	defer f.Close() //nolint:errcheck

	var entry logCacheEntry
	if ok && fi.Size() > prev.size && fi.Size()-prev.offset <= logTailBytes {
		entry = scanAppended(f, prev, fi.Size())
	} else {
		entry = scanFresh(f, fi.Size())
	}
	entry.size, entry.mtime = fi.Size(), fi.ModTime()

	logCacheMu.Lock()
	logCache[path] = entry
	logCacheMu.Unlock()
	return entry.info
}

// ScanLogFrom is the incremental, cross-process counterpart of ReadLogInfo for
// callers that persist their own cursor (the statusline is a fresh process per
// push, so the in-process cache never helps). prevOffset and prevSize are the
// values returned by the previous call for the same path (zero on first sight).
//
//   - fresh=true: the log was scanned from its tail (first sight, shrunk, or
//     appended past the tail budget); info is the complete result and replaces
//     whatever the caller held.
//   - fresh=false: only bytes after prevOffset were scanned; info holds just
//     the newer records (empty when nothing was appended) and should be laid
//     over the caller's previous result.
//
// ok=false means the log is missing or unreadable.
func ScanLogFrom(path string, prevOffset, prevSize int64) (offset, size int64, info LogInfo, fresh, ok bool) {
	if path == "" {
		return 0, 0, LogInfo{}, false, false
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return 0, 0, LogInfo{}, false, false
	}
	size = fi.Size()
	if prevSize > 0 && size == prevSize {
		return prevOffset, size, LogInfo{}, false, true
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, LogInfo{}, false, false
	}
	defer f.Close() //nolint:errcheck
	if prevSize > 0 && size > prevSize && size-prevOffset <= logTailBytes {
		e := scanAppended(f, logCacheEntry{offset: prevOffset}, size)
		return e.offset, size, e.info, false, true
	}
	e := scanFresh(f, size)
	return e.offset, size, e.info, true, true
}

// forgetLog drops path from the cache.
func forgetLog(path string) {
	logCacheMu.Lock()
	delete(logCache, path)
	logCacheMu.Unlock()
}

// scanFresh reads the tail of a log of the given size, falling back to the
// head for the AI title when the tail holds none.
func scanFresh(f *os.File, size int64) logCacheEntry {
	start := size - logTailBytes
	if start < 0 {
		start = 0
	}
	buf, err := readAt(f, start, size-start)
	if err != nil {
		return logCacheEntry{}
	}
	// A tail that starts mid-file starts mid-line: drop the partial line.
	if start > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf, start = buf[i+1:], start+int64(i+1)
		} else {
			buf = nil
		}
	}
	consumed := completeLines(buf)
	var info LogInfo
	scanBackwards(buf[:consumed], &info)
	if info.AITitle == "" && start > 0 {
		if head, err := readAt(f, 0, min(logHeadBytes, start)); err == nil {
			info.AITitle = newestAITitle(head[:completeLines(head)])
		}
	}
	return logCacheEntry{offset: start + int64(consumed), info: info}
}

// scanAppended scans only the bytes appended since prev was cached and lays
// any records found over prev's (they are newer).
func scanAppended(f *os.File, prev logCacheEntry, size int64) logCacheEntry {
	buf, err := readAt(f, prev.offset, size-prev.offset)
	if err != nil {
		return logCacheEntry{}
	}
	consumed := completeLines(buf)
	var fresh LogInfo
	scanBackwards(buf[:consumed], &fresh)
	info := prev.info
	if fresh.Recap != "" {
		info.Recap = fresh.Recap
	}
	if fresh.AITitle != "" {
		info.AITitle = fresh.AITitle
	}
	if fresh.PRURL != "" || fresh.PRNumber != 0 {
		info.PRNumber, info.PRURL = fresh.PRNumber, fresh.PRURL
	}
	return logCacheEntry{offset: prev.offset + int64(consumed), info: info}
}

// readAt reads n bytes at off.
func readAt(f *os.File, off, n int64) ([]byte, error) {
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:got], nil
}

// completeLines returns the length of buf up to and including its last
// newline: a trailing line still being written is left for the next scan.
func completeLines(buf []byte) int {
	return bytes.LastIndexByte(buf, '\n') + 1
}

// scanBackwards walks buf's lines newest first, filling each empty field of
// info from the first (newest) record that provides it, and stops once all
// are filled.
func scanBackwards(buf []byte, info *LogInfo) {
	end := len(buf)
	for end > 0 && (info.Recap == "" || info.AITitle == "" || info.PRURL == "") {
		start := bytes.LastIndexByte(buf[:end-1], '\n') + 1
		applyLine(buf[start:end], info)
		end = start
	}
}

// applyLine fills empty fields of info from one JSONL line.
func applyLine(line []byte, info *LogInfo) {
	wantRecap := info.Recap == "" && bytes.Contains(line, markAwaySummary)
	wantTitle := info.AITitle == "" && bytes.Contains(line, markAITitle)
	wantPR := info.PRURL == "" && bytes.Contains(line, markPRLink)
	if !wantRecap && !wantTitle && !wantPR {
		return
	}
	var rec logRecord
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	switch {
	case wantRecap && rec.Type == "system" && rec.Subtype == "away_summary":
		if s, ok := rec.Content.(string); ok {
			info.Recap = cleanRecap(s)
		}
	case wantTitle && rec.Type == "ai-title":
		info.AITitle = flattenWhitespace(rec.AITitle)
	case wantPR && rec.Type == "pr-link" && rec.PRURL != "":
		info.PRNumber, info.PRURL = rec.PRNumber, rec.PRURL
	}
}

// newestAITitle returns the newest ai-title in buf.
func newestAITitle(buf []byte) string {
	var info LogInfo
	info.Recap, info.PRURL = "-", "-" // only the title is wanted
	scanBackwards(buf, &info)
	return info.AITitle
}

// cleanRecap flattens a recap to one line and strips Claude Code's
// "(disable recaps in /config)" hint.
func cleanRecap(s string) string {
	s = flattenWhitespace(s)
	return strings.TrimSpace(strings.TrimSuffix(s, recapSuffix))
}
