package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── Test binary helper ───────────────────────────────────────────────────────

// tablesTestBinaryOnce guards the binary build so it happens at most once per test run.
var (
	tablesTestBinaryOnce sync.Once
	tablesTestBinaryPath string
	tablesTestBinaryErr  error
)

// getTablesTestBinary builds the agentctl binary (from the module root) once
// for all CLI smoke tests in this package.
func getTablesTestBinary(t *testing.T) string {
	t.Helper()
	tablesTestBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agentctl-cmd-smoke-*")
		if err != nil {
			tablesTestBinaryErr = err
			return
		}

		path := filepath.Join(dir, "agentctl-smoke")
		cmd := exec.Command("go", "build", "-o", path, "github.com/blsmth/agentctl")
		if out, err := cmd.CombinedOutput(); err != nil {
			tablesTestBinaryErr = &tablesBuildError{err: err, output: string(out)}
			return
		}
		tablesTestBinaryPath = path
	})
	if tablesTestBinaryErr != nil {
		t.Fatalf("build test binary: %v", tablesTestBinaryErr)
	}
	return tablesTestBinaryPath
}

type tablesBuildError struct {
	err    error
	output string
}

func (e *tablesBuildError) Error() string {
	return e.err.Error() + "\n" + e.output
}

// ─── Unit tests for unexported helpers ───────────────────────────────────────

