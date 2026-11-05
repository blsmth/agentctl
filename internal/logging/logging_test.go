package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	charmlog "github.com/charmbracelet/log"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/blsmth/agentctl/internal/paths"
)

// resetState tears down the global state between tests so each test gets a
// clean slate. Only valid in single-goroutine test contexts.
func resetState() {
	mu.Lock()
	defer mu.Unlock()
	logger = nil
	once = sync.Once{}
	debugOn = false
}

// openTempLog sets up a charmbracelet/log Logger writing to a temp file and
// returns the file. The caller is responsible for closing and removing the file.
// It also fires once so that Log/Debug/Timing don't call initInternal and
// overwrite the test's logger.
func openTempLog(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp("", "agentctl-log-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	l := newLogger(f)
	mu.Lock()
	logger = l
	mu.Unlock()
	once.Do(func() {}) // mark as initialised; prevents Log/Debug from calling initInternal
	return f
}

func readLog(t *testing.T, f *os.File) string {
	t.Helper()
	f.Seek(0, 0) //nolint:errcheck
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	return string(buf[:n])
}

// ── startup banner ────────────────────────────────────────────────────────────

func TestStartupBannerContainsKeyFields(t *testing.T) {
	resetState()
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()

	writeStartupBanner()

	banner := readLog(t, f)
	for _, want := range []string{"binary", "args", "cwd", "agentctl"} {
		if !strings.Contains(banner, want) {
			t.Errorf("startup banner missing field %q:\n%s", want, banner)
		}
	}
}

func TestStartupBannerIncludesPid(t *testing.T) {
	resetState()
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()

	writeStartupBanner()

	banner := readLog(t, f)
	if !strings.Contains(banner, "pid=") {
		t.Errorf("startup banner missing pid:\n%s", banner)
	}
}

func TestStartupBannerIncludesEnvVarsWhenSet(t *testing.T) {
	resetState()

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_AGENT_NAME", "test-agent")
	t.Setenv("TMUX_PANE", "%7")

	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()

	writeStartupBanner()

	banner := readLog(t, f)
	for _, want := range []string{"AGENTCTL", "test-agent", "TMUX_PANE", "%7"} {
		if !strings.Contains(banner, want) {
			t.Errorf("startup banner missing env %q:\n%s", want, banner)
		}
	}
}

// ── Log ───────────────────────────────────────────────────────────────────────

func TestLogIncludesCallerFileAndLine(t *testing.T) {
	resetState()
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()

	Log("hello from test") // <-- source line that should appear in log

	line := readLog(t, f)
	if !strings.Contains(line, "logging_test.go:") {
		t.Errorf("log line missing caller info (expected logging_test.go:NNN):\n%s", line)
	}
	if !strings.Contains(line, "hello from test") {
		t.Errorf("log line missing message:\n%s", line)
	}
}

func TestLogIncludesWallClockTimestamp(t *testing.T) {
	resetState()
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()

	Log("timing test")

	line := readLog(t, f)
	// Timestamp format: 2006/01/02 15:04:05.000
	if !strings.Contains(line, "/") || !strings.Contains(line, ":") {
		t.Errorf("log line missing wall-clock timestamp:\n%s", line)
	}
	// Must not contain the old elapsed-time format
	if strings.Contains(line, "[+") {
		t.Errorf("log line should not contain elapsed-time marker [+:\n%s", line)
	}
}

func TestLogIncludesInfoLevel(t *testing.T) {
	resetState()
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()

	Log("info test")

	line := readLog(t, f)
	if !strings.Contains(line, "INFO") {
		t.Errorf("Log output missing INFO level tag:\n%s", line)
	}
}

func TestLogNoopWhenNotInitialised(t *testing.T) {
	resetState()
	// logger is nil and paths is not initialised; Log should not panic.
	Log("this should not panic")
}

// ── Debug ─────────────────────────────────────────────────────────────────────

func TestDebugSuppressedByDefault(t *testing.T) {
	resetState()
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	// debugOn is false after resetState → Debug is a no-op

	Debug("this should not appear")

	if got := readLog(t, f); got != "" {
		t.Errorf("Debug wrote output with debugOn=false: %q", got)
	}
}

func TestDebugVisibleWhenEnabled(t *testing.T) {
	resetState()
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()

	mu.Lock()
	debugOn = true
	logger.SetLevel(charmlog.DebugLevel)
	mu.Unlock()

	Debug("debug message here")

	line := readLog(t, f)
	if !strings.Contains(line, "DEBU") {
		t.Errorf("Debug line missing DEBUG tag:\n%s", line)
	}
	if !strings.Contains(line, "debug message here") {
		t.Errorf("Debug line missing message:\n%s", line)
	}
	if !strings.Contains(line, "logging_test.go:") {
		t.Errorf("Debug line missing caller info:\n%s", line)
	}
}

// ── Warn ──────────────────────────────────────────────────────────────────────

func TestWarnAlwaysEmitted(t *testing.T) {
	resetState()
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	// debugOn is false — Warn must still emit

	Warn("something looks off")

	line := readLog(t, f)
	if !strings.Contains(line, "WARN") {
		t.Errorf("Warn output missing WARN tag:\n%s", line)
	}
	if !strings.Contains(line, "something looks off") {
		t.Errorf("Warn output missing message:\n%s", line)
	}
}

// ── Error ─────────────────────────────────────────────────────────────────────

func TestErrorAlwaysEmitted(t *testing.T) {
	resetState()
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	// debugOn is false — Error must still emit

	Error("something broke")

	line := readLog(t, f)
	if !strings.Contains(line, "ERRO") {
		t.Errorf("Error output missing ERRO tag:\n%s", line)
	}
	if !strings.Contains(line, "something broke") {
		t.Errorf("Error output missing message:\n%s", line)
	}
}

// ── Timing ────────────────────────────────────────────────────────────────────

func TestTimingAttributedToCallSite(t *testing.T) {
	resetState()
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()

	done := Timing("myop")
	done() // this line should appear in log (caller of the closure)

	line := readLog(t, f)
	// Source should point to THIS test file, not logging.go.
	if !strings.Contains(line, "logging_test.go:") {
		t.Errorf("Timing log should point to call site (logging_test.go:NNN), got:\n%s", line)
	}
	if !strings.Contains(line, "myop") {
		t.Errorf("Timing log missing label:\n%s", line)
	}
}

func TestTimingMeasuresElapsed(t *testing.T) {
	resetState()
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()

	done := Timing("slowop")
	time.Sleep(5 * time.Millisecond)
	done()

	line := readLog(t, f)
	// Should contain "ms" since it took at least 5ms.
	if !strings.Contains(line, "ms") {
		t.Errorf("Timing log missing duration (ms): %s", line)
	}
}

// ── newLogger seam ────────────────────────────────────────────────────────────

// TestNewLoggerWritesThroughArbitraryWriter guards the seam that the rotating
// writer plugs into: newLogger must work with any io.Writer, not just *os.File.
func TestNewLoggerWritesThroughArbitraryWriter(t *testing.T) {
	var buf bytes.Buffer
	l := newLogger(&buf)
	l.Info("hello via arbitrary writer")

	out := buf.String()
	if !strings.Contains(out, "hello via arbitrary writer") {
		t.Errorf("newLogger did not write message through custom io.Writer: %q", out)
	}
	if !strings.Contains(out, "INFO") {
		t.Errorf("newLogger output missing INFO level tag: %q", out)
	}
}

// ── rotation ──────────────────────────────────────────────────────────────────

// TestLumberjackRotationTriggers verifies that writing past MaxSize produces
// a backup file and truncates the live file — the behavior initInternal
// relies on to cap agentctl.log's on-disk footprint.
func TestLumberjackRotationTriggers(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agentctl.log")

	w := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    1, // lumberjack's minimum unit is MB; smallest we can set
		MaxBackups: 3,
		Compress:   false,
	}
	defer func() { _ = w.Close() }()

	// MaxSize is in megabytes and lumberjack won't accept fractional/zero
	// values, so write comfortably past 1MB to force a rotation.
	chunk := bytes.Repeat([]byte("x"), 1024*1024) // 1MB
	for i := 0; i < 2; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	// This write should have pushed us over MaxSize and triggered a rotation.
	if _, err := w.Write([]byte("trigger rotation\n")); err != nil {
		t.Fatalf("write trigger: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var sawBackup bool
	for _, e := range entries {
		if e.Name() != "agentctl.log" {
			sawBackup = true
		}
	}
	if !sawBackup {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected a rotated backup file in %s, got: %v", dir, names)
	}

	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("stat live log file: %v", err)
	}
	if info.Size() >= int64(len(chunk)) {
		t.Errorf("live log file was not truncated after rotation, size=%d", info.Size())
	}
}

// ── LogPath ───────────────────────────────────────────────────────────────────

func TestLogPath(t *testing.T) {
	tests := []struct {
		name       string
		sessionID  string
		wantSuffix string
	}{
		{
			name:       "shared log when no session id",
			sessionID:  "",
			wantSuffix: filepath.Join("logs", "agentctl.log"),
		},
		{
			name:       "per-session log when AGENTCTL_SESSION_ID set",
			sessionID:  "abc-123",
			wantSuffix: filepath.Join("logs", "sessions", "abc-123.log"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths.ResetForTest()
			dir := t.TempDir()
			// Agent windows export AGENTCTL_RUNTIME_DIR, which silently
			// overrides paths.Init's baseDir argument if inherited.
			t.Setenv("AGENTCTL_RUNTIME_DIR", "")
			paths.Init(dir)

			if tt.sessionID == "" {
				t.Setenv("AGENTCTL_SESSION_ID", "")
			} else {
				t.Setenv("AGENTCTL_SESSION_ID", tt.sessionID)
			}

			got := LogPath()
			if !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("LogPath() = %q, want suffix %q", got, tt.wantSuffix)
			}
		})
	}
}

