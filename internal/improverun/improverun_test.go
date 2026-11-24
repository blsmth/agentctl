package improverun

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/agenthistory"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

type env struct {
	home, cfg string
	notified  []notify.Options
	published []string
}

// setup isolates HOME, state and config dirs and captures both notifications.
func setup(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	e := &env{home: filepath.Join(root, "home"), cfg: filepath.Join(root, "home", ".config", "agentctl")}
	t.Setenv("HOME", e.home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", "")
	oldN, oldP := notifyDesktop, publishPhone
	notifyDesktop = func(o notify.Options) { e.notified = append(e.notified, o) }
	publishPhone = func(path string, r Run) error {
		b, err := os.ReadFile(path)
		e.published = append(e.published, r.Title()+"\n"+string(b))
		return err
	}
	t.Cleanup(func() { notifyDesktop, publishPhone = oldN, oldP })
	write(t, filepath.Join(e.home, ".claude", "agents", "a.md"), "v1\n")
	write(t, filepath.Join(e.cfg, "config.json"), "{}\n")
	return e
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

func TestRunWithChangesNotifiesOnce(t *testing.T) {
	e := setup(t)
	if err := Start(StartOptions{ID: "r1", Agent: "brandon", Fleet: true}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(e.home, ".claude", "agents", "a.md"), "v2\n")
	write(t, filepath.Join(e.cfg, "templates", "t.md"), "new\n")
	sp, _ := SummaryPath("r1")
	write(t, sp, "a.md: clearer rules (sessions abc123)\n")

	if err := Finish("r1"); err != nil {
		t.Fatal(err)
	}
	if err := Finish("r1"); err != nil { // idempotent
		t.Fatal(err)
	}
	if len(e.notified) != 1 || len(e.published) != 1 {
		t.Fatalf("notified %d, published %d; want 1 each", len(e.notified), len(e.published))
	}
	n := e.notified[0]
	if n.Title != "Fleet improve: 2 changes" {
		t.Errorf("title = %q", n.Title)
	}
	for _, want := range []string{"a.md", "config", "agentctl agents revert", "agentctl improve runs"} {
		if !strings.Contains(n.Message+e.published[0], want) {
			t.Errorf("notification missing %q:\n%s\n%s", want, n.Message, e.published[0])
		}
	}
	if !strings.Contains(e.published[0], "clearer rules (sessions abc123)") {
		t.Errorf("report lacks the rationale:\n%s", e.published[0])
	}

	r, err := Load("r1")
	if err != nil || r.Finished.IsZero() || len(r.Files) != 2 || len(r.Revs) != 2 || len(r.Reverts) != 2 {
		t.Fatalf("run = %+v, %v", r, err)
	}
	// The end commit carries the rationale, and the revert command works.
	home, _ := agenthistory.HomeStore()
	msg, err := exec.Command("git", "--git-dir="+home.GitDir, "log", "-n1", "--format=%B").Output()
	if err != nil || !strings.HasPrefix(string(msg), "improve r1\n\na.md: clearer rules") {
		t.Fatalf("commit message = %q, %v", msg, err)
	}
	runs, err := List(time.Time{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("List = %v, %v", runs, err)
	}
}

func TestRunWithoutChangesIsQuiet(t *testing.T) {
	e := setup(t)
	if err := Start(StartOptions{ID: "r2", Agent: "go-engineer"}); err != nil {
		t.Fatal(err)
	}
	if !Active() {
		t.Error("run should be active before Finish")
	}
	if err := Finish("r2"); err != nil {
		t.Fatal(err)
	}
	if len(e.notified)+len(e.published) != 0 {
		t.Fatalf("no-change run notified: %v %v", e.notified, e.published)
	}
	if Active() {
		t.Error("finished run still active")
	}
	r, _ := Load("r2")
	if r.Finished.IsZero() || len(r.Files) != 0 {
		t.Fatalf("run = %+v", r)
	}
}

func TestStartInitialisesStoresOnDemand(t *testing.T) {
	e := setup(t)
	if err := Start(StartOptions{ID: "r3", Agent: "x"}); err != nil {
		t.Fatal(err)
	}
	r, _ := Load("r3")
	if len(r.Stores) != 2 || r.Stores[0].StartRev == "" || r.Stores[1].StartRev == "" {
		t.Fatalf("stores = %+v", r.Stores)
	}
	_ = e
}

func TestFinishUnknownRunIsNoop(t *testing.T) {
	setup(t)
	if err := Finish("nope"); err != nil {
		t.Fatal(err)
	}
	if err := Finish(""); err != nil {
		t.Fatal(err)
	}
	if err := Finish("../x"); err == nil {
		t.Fatal("path-like run id accepted")
	}
}

func TestFinishStaleClosesAbandonedRuns(t *testing.T) {
	e := setup(t)
	if err := Start(StartOptions{ID: "r4", Agent: "x"}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(e.home, ".claude", "CLAUDE.md"), "rules\n")
	FinishStale() // too young
	if r, _ := Load("r4"); !r.Finished.IsZero() {
		t.Fatal("young run finished")
	}
	old := now
	now = func() time.Time { return time.Now().Add(StaleAfter + time.Minute) }
	t.Cleanup(func() { now = old })
	if Active() {
		t.Error("stale run counted as active")
	}
	FinishStale()
	r, _ := Load("r4")
	if r.Finished.IsZero() || len(r.Files) != 1 || !strings.HasSuffix(r.Files[0], "CLAUDE.md") {
		t.Fatalf("run = %+v", r)
	}
	if len(e.notified) != 1 {
		t.Fatalf("notified %d", len(e.notified))
	}
}

func TestTitle(t *testing.T) {
	for _, tc := range []struct {
		r    Run
		want string
	}{
		{Run{Fleet: true, Files: []string{"a"}}, "Fleet improve: 1 change"},
		{Run{Agent: "go-engineer", Files: []string{"a", "b", "c"}}, "Improve go-engineer: 3 changes"},
	} {
		if got := tc.r.Title(); got != tc.want {
			t.Errorf("Title = %q, want %q", got, tc.want)
		}
	}
}

// A run.json edited to point at another repo must not steer Finish.
func TestFinishIgnoresStoredStore(t *testing.T) {
	e := setup(t)
	if err := Start(StartOptions{ID: "r5", Agent: "x"}); err != nil {
		t.Fatal(err)
	}
	dir, _ := Dir("r5")
	p := filepath.Join(dir, "run.json")
	data, _ := os.ReadFile(p)
	evil := t.TempDir()
	forged := strings.Replace(string(data), `"name": "agents"`, `"name": "agents", "store": {"git_dir": "`+evil+`", "work_tree": "`+evil+`", "paths": ["."]}`, 1)
	if err := os.WriteFile(p, []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(e.home, ".claude", "agents", "a.md"), "v2\n")
	if err := Finish("r5"); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(evil); len(ents) != 0 {
		t.Fatalf("forged store was used: %v", ents)
	}
	if r, _ := Load("r5"); len(r.Files) != 1 {
		t.Fatalf("run = %+v", r)
	}
}

// stubLive replaces the window check and the clock for one test.
func stubLive(t *testing.T, live bool, age time.Duration) {
	t.Helper()
	oldL, oldN := windowLive, now
	windowLive = func(Run) bool { return live }
	now = func() time.Time { return time.Now().Add(age) }
	t.Cleanup(func() { windowLive, now = oldL, oldN })
}

func TestFinishStaleSkipsLiveWindow(t *testing.T) {
	e := setup(t)
	if err := Start(StartOptions{ID: "live1", Agent: "x", Fleet: true}); err != nil {
		t.Fatal(err)
	}
	if err := RecordWindow("live1", "@9", "sess", "/rt"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(e.home, ".claude", "CLAUDE.md"), "rules\n")

	stubLive(t, true, 5*time.Hour) // past StaleAfter, window alive
	FinishStale()
	if r, _ := Load("live1"); !r.Finished.IsZero() {
		t.Fatal("run with a live window was closed after 5h")
	}
	if !Active() {
		t.Error("live run not active")
	}

	stubLive(t, true, MaxLive+time.Minute) // past the cap
	FinishStale()
	if r, _ := Load("live1"); r.Finished.IsZero() {
		t.Fatal("run past MaxLive was not closed")
	}
}

func TestFinishStaleClosesRunWhoseWindowIsGone(t *testing.T) {
	setup(t)
	if err := Start(StartOptions{ID: "gone1", Agent: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := RecordWindow("gone1", "@9", "sess", "/rt"); err != nil {
		t.Fatal(err)
	}
	stubLive(t, false, StaleAfter+time.Minute)
	FinishStale()
	if r, _ := Load("gone1"); r.Finished.IsZero() {
		t.Fatal("run with a dead window was not closed")
	}
}

func TestCheckNoLiveRun(t *testing.T) {
	setup(t)
	stubLive(t, true, 0)
	if err := CheckNoLiveRun(); err != nil {
		t.Fatalf("no runs: %v", err)
	}
	if err := Start(StartOptions{ID: "a1", Agent: "x"}); err != nil {
		t.Fatal(err)
	}
	// Window not recorded yet: young run counts.
	if err := CheckNoLiveRun(); !errors.Is(err, ErrRunActive) {
		t.Fatalf("err = %v, want ErrRunActive", err)
	}
	if err := Finish("a1"); err != nil {
		t.Fatal(err)
	}
	if err := CheckNoLiveRun(); err != nil {
		t.Fatalf("after finish: %v", err)
	}
}

func TestPruneRemovesOldFinishedRuns(t *testing.T) {
	setup(t)
	for _, id := range []string{"old", "new", "oldlive"} {
		if err := Start(StartOptions{ID: id, Agent: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"old", "new"} {
		if err := Finish(id); err != nil {
			t.Fatal(err)
		}
	}
	dir, _ := Dir("old")
	r, _ := load(dir)
	r.Started = time.Now().Add(-MaxAge - 24*time.Hour)
	if err := save(dir, r); err != nil {
		t.Fatal(err)
	}
	dir, _ = Dir("oldlive")
	r, _ = load(dir)
	r.Started = time.Now().Add(-MaxAge - 24*time.Hour)
	if err := save(dir, r); err != nil {
		t.Fatal(err)
	}
	n, err := Prune(time.Now(), MaxAge)
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v; want 1", n, err)
	}
	if _, err := Load("old"); err == nil {
		t.Error("old run kept")
	}
	for _, id := range []string{"new", "oldlive"} {
		if _, err := Load(id); err != nil {
			t.Errorf("run %s removed: %v", id, err)
		}
	}
}

// A project's own .claude/agents is a store of the run, whether or not the
// run's own dirs name it: an edit there is recorded with its revert command.
func TestRunSnapshotsKnownProjectStores(t *testing.T) {
	e := setup(t)
	proj := filepath.Join(t.TempDir(), "proj")
	write(t, filepath.Join(proj, ".claude", "agents", "brandon.md"), "v1\n")
	old := knownDirs
	knownDirs = func(extra []string) []string { return append(extra, proj) }
	t.Cleanup(func() { knownDirs = old })

	if err := Start(StartOptions{ID: "rp", Agent: "fleet-improver", Fleet: true}); err != nil {
		t.Fatal(err)
	}
	r, _ := Load("rp")
	if len(r.Stores) != 3 {
		t.Fatalf("stores = %+v", r.Stores)
	}
	write(t, filepath.Join(proj, ".claude", "agents", "brandon.md"), "v2\n")
	if err := Finish("rp"); err != nil {
		t.Fatal(err)
	}
	r, _ = Load("rp")
	canon := agenthistory.CanonicalRoot(proj)
	wantFile := filepath.Join(canon, ".claude", "agents", "brandon.md")
	if len(r.Files) != 1 || r.Files[0] != wantFile {
		t.Fatalf("files = %v, want [%s]", r.Files, wantFile)
	}
	if len(r.Reverts) != 1 || !strings.Contains(r.Reverts[0], "--store "+canon) || !strings.Contains(r.Reverts[0], ".claude/agents/brandon.md") {
		t.Fatalf("reverts = %v", r.Reverts)
	}
	if len(e.notified) != 1 || !strings.Contains(e.published[0], "brandon.md") {
		t.Fatalf("summary lacks the project file: %v", e.published)
	}
	// The recorded revert command works against the project store.
	st, err := agenthistory.StoreByName(canon)
	if err != nil {
		t.Fatal(err)
	}
	path, err := st.Resolve(".claude/agents/brandon.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Revert(path, r.Stores[2].StartRev, ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(proj, ".claude", "agents", "brandon.md")); string(b) != "v1\n" {
		t.Fatalf("after revert = %q", b)
	}
}