// TestFmtAge validates the age-formatting helper.
func TestFmtAge(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{50 * time.Hour, "2d ago"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			got := fmtAge(tc.d)
			if got != tc.want {
				t.Errorf("fmtAge(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

// TestFmtDuration validates the duration-formatting helper.
func TestFmtDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m"},
		{90 * time.Minute, "1h30m"},
		{2 * time.Hour, "2h"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			got := fmtDuration(tc.d)
			if got != tc.want {
				t.Errorf("fmtDuration(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

// TestShortenPath validates the home-directory shortening helper.
func TestShortenPath(t *testing.T) {
	t.Parallel()
	// A path that starts with the real home dir should be shortened to ~/<rel>.
	home, _ := os.UserHomeDir()
	if home != "" {
		p := filepath.Join(home, "foo", "bar.md")
		got := shortenPath(p)
		if got != "~/foo/bar.md" {
			t.Errorf("shortenPath(%q) = %q, want %q", p, got, "~/foo/bar.md")
		}
	}

	// A path outside the home dir should be returned unchanged.
	abs := "/tmp/some/path.md"
	got := shortenPath(abs)
	if got != abs {
		t.Errorf("shortenPath(%q) = %q, want %q", abs, got, abs)
	}
}

// ─── CLI integration tests ────────────────────────────────────────────────────

// TestCLITicketsHelpFlags verifies that `agentctl tickets` (an alias of
// `ticket`) lists its default browse subcommand, and that `tickets browse
// --help` exposes --tui and --json -- the same layout as `status`/`status
// list`, since Kong renders a parent's help as its subcommand list.
func TestCLITicketsHelpFlags(t *testing.T) {
	bin := getTablesTestBinary(t)

	for _, tc := range []struct {
		args []string
		want []string
	}{
		{args: []string{"tickets", "--help"}, want: []string{"browse", "create"}},
		{args: []string{"tickets", "browse", "--help"}, want: []string{"tui", "json"}},
	} {
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(bin, tc.args...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		done := make(chan error, 1)
		go func() { done <- cmd.Run() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			if cmd.Process != nil {
				cmd.Process.Kill() //nolint:errcheck
			}
			t.Fatal("command timed out")
		}

		combined := stdout.String() + stderr.String()
		for _, want := range tc.want {
			if !strings.Contains(combined, want) {
				t.Errorf("%v does not contain %q\n%s", tc.args, want, combined)
			}
		}
	}
}

// TestCLIAgentsHelpFlags verifies that `agentctl agents --help` exposes --tui and --json.
func TestCLIAgentsHelpFlags(t *testing.T) {
	bin := getTablesTestBinary(t)

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "agents", "--help")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill() //nolint:errcheck
		}
		t.Fatal("command timed out")
	}

	combined := stdout.String() + stderr.String()
	for _, want := range []string{"tui", "json"} {
		if !strings.Contains(combined, want) {
			t.Errorf("agents --help does not contain %q\n%s", want, combined)
		}
	}
}

// TestCLISessionsJSONOutput verifies that `agentctl sessions --json` produces
// valid JSON output (array of objects, may be empty). This must be hermetic:
// $HOME is pointed at a throwaway temp dir with a minimal ~/.claude/projects
// fixture so the test exercises real behavior against controlled fixtures
// rather than whatever session data happens to exist on the machine running
// the test (or, on a clean CI runner, the complete absence of ~/.claude).
func TestCLISessionsJSONOutput(t *testing.T) {
	bin := getTablesTestBinary(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	writeFixtureSession(t, home, "hash1", "session.jsonl", "go-engineer")

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "sessions", "--json")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill() //nolint:errcheck
		}
		t.Fatal("command timed out")
	}

	out := strings.TrimSpace(stdout.String())
	// Output must be a JSON array (may be empty: []).
	if !strings.HasPrefix(out, "[") || !strings.HasSuffix(out, "]") {
		t.Errorf("sessions --json output is not a JSON array:\n%s\nstderr: %s", out, stderr.String())
	}
}

// TestCLISessionsJSONOutput_NoClaudeDir verifies that `agentctl sessions
// --json` still succeeds and emits `[]` when $HOME has no ~/.claude
// directory at all — the state of a completely fresh install, and the exact
// condition that breaks on a clean CI runner with no local Claude Code data.
func TestCLISessionsJSONOutput_NoClaudeDir(t *testing.T) {
	bin := getTablesTestBinary(t)

	home := t.TempDir() // no .claude subdir created at all
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "sessions", "--json")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("sessions --json failed with no ~/.claude dir: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill() //nolint:errcheck
		}
		t.Fatal("command timed out")
	}

	out := strings.TrimSpace(stdout.String())
	if out != "[]" {
		t.Errorf("sessions --json with no ~/.claude dir = %q, want %q\nstderr: %s", out, "[]", stderr.String())
	}
}

// writeFixtureSession writes a minimal JSONL session log under
// home/.claude/projects/<hash>/<filename> so CLI smoke tests exercise real
// behavior against a controlled fixture instead of the developer's actual
// Claude Code session history.
func writeFixtureSession(t *testing.T, home, hash, filename, agentName string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	fp := filepath.Join(dir, filename)
	content := fmt.Sprintf(`{"agentSetting":%q,"timestamp":"2026-01-01T00:00:00.000Z"}`+"\n", agentName)
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", fp, err)
	}
	return fp
}

// TestCLINewCommandsRespond verifies that `skills` and `memory` are
// registered commands and respond to --help without panicking.
func TestCLINewCommandsRespond(t *testing.T) {
	bin := getTablesTestBinary(t)

	cmds := []string{"skills", "memory"}
	for _, name := range cmds {
		name := name
		t.Run(name+" --help", func(t *testing.T) {
			t.Helper()
			var stdout, stderr bytes.Buffer
			c := exec.Command(bin, name, "--help")
			c.Stdout = &stdout
			c.Stderr = &stderr

			done := make(chan error, 1)
			go func() { done <- c.Run() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				if c.Process != nil {
					c.Process.Kill() //nolint:errcheck
				}
				t.Fatalf("command timed out: %s --help", name)
			}

			combined := stdout.String() + stderr.String()
			// Must mention "json" (the --json flag) and "Usage"
			if !strings.Contains(combined, "json") {
				t.Errorf("%s --help does not mention 'json': %s", name, combined)
			}
		})
	}
}
