package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/transcript"
)

// The status pane's activity sparkline: tool calls over the last 30 minutes of
// the selected session, hubActivityBucket per cell, drawn on the title row
// left of the project. It is computed in the status pane process, not the
// hub: the hub's tick is shared by every row and must stay a cheap join, while
// the pane only ever looks at one session and already runs on its own clock.
// The transcript tail is read in a tea.Cmd at most every hubActivityRefresh
// per session; the buckets are laid out at render time from the cached
// call times, so the bars shift every two minutes without a re-read.

const (
	// hubActivityCells is the sparkline's width; each cell counts the calls
	// in hubActivityBucket, so the cells span hubActivityWindow.
	hubActivityCells  = 15
	hubActivityBucket = 2 * time.Minute
	hubActivityWindow = hubActivityCells * hubActivityBucket
	// hubActivityRefresh is how long a session's tool-call times are reused
	// before the transcript tail is read again.
	hubActivityRefresh = 10 * time.Second
	// hubActivityTailBytes bounds how much of the transcript is read: half an
	// hour of tool calls fits comfortably; a session busier than that loses
	// its oldest minutes, never the recent ones.
	hubActivityTailBytes = 2 << 20
	// hubActivityLabel follows the bars, dim.
	hubActivityLabel = "30m"
)

// hubSparkLevels are the bar glyphs, lowest first. A cell with no tool call
// is the lowest bar, dim; any call draws at least the second level, so an
// idle cell and a quiet one are told apart.
var hubSparkLevels = []rune("▁▂▃▄▅▆▇█")

// hubActivityEntry is one session's cached tool-call times.
type hubActivityEntry struct {
	at       time.Time   // when calls was read; zero until the first read lands
	calls    []time.Time // tool_use timestamps inside the window at read time
	inflight bool
}

// hubActivityCache holds the tool-call times per session key. It is mutated
// only from the pane's Update, so it needs no lock.
type hubActivityCache struct {
	entries map[string]*hubActivityEntry
}

func newHubActivityCache() *hubActivityCache {
	return &hubActivityCache{entries: map[string]*hubActivityEntry{}}
}

// hubActivityMsg carries a finished transcript read back to Update.
type hubActivityMsg struct {
	key   string
	at    time.Time
	calls []time.Time
}

// hubActivityKey is the cache key of st's session: its session id, else its
// log path; "" when there is no transcript to read.
func hubActivityKey(st *hubStatus) string {
	if st == nil || st.LogPath == "" {
		return ""
	}
	if st.SessionID != "" {
		return st.SessionID
	}
	return st.LogPath
}

// readToolCallTimes is the transcript reader; a seam for tests.
var readToolCallTimes = toolCallTimes

// refresh returns a command reading st's transcript when its cached entry is
// older than hubActivityRefresh and no read is in flight, else nil.
func (c *hubActivityCache) refresh(st *hubStatus, now time.Time) tea.Cmd {
	key := hubActivityKey(st)
	if c == nil || key == "" {
		return nil
	}
	e := c.entries[key]
	if e == nil {
		e = &hubActivityEntry{}
		c.entries[key] = e
	}
	if e.inflight || (!e.at.IsZero() && now.Sub(e.at) < hubActivityRefresh) {
		return nil
	}
	e.inflight = true
	path := st.LogPath
	return func() tea.Msg {
		return hubActivityMsg{key: key, at: now, calls: readToolCallTimes(path, now.Add(-hubActivityWindow))}
	}
}

// store records a finished read.
func (c *hubActivityCache) store(msg hubActivityMsg) {
	if c == nil {
		return
	}
	e := c.entries[msg.key]
	if e == nil {
		e = &hubActivityEntry{}
		c.entries[msg.key] = e
	}
	e.at, e.calls, e.inflight = msg.at, msg.calls, false
}

// calls returns st's cached tool-call times (nil when unread).
func (c *hubActivityCache) calls(st *hubStatus) []time.Time {
	if c == nil {
		return nil
	}
	if e := c.entries[hubActivityKey(st)]; e != nil {
		return e.calls
	}
	return nil
}

