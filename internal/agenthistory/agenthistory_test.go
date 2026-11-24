package agenthistory

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// setup points HOME and the state dir at temp dirs and wraps runGit so a
// call that is not confined to them fails the test.
func setup(t *testing.T) (home string, s *Store) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	if err := os.MkdirAll(filepath.Join(home, ".claude", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := runGit
	runGit = func(env []string, stdin string, args ...string) ([]byte, error) {
		var gd, wt bool
		for _, a := range args {
			gd = gd || (strings.HasPrefix(a, "--git-dir="+root))
			wt = wt || (strings.HasPrefix(a, "--work-tree="+root)) || (strings.HasPrefix(a, "--work-tree="+resolved))
		}
		if !gd || !wt {
			t.Fatalf("git call not confined to the temp dirs: %v", args)
		}
		return orig(env, stdin, args...)
	}
	t.Cleanup(func() { runGit = orig })
	s, err = HomeStore()
	if err != nil {
		t.Fatal(err)
	}
	return home, s
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotBaselineAndDrift(t *testing.T) {
	home, s := setup(t)
	claude := filepath.Join(home, ".claude")
	write(t, filepath.Join(claude, "agents", "a.md"), "one\n")
	write(t, filepath.Join(claude, "shared", "x.md"), "shared\n")
	write(t, filepath.Join(claude, "settings.json"), "{}")
	write(t, filepath.Join(claude, "projects", "log.jsonl"), "private")
	write(t, filepath.Join(claude, ".credentials.json"), "secret")

	rev, err := s.Snapshot("housekeeping: edit", "")
	if err != nil || rev == "" {
		t.Fatalf("baseline: rev=%q err=%v", rev, err)
	}
	if rev2, err := s.Snapshot("again", ""); err != nil || rev2 != "" {
		t.Fatalf("no drift should not commit: rev=%q err=%v", rev2, err)
	}
	if _, err := os.Stat(filepath.Join(claude, ".git")); err == nil {
		t.Fatal(".git created inside ~/.claude")
	}
	files, _ := s.git(nil, "", "ls-files")
	if got := strings.Fields(string(files)); len(got) != 3 {
		t.Fatalf("tracked = %v, want agents/a.md, settings.json and shared/x.md only", got)
	}

	write(t, filepath.Join(claude, "agents", "a.md"), "two\n")
	if rev, err := s.Snapshot("edit", "improve-1"); err != nil || rev == "" {
		t.Fatalf("drift: rev=%q err=%v", rev, err)
	}
	log, err := s.Log("agents/a.md", 0)
	if err != nil || len(log) != 2 {
		t.Fatalf("log = %v err=%v", log, err)
	}
	if log[0].Author != "improve-1" || log[0].Subject != "edit" || !strings.HasPrefix(log[1].Subject, "baseline") {
		t.Fatalf("log = %+v", log)
	}
}

func TestSnapshotSkipsLargeFilesAndKeepsSymlinks(t *testing.T) {
	home, s := setup(t)
	agents := filepath.Join(home, ".claude", "agents")
	write(t, filepath.Join(agents, "a.md"), "a")
	write(t, filepath.Join(agents, "big.md"), strings.Repeat("x", MaxFileSize+1))
	outside := filepath.Join(home, "outside.md")
	write(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(agents, "link.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot("t", ""); err != nil {
		t.Fatal(err)
	}
	files, _ := s.git(nil, "", "ls-files", "-s")
	out := string(files)
	if strings.Contains(out, "big.md") {
		t.Errorf("large file tracked:\n%s", out)
	}
	if !strings.Contains(out, "120000") || !strings.Contains(out, "agents/link.md") {
		t.Errorf("symlink not tracked as a link:\n%s", out)
	}
	if blob, _ := s.git(nil, "", "show", ":agents/link.md"); string(blob) != outside {
		t.Errorf("link target = %q, want %q", blob, outside)
	}
}

func TestSnapshotDeletedRoot(t *testing.T) {
	home, s := setup(t)
	write(t, filepath.Join(home, ".claude", "agents", "a.md"), "a")
	if _, err := s.Snapshot("t", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".claude", "agents")); err != nil {
		t.Fatal(err)
	}
	if rev, err := s.Snapshot("gone", ""); err != nil || rev == "" {
		t.Fatalf("deleting the dir should snapshot: rev=%q err=%v", rev, err)
	}
}

func TestRevertCreatesNewSnapshot(t *testing.T) {
	home, s := setup(t)
	p := filepath.Join(home, ".claude", "agents", "a.md")
	write(t, p, "v1\n")
	if _, err := s.Snapshot("t", ""); err != nil {
		t.Fatal(err)
	}
	write(t, p, "v2\n")
	if _, err := s.Snapshot("edit", ""); err != nil {
		t.Fatal(err)
	}
	write(t, p, "v3 unsnapshotted\n")

	path, err := s.Resolve("a")
	if err != nil || path != "agents/a.md" {
		t.Fatalf("resolve = %q, %v", path, err)
	}
	_, snap, err := s.Revert(path, "", "agent-x")
	if err != nil || snap == "" {
		t.Fatalf("revert: snap=%q err=%v", snap, err)
	}
	if got, _ := os.ReadFile(p); string(got) != "v2\n" {
		t.Fatalf("content after revert = %q", got)
	}
	log, _ := s.Log("", 0)
	// "Undo the latest change" undoes the unsnapshotted v3 edit.
	// baseline, edit, before-revert (v3), revert: nothing rewritten.
	if len(log) != 4 || !strings.HasPrefix(log[0].Subject, "revert agents/a.md") || log[0].Author != "agent-x" {
		t.Fatalf("log = %+v", log)
	}
	if d, err := s.Diff(path, ""); err != nil || d != "" {
		t.Fatalf("diff after revert = %q, %v", d, err)
	}
}

func TestRevertToExplicitRevAndAbsence(t *testing.T) {
	home, s := setup(t)
	agents := filepath.Join(home, ".claude", "agents")
	write(t, filepath.Join(agents, "keep.md"), "k")
	if _, err := s.Snapshot("t", ""); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(agents, "new.md"), "n")
	if _, err := s.Snapshot("add", ""); err != nil {
		t.Fatal(err)
	}
	// The latest change to new.md created it: reverting removes it.
	if _, _, err := s.Revert("agents/new.md", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(agents, "new.md")); !os.IsNotExist(err) {
		t.Fatalf("new.md should be gone: %v", err)
	}
	if _, _, err := s.Revert("agents/keep.md", "--evil", ""); err == nil {
		t.Fatal("option-like rev accepted")
	}
}

func TestResolve(t *testing.T) {
	_, s := setup(t)
	home := s.WorkTree
	for _, tc := range []struct {
		in, want string
		err      bool
	}{
		{"reviewer", "agents/reviewer.md", false},
		{"reviewer.md", "agents/reviewer.md", false},
		{"shared/x.md", "shared/x.md", false},
		{filepath.Join(home, "agents", "a.md"), "agents/a.md", false},
		{"../settings.json", "", true},
		{"settings.json", "settings.json", false},
		{"CLAUDE.md", "CLAUDE.md", false},
		{"settings.json/..", "", true},
		{filepath.Join(home, ".credentials.json"), "", true},
		{filepath.Join(home, "projects", "x.jsonl"), "", true},
		{"/etc/passwd", "", true},
		{"agents/../.credentials.json", "", true},
	} {
		got, err := s.Resolve(tc.in)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("Resolve(%q) = %q, %v; want %q err=%v", tc.in, got, err, tc.want, tc.err)
		}
	}
}

