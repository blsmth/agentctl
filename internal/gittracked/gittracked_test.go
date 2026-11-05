package gittracked

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFilesParsesNULSeparatedOutput(t *testing.T) {
	var gotArgs []string
	run := func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("a\x00dir/b c\x00"), nil
	}
	files, err := Files(run, "/repo", ".agentctl")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "dir/b c"}; !reflect.DeepEqual(files, want) {
		t.Errorf("files = %q, want %q", files, want)
	}
	wantArgs := []string{"-C", "/repo", "--no-optional-locks", "ls-files", "-z", "--", ".agentctl"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("args = %q, want %q", gotArgs, wantArgs)
	}
}

func TestFilesRunnerErrorIsUnavailable(t *testing.T) {
	run := func(...string) ([]byte, error) { return nil, exec.ErrNotFound }
	if _, err := Files(run, "/repo"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestFilesRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitCmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Not a repo yet: unavailable.
	if _, err := Files(ExecGit, dir, "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("non-repo err = %v, want ErrUnavailable", err)
	}

	gitCmd("init", "-q")
	if err := os.MkdirAll(filepath.Join(dir, ".agentctl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agentctl", "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../x", filepath.Join(dir, ".agentctl", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd("add", ".agentctl")

	files, err := Files(ExecGit, dir, ".agentctl", "untracked")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".agentctl/config.json", ".agentctl/link"}; !reflect.DeepEqual(files, want) {
		t.Errorf("files = %q, want %q", files, want)
	}
}

func TestFilesClassifiesGitErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// A real "not a git repository" failure is ErrUnavailable.
	if _, err := Files(ExecGit, t.TempDir(), "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("non-repo err = %v, want ErrUnavailable", err)
	}
	// Any other git failure (here: an exit error with different stderr) is
	// a plain error that carries git's stderr.
	other := func(...string) ([]byte, error) {
		err := exec.Command("sh", "-c", "echo 'fatal: detected dubious ownership' >&2; exit 128").Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			ee.Stderr = []byte("fatal: detected dubious ownership in repository")
		}
		return nil, err
	}
	_, err := Files(other, "/repo", "x")
	if err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want non-ErrUnavailable error", err)
	}
	if !strings.Contains(err.Error(), "dubious ownership") {
		t.Errorf("err = %v, want git stderr included", err)
	}
}