// toolCallTimes reads the tail of the transcript at path and returns the
// timestamps of its tool calls at or after since, one per tool_use block.
// Claude Code records are read as they are; other harnesses' records go
// through transcript.Normalize. A missing or unreadable file yields nil.
func toolCallTimes(path string, since time.Time) []time.Time {
	buf, err := readTail(path, hubActivityTailBytes)
	if err != nil {
		return nil
	}
	var out []time.Time
	for _, line := range bytes.Split(buf, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`"tool_use"`)) && !bytes.Contains(line, []byte(`"toolCall"`)) {
			continue
		}
		var rec map[string]any
		if json.Unmarshal(line, &rec) != nil {
			continue // a partial first line, or not JSON
		}
		rec = transcript.Normalize(rec)
		if t, _ := rec["type"].(string); t != "assistant" {
			continue
		}
		ts := recordTime(rec["timestamp"])
		if ts.IsZero() || ts.Before(since) {
			continue
		}
		msg, _ := rec["message"].(map[string]any)
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			if bm, _ := b.(map[string]any); bm["type"] == "tool_use" {
				out = append(out, ts)
			}
		}
	}
	return out
}

// recordTime parses a record timestamp: an RFC 3339 string, or Unix
// milliseconds.
func recordTime(v any) time.Time {
	switch ts := v.(type) {
	case string:
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			return t
		}
	case float64:
		if ts > 0 {
			return time.UnixMilli(int64(ts))
		}
	}
	return time.Time{}
}

// errNotRegular rejects a transcript path that is not a regular file.
var errNotRegular = errors.New("not a regular file")

// readTail returns at most n bytes from the end of the regular file at path.
// The path comes from the selection file, so it is checked before the open:
// opening a FIFO would block the read (and its cache entry) forever.
func readTail(path string, n int64) ([]byte, error) {
	if fi, err := os.Stat(path); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", path, errNotRegular)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", path, errNotRegular)
	}
	off := fi.Size() - n
	if off < 0 {
		off = 0
	}
	buf := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil, err
	}
	return buf, nil
}

// activityBuckets counts calls per hubActivityBucket over the
// hubActivityCells buckets ending with the one now falls in, oldest first.
// Buckets are aligned to even wall-clock minutes (time.Truncate), so a call
// belongs to the two-minute slot it falls in; calls older than the window are
// dropped and calls after now (clock skew) count in the current bucket.
func activityBuckets(calls []time.Time, now time.Time) []int {
	b := make([]int, hubActivityCells)
	cur := now.Truncate(hubActivityBucket)
	for _, t := range calls {
		age := int(cur.Sub(t.Truncate(hubActivityBucket)) / hubActivityBucket)
		if age < 0 {
			age = 0
		}
		if age >= hubActivityCells {
			continue
		}
		b[hubActivityCells-1-age]++
	}
	return b
}

// renderSparkline draws the buckets followed by a dim "30m", with its display
// width; "" when no bucket has a call.
func renderSparkline(buckets []int) (string, int) {
	peak := 0
	for _, c := range buckets {
		peak = max(peak, c)
	}
	if peak == 0 {
		return "", 0
	}
	var sb strings.Builder
	top := len(hubSparkLevels) - 1
	for _, c := range buckets {
		if c == 0 {
			sb.WriteString(fleetDimStyle.Render(string(hubSparkLevels[0])))
			continue
		}
		lvl := (c*top + peak - 1) / peak // 1..top, the peak cell at top
		sb.WriteString(fleetWorkingStyle.Render(string(hubSparkLevels[min(lvl, top)])))
	}
	sb.WriteString(" " + fleetDimStyle.Render(hubActivityLabel))
	return sb.String(), len(buckets) + 1 + len(hubActivityLabel)
}

// hubStatusSpark is st's sparkline for now, or "" with no recent tool call.
func hubStatusSpark(st *hubStatus, now time.Time) (string, int) {
	if len(st.activity) == 0 {
		return "", 0
	}
	return renderSparkline(activityBuckets(st.activity, now))
}

// joinRight is the title line's right-aligned part: the sparkline, then the
// project, two spaces apart when both are present.
func joinRight(spark string, sparkW int, project string, projectW int) (string, int) {
	switch {
	case sparkW > 0 && projectW > 0:
		return spark + "  " + project, sparkW + 2 + projectW
	case sparkW > 0:
		return spark, sparkW
	}
	return project, projectW
}
