// Package events is the append-only log of agent state transitions that feeds
// `agentctl metrics serve`.
//
// The writers that decide a transition (winname.Apply for hook- and
// signal-driven State changes, reconcile.Finish for Gone) append one NDJSON
// line to <runtimeDir>/metrics/events-<YYYY-MM-DD>.ndjson. Each line is a
// single O_APPEND write kept under PIPE_BUF, so concurrent hook processes
// never interleave. Nothing is written unless metrics are enabled, and a
// failed write is logged at debug and otherwise ignored: metrics must never
// fail a hook.
package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/safefs"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

const (
	// Dir is the subdirectory of the runtime dir holding event files.
	Dir = "metrics"
	// MaxLine bounds one encoded line (including the newline). POSIX
	// guarantees a single write up to PIPE_BUF (4096 on every platform we
	// support) is not interleaved with other O_APPEND writers.
	MaxLine = 4096
	// maxField bounds any single string field.
	maxField = 256

	filePrefix = "events-"
	fileSuffix = ".ndjson"
	dateLayout = "2006-01-02"
)

// Event is one state transition. From and To use the AgentStatus names
// ("idle", "working", "blocked", "gone").
type Event struct {
	TS       time.Time `json:"ts"`
	Project  string    `json:"project"` // project root (parent of the runtime dir)
	WindowID string    `json:"window"`
	Instance string    `json:"instance,omitempty"`
	Agent    string    `json:"agent"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Tool     string    `json:"tool,omitempty"`
}

// Seams, replaced in tests.
var (
	// Enabled reports whether metrics are enabled in the current config.
	Enabled = func() bool { return config.Load().EffectiveMetricsConfig().Enabled }
	// RuntimeDir returns the runtime dir hook processes write to.
	RuntimeDir = paths.RuntimeDir
	// Now is the clock.
	Now = time.Now
)

// FileName is the event file name for the day containing t.
func FileName(t time.Time) string {
	return filePrefix + t.Format(dateLayout) + fileSuffix
}

// IsEventFile reports whether name is an event file name.
func IsEventFile(name string) bool {
	return strings.HasPrefix(name, filePrefix) && strings.HasSuffix(name, fileSuffix)
}

// DirFor returns the events directory under runtimeDir.
func DirFor(runtimeDir string) string { return filepath.Join(runtimeDir, Dir) }

// encode renders e as one newline-terminated line under MaxLine bytes.
func encode(e Event) ([]byte, error) {
	e.Project = clip(e.Project, maxField)
	e.WindowID = clip(e.WindowID, maxField)
	e.Instance = clip(e.Instance, maxField)
	e.Agent = clip(e.Agent, maxField)
	e.From = clip(e.From, 32)
	e.To = clip(e.To, 32)
	e.Tool = clip(e.Tool, maxField)
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if len(b)+1 > MaxLine {
		// Cannot happen with the field caps (JSON escaping can at most 6x
		// a field), but never emit a line that could interleave.
		e.Project, e.Instance, e.Tool = "", "", ""
		if b, err = json.Marshal(e); err != nil || len(b)+1 > MaxLine {
			return nil, fmt.Errorf("events: line too long")
		}
	}
	return append(b, '\n'), nil
}

// clip truncates s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// Append writes e to the day file under runtimeDir/metrics, unconditionally
// (no enabled check). The file is opened O_APPEND|O_CREATE, mode 0600,
// refusing symlinks, and written with a single write call.
func Append(runtimeDir string, e Event) error {
	if runtimeDir == "" {
		return fmt.Errorf("events: no runtime dir")
	}
	if e.TS.IsZero() {
		e.TS = Now()
	}
	line, err := encode(e)
	if err != nil {
		return err
	}
	path := filepath.Join(DirFor(runtimeDir), FileName(e.TS))
	f, err := safefs.OpenNoFollow(runtimeDir, path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("events: opening %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("events: writing %s: %w", path, err)
	}
	return nil
}

// Emit appends e under runtimeDir when metrics are enabled. It never returns
// an error: failures are logged at debug.
func Emit(runtimeDir string, e Event) {
	defer func() {
		if r := recover(); r != nil {
			logging.Debug("events: recovered: %v", r)
		}
	}()
	if runtimeDir == "" || !Enabled() {
		return
	}
	if e.Project == "" {
		e.Project = filepath.Dir(runtimeDir)
	}
	if err := Append(runtimeDir, e); err != nil {
		logging.Debug("events: %v", err)
	}
}

// Status normalises a windowstate State string to an AgentStatus name; the
// empty string (no hook state yet) counts as idle, as the monitor did.
func Status(s string) string {
	switch s {
	case "working", "blocked", "gone":
		return s
	}
	return "idle"
}

// Identity derives a record's instance and base agent name. ok is false for
// windows that fire no transitions (unidentifiable, shell, improve~).
func Identity(ws windowstate.WindowState) (instance, base string, ok bool) {
	instance = tmux.StripAgentPrefix(ws.Instance)
	if instance == "" {
		instance = tmux.StripAgentPrefix(ws.RenderedName)
	}
	base = ws.AgentName
	if base == "" {
		base = tmux.BaseAgentName(instance)
	}
	if base == "" || instance == "shell" || strings.HasPrefix(instance, "improve~") {
		return instance, base, false
	}
	return instance, base, true
}

// RecordStateChange emits the event for a windowstate State change made by a
// writer: prev is the State before, ws the record after. It does nothing when
// the normalised state did not change.
func RecordStateChange(windowID, prev string, ws windowstate.WindowState) {
	from, to := Status(prev), Status(ws.State)
	if from == to {
		return
	}
	instance, base, ok := Identity(ws)
	if !ok {
		return
	}
	e := Event{WindowID: windowID, Instance: instance, Agent: base, From: from, To: to}
	// A tool is attributed to the transition into working or blocked, but not
	// to the resume from blocked (the same tool call, already counted).
	if to != "idle" && from != "blocked" {
		e.Tool = ws.PendingTool
	}
	Emit(RuntimeDir(), e)
}

// RecordGone emits the Gone event for a claimed record.
func RecordGone(runtimeDir, windowID string, ws windowstate.WindowState) {
	instance, base, ok := Identity(ws)
	if !ok {
		return
	}
	Emit(runtimeDir, Event{WindowID: windowID, Instance: instance, Agent: base, From: Status(ws.State), To: "gone"})
}

// Files lists the event files under runtimeDir, oldest first.
func Files(runtimeDir string) []string {
	ents, err := os.ReadDir(DirFor(runtimeDir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && IsEventFile(e.Name()) {
			out = append(out, filepath.Join(DirFor(runtimeDir), e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// Retention is how long event files are kept.
const Retention = 3 * 24 * time.Hour

// Prune removes event files whose day is older than keep before now.
func Prune(runtimeDir string, now time.Time, keep time.Duration) {
	cutoff := now.Add(-keep).Format(dateLayout)
	for _, p := range Files(runtimeDir) {
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), filePrefix), fileSuffix)
		if day < cutoff {
			os.Remove(p) //nolint:errcheck
		}
	}
}

// Decode parses one NDJSON line.
func Decode(line []byte) (Event, bool) {
	var e Event
	if err := json.Unmarshal(line, &e); err != nil || e.WindowID == "" || e.To == "" {
		return Event{}, false
	}
	return e, true
}

// Tailer follows one runtime dir's event files from the point it was created:
// existing content is skipped (counters reset with the serve process, and
// Prometheus handles that), later appends and new day files are returned by
// Poll. A deleted, truncated or replaced file is handled.
type Tailer struct {
	runtimeDir string
	offsets    map[string]int64
	since      time.Time // events older than this are skipped (zero = none)
}

// NewTailerSince starts a tailer that reads every existing file from the
// beginning but returns only events stamped at or after since. The serve uses
// it for a project that appeared after startup, so the first transitions of a
// fresh session are not lost to discovery latency.
func NewTailerSince(runtimeDir string, since time.Time) *Tailer {
	return &Tailer{runtimeDir: runtimeDir, offsets: make(map[string]int64), since: since}
}

// NewTailer starts a tailer positioned at the end of every existing file.
func NewTailer(runtimeDir string) *Tailer {
	t := &Tailer{runtimeDir: runtimeDir, offsets: make(map[string]int64)}
	for _, p := range Files(runtimeDir) {
		if fi, err := os.Stat(p); err == nil {
			t.offsets[p] = completeEnd(p, fi.Size())
		}
	}
	return t
}

// completeEnd returns size, backed up to the end of the last complete line
// so a half-written line at startup is read once finished.
func completeEnd(path string, size int64) int64 {
	f, err := os.Open(path)
	if err != nil {
		return size
	}
	defer f.Close() //nolint:errcheck
	n := int64(MaxLine)
	if n > size {
		n = size
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, size-n); err != nil && n > 0 {
		return size
	}
	if n == 0 || buf[n-1] == '\n' {
		return size
	}
	if i := strings.LastIndexByte(string(buf), '\n'); i >= 0 {
		return size - n + int64(i) + 1
	}
	return size - n
}

// Poll returns the events appended since the previous call, oldest first.
func (t *Tailer) Poll() []Event {
	var out []Event
	present := make(map[string]bool)
	for _, p := range Files(t.runtimeDir) {
		present[p] = true
		out = append(out, t.readNew(p)...)
	}
	for p := range t.offsets {
		if !present[p] {
			delete(t.offsets, p) // deleted: forget; a recreated file is new
		}
	}
	return out
}

func (t *Tailer) readNew(path string) []Event {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	off, known := t.offsets[path]
	if !known {
		off = 0 // a file created after the tailer started is all new
	}
	if fi.Size() < off {
		off = 0 // truncated or replaced
	}
	if fi.Size() == off {
		t.offsets[path] = off
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.Seek(off, 0); err != nil {
		return nil
	}
	var out []Event
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break // partial last line: leave it for the next poll
		}
		off += int64(len(line))
		if e, ok := Decode(line); ok && !e.TS.Before(t.since) {
			out = append(out, e)
		}
	}
	t.offsets[path] = off
	return out
}
