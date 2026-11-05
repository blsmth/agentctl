// Package logstream provides a persistent JSONL stream processor with
// subscriber fan-out for incrementally reading Claude Code session logs.
package logstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
)

const (
	// readChunkSize is the bufio.Reader size used to read the log.
	readChunkSize = 64 * 1024
)

// maxLineBytes is the longest JSONL line Tick will parse. Longer lines are
// skipped (and logged) without blocking later entries. A var so tests can
// lower it.
var maxLineBytes = 2 * 1024 * 1024

// bufPool pools 512 KB line buffers to reduce per-tick allocations.
// The pool stores *[]byte (pointer-to-slice) to avoid the SA6002 allocation
// warning: sync.Pool copies the interface value, and storing a non-pointer
// slice header defeats the pooling benefit.
var bufPool = sync.Pool{
	New: func() any { b := make([]byte, 0, 512*1024); return &b },
}

// Entry is a parsed JSONL log entry with pre-extracted common fields.
type Entry struct {
	Raw       map[string]any // full parsed JSON
	Type      string         // "assistant", "user", etc.
	Timestamp time.Time      // parsed from "timestamp" field

	// Pre-extracted for assistant entries (zero for other types):
	Usage   *Usage
	Model   string
	Content []ContentBlock
}

// Usage holds token usage from an assistant message.
type Usage struct {
	InputTokens       int
	OutputTokens      int
	CacheReadTokens   int
	CacheCreateTokens int
}

// ContentBlock represents a single block from an assistant message's content array.
type ContentBlock struct {
	Type      string         // "text", "tool_use", "tool_result"
	Text      string         // for text blocks
	ToolName  string         // for tool_use blocks
	ToolInput map[string]any // for tool_use blocks
}

// Subscriber receives parsed entries from a Stream.
type Subscriber interface {
	OnEntry(entry Entry)
	OnReset()
}

// Stream manages a single log file's persistent reader and subscriber fan-out.
// It opens the file on the first Tick() call (lazy open) and tracks cursor
// position via seek so subsequent ticks only read new data. Call Close() to
// release resources when the agent window is gone.
//
// Stream is not safe for concurrent use from multiple goroutines.
type Stream struct {
	path   string
	offset int64
	subs   []Subscriber
}

// NewStream returns a Stream that will read from path on the first Tick().
func NewStream(path string) *Stream {
	return &Stream{path: path}
}

// Subscribe registers sub to receive all future entries delivered by this Stream.
func (s *Stream) Subscribe(sub Subscriber) {
	s.subs = append(s.subs, sub)
}

// SetPath switches the stream to a new log file (e.g. after /clear).
// It resets the read offset to 0 and calls OnReset() on all subscribers.
func (s *Stream) SetPath(path string) {
	s.path = path
	s.offset = 0
	for _, sub := range s.subs {
		sub.OnReset()
	}
}

// Tick reads any new complete lines from the log file since the last call,
// parses them as JSONL, and delivers Entry values to all subscribers.
// It returns the number of entries delivered.
//
// File-shrink detection: if the file is smaller than the last-known offset
// (indicating /clear in the same path), Tick resets the offset to 0, calls
// OnReset on all subscribers, and reads from the beginning.
//
// Tick is a no-op when the path is empty or the file cannot be opened.
func (s *Stream) Tick() int {
	if s.path == "" {
		return 0
	}

	f, err := os.Open(s.path)
	if err != nil {
		return 0
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0
	}
	size := info.Size()

	// File shrunk — treat as /clear: reset and re-read from start.
	if s.offset > size {
		s.offset = 0
		for _, sub := range s.subs {
			sub.OnReset()
		}
	}

	// Nothing new to read.
	if s.offset == size {
		return 0
	}

	if _, err := f.Seek(s.offset, io.SeekStart); err != nil {
		return 0
	}

	// Read in bounded chunks through a bufio.Reader rather than slurping the
	// whole tail: the first tick on a long-lived session can otherwise pull in
	// hundreds of MB. The offset only advances past lines that were fully
	// consumed (terminated by '\n'), so a partial final line is retried on the
	// next Tick once it is complete.
	r := bufio.NewReaderSize(f, readChunkSize)
	bufPtr := bufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	defer func() { *bufPtr = buf[:0]; bufPool.Put(bufPtr) }()

	count := 0
	for {
		line, consumed, oversized, err := readLine(r, buf[:0], maxLineBytes)
		if cap(line) > cap(buf) {
			buf = line[:0] // keep the grown buffer for pooling
		}
		if err != nil {
			// io.EOF mid-line: partial line, wait for the rest. Any other
			// error: stop without advancing so nothing is silently dropped.
			if err != io.EOF {
				logging.Warn("logstream: reading %s at offset %d: %v", s.path, s.offset, err)
			}
			return count
		}
		s.offset += consumed
		if oversized {
			logging.Warn("logstream: skipping oversized JSONL line (%d bytes > %d) in %s ending at offset %d",
				consumed, maxLineBytes, s.path, s.offset)
			continue
		}
		line = bytes.TrimRight(line, "\r\n")
		// Blank lines carry no entry; skip them (the offset still advanced).
		if len(line) == 0 {
			continue
		}
		entry := parseEntry(line)
		for _, sub := range s.subs {
			sub.OnEntry(entry)
		}
		count++
	}
}