func TestGitMissing(t *testing.T) {
	_, s := setup(t)
	old := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("nope") }
	t.Cleanup(func() { lookPath = old })
	if _, err := s.Snapshot("t", ""); !errors.Is(err, ErrGitMissing) {
		t.Fatalf("err = %v", err)
	}
	SnapshotAll("t", nil)
	SnapshotAll("t", nil)
	sd, _ := stateDir()
	if _, err := os.Stat(filepath.Join(sd, "agent-history.nogit")); err != nil {
		t.Fatalf("warn marker missing: %v", err)
	}
}

func TestProjectStoreAndSnapshotAll(t *testing.T) {
	home, _ := setup(t)
	write(t, filepath.Join(home, ".claude", "agents", "g.md"), "g")
	repo := filepath.Join(t.TempDir(), "proj")
	write(t, filepath.Join(repo, ".claude", "agents", "p.md"), "p")
	write(t, filepath.Join(repo, ".gitignore"), ".claude/agents\n")
	wt := filepath.Join(repo, ".worktrees", "w")
	write(t, filepath.Join(wt, ".claude", "agents", "w.md"), "w")

	// The project store lives under the state dir, outside the temp-confined
	// check's root only for the repo work tree, so relax the seam.
	runGit = func(env []string, stdin string, args ...string) ([]byte, error) {
		return defaultRunGit(env, stdin, args...)
	}
	SnapshotAll("housekeeping: edit", []string{repo, wt, repo})

	ps, _ := ProjectStore(repo)
	files, err := ps.git(nil, "", "ls-files")
	if err != nil || strings.TrimSpace(string(files)) != ".claude/agents/p.md" {
		t.Fatalf("project files = %q err=%v", files, err)
	}
	wps, _ := ProjectStore(wt)
	if wps.GitDir != ps.GitDir || wps.WorkTree != ps.WorkTree {
		t.Errorf("worktree got its own store: %s vs %s", wps.GitDir, ps.GitDir)
	}
	hs, _ := HomeStore()
	if l, err := hs.Log("", 0); err != nil || len(l) != 1 {
		t.Fatalf("home log = %v, %v", l, err)
	}
}

