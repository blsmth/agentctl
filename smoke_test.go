package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// testBinary is a lazily-built copy of the binary used across smoke tests.
var (
	testBinaryOnce sync.Once
	testBinaryPath string
	testBinaryErr  error
	testBinaryDir  string
)

// getTestBinary builds the binary once for all smoke tests. The temp dir is
// cleaned up automatically by the OS; we don't remove it mid-run so parallel
// tests can share it.
func getTestBinary(t *testing.T) string {
	t.Helper()
	testBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agentctl-smoke-*")
		if err != nil {
			testBinaryErr = err
			return
		}
		testBinaryDir = dir

		path := filepath.Join(dir, "agentctl-smoke")
		cmd := exec.Command("go", "build", "-o", path, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			testBinaryErr = &buildError{err: err, output: string(out)}
			return
		}
		testBinaryPath = path
	})
	if testBinaryErr != nil {
		t.Fatalf("build test binary: %v", testBinaryErr)
	}
	return testBinaryPath
}

type buildError struct {
	err    error
	output string
}

func (e *buildError) Error() string {
	return e.err.Error() + "\n" + e.output
}

// TestBuildCompiles verifies that the package compiles cleanly. This acts as a
// fast guard against import cycles, syntax errors, and missing dependencies.
// Note: if this test runs at all, the package already compiled — the subprocess
// build provides an explicit, isolated verification that matches CI.
func TestBuildCompiles(t *testing.T) {
	cmd := exec.Command("go", "build", "-o", os.DevNull, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
}

// TestVersionOutput builds the binary, runs `agentctl version`, and asserts:
//   - exit code 0
//   - stdout contains the word "agentctl"
func TestVersionOutput(t *testing.T) {
	bin := getTestBinary(t)

	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		t.Fatalf("agentctl version: %v", err)
	}

	got := string(out)
	if !strings.Contains(got, "agentctl") {
		t.Errorf("version output %q does not contain 'agentctl'", got)
	}
}

// TestHelpExitsCleanly verifies that `agentctl --help` prints usage and exits 0.
// Kong handles --help natively; this test ensures the migration didn't break it.
func TestHelpExitsCleanly(t *testing.T) {
	bin := getTestBinary(t)

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "--help")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		t.Errorf("--help exited non-zero: %v\nstderr: %s", err, stderr.String())
	}

	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "Usage:") {
		t.Errorf("--help output does not contain 'Usage:'\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
	}
}

// TestMonitorSubcommandExists runs `agentctl monitor` under a 3-second timeout.
// The monitor will fail quickly without a tmux session, or be killed by the
// context deadline — both are acceptable. We verify no runtime panic occurred.
func TestMonitorSubcommandExists(t *testing.T) {
	if os.Getenv("AGENTCTL_TEST_TMUX") == "" {
		t.Skip("set AGENTCTL_TEST_TMUX=1 to run: the monitor spawns child processes that need a real tmux server")
	}
	bin := getTestBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "monitor")
	cmd.Env = append(os.Environ(), "AGENTCTL_TEST=1")
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the entire process group so child processes (chrome-bar, etc.)
		// don't leak as orphans.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	_ = cmd.Run() // exit by timeout-kill or naturally — both acceptable

	if s := stderr.String(); strings.Contains(s, "panic:") {
		t.Errorf("monitor panicked: %s", s)
	}
}