func TestLogPathEmptyWhenPathsNotInitialised(t *testing.T) {
	paths.ResetForTest()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")

	if got := LogPath(); got != "" {
		t.Errorf("LogPath() = %q, want empty string when paths.Init was never called", got)
	}
}

// TestLogPathMatchesMonitorLogPathWithoutSessionID asserts LogPath's
// refactor to delegate to MonitorLogPath did not change its behavior: with
// no AGENTCTL_SESSION_ID set, the two must be identical.
func TestLogPathMatchesMonitorLogPathWithoutSessionID(t *testing.T) {
	paths.ResetForTest()
	dir := t.TempDir()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(dir)
	t.Setenv("AGENTCTL_SESSION_ID", "")

	got, want := LogPath(), MonitorLogPath()
	if got != want {
		t.Errorf("LogPath() = %q, want equal to MonitorLogPath() = %q", got, want)
	}
}

// TestLogPathStillReturnsSessionPathWithSessionID asserts LogPath's refactor
// to delegate to MonitorLogPath did not change its behavior: with
// AGENTCTL_SESSION_ID set, LogPath must still return the per-session path,
// distinct from MonitorLogPath.
func TestLogPathStillReturnsSessionPathWithSessionID(t *testing.T) {
	paths.ResetForTest()
	dir := t.TempDir()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(dir)
	t.Setenv("AGENTCTL_SESSION_ID", "session-xyz")

	wantSuffix := filepath.Join("logs", "sessions", "session-xyz.log")
	if got := LogPath(); !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("LogPath() = %q, want suffix %q", got, wantSuffix)
	}
	if got, monitor := LogPath(), MonitorLogPath(); got == monitor {
		t.Errorf("LogPath() = %q, should differ from MonitorLogPath() = %q when AGENTCTL_SESSION_ID is set", got, monitor)
	}
}

