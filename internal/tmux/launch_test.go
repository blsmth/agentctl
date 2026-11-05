package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/shell"
	"mvdan.cc/sh/v3/syntax"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/shellquote"
)

// hostilePayloads are prompt-like values that break, split, or execute
// something when interpolated into a shell script unquoted.
var hostilePayloads = []struct {
	name    string
	payload string
}{
	{"one backtick", "Task: fix `settings.go handling"},
	{"two backticks", "Task: fix `settings.go` handling"},
	{"dollar sign", "Task: preserve $HOME across restarts"},
	{"command substitution", "Task: check $(touch /tmp/agentctl-pwned) first"},
	{"double quote", `Task: rename the "legacy" field`},
	{"single quote", "Task: don't drop the user's config"},
	{"semicolon", "x; touch /tmp/agentctl-pwned"},
	{"newlines", "line one\nline two\n\n  indented"},
	{"spaces only", "   "},
	{"empty", ""},
}

// TestBuildLaunchScriptIsPOSIX is the regression test for the zsh hard
// dependency: the launch script used `#!/bin/zsh -l` and `2> >(tee ...)`
// process substitution, neither of which exists on a Linux box without zsh.
// The script must parse as strict POSIX sh for every hostile payload.
func TestBuildLaunchScriptIsPOSIX(t *testing.T) {
	parser := syntax.NewParser(syntax.Variant(syntax.LangPOSIX))
	for _, tt := range hostilePayloads {
		t.Run(tt.name, func(t *testing.T) {
			shellCmd := "claude --agent go-engineer " + shellquote.Quote(tt.payload)
			script := buildLaunchScript(shellCmd, "/home/a user/launch.log", "/home/a user/launch-1.sh")
			if !strings.HasPrefix(script, "#!/bin/sh\n") {
				t.Errorf("script shebang is not #!/bin/sh:\n%s", script)
			}
			if _, err := parser.Parse(strings.NewReader(script), "launch.sh"); err != nil {
				t.Errorf("script is not POSIX sh: %v\nscript:\n%s", err, script)
			}
			if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
				t.Errorf("sh -n rejected script: %v\n%s", err, out)
			}
		})
	}
}

// TestBuildLaunchScriptRunsUnderSh executes the generated script with
// /bin/sh: each hostile payload must reach the command byte-for-byte, the
// command's stderr and exit status must land in the launch log, and the
// script must delete itself.
func TestBuildLaunchScriptRunsUnderSh(t *testing.T) {
	for _, tt := range hostilePayloads {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "dir with 'quote' and space")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			outFile := filepath.Join(dir, "out")
			launchLog := filepath.Join(dir, "launch.log")
			shellCmd := "printf '%s' " + shellquote.Quote(tt.payload) + " > " + shellquote.Quote(outFile) +
				"; echo to-stderr >&2; exit 3"

			scriptPath, _, err := writeLaunchScript(dir, shellCmd, launchLog)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", scriptPath)
			cmd.Stdin = strings.NewReader("\n")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("script failed: %v\n%s", err, out)
			}

			got, err := os.ReadFile(outFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.payload {
				t.Errorf("payload = %q, want %q", got, tt.payload)
			}
			logData, err := os.ReadFile(launchLog)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"to-stderr", "Exit code: 3"} {
				if !strings.Contains(string(logData), want) {
					t.Errorf("launch log missing %q:\n%s", want, logData)
				}
			}
			if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
				t.Errorf("script %s not removed after run (stat err = %v)", scriptPath, err)
			}
			if _, err := os.Stat("/tmp/agentctl-pwned"); err == nil {
				t.Error("payload was executed by the shell")
			}
		})
	}
}

func TestLaunchWindowArgv(t *testing.T) {
	tests := []struct {
		name      string
		userShell string
		wantShell string
	}{
		{"uses $SHELL", "/usr/bin/fish", "/usr/bin/fish"},
		{"falls back to /bin/sh", "", "/bin/sh"},
		{"bash", "/bin/bash", "/bin/bash"},
		{"zsh in homebrew prefix", "/opt/homebrew/bin/zsh", "/opt/homebrew/bin/zsh"},
		{"dash", "/usr/bin/dash", "/usr/bin/dash"},
		{"ksh", "/bin/ksh", "/bin/ksh"},
		{"mksh", "/bin/mksh", "/bin/mksh"},
		{"tcsh rejects -l with other flags", "/bin/tcsh", "/bin/sh"},
		{"csh rejects -l with other flags", "/bin/csh", "/bin/sh"},
		{"unknown shell", "/usr/local/bin/nu", "/bin/sh"},
		{"lookalike name", "/usr/bin/bash-wrapper", "/bin/sh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := "/home/a user/it's/launch-1.sh"
			got := launchWindowArgv(tt.userShell, script)
			want := []string{tt.wantShell, "-l", "-c", "exec /bin/sh " + shellquote.Quote(script)}
			if !slices.Equal(got, want) {
				t.Errorf("launchWindowArgv = %q, want %q", got, want)
			}
		})
	}
}

