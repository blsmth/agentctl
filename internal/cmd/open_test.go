package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type openRec struct {
	ensured  []string
	switched []string
	attached []string
	out      bytes.Buffer
}

// openTestDeps builds deps over a fake filesystem: dirs maps path to
// isDir, roots maps a dir to its main repo root.
func openTestDeps(r *openRec, cwd, home string, dirs map[string]bool, roots map[string]string) openDeps {
	return openDeps{
		cwd:  func() (string, error) { return cwd, nil },
		home: func() (string, error) { return home, nil },
		repoRoot: func(dir string) (string, bool) {
			root, ok := roots[dir]
			return root, ok
		},
		isDir: func(p string) (bool, bool) {
			d, ok := dirs[p]
			return ok, d
		},
		ensure:         func(dir string) error { r.ensured = append(r.ensured, dir); return nil },
		sessionName:    func(dir string) string { return "agentctl-" + filepath.Base(dir) },
		currentSession: func() string { return "" },
		switchTo:       func(s string) error { r.switched = append(r.switched, s); return nil },
		attach:         func(s string) error { r.attached = append(r.attached, s); return nil },
		out:            &r.out,
	}
}

func TestResolveOpenDir(t *testing.T) {
	dirs := map[string]bool{
		"/work/proj": true, "/work/proj/sub": true, "/home/u/code/app": true,
		"/work/proj/.worktrees/agent-x": true, "/work/file": false, "/plain": true, "/work": true,
	}
	roots := map[string]string{
		"/work/proj":                    "/work/proj",
		"/work/proj/sub":                "/work/proj",
		"/work/proj/.worktrees/agent-x": "/work/proj",
		"/home/u/code/app":              "/home/u/code/app",
	}
	tests := []struct {
		name, arg, cwd, want, wantErr string
	}{
		{"cwd default", "", "/work/proj/sub", "/work/proj", ""},
		{"dot", ".", "/plain", "/plain", ""},
		{"relative", "proj", "/work", "/work/proj", ""},
		{"relative dotdot", "..", "/work/proj/sub", "/work/proj", ""},
		{"tilde", "~/code/app", "/work", "/home/u/code/app", ""},
		{"absolute subdir to repo root", "/work/proj/sub/", "/", "/work/proj", ""},
		{"worktree to main root", "/work/proj/.worktrees/agent-x", "/", "/work/proj", ""},
		{"not in a repo", "/plain", "/", "/plain", ""},
		{"missing", "/nope", "/", "", "does not exist"},
		{"file", "/work/file", "/", "", "not a directory"},
		{"~user", "~bob", "/", "", "unsupported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r openRec
			d := openTestDeps(&r, tt.cwd, "/home/u", dirs, roots)
			got, err := resolveOpenDir(tt.arg, d)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestRunOpenSwitchAttachDetach(t *testing.T) {
	dirs := map[string]bool{"/work/proj": true}
	t.Run("outside tmux attaches", func(t *testing.T) {
		var r openRec
		d := openTestDeps(&r, "/work/proj", "/h", dirs, nil)
		if err := runOpen("", OpenOptions{}, d); err != nil {
			t.Fatal(err)
		}
		if len(r.attached) != 1 || r.attached[0] != "agentctl-proj" || len(r.switched) != 0 || len(r.ensured) != 1 {
			t.Errorf("%+v", r)
		}
	})
	t.Run("inside tmux switches", func(t *testing.T) {
		var r openRec
		d := openTestDeps(&r, "/work/proj", "/h", dirs, nil)
		d.currentSession = func() string { return "agentctl-other" }
		if err := runOpen("", OpenOptions{}, d); err != nil {
			t.Fatal(err)
		}
		if len(r.switched) != 1 || r.switched[0] != "agentctl-proj" || len(r.attached) != 0 {
			t.Errorf("%+v", r)
		}
	})
	t.Run("same session is a no-op", func(t *testing.T) {
		var r openRec
		d := openTestDeps(&r, "/work/proj", "/h", dirs, nil)
		d.currentSession = func() string { return "agentctl-proj" }
		if err := runOpen("", OpenOptions{}, d); err != nil {
			t.Fatal(err)
		}
		if len(r.switched)+len(r.attached) != 0 || !strings.Contains(r.out.String(), "already in agentctl-proj") {
			t.Errorf("%+v out=%q", r, r.out.String())
		}
	})
	t.Run("detach prints the name only", func(t *testing.T) {
		for _, cur := range []string{"", "agentctl-other"} {
			var r openRec
			d := openTestDeps(&r, "/work/proj", "/h", dirs, nil)
			d.currentSession = func() string { return cur }
			if err := runOpen("", OpenOptions{Detach: true}, d); err != nil {
				t.Fatal(err)
			}
			if len(r.switched)+len(r.attached) != 0 || strings.TrimSpace(r.out.String()) != "agentctl-proj" || len(r.ensured) != 1 {
				t.Errorf("cur=%q: %+v out=%q", cur, r, r.out.String())
			}
		}
	})
	t.Run("switch failure is reported", func(t *testing.T) {
		var r openRec
		d := openTestDeps(&r, "/work/proj", "/h", dirs, nil)
		d.currentSession = func() string { return "x" }
		d.switchTo = func(string) error { return errors.New("boom") }
		if err := runOpen("", OpenOptions{}, d); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRunOpenMissingDirDoesNotInit(t *testing.T) {
	var r openRec
	d := openTestDeps(&r, "/", "/h", map[string]bool{}, nil)
	if err := runOpen("/nope", OpenOptions{}, d); err == nil {
		t.Fatal("expected an error")
	}
	if len(r.ensured) != 0 {
		t.Error("ensure ran for a missing dir")
	}
}

func TestRunOpenEnsureErrorStops(t *testing.T) {
	var r openRec
	d := openTestDeps(&r, "/work/proj", "/h", map[string]bool{"/work/proj": true}, nil)
	d.ensure = func(string) error { return errors.New("tmux session agentctl-proj already belongs to /elsewhere/proj") }
	err := runOpen("", OpenOptions{}, d)
	if err == nil || !strings.Contains(err.Error(), "already belongs to") {
		t.Fatalf("err = %v", err)
	}
	if len(r.attached)+len(r.switched) != 0 {
		t.Error("attached after a failed ensure")
	}
}

// The same-name refusal comes from ensureHubSession, which open reuses.
func TestOpenEnsureRefusesSameNamedSessionOfOtherDir(t *testing.T) {
	dir := t.TempDir()
	d := hubNewSessionDeps{
		runtimeDir:    func() string { return filepath.Join(dir, ".agentctl") },
		sessionName:   func() string { return "agentctl-proj" },
		sessionExists: func() bool { return true },
		sessionPath:   func(string) string { return "/somewhere/else/proj" },
		startSession:  func() error { t.Error("started a session"); return nil },
		launch:        func() { t.Error("launched an agent") },
	}
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := ensureHubSession(dir, d)
	if err == nil || !strings.Contains(err.Error(), "already belongs to") {
		t.Fatalf("err = %v", err)
	}
}