// ── MonitorLogPath ────────────────────────────────────────────────────────────

// TestMonitorLogPathIsEnvIndependent is the whole point of MonitorLogPath:
// it must return <logdir>/agentctl.log regardless of AGENTCTL_SESSION_ID,
// unlike LogPath.
func TestMonitorLogPathIsEnvIndependent(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
	}{
		{name: "unset", sessionID: ""},
		{name: "garbage value", sessionID: "not-a-real-session-id-!@#$"},
		{name: "looks like a uuid", sessionID: "b469f867-0000-4000-8000-000000000000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths.ResetForTest()
			dir := t.TempDir()
			t.Setenv("AGENTCTL_RUNTIME_DIR", "")
			paths.Init(dir)
			t.Setenv("AGENTCTL_SESSION_ID", tt.sessionID)

			want := filepath.Join(dir, ".agentctl", "logs", "agentctl.log")
			if got := MonitorLogPath(); got != want {
				t.Errorf("MonitorLogPath() = %q, want %q", got, want)
			}
		})
	}
}

func TestMonitorLogPathEmptyWhenPathsNotInitialised(t *testing.T) {
	paths.ResetForTest()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")

	if got := MonitorLogPath(); got != "" {
		t.Errorf("MonitorLogPath() = %q, want empty string when paths.Init was never called", got)
	}
}