// TestLaunchWindowArgvRuns runs the real argv with /bin/sh as the login
// shell and checks the script path survives the -c round trip.
func TestLaunchWindowArgvRuns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a dir; with $(stuff)")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "it's.sh")
	if err := os.WriteFile(script, []byte("touch "+shellquote.Quote(marker)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	argv := launchWindowArgv("", script)
	if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("%q: %v\n%s", argv, err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("script did not run: %v", err)
	}
}

func TestLaunchDirPermissions(t *testing.T) {
	base := t.TempDir()
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	t.Setenv("AGENTCTL_RUNTIME_DIR", filepath.Join(base, ".agentctl"))
	paths.Init(base)

	// Pre-create with loose permissions: LaunchDir must tighten them.
	want := filepath.Join(base, ".agentctl", launchDirName)
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(want, 0o755); err != nil {
		t.Fatal(err)
	}

	dir, err := LaunchDir()
	if err != nil {
		t.Fatalf("LaunchDir: %v", err)
	}
	if dir != want {
		t.Errorf("LaunchDir = %q, want %q", dir, want)
	}
	assertPerm(t, dir, 0o700)

	scriptPath, _, err := writeLaunchScript(dir, "true", launchLogPath(dir, "w"))
	if err != nil {
		t.Fatal(err)
	}
	assertPerm(t, scriptPath, 0o600)

	logPath := launchLogPath(dir, "go-engineer")
	if err := writeLaunchLogHeader(logPath, "/tmp", "claude"); err != nil {
		t.Fatal(err)
	}
	assertPerm(t, logPath, 0o600)
}

func TestLaunchDirRejectsSymlink(t *testing.T) {
	base := t.TempDir()
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)
	t.Setenv("AGENTCTL_RUNTIME_DIR", filepath.Join(base, ".agentctl"))
	paths.Init(base)
	paths.EnsureRuntimeDir()

	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(base, ".agentctl", launchDirName)); err != nil {
		t.Fatal(err)
	}
	if _, err := LaunchDir(); err == nil {
		t.Error("LaunchDir followed a symlinked launch dir, want error")
	}
}

// TestWriteLaunchLogHeaderTightensExistingLog covers logs created by older
// versions with mode 0644.
func TestWriteLaunchLogHeaderTightensExistingLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "launch-w.log")
	if err := os.WriteFile(logPath, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeLaunchLogHeader(logPath, "/tmp", "claude"); err != nil {
		t.Fatal(err)
	}
	assertPerm(t, logPath, 0o600)
}

// TestWriteLaunchLogHeaderDoesNotFollowSymlink is the regression test for
// the predictable launch log path: a symlink planted at
// launch-<window>.log must not redirect the (prompt-bearing) header into
// its target.
func TestWriteLaunchLogHeaderDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "victim")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := launchLogPath(dir, "go-engineer")
	if err := os.Symlink(target, logPath); err != nil {
		t.Fatal(err)
	}

	if err := writeLaunchLogHeader(logPath, "/tmp", "claude secret prompt"); err == nil {
		t.Error("writeLaunchLogHeader wrote through a symlink, want error")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("symlink target modified: %q", got)
	}
}

func TestLaunchLogPathStaysInDir(t *testing.T) {
	dir := "/run/launch"
	for _, name := range []string{"go-engineer", "../../etc/passwd", "a/b", `a\b`} {
		got := launchLogPath(dir, name)
		if filepath.Dir(got) != dir {
			t.Errorf("launchLogPath(%q) = %q escapes %q", name, got, dir)
		}
	}
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode = %s, want %s", path, got, fmt.Sprint(want))
	}
}

// TestChromeBarHookShellCmdRuns executes the hook's shell command (with the
// tmux formats expanded by hand) against a stand-in binary whose path has a
// space and a single quote, checking it receives the runtime dir and window
// ID intact.
func TestChromeBarHookShellCmdRuns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Jane O'Neil $(x)")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	exe := filepath.Join(dir, "agent ctl")
	script := "#!/bin/sh\nprintf '%s|%s|%s' \"$AGENTCTL_RUNTIME_DIR\" \"$1\" \"$2\" > " + shellquote.Quote(out) + "\n"
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(dir, ".agentctl")

	args := chromeBarHookArgs("s", exe, runtimeDir, dir)
	tmuxArgs, err := shell.Fields(args[4], nil)
	if err != nil || len(tmuxArgs) != 5 {
		t.Fatalf("tokenizing %q: %q, %v", args[4], tmuxArgs, err)
	}
	shellCmd := strings.NewReplacer("#{window_id}", "@7").Replace(tmuxArgs[4])
	if b, err := exec.Command("/bin/sh", "-c", shellCmd).CombinedOutput(); err != nil {
		t.Fatalf("running %q: %v\n%s", shellCmd, err, b)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := runtimeDir + "|chrome-bar-pane|@7"; string(got) != want {
		t.Errorf("stand-in got %q, want %q", got, want)
	}
}
