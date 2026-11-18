package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitProjectCreatesAgentsDir(t *testing.T) {
	dir := t.TempDir()
	if !needsInit(dir) {
		t.Fatal("fresh dir should need init")
	}
	// InitProject must never read stdin: hand it a closed pipe.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; _ = r.Close() })

	res, err := InitProject(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || len(res.Installed) != 0 || len(res.Skipped) != 0 {
		t.Errorf("res = %+v, want Created and no templates", res)
	}
	if fi, err := os.Stat(filepath.Join(dir, ".claude", "agents")); err != nil || !fi.IsDir() {
		t.Fatalf(".claude/agents not created: %v", err)
	}
	if needsInit(dir) {
		t.Error("initialized dir still needs init")
	}
	if entries, _ := os.ReadDir(agentsDir(dir)); len(entries) != 0 {
		t.Errorf("templates=false installed %d files", len(entries))
	}

	// A second run is a no-op.
	marker := filepath.Join(agentsDir(dir), "mine.md")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = InitProject(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created {
		t.Error("second run reported Created")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("second run disturbed existing agents: %v", err)
	}
}

func TestInitProjectRefusesFileInTheWay(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".claude"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InitProject(dir, false); err == nil {
		t.Error("InitProject succeeded with a file at .claude")
	}
}

// fakeHubNewSession records which side effects ran.
type fakeHubNewSession struct {
	exists   bool
	path     string
	startErr error
	started  int
	launched int
}

func (f *fakeHubNewSession) deps(runtimeDir string) hubNewSessionDeps {
	return hubNewSessionDeps{
		runtimeDir:    func() string { return runtimeDir },
		sessionName:   func() string { return "agentctl-proj" },
		sessionExists: func() bool { return f.exists },
		sessionPath:   func(string) string { return f.path },
		startSession:  func() error { f.started++; return f.startErr },
		launch:        func() { f.launched++ },
	}
}

func TestRunHubNewSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Run("fresh project: init, start, launch", func(t *testing.T) {
		dir := t.TempDir()
		f := &fakeHubNewSession{}
		if err := runHubNewSession(dir, f.deps(filepath.Join(dir, ".agentctl"))); err != nil {
			t.Fatal(err)
		}
		if needsInit(dir) {
			t.Error("project not initialized")
		}
		if f.started != 1 || f.launched != 1 {
			t.Errorf("started=%d launched=%d, want 1/1", f.started, f.launched)
		}
	})
	t.Run("running session in the same dir: launch only", func(t *testing.T) {
		dir := t.TempDir()
		f := &fakeHubNewSession{exists: true, path: dir}
		if err := runHubNewSession(dir, f.deps(filepath.Join(dir, ".agentctl"))); err != nil {
			t.Fatal(err)
		}
		if f.started != 0 || f.launched != 1 {
			t.Errorf("started=%d launched=%d, want 0/1", f.started, f.launched)
		}
	})
	t.Run("session name taken by another project", func(t *testing.T) {
		dir := t.TempDir()
		f := &fakeHubNewSession{exists: true, path: t.TempDir()}
		err := runHubNewSession(dir, f.deps(filepath.Join(dir, ".agentctl")))
		if err == nil || !strings.Contains(err.Error(), "already belongs to") {
			t.Fatalf("err = %v", err)
		}
		if f.launched != 0 {
			t.Error("launched into another project's session")
		}
	})
	t.Run("runtime dir not pinned to the project", func(t *testing.T) {
		dir := t.TempDir()
		f := &fakeHubNewSession{}
		err := runHubNewSession(dir, f.deps("/elsewhere/hub/.agentctl"))
		if err == nil || !strings.Contains(err.Error(), "runtime dir") {
			t.Fatalf("err = %v", err)
		}
		if f.started+f.launched != 0 || !needsInit(dir) {
			t.Error("acted with the wrong runtime dir")
		}
	})
	t.Run("only a prefix-matching session is live: start our own", func(t *testing.T) {
		// agentctl-foobar is live, agentctl-foo is not: the exact-match
		// check reports no session, so ours is started rather than
		// launching into the other project's session.
		dir := t.TempDir()
		f := &fakeHubNewSession{exists: false, path: "/src/foobar"}
		if err := runHubNewSession(dir, f.deps(filepath.Join(dir, ".agentctl"))); err != nil {
			t.Fatal(err)
		}
		if f.started != 1 || f.launched != 1 {
			t.Errorf("started=%d launched=%d, want 1/1", f.started, f.launched)
		}
	})
	t.Run("existing session with unknown directory is refused", func(t *testing.T) {
		dir := t.TempDir()
		f := &fakeHubNewSession{exists: true, path: ""}
		err := runHubNewSession(dir, f.deps(filepath.Join(dir, ".agentctl")))
		if err == nil || !strings.Contains(err.Error(), "directory is unknown") || f.launched+f.started != 0 {
			t.Errorf("err = %v, started=%d launched=%d", err, f.started, f.launched)
		}
	})
	t.Run("start failure stops the launch", func(t *testing.T) {
		dir := t.TempDir()
		f := &fakeHubNewSession{startErr: errors.New("boom")}
		err := runHubNewSession(dir, f.deps(filepath.Join(dir, ".agentctl")))
		if err == nil || !strings.Contains(err.Error(), "boom") || f.launched != 0 {
			t.Errorf("err = %v, launched = %d", err, f.launched)
		}
	})
	t.Run("home is refused before anything runs", func(t *testing.T) {
		home := os.Getenv("HOME")
		f := &fakeHubNewSession{}
		if err := runHubNewSession(home, f.deps(filepath.Join(home, ".agentctl"))); err == nil {
			t.Fatal("home accepted")
		}
		if f.started+f.launched != 0 || !needsInit(home) {
			t.Error("acted on $HOME")
		}
	})
}
