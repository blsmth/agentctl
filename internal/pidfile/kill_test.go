package pidfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// startSleeper starts a long-running child that is not an agentctl monitor
// and returns it along with a channel closed when it exits.
func startSleeper(t *testing.T) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleep: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	return cmd, done
}

func writePID(t *testing.T, dir string, pid int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "monitor.pid"), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatalf("writing pid file: %v", err)
	}
}

func TestKillOnlySignalsMonitors(t *testing.T) {
	tests := []struct {
		name       string
		isMonitor  bool
		wantKilled bool
	}{
		{name: "non-monitor pid is not signaled", isMonitor: false, wantKilled: false},
		{name: "monitor pid is signaled", isMonitor: true, wantKilled: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := isMonitor
			t.Cleanup(func() { isMonitor = orig })
			isMonitor = func(int) bool { return tc.isMonitor }

			cmd, done := startSleeper(t)
			dir := t.TempDir()
			writePID(t, dir, cmd.Process.Pid)

			Kill(dir, "monitor.pid")

			select {
			case <-done:
				if !tc.wantKilled {
					t.Fatal("Kill signaled a process that is not an agentctl monitor")
				}
			case <-time.After(500 * time.Millisecond):
				if tc.wantKilled {
					t.Fatal("Kill did not signal the monitor process")
				}
			}
		})
	}
}

func TestKillRealGuardSparesNonMonitor(t *testing.T) {
	// No stub: the real ps-based guard must reject a plain `sleep`.
	cmd, done := startSleeper(t)
	dir := t.TempDir()
	writePID(t, dir, cmd.Process.Pid)

	Kill(dir, "monitor.pid")

	select {
	case <-done:
		t.Fatal("Kill signaled a non-monitor process")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestKillSkipsOwnPID(t *testing.T) {
	orig := isMonitor
	t.Cleanup(func() { isMonitor = orig })
	isMonitor = func(int) bool { return true }

	dir := t.TempDir()
	writePID(t, dir, os.Getpid())
	// If the guard were missing this would SIGTERM the test binary.
	Kill(dir, "monitor.pid")
}

func TestRunning(t *testing.T) {
	tests := []struct {
		name    string
		pid     func(t *testing.T) int
		matches bool
		want    bool
	}{
		{name: "live matching process", pid: func(t *testing.T) int { c, _ := startSleeper(t); return c.Process.Pid }, matches: true, want: true},
		{name: "live non-matching process", pid: func(t *testing.T) int { c, _ := startSleeper(t); return c.Process.Pid }, matches: false, want: false},
		{name: "own pid", pid: func(*testing.T) int { return os.Getpid() }, matches: true, want: false},
		{name: "garbage", pid: func(*testing.T) int { return -1 }, matches: true, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := isAgentctlCmd
			t.Cleanup(func() { isAgentctlCmd = orig })
			isAgentctlCmd = func(int, string) bool { return tc.matches }
			dir := t.TempDir()
			pid := tc.pid(t)
			if err := os.WriteFile(filepath.Join(dir, "serve.pid"), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, ok := Running(dir, "serve.pid", "serve")
			if ok != tc.want || (ok && got != pid) {
				t.Errorf("Running = %d, %v; want %v", got, ok, tc.want)
			}
		})
	}
	if _, ok := Running(t.TempDir(), "missing.pid", "serve"); ok {
		t.Error("missing pidfile reported running")
	}
}