// readLine reads one '\n'-terminated line from r into buf. If the line is
// longer than max bytes it is consumed from r but not buffered, and oversized
// is reported true (line is then empty). consumed is the number of bytes read
// from r. err is io.EOF when the data ended before a newline (a partial line)
// or any other read error; in both cases the caller must not treat the line as
// consumed.
func readLine(r *bufio.Reader, buf []byte, max int) (line []byte, consumed int64, oversized bool, err error) {
	for {
		chunk, rerr := r.ReadSlice('\n')
		consumed += int64(len(chunk))
		if !oversized {
			// Compare content length: the trailing newline does not count against max.
			if len(bytes.TrimRight(chunk, "\n"))+len(buf) > max {
				oversized = true
				buf = buf[:0]
			} else {
				buf = append(buf, chunk...)
			}
		}
		switch rerr {
		case nil:
			return buf, consumed, oversized, nil
		case bufio.ErrBufferFull:
			continue
		default:
			return buf, consumed, oversized, rerr
		}
	}
}

// Close releases all resources held by the Stream. The Stream should not be
// used after Close is called.
func (s *Stream) Close() {
	s.path = ""
	s.offset = 0
}

// parseEntry parses a raw JSONL line into a structured Entry.
func parseEntry(raw []byte) Entry {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return Entry{Raw: m}
	}

	e := Entry{Raw: m}
	e.Type, _ = m["type"].(string)

	if ts, ok := m["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			e.Timestamp = t
		} else if t, err := time.Parse("2006-01-02T15:04:05Z", ts); err == nil {
			e.Timestamp = t
		}
	}

	if e.Type != "assistant" {
		return e
	}

	msg, _ := m["message"].(map[string]any)
	if msg == nil {
		return e
	}

	e.Model, _ = msg["model"].(string)

	if usage, ok := msg["usage"].(map[string]any); ok {
		e.Usage = &Usage{
			InputTokens:       int(JSONFloat(usage, "input_tokens")),
			OutputTokens:      int(JSONFloat(usage, "output_tokens")),
			CacheReadTokens:   int(JSONFloat(usage, "cache_read_input_tokens")),
			CacheCreateTokens: int(JSONFloat(usage, "cache_creation_input_tokens")),
		}
	}

	if content, ok := msg["content"].([]any); ok {
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			cb := ContentBlock{}
			cb.Type, _ = b["type"].(string)
			switch cb.Type {
			case "text":
				cb.Text, _ = b["text"].(string)
			case "tool_use":
				cb.ToolName, _ = b["name"].(string)
				cb.ToolInput, _ = b["input"].(map[string]any)
			}
			e.Content = append(e.Content, cb)
		}
	}

	return e
}

// JSONFloat extracts a float64 from a JSON object by key.
// It is exported so that packages that parse Claude's JSONL format
// (sessions, tui, etc.) can share a single implementation.
func JSONFloat(m map[string]any, key string) float64 {
	v, ok := m[key]
	if !ok {
		return 0
	}
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return f
}
