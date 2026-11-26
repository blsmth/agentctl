package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
)

// fakeOpener records the dirs it was asked to open instead of spawning
// `agentctl open`.
type fakeOpener struct {
	mu      sync.Mutex
	dirs    []string
	out     string
	err     error
	started chan struct{}
	block   chan struct{}
}

func (f *fakeOpener) open(dir string) (string, error) {
	f.mu.Lock()
	f.dirs = append(f.dirs, dir)
	out, err, started, block := f.out, f.err, f.started, f.block
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		<-block
	}
	if out == "" && err == nil {
		out = "agentctl-" + filepath.Base(dir) + "\n"
	}
	return out, err
}

func (f *fakeOpener) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dirs...)
}

func openPost(e *writeEnv, dir string) *http.Response {
	w := e.post("/v1/projects/open", fmt.Sprintf(`{"dir":%q}`, dir))
	return w.Result()
}

func decodeOpen(t *testing.T, r *http.Response) OpenProjectResult {
	t.Helper()
	var got OpenProjectResult
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestOpenProjectStartsSession(t *testing.T) {
	e := newWriteEnv(t, true)
	dir := t.TempDir()
	r := openPost(e, dir+"/")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status %d", r.StatusCode)
	}
	got := decodeOpen(t, r)
	want := OpenProjectResult{Session: "agentctl-" + filepath.Base(dir), Dir: dir, Running: false}
	if got != want {
		t.Errorf("result %+v, want %+v", got, want)
	}
	if calls := e.opener.all(); !slices.Equal(calls, []string{dir}) {
		t.Errorf("opened %v, want [%s]", calls, dir)
	}
	lines := e.auditLines(t)
	if len(lines) != 2 || lines[0].Action != "open-project" || lines[0].Project != dir || lines[1].Result != "ok" || lines[1].Session != want.Session {
		t.Errorf("audit = %+v", lines)
	}
}

func TestOpenProjectAlreadyRunning(t *testing.T) {
	e := newWriteEnv(t, true)
	e.opener.out = "agentctl-proj\n"
	r := openPost(e, t.TempDir())
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status %d", r.StatusCode)
	}
	if got := decodeOpen(t, r); got.Session != "agentctl-proj" || !got.Running {
		t.Errorf("result %+v, want the running agentctl-proj", got)
	}
}

func TestOpenProjectTildeExpands(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "code"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := userHomeDir
	t.Cleanup(func() { userHomeDir = orig })
	userHomeDir = func() (string, error) { return home, nil }
	e := newWriteEnv(t, true)
	if r := openPost(e, "~/code"); r.StatusCode != http.StatusOK {
		t.Fatalf("status %d", r.StatusCode)
	}
	if calls := e.opener.all(); !slices.Equal(calls, []string{filepath.Join(home, "code")}) {
		t.Errorf("opened %v", calls)
	}
}

func TestOpenProjectValidation(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, body, want string
	}{
		{"empty", `{"dir":"  "}`, "enter a project directory"},
		{"relative", `{"dir":"code/proj"}`, "not an absolute path"},
		{"dot", `{"dir":"."}`, "not an absolute path"},
		{"other user", `{"dir":"~bob/x"}`, "unsupported path"},
		{"missing", fmt.Sprintf(`{"dir":%q}`, filepath.Join(root, "gone")), "does not exist"},
		{"file", fmt.Sprintf(`{"dir":%q}`, file), "is not a directory"},
		{"unknown field", `{"dir":"/tmp","cmd":"x"}`, "invalid JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWriteEnv(t, true)
			w := e.post("/v1/projects/open", tt.body)
			if w.Code != http.StatusBadRequest || errCode(t, w) != codeBadRequest || !strings.Contains(w.Body.String(), tt.want) {
				t.Fatalf("status %d: %s, want 400 %q", w.Code, w.Body, tt.want)
			}
			if calls := e.opener.all(); len(calls) != 0 {
				t.Errorf("opened %v on a rejected request", calls)
			}
		})
	}
}