// ── PruneSessionLogs ──────────────────────────────────────────────────────────

// setupSessionLogTest pins paths.Init to a fresh temp dir (guarding against
// an inherited AGENTCTL_RUNTIME_DIR, see TestAuditCmd_PerSessionLogPath in
// internal/hooks for why that env var must always be pinned in tests that
// touch paths), and returns the resulting sessions log dir.
func setupSessionLogTest(t *testing.T) string {
	t.Helper()
	resetState()
	tmpDir := t.TempDir()
	paths.ResetForTest()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	paths.Init(tmpDir)
	t.Cleanup(paths.ResetForTest)

	dir := paths.SessionLogDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session log dir: %v", err)
	}
	return dir
}

// writeAgedFile creates name inside dir and backdates its mtime by age.
func writeAgedFile(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("log line\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	mtime := time.Now().Add(-age)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

func TestPruneSessionLogs(t *testing.T) {
	const maxAge = 14 * 24 * time.Hour

	t.Run("old unprotected file is removed", func(t *testing.T) {
		dir := setupSessionLogTest(t)
		old := writeAgedFile(t, dir, "old-session.log", maxAge+24*time.Hour)

		n, err := PruneSessionLogs(maxAge, nil)
		if err != nil {
			t.Fatalf("PruneSessionLogs: %v", err)
		}
		if n != 1 {
			t.Errorf("removed = %d, want 1", n)
		}
		if _, err := os.Stat(old); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat err = %v", old, err)
		}
	})

	t.Run("old protected file is kept", func(t *testing.T) {
		dir := setupSessionLogTest(t)
		protected := writeAgedFile(t, dir, "protected-session.log", maxAge+30*24*time.Hour)

		n, err := PruneSessionLogs(maxAge, []string{protected})
		if err != nil {
			t.Fatalf("PruneSessionLogs: %v", err)
		}
		if n != 0 {
			t.Errorf("removed = %d, want 0", n)
		}
		if _, err := os.Stat(protected); err != nil {
			t.Errorf("expected protected file to survive, stat err = %v", err)
		}
	})

	t.Run("file newer than maxAge is kept", func(t *testing.T) {
		dir := setupSessionLogTest(t)
		fresh := writeAgedFile(t, dir, "fresh-session.log", 2*time.Hour)

		n, err := PruneSessionLogs(maxAge, nil)
		if err != nil {
			t.Fatalf("PruneSessionLogs: %v", err)
		}
		if n != 0 {
			t.Errorf("removed = %d, want 0", n)
		}
		if _, err := os.Stat(fresh); err != nil {
			t.Errorf("expected fresh file to survive, stat err = %v", err)
		}
	})

	t.Run("file older than maxAge but inside safetyWindow is kept", func(t *testing.T) {
		dir := setupSessionLogTest(t)
		// maxAge is 0 here so only the safetyWindow keeps this file alive.
		recent := writeAgedFile(t, dir, "just-old-enough.log", 30*time.Minute)

		n, err := PruneSessionLogs(0, nil)
		if err != nil {
			t.Fatalf("PruneSessionLogs: %v", err)
		}
		if n != 0 {
			t.Errorf("removed = %d, want 0", n)
		}
		if _, err := os.Stat(recent); err != nil {
			t.Errorf("expected file inside safety window to survive, stat err = %v", err)
		}
	})

	t.Run("non-log file is untouched", func(t *testing.T) {
		dir := setupSessionLogTest(t)
		notes := writeAgedFile(t, dir, "notes.txt", maxAge+24*time.Hour)

		n, err := PruneSessionLogs(maxAge, nil)
		if err != nil {
			t.Fatalf("PruneSessionLogs: %v", err)
		}
		if n != 0 {
			t.Errorf("removed = %d, want 0", n)
		}
		if _, err := os.Stat(notes); err != nil {
			t.Errorf("expected non-.log file to survive, stat err = %v", err)
		}
	})

	t.Run("missing directory returns zero without error", func(t *testing.T) {
		resetState()
		tmpDir := t.TempDir()
		paths.ResetForTest()
		t.Setenv("AGENTCTL_RUNTIME_DIR", "")
		t.Setenv("AGENTCTL_SESSION_ID", "")
		paths.Init(tmpDir)
		t.Cleanup(paths.ResetForTest)
		// Note: setupSessionLogTest normally MkdirAll's the sessions dir; here
		// we deliberately skip that so the directory does not exist.

		n, err := PruneSessionLogs(maxAge, nil)
		if err != nil {
			t.Fatalf("PruneSessionLogs on missing dir: %v", err)
		}
		if n != 0 {
			t.Errorf("removed = %d, want 0", n)
		}
	})

	t.Run("returned count matches files actually removed", func(t *testing.T) {
		dir := setupSessionLogTest(t)
		writeAgedFile(t, dir, "old-1.log", maxAge+24*time.Hour)
		writeAgedFile(t, dir, "old-2.log.gz", maxAge+24*time.Hour)
		protected := writeAgedFile(t, dir, "old-protected.log", maxAge+24*time.Hour)
		writeAgedFile(t, dir, "fresh.log", time.Minute)

		n, err := PruneSessionLogs(maxAge, []string{protected})
		if err != nil {
			t.Fatalf("PruneSessionLogs: %v", err)
		}
		if n != 2 {
			t.Errorf("removed = %d, want 2", n)
		}

		remaining, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		if len(remaining) != 2 {
			names := make([]string, 0, len(remaining))
			for _, e := range remaining {
				names = append(names, e.Name())
			}
			t.Errorf("remaining files = %v, want 2 entries (protected + fresh)", names)
		}
	})
}

// ── fast-path banner ──────────────────────────────────────────────────────────

func TestStartupBannerFastPathNotInfo(t *testing.T) {
	resetState()
	t.Cleanup(func() { SetFastPath(false) })
	SetFastPath(true)
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()

	writeStartupBanner()
	if got := readLog(t, f); got != "" {
		t.Errorf("fast-path banner emitted without AGENTCTL_DEBUG: %q", got)
	}

	mu.Lock()
	debugOn = true
	mu.Unlock()
	writeStartupBanner()
	got := readLog(t, f)
	if strings.Contains(got, "INFO") || !strings.Contains(got, "DEBU") {
		t.Errorf("fast-path banner should be DEBUG only, got:\n%s", got)
	}
}

func TestStartupBannerInteractiveIsInfoSingleRecord(t *testing.T) {
	resetState()
	SetFastPath(false)
	t.Setenv("AGENTCTL", "1")
	t.Setenv("TMUX_PANE", "%7")
	f := openTempLog(t)
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()

	writeStartupBanner()
	got := readLog(t, f)
	if !strings.Contains(got, "INFO") {
		t.Errorf("interactive banner should be INFO:\n%s", got)
	}
	if n := strings.Count(strings.TrimSpace(got), "\n"); n != 0 {
		t.Errorf("banner spans %d extra lines, want a single record:\n%s", n, got)
	}
}
