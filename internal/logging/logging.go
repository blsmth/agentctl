// Package logging provides structured, timestamped file logging for agentctl.
//
// Design:
//   - Every log line includes a wall-clock timestamp and the caller's file:line,
//     so you can pinpoint exactly where a message came from without reading code.
//   - The startup banner captures the binary path, full args, cwd, and all key
//     env vars so a log file alone is enough to debug startup failures (SIGKILL,
//     wrong binary, missing env, etc.).
//   - Init is optional: the first call to Log/Debug auto-initialises with
//     default paths. Explicit Init() selects the named log dir from paths.
//   - Debug lines are gated by AGENTCTL_DEBUG=1.
//   - Formatting and output are handled by charmbracelet/log (TextFormatter).
package logging

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	charmlog "github.com/charmbracelet/log"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/safefs"
)

var (
	logger  *charmlog.Logger
	mu      sync.Mutex
	once    sync.Once
	debugOn bool
)

// LogPath returns the path that Init will write to (or already opened).
// Useful for surfacing the log location in diagnostics.
//
// If AGENTCTL_SESSION_ID is set, logs go to .agentctl/logs/sessions/<uuid>.log
// so each agent session gets its own file (easy to display in the HUD).
// Otherwise, all agentctl processes share .agentctl/logs/agentctl.log.
func LogPath() string {
	if sid := os.Getenv("AGENTCTL_SESSION_ID"); sid != "" {
		base := paths.LogDir()
		if base == "" {
			return ""
		}
		return filepath.Join(paths.SessionLogDir(), sid+".log")
	}
	return MonitorLogPath()
}

// MonitorLogPath returns the shared log file that the monitor and any
// process without AGENTCTL_SESSION_ID writes to. Unlike LogPath, it does
// not depend on the calling process's environment, so diagnostics can
// report the monitor log regardless of where they are invoked from.
func MonitorLogPath() string {
	base := paths.LogDir()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "agentctl.log")
}

// Init opens (or creates) the log file.
//
// Safe to call multiple times; only the first call takes effect.
// If Init is never called, the first Log/Debug call auto-initialises.
// paths.Init must be called before Init so that LogPath resolves correctly.
func Init() {
	once.Do(func() { initInternal() })
}

func initInternal() {
	debugOn = os.Getenv("AGENTCTL_DEBUG") == "1"

	path := LogPath()
	if path == "" {
		fmt.Fprintf(os.Stderr, "agentctl: logging.Init: could not resolve home dir\n")
		return
	}

	// The log carries prompt fragments, issue titles, and command lines, so
	// the directory and file are private to the user.
	//
	// The log dir is inside the runtime dir in the user's repository, which
	// a cloned repo can pre-populate: refuse a symlink anywhere from the
	// runtime dir down rather than logging into wherever it points.
	root := logRoot(path)
	if err := safefs.MkdirAll(root, filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "agentctl: logging.Init: mkdir %s: %v\n", filepath.Dir(path), err)
		return
	}
	_ = os.Chmod(filepath.Dir(path), 0o700) // tighten a dir created by an older version

	// lumberjack defers file creation to its first Write call, so probe the
	// path up front with the same open flags we used to use directly. This
	// preserves the early-failure diagnostic (permission denied, read-only
	// filesystem, etc.) instead of silently losing log output on first write.
	if err := probeWritable(root, path); err != nil {
		fmt.Fprintf(os.Stderr, "agentctl: logging.Init: open %s: %v\n", path, err)
		return
	}

	// Rotating writer, capping each log file's on-disk footprint.
	//
	// NOTE: multiple agentctl processes (monitor, chrome bar panes, CLI
	// commands) all append to the same agentctl.log concurrently, and
	// lumberjack is not multi-process safe. A concurrent rotation from two
	// processes can interleave oddly or drop a handful of lines. That's an
	// accepted tradeoff for a best-effort diagnostics log — do not add file
	// locking, a log daemon, or per-PID files to work around it.
	w := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    50, // megabytes
		MaxBackups: 3,
		MaxAge:     int(MaxSessionLogAge / (24 * time.Hour)), // days
		Compress:   true,
	}

	mu.Lock()
	logger = newLogger(&guardedWriter{root: root, path: path, w: w})
	mu.Unlock()

	writeStartupBanner()
}

// guardedWriter drops writes once the log path stops being safe. lumberjack
// (re)opens the file by name -- on first write and on every rotation -- and
// follows symlinks when it does, and a long-lived process (the monitor) can
// outlive a `git pull` that replaces the repo-controlled logs dir with a
// committed symlink. Checking the chain before each write costs a few Lstat
// calls; logging is best-effort, so an unsafe path loses lines rather than
// redirecting them.
type guardedWriter struct {
	root, path string
	w          io.Writer
}

