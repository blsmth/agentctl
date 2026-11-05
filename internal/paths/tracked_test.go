package paths

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/gittracked"
)

// fakeLsFiles returns a Runner that reports files as tracked.
func fakeLsFiles(files ...string) gittracked.Runner {
	return func(...string) ([]byte, error) {
		if len(files) == 0 {
			return nil, nil
		}
		return []byte(strings.Join(files, "\x00") + "\x00"), nil
	}
}

func TestCheckRuntimeDir(t *testing.T) {
	noGit := func(...string) ([]byte, error) { return nil, exec.ErrNotFound }

	tests := []struct {
		name    string
		setup   func(t *testing.T, project string) // builds .agentctl on disk
		run     gittracked.Runner
		wantErr string // substring; "" means no error
	}{
		{
			name:  "no runtime dir",
			setup: func(*testing.T, string) {},
			run:   fakeLsFiles(".agentctl/settings/x.json"),
		},
		{
			name:  "no git",
			setup: mkRuntime,
			run:   noGit,
		},
		{
			name:  "untracked",
			setup: mkRuntime,
			run:   fakeLsFiles(),
		},
		{
			name: "tracked config.json ok",
			setup: func(t *testing.T, project string) {
				mkRuntime(t, project)
				writeTestFile(t, filepath.Join(project, ".agentctl", "config.json"))
			},
			run: fakeLsFiles(".agentctl/config.json"),
		},
		{
			name: "tracked config.json symlink refused",
			setup: func(t *testing.T, project string) {
				mkRuntime(t, project)
				if err := os.Symlink("../../elsewhere.json", filepath.Join(project, ".agentctl", "config.json")); err != nil {
					t.Fatal(err)
				}
			},
			run:     fakeLsFiles(".agentctl/config.json"),
			wantErr: ".agentctl/config.json",
		},
		{
			name: "tracked other file refused",
			setup: func(t *testing.T, project string) {
				mkRuntime(t, project)
				writeTestFile(t, filepath.Join(project, ".agentctl", "config.json"))
			},
			run:     fakeLsFiles(".agentctl/config.json", ".agentctl/settings/go-engineer.json"),
			wantErr: ".agentctl/settings/go-engineer.json",
		},
		{
			name: "runtime dir symlink refused",
			setup: func(t *testing.T, project string) {
				if err := os.Symlink(t.TempDir(), filepath.Join(project, ".agentctl")); err != nil {
					t.Fatal(err)
				}
			},
			run:     fakeLsFiles(),
			wantErr: "symlink",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := t.TempDir()
			tt.setup(t, project)
			err := checkRuntimeDir(filepath.Join(project, ".agentctl"), tt.run)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var ue *UnsafeRuntimeDirError
			if !errors.As(err, &ue) {
				t.Fatalf("err = %v, want *UnsafeRuntimeDirError", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %q, want it to mention %q", err, tt.wantErr)
			}
			if strings.Contains(tt.name, "other file") && strings.Contains(strings.Join(ue.Tracked, " "), "config.json") {
				t.Errorf("regular tracked config.json should not be listed: %q", ue.Tracked)
			}
		})
	}
}

// TestCheckRuntimeDirRealGit runs the check against a real repository that
// commits a symlink under .agentctl/, the shape of the reported PoC.
func TestCheckRuntimeDirRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	project := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", project}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	settings := filepath.Join(project, ".agentctl", "settings")
	if err := os.MkdirAll(settings, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../../.zshenv", filepath.Join(settings, "go-engineer.json")); err != nil {
		t.Fatal(err)
	}
	git("add", ".agentctl")

	err := checkRuntimeDir(filepath.Join(project, ".agentctl"), gittracked.ExecGit)
	if err == nil || !strings.Contains(err.Error(), ".agentctl/settings/go-engineer.json") {
		t.Fatalf("err = %v, want refusal naming the symlink", err)
	}

	// CheckRuntimeDir(false) skips git and so only sees a real directory.
	ResetForTest()
	t.Cleanup(ResetForTest)
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	Init(project)
	if err := CheckRuntimeDir(false); err != nil {
		t.Fatalf("CheckRuntimeDir(false) = %v, want nil", err)
	}
}

func TestEnsureRuntimeDirModeAndSymlink(t *testing.T) {
	t.Run("created 0700 and tightened", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), ".agentctl")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		ensureRuntimeDir(dir)
		fi, err := os.Lstat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("mode = %v, want 0700", fi.Mode().Perm())
		}
	})
	t.Run("symlinked dir is not written through", func(t *testing.T) {
		target := t.TempDir()
		dir := filepath.Join(t.TempDir(), ".agentctl")
		if err := os.Symlink(target, dir); err != nil {
			t.Fatal(err)
		}
		ensureRuntimeDir(dir)
		if _, err := os.Lstat(filepath.Join(target, ".gitignore")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf(".gitignore written through symlink: %v", err)
		}
	})
}

func mkRuntime(t *testing.T, project string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(project, ".agentctl"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// caseInsensitiveFS reports whether dir's filesystem folds case.
func caseInsensitiveFS(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "case-probe")
	writeTestFile(t, probe)
	_, err := os.Lstat(filepath.Join(dir, "CASE-PROBE"))
	os.Remove(probe) //nolint:errcheck
	return err == nil
}

// TestCheckRuntimeDirCaseInsensitiveBypass: on a case-insensitive
// filesystem a repo committing ".AGENTCTL/..." plants files in the runtime
// dir; the check must see them even though git pathspecs are case-sensitive.
func TestCheckRuntimeDirCaseInsensitiveBypass(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	project := t.TempDir()
	if !caseInsensitiveFS(t, project) {
		t.Skip("filesystem is case-sensitive")
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", project}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "core.ignorecase", "true")
	planted := filepath.Join(project, ".AGENTCTL")
	if err := os.Mkdir(planted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "target"), filepath.Join(planted, "audit")); err != nil {
		t.Fatal(err)
	}
	git("add", ".AGENTCTL")

	err := checkRuntimeDir(filepath.Join(project, ".agentctl"), gittracked.ExecGit)
	if err == nil || !strings.Contains(err.Error(), ".AGENTCTL/audit") {
		t.Fatalf("err = %v, want refusal naming .AGENTCTL/audit", err)
	}
}

// TestCheckRuntimeDirDistinctCaseDir: on a case-sensitive filesystem a
// tracked ".AGENTCTL/" is a different directory and must not be flagged.
func TestCheckRuntimeDirDistinctCaseDir(t *testing.T) {
	project := t.TempDir()
	if caseInsensitiveFS(t, project) {
		t.Skip("filesystem is case-insensitive")
	}
	mkRuntime(t, project)
	if err := os.Mkdir(filepath.Join(project, ".AGENTCTL"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(project, ".AGENTCTL", "x"))
	if err := checkRuntimeDir(filepath.Join(project, ".agentctl"), fakeLsFiles(".AGENTCTL/x")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestCheckRuntimeDirGitFailureRefuses: a git failure inside a repository
// (e.g. dubious ownership) is not "nothing tracked".
func TestCheckRuntimeDirGitFailureRefuses(t *testing.T) {
	project := t.TempDir()
	mkRuntime(t, project)
	failing := func(...string) ([]byte, error) {
		return nil, errors.New("exit status 128")
	}
	err := checkRuntimeDir(filepath.Join(project, ".agentctl"), failing)
	if err == nil || !strings.Contains(err.Error(), "cannot tell") {
		t.Fatalf("err = %v, want refusal", err)
	}
}