func TestOpenProjectSpawnError(t *testing.T) {
	e := newWriteEnv(t, true)
	e.opener.err = errors.New("tmux session agentctl-proj already belongs to /other/proj")
	w := e.post("/v1/projects/open", fmt.Sprintf(`{"dir":%q}`, t.TempDir()))
	if w.Code != http.StatusBadGateway || errCode(t, w) != codeActionFailed || !strings.Contains(w.Body.String(), "already belongs to /other/proj") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestOpenProjectOneAtATime(t *testing.T) {
	e := newWriteEnv(t, true)
	e.opener.started, e.opener.block = make(chan struct{}, 1), make(chan struct{})
	dir := t.TempDir()
	done := make(chan int)
	go func() { done <- e.post("/v1/projects/open", fmt.Sprintf(`{"dir":%q}`, dir)).Code }()
	<-e.opener.started
	w := e.post("/v1/projects/open", fmt.Sprintf(`{"dir":%q}`, dir))
	if w.Code != http.StatusConflict || errCode(t, w) != codeBusy {
		t.Errorf("second open: status %d: %s", w.Code, w.Body)
	}
	close(e.opener.block)
	if code := <-done; code != http.StatusOK {
		t.Errorf("first open: status %d", code)
	}
}

func TestOpenProjectFeature(t *testing.T) {
	for _, allow := range []bool{false, true} {
		e := newWriteEnv(t, allow)
		if has := slices.Contains(e.router.Features(), "open-project"); has != allow {
			t.Errorf("allow_write=%v: open-project feature = %v", allow, has)
		}
	}
}

// recentEnv is a write env whose closed ledger lists recs.
func recentEnv(t *testing.T, recs []closedlog.Record, loadErr error) (*writeEnv, *[]time.Time) {
	t.Helper()
	var since []time.Time
	e := newWriteEnvWith(t, false, func(o *Options) {
		o.ClosedRecords = func(s time.Time) ([]closedlog.Record, error) {
			since = append(since, s)
			return recs, loadErr
		}
	})
	return e, &since
}

func TestRecentProjects(t *testing.T) {
	home := t.TempDir()
	orig := userHomeDir
	t.Cleanup(func() { userHomeDir = orig })
	userHomeDir = func() (string, error) { return home, nil }
	mk := func(p string) string {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	app := mk(filepath.Join(home, "code", "app"))
	live := mk(filepath.Join(home, "code", "live"))
	wt := mk(filepath.Join(app, ".worktrees", "agent-x"))
	outside := mk(filepath.Join(t.TempDir(), "outside"))
	recs := []closedlog.Record{
		{ProjectDir: app}, {ProjectDir: live}, {ProjectDir: wt}, {ProjectDir: outside},
		{ProjectDir: filepath.Join(home, "gone")}, {ProjectDir: app},
	}
	// Read-only: the list is a read route, served without allow_write.
	e, since := recentEnv(t, recs, nil)
	g := writeGroup("idle", false, "go-engineer", "bare")
	g.Dir = live
	e.fleet.set(g)
	w := e.get("/v1/projects/recent")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var b recentProjectsBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	want := []recentProject{{Dir: app, Label: "~/code/app"}, {Dir: outside, Label: outside}}
	if b.Error != "" || !slices.Equal(b.Dirs, want) {
		t.Errorf("body %+v, want dirs %+v", b, want)
	}
	if len(*since) != 1 || !(*since)[0].Equal(newClock().Now().Add(-closedlog.RecentWindow)) {
		t.Errorf("ledger read since %v", *since)
	}
}

func TestRecentProjectsLedgerError(t *testing.T) {
	e, _ := recentEnv(t, nil, errors.New("boom"))
	w := e.get("/v1/projects/recent")
	var b recentProjectsBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || b.Error == "" || b.Dirs == nil || len(b.Dirs) != 0 {
		t.Errorf("status %d body %s", w.Code, w.Body)
	}
}

func TestRecentProjectsRequiresDevice(t *testing.T) {
	e, _ := recentEnv(t, nil, nil)
	if w := do(e.Handler(), "GET", "/v1/projects/recent"); w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
}

func TestTildeDir(t *testing.T) {
	for _, c := range []struct{ in, home, want string }{
		{"/h/u/x", "/h/u", "~/x"}, {"/h/u", "/h/u", "~"}, {"/h/user2", "/h/u", "/h/user2"}, {"/x", "", "/x"},
	} {
		if got := tildeDir(c.in, c.home); got != c.want {
			t.Errorf("tildeDir(%q,%q) = %q, want %q", c.in, c.home, got, c.want)
		}
	}
}

func TestSubprocessError(t *testing.T) {
	fallback := errors.New("exit status 1")
	for _, c := range []struct{ stderr, want string }{
		{"noise\nError: tmux session agentctl-x already belongs to /y\n", "tmux session agentctl-x already belongs to /y"},
		{"", "exit status 1"},
		{"plain message", "plain message"},
	} {
		if got := subprocessError(c.stderr, fallback).Error(); got != c.want {
			t.Errorf("subprocessError(%q) = %q, want %q", c.stderr, got, c.want)
		}
	}
}