func (g *guardedWriter) Write(p []byte) (int, error) {
	if safefs.CheckDir(g.root, filepath.Dir(g.path)) != nil || safefs.IsSymlink(g.path) {
		return len(p), nil
	}
	return g.w.Write(p)
}

// probeWritable verifies path can be opened for append-writing. lumberjack
// defers actual file creation to its first Write call, so we do this upfront
// probe to preserve the same early-failure diagnostics os.OpenFile used to
// give us directly.
//
// The file is created 0600, and an existing file is tightened to 0600:
// lumberjack copies the current file's mode onto every rotated file, so this
// is what keeps the whole log set private.
//
// The open uses O_NOFOLLOW after checking that no directory from root down
// is a symlink (safefs.OpenNoFollow), and the chmod goes through the opened
// descriptor, so a symlinked log file is refused before anything is created,
// opened, or chmodded through it. A refusal also keeps lumberjack from ever
// opening the path.
func probeWritable(root, path string) error {
	f, err := safefs.OpenNoFollow(root, path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_ = f.Chmod(0o600)
	return f.Close()
}

// logRoot returns the directory from which no component of the log path may
// be a symlink: the runtime dir when path lies beneath it, else path's own
// directory.
func logRoot(path string) string {
	if rt := paths.RuntimeDir(); rt != "" {
		if rel, err := filepath.Rel(rt, path); err == nil && !strings.HasPrefix(rel, "..") {
			return rt
		}
	}
	return filepath.Dir(path)
}

// newLogger creates a charmbracelet/log Logger that writes to w.
// CallerOffset(1) skips our thin wrapper so every log line shows the
// actual call site (e.g. "monitor.go:42") rather than logging.go.
// The logger is configured at DebugLevel so all levels flow through;
// debug gating is enforced by the debugOn flag in our wrapper.
func newLogger(w io.Writer) *charmlog.Logger {
	return charmlog.NewWithOptions(w, charmlog.Options{
		ReportTimestamp: true,
		ReportCaller:    true,
		CallerOffset:    1,
		TimeFormat:      "2006/01/02 15:04:05.000",
		Level:           charmlog.DebugLevel,
	})
}

// ensureInit is called by Log/Debug so callers don't have to call Init
// explicitly. It is a no-op if Init was already called.
func ensureInit() {
	once.Do(func() { initInternal() })
}

// fastPath marks a short-lived, high-frequency process (hook handler or
// `agentctl statusline`, which runs every second). See SetFastPath.
var fastPath bool

// SetFastPath marks this process as a fast-path invocation. Call it before
// Init. Fast-path processes emit their startup banner at DEBUG (so only with
// AGENTCTL_DEBUG=1) instead of INFO; otherwise every statusline refresh and
// every hook would add a banner to the session log.
func SetFastPath(v bool) { fastPath = v }

// writeStartupBanner emits a single structured header record to the log file:
// pid, binary, args, cwd, plus any set env vars that matter for debugging.
// INFO for interactive/long-lived commands; DEBUG (gated by AGENTCTL_DEBUG=1)
// for fast-path invocations. Called from initInternal.
func writeStartupBanner() {
	mu.Lock()
	l := logger
	mu.Unlock()
	if l == nil {
		return
	}
	if fastPath && !debugOn {
		return
	}

	exe, _ := os.Executable()
	cwd, _ := os.Getwd()

	fields := []any{
		"pid", os.Getpid(),
		"binary", exe,
		"args", strings.Join(os.Args, " "),
		"cwd", cwd,
	}
	for _, k := range []string{
		"AGENTCTL",
		"AGENTCTL_AGENT_NAME",
		"AGENTCTL_INSTANCE_NAME",
		"AGENTCTL_WINDOW_ID",
		"AGENTCTL_WINDOW_INDEX",
		"AGENTCTL_SESSION_ID",
		"TMUX_PANE",
		"CLAUDE_ENV_FILE",
	} {
		if v := os.Getenv(k); v != "" {
			fields = append(fields, k, v)
		}
	}
	if fastPath {
		l.Debug("agentctl startup", fields...)
		return
	}
	l.Info("agentctl startup", fields...)
}

// Log writes an INFO-level timestamped line to the log file.
// The caller's file:line is recorded automatically.
// No-op if the log file could not be opened.
func Log(format string, args ...any) {
	ensureInit()
	mu.Lock()
	l := logger
	mu.Unlock()
	if l == nil {
		return
	}
	l.Infof(format, args...)
}

// Debug writes a DEBUG-level line. It is a no-op unless AGENTCTL_DEBUG=1.
// Use for high-frequency or verbose entries that would clutter normal logs.
func Debug(format string, args ...any) {
	if !debugOn {
		return
	}
	ensureInit()
	mu.Lock()
	l := logger
	mu.Unlock()
	if l == nil {
		return
	}
	l.Debugf(format, args...)
}

// Warn writes a WARNING-level line. Always emitted (not gated like Debug).
// The caller's file:line is recorded automatically.
func Warn(format string, args ...any) {
	ensureInit()
	mu.Lock()
	l := logger
	mu.Unlock()
	if l == nil {
		return
	}
	l.Warnf(format, args...)
}

// Error writes an ERROR-level line. Always emitted (not gated like Debug).
// The caller's file:line is recorded automatically.
func Error(format string, args ...any) {
	ensureInit()
	mu.Lock()
	l := logger
	mu.Unlock()
	if l == nil {
		return
	}
	l.Errorf(format, args...)
}

// MaxSessionLogAge is the maximum age a session log file is allowed to reach
// before PruneSessionLogs removes it. Mirrors mailbox.MaxPendingAge: a named
// constant so the value lumberjack already uses for agentctl.log's MaxAge
// isn't duplicated as a bare literal elsewhere.
const MaxSessionLogAge = 14 * 24 * time.Hour

// PruneSessionLogs deletes session log files under paths.SessionLogDir()
// older than maxAge, returning the count removed.
//
// Session logs (.agentctl/logs/sessions/<session-id>.log) are each capped in
// size by lumberjack, but lumberjack's MaxAge/MaxBackups retention is
// per-filename: since every session gets a brand-new filename, lumberjack
// never sees old session log files again to prune them. This function is the
// backstop that actually bounds the directory's total size and file count.
//
// protected is a list of paths that must never be deleted regardless of age
// -- typically the LogPath of every window with a live agent, since a log
// file backing an active session may still be written to well past maxAge
// if that session has been running a long time. Paths are compared after
// filepath.Clean on both sides.
//
// A file is only deleted when all of the following hold:
//   - its name ends in ".log" or ".log.gz" (anything else is ignored)
//   - its ModTime is older than both maxAge and the safetyWindow (a
//     recently-touched file may belong to a process still mid-write)
//   - its absolute path is not in protected
//   - its absolute path is not the current process's own LogPath()
//
// Returns (0, nil) if the session log directory does not exist. Per-file
// removal errors are accumulated and returned (joined) rather than aborting
// the sweep -- one unremovable file must not strand the rest.
func PruneSessionLogs(maxAge time.Duration, protected []string) (int, error) {
	const safetyWindow = time.Hour

	dir := paths.SessionLogDir()
	if dir == "" {
		return 0, nil
	}
	// Never delete through a symlinked logs/ or sessions/ dir: that would
	// remove old *.log files wherever it points.
	if err := safefs.CheckDir(logRoot(dir), dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("logging: prune session logs: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("logging: prune session logs: read dir: %w", err)
	}

	protectedSet := make(map[string]struct{}, len(protected)+1)
	for _, p := range protected {
		if p == "" {
			continue
		}
		protectedSet[filepath.Clean(p)] = struct{}{}
	}
	if own := LogPath(); own != "" {
		protectedSet[filepath.Clean(own)] = struct{}{}
	}

	now := time.Now()
	var removed int
	var errs []error
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".log.gz") {
			continue
		}
		path := filepath.Join(dir, name)
		if _, ok := protectedSet[filepath.Clean(path)]; ok {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			errs = append(errs, fmt.Errorf("stat %s: %w", path, err))
			continue
		}
		age := now.Sub(info.ModTime())
		if age < maxAge || age < safetyWindow {
			continue
		}

		if err := os.Remove(path); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", path, err))
			continue
		}
		removed++
	}

	return removed, errors.Join(errs...)
}

// Timing returns a function that, when called, logs the elapsed time since
// Timing was called. The log line is attributed to the done() call site.
// The duration is appended with a Δ prefix: "applyConfig  Δ42.3ms"
//
//	done := logging.Timing("applyConfig")
//	doWork()
//	done()   // logs: INFO  applyConfig  Δ42.3ms
func Timing(label string) func() {
	ensureInit()
	t0 := time.Now()
	return func() {
		elapsed := time.Since(t0).Round(100 * time.Microsecond)
		mu.Lock()
		l := logger
		mu.Unlock()
		if l == nil {
			return
		}
		l.Infof("%s  Δ%s", label, elapsed)
	}
}