func TestLogBeforeInit(t *testing.T) {
	_, s := setup(t)
	if _, err := s.Log("", 0); err == nil {
		t.Fatal("want an error before the first snapshot")
	}
}

func TestConcurrentSnapshots(t *testing.T) {
	home, s := setup(t)
	write(t, filepath.Join(home, ".claude", "agents", "a.md"), "a")
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		go func() { _, err := s.Snapshot("t", ""); errs <- err }()
	}
	for i := 0; i < 6; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent snapshot: %v", err)
		}
	}
	if l, _ := s.Log("", 0); len(l) != 1 {
		t.Fatalf("want exactly one baseline commit, got %d", len(l))
	}
}

func TestRevertBaselineOnlyIsNotAChange(t *testing.T) {
	home, s := setup(t)
	p := filepath.Join(home, ".claude", "agents", "rev.md")
	write(t, p, "x")
	if _, err := s.Snapshot("t", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Revert("agents/rev.md", "", ""); err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("err = %v, want a baseline error", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("file removed: %v", err)
	}
}

func TestRevertUnknownRevDoesNotDelete(t *testing.T) {
	home, s := setup(t)
	p := filepath.Join(home, ".claude", "agents", "rev.md")
	write(t, p, "x")
	if _, err := s.Snapshot("t", ""); err != nil {
		t.Fatal(err)
	}
	for _, rev := range []string{"deadbeef~1", "deadbeef", "HEAD~5"} {
		if _, _, err := s.Revert("agents/rev.md", rev, ""); err == nil || !strings.Contains(err.Error(), "unknown revision") {
			t.Errorf("rev %q: err = %v, want unknown revision", rev, err)
		}
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("file removed: %v", err)
	}
	if _, _, err := s.Revert("agents", "", ""); err == nil {
		t.Error("directory revert accepted")
	}
}

func TestHomeStoreTracksClaudeMdAndSettings(t *testing.T) {
	home, s := setup(t)
	claude := filepath.Join(home, ".claude")
	write(t, filepath.Join(claude, "CLAUDE.md"), "rules\n")
	write(t, filepath.Join(claude, "settings.json"), "{}\n")
	if _, err := s.Snapshot("t", ""); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(claude, "CLAUDE.md"), "better rules\n")
	if _, err := s.Snapshot("edit", ""); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{"CLAUDE.md": "CLAUDE.md", "settings.json": "settings.json", "reviewer": "agents/reviewer.md"} {
		if got, err := s.Resolve(in); err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, _, err := s.Revert("CLAUDE.md", "", ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(claude, "CLAUDE.md")); string(b) != "rules\n" {
		t.Fatalf("CLAUDE.md = %q", b)
	}
}

func TestConfigStoreTracksOnlyConfigAndTemplates(t *testing.T) {
	home, _ := setup(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	cfgDir := filepath.Join(home, ".config", "agentctl")
	write(t, filepath.Join(cfgDir, "config.json"), "{}\n")
	write(t, filepath.Join(cfgDir, "templates", "t.md"), "tmpl\n")
	write(t, filepath.Join(cfgDir, "serve", "vapid.json"), "secret")
	write(t, filepath.Join(cfgDir, "token"), "secret")
	s, err := ConfigStore()
	if err != nil {
		t.Fatal(err)
	}
	if s.WorkTree != cfgDir {
		t.Fatalf("work tree = %s, want %s", s.WorkTree, cfgDir)
	}
	if _, err := s.Snapshot("t", ""); err != nil {
		t.Fatal(err)
	}
	files, _ := s.git(nil, "", "ls-files")
	if got := strings.Fields(string(files)); len(got) != 2 || got[0] != "config.json" || got[1] != "templates/t.md" {
		t.Fatalf("tracked = %v", got)
	}
	if got, err := s.Resolve("t"); err != nil || got != "templates/t.md" {
		t.Fatalf("Resolve(t) = %q, %v", got, err)
	}
	if got := StoreForPath(filepath.Join(cfgDir, "config.json")); got != NameConfig {
		t.Fatalf("StoreForPath = %q", got)
	}
	if got := StoreForPath("reviewer"); got != NameHome {
		t.Fatalf("StoreForPath(reviewer) = %q", got)
	}
}

func TestSnapshotBodyHeadAndChangedFiles(t *testing.T) {
	home, s := setup(t)
	agents := filepath.Join(home, ".claude", "agents")
	write(t, filepath.Join(agents, "a.md"), "1")
	write(t, filepath.Join(agents, "b.md"), "1")
	if _, err := s.Snapshot("start", ""); err != nil {
		t.Fatal(err)
	}
	start := s.Head()
	write(t, filepath.Join(agents, "a.md"), "2")
	if _, err := s.SnapshotBody("improve x", "a.md: clearer\n", ""); err != nil {
		t.Fatal(err)
	}
	files, err := s.ChangedFiles(start, s.Head())
	if err != nil || len(files) != 1 || files[0] != "agents/a.md" {
		t.Fatalf("files = %v, %v", files, err)
	}
	msg, _ := s.git(nil, "", "log", "-n1", "--format=%B")
	if !strings.HasPrefix(string(msg), "improve x\n\na.md: clearer") {
		t.Fatalf("message = %q", msg)
	}
	if _, err := s.ChangedFiles("--evil", "HEAD"); err == nil {
		t.Fatal("option-like revision accepted")
	}
}

func TestStoreDirsAreOwnerOnly(t *testing.T) {
	home, _ := setup(t)
	write(t, filepath.Join(home, ".config", "agentctl", "config.json"), "{}\n")
	t.Setenv("XDG_CONFIG_HOME", "")
	s, err := ConfigStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot("t", ""); err != nil {
		t.Fatal(err)
	}
	// An older, looser store is tightened on the next use.
	if err := os.Chmod(s.GitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, ".config", "agentctl", "config.json"), "{\"a\":1}\n")
	if _, err := s.Snapshot("t2", ""); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(s.GitDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("git dir mode = %v, %v", fi.Mode(), err)
	}
	filepath.WalkDir(filepath.Join(s.GitDir, "objects"), func(p string, d os.DirEntry, err error) error { //nolint:errcheck
		if err == nil && !d.IsDir() {
			if fi, _ := d.Info(); fi.Mode().Perm()&0o077 != 0 {
				t.Errorf("%s mode %v has group/other bits", p, fi.Mode())
			}
		}
		return nil
	})
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

func TestSnapshotSkipsNestedGitRepo(t *testing.T) {
	home, s := setup(t)
	write(t, filepath.Join(home, ".claude", "agents", "a.md"), "a")
	nested := filepath.Join(home, ".claude", "agents", "launcher")
	write(t, filepath.Join(nested, "README.md"), "x")
	gitInit(t, nested)
	rev, err := s.Snapshot("test", "")
	if err != nil || rev == "" {
		t.Fatalf("snapshot with nested repo: %q %v", rev, err)
	}
	files, err := s.git(nil, "", "ls-files")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(files)); got != "agents/a.md" {
		t.Fatalf("tracked = %q, want only agents/a.md", got)
	}
}

func TestSnapshotWithoutGlobalConfigOrIdentity(t *testing.T) {
	home, s := setup(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("EMAIL", "")
	write(t, filepath.Join(home, ".claude", "agents", "a.md"), "a")
	rev, err := s.Snapshot("test", "")
	if err != nil || rev == "" {
		t.Fatalf("snapshot: %q %v", rev, err)
	}
	if s.Head() != rev {
		t.Fatalf("head %q, want %q", s.Head(), rev)
	}
}

func TestLogNoSnapshotsYet(t *testing.T) {
	_, s := setup(t)
	if _, err := s.ensureInit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Log("", 0); !errors.Is(err, ErrNoSnapshots) {
		t.Fatalf("err = %v, want ErrNoSnapshots", err)
	}
}

func TestSkippedNestedReposLoggedOncePerDay(t *testing.T) {
	home, s := setup(t)
	write(t, filepath.Join(home, ".claude", "agents", "a.md"), "a")
	nested := filepath.Join(home, ".claude", "agents", "launcher")
	write(t, filepath.Join(nested, "README.md"), "x")
	gitInit(t, nested)

	var lines []string
	orig := logf
	logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { logf = orig })

	if got := s.SkippedRepos(); !slices.Equal(got, []string{"agents/launcher"}) {
		t.Fatalf("SkippedRepos = %q", got)
	}
	for i := 0; i < 3; i++ {
		write(t, filepath.Join(home, ".claude", "agents", "a.md"), fmt.Sprint("v", i))
		if _, err := s.Snapshot("test", ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "launcher") {
		t.Fatalf("log lines = %q, want one about launcher", lines)
	}
}

func TestSnapshotSkipsTrackedRootThatIsRepo(t *testing.T) {
	home, s := setup(t)
	write(t, filepath.Join(home, ".claude", "CLAUDE.md"), "c")
	agents := filepath.Join(home, ".claude", "agents")
	write(t, filepath.Join(agents, "a.md"), "a")
	gitInit(t, agents)
	rev, err := s.Snapshot("test", "")
	if err != nil || rev == "" {
		t.Fatalf("snapshot with repo root: %q %v", rev, err)
	}
	files, err := s.git(nil, "", "ls-files")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(files)); got != "CLAUDE.md" {
		t.Fatalf("tracked = %q, want only CLAUDE.md", got)
	}
	if got := s.SkippedRepos(); !slices.Equal(got, []string{"agents"}) {
		t.Fatalf("SkippedRepos = %q", got)
	}
}

func TestProjectStoreKeyResolvesSymlinksAndWorktrees(t *testing.T) {
	home, _ := setup(t)
	root := t.TempDir()
	repo := filepath.Join(root, "proj")
	write(t, filepath.Join(repo, ".claude", "agents", "p.md"), "p")
	write(t, filepath.Join(repo, ".worktrees", "w", ".claude", "agents", "p.md"), "p")
	link := filepath.Join(root, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	want, _ := ProjectStore(repo)
	for _, d := range []string{link, filepath.Join(repo, ".worktrees", "w"), filepath.Join(link, ".worktrees", "w")} {
		got, err := ProjectStore(d)
		if err != nil || got.GitDir != want.GitDir || got.WorkTree != want.WorkTree {
			t.Errorf("ProjectStore(%s) = %+v, %v; want %+v", d, got, err, want)
		}
	}

	// Path routing: project paths (any spelling) pick the project store, the
	// global agents dir stays on the home store.
	for _, p := range []string{
		filepath.Join(repo, ".claude", "agents", "p.md"),
		filepath.Join(link, ".claude", "agents", "p.md"),
		filepath.Join(repo, ".worktrees", "w", ".claude", "agents", "p.md"),
	} {
		which := StoreForPath(p)
		if which == NameHome || which == NameConfig {
			t.Fatalf("StoreForPath(%s) = %q", p, which)
		}
		s, err := StoreByName(which)
		if err != nil || s.GitDir != want.GitDir {
			t.Fatalf("StoreByName(%q) = %+v, %v", which, s, err)
		}
		rel, err := s.Resolve(p)
		if err != nil || rel != ".claude/agents/p.md" {
			t.Errorf("Resolve(%s) = %q, %v", p, rel, err)
		}
	}
	if got := StoreForPath(filepath.Join(home, ".claude", "agents", "a.md")); got != NameHome {
		t.Errorf("home path routed to %q", got)
	}
}

func TestProjectStoreKeepsLegacyKeyedStore(t *testing.T) {
	home, _ := setup(t)
	root := filepath.Dir(home) // inside the confined temp root
	repo := filepath.Join(root, "proj")
	write(t, filepath.Join(repo, ".claude", "agents", "p.md"), "p")
	link := filepath.Join(root, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	// A store made before keys were canonical: keyed by the spelling.
	sd, _ := stateDir()
	legacy := &Store{GitDir: projectGitDir(sd, link), WorkTree: link, Paths: []string{".claude/agents"}}
	if _, err := legacy.Snapshot("old", ""); err != nil {
		t.Fatal(err)
	}
	for _, via := range []string{link, StoreForPath(filepath.Join(link, ".claude", "agents", "p.md"))} {
		s, err := ProjectStore(via)
		if err != nil || s.GitDir != legacy.GitDir {
			t.Fatalf("ProjectStore(%s) = %+v, %v; want legacy gitdir %s", via, s, err, legacy.GitDir)
		}
		if l, err := s.Log("", 0); err != nil || len(l) != 1 {
			t.Fatalf("log via %s = %v, %v", via, l, err)
		}
	}
	ps := ProjectStores([]string{repo, link})
	if len(ps) != 1 || ps[0].GitDir != legacy.GitDir {
		t.Fatalf("ProjectStores = %+v", ps)
	}
}

func TestProjectStoresSkipsHome(t *testing.T) {
	home, _ := setup(t)
	if ps := ProjectStores([]string{home}); len(ps) != 0 {
		t.Fatalf("home dir got a project store: %+v", ps)
	}
}
