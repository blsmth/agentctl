package serve

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// testRuntimeDir returns <tmp>/proj/.agentctl.
func testRuntimeDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "proj", ".agentctl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

type sessionGlobals struct {
	runtimeDir, session, windowsDir, configPath string
}

// normalizedGlobals re-derives the windowstate and config paths from the
// current paths runtime dir (what withSession's restore does) and returns
// the globals: the baseline a restored state must equal.
func normalizedGlobals(t *testing.T) sessionGlobals {
	t.Helper()
	windowstate.Init()
	if err := config.Init(); err != nil {
		t.Fatal(err)
	}
	return currentGlobals()
}

func currentGlobals() sessionGlobals {
	return sessionGlobals{paths.RuntimeDir(), tmux.SessionName, windowstate.Dir(), config.FilePath()}
}

func TestWithSessionBindsAndRestores(t *testing.T) {
	rt := testRuntimeDir(t)
	before := normalizedGlobals(t)

	var inside sessionGlobals
	err := withSession("agentctl-proj", rt, func() error {
		inside = currentGlobals()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := sessionGlobals{rt, "agentctl-proj", filepath.Join(rt, "windows"), filepath.Join(rt, "config.json")}
	if inside != want {
		t.Errorf("inside fn = %+v, want %+v", inside, want)
	}
	if after := currentGlobals(); after != before {
		t.Errorf("after = %+v, want restored %+v", after, before)
	}

	// fn's error is returned and the globals are still restored.
	boom := errors.New("boom")
	if err := withSession("agentctl-proj", rt, func() error { return boom }); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
	if after := currentGlobals(); after != before {
		t.Errorf("after error = %+v, want restored %+v", after, before)
	}
}

// When config.Init fails fn must not run (it would act on a half-bound
// session) and the globals are restored.
func TestWithSessionConfigInitError(t *testing.T) {
	rt := testRuntimeDir(t)
	before := normalizedGlobals(t)
	t.Setenv("HOME", "") // config.GlobalDir -> os.UserHomeDir fails
	t.Setenv("XDG_CONFIG_HOME", "")
	called := false
	err := withSession("agentctl-proj", rt, func() error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("err = %v, called = %v; want an error and fn not run", err, called)
	}
	if after := currentGlobals(); after.runtimeDir != before.runtimeDir || after.session != before.session || after.windowsDir != before.windowsDir {
		t.Errorf("after = %+v, want restored %+v", after, before)
	}
}

// Regression: dispatch held globalsMu's write lock across `gh repo view`, so
// a hung gh stalled every fleet read (SSE, /v1/fleet, push, resolveWindow).
func TestDispatchSlowRepoLookupDoesNotBlockReads(t *testing.T) {
	rt := testRuntimeDir(t)
	release := make(chan struct{})
	inLookup := make(chan struct{})
	var mu sync.Mutex
	var got dispatch.AdhocRequest
	var order []string
	d := launchDispatcher{
		repoSlug: func(string) (string, error) {
			mu.Lock()
			order = append(order, "slug")
			mu.Unlock()
			close(inLookup)
			<-release
			return "o/r", nil
		},
		checkRuntimeDir: func(string) error {
			mu.Lock()
			order = append(order, "check")
			mu.Unlock()
			return nil
		},
		validateAgent: func(string, string) error { return nil },
		launch: func(req dispatch.AdhocRequest) (dispatch.Result, error) {
			mu.Lock()
			got = req
			order = append(order, "launch")
			mu.Unlock()
			return dispatch.Result{WindowID: "@9", Branch: "b"}, nil
		},
	}
	done := make(chan error, 1)
	go func() {
		_, err := d.Dispatch(DispatchRequest{Agent: "go-engineer", Prompt: "p", Dir: filepath.Dir(rt), Session: "agentctl-proj", RuntimeDir: rt})
		done <- err
	}()
	<-inLookup

	reads := make(chan error, 1)
	go func() {
		_, err := lockedFleet{src: &fakeFleet{groups: []fleetwatch.Group{{Session: "s"}}}}.Fleet()
		reads <- err
	}()
	select {
	case err := <-reads:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("fleet read blocked while the repo lookup ran")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.RepoSlug != "o/r" || !got.RepoSlugKnown || !got.Detached || !got.UseWorktree {
		t.Errorf("launch request = %+v", got)
	}
	if len(order) != 3 || order[2] != "launch" {
		t.Errorf("order = %v, want checks before launch", order)
	}
}

func TestDispatchRepoLookupFailureIsNotFatal(t *testing.T) {
	rt := testRuntimeDir(t)
	var got dispatch.AdhocRequest
	d := launchDispatcher{
		repoSlug:        func(string) (string, error) { return "", errors.New("not a GitHub repo") },
		checkRuntimeDir: func(string) error { return nil },
		validateAgent:   func(string, string) error { return nil },
		launch: func(req dispatch.AdhocRequest) (dispatch.Result, error) {
			got = req
			return dispatch.Result{WindowID: "@9"}, nil
		},
	}
	if _, err := d.Dispatch(DispatchRequest{Agent: "a", Prompt: "p", Dir: filepath.Dir(rt), Session: "agentctl-proj", RuntimeDir: rt}); err != nil {
		t.Fatal(err)
	}
	if got.RepoSlug != "" || !got.RepoSlugKnown {
		t.Errorf("launch request = %+v, want a known empty slug", got)
	}
}

func TestDispatchRefusesUnsafeRuntimeDirBeforeLaunch(t *testing.T) {
	rt := testRuntimeDir(t)
	launched := false
	d := launchDispatcher{
		repoSlug:        func(string) (string, error) { return "o/r", nil },
		checkRuntimeDir: func(string) error { return errors.New("tracked") },
		validateAgent:   func(string, string) error { return nil },
		launch: func(dispatch.AdhocRequest) (dispatch.Result, error) {
			launched = true
			return dispatch.Result{}, nil
		},
	}
	if _, err := d.Dispatch(DispatchRequest{Agent: "a", Prompt: "p", Dir: filepath.Dir(rt), Session: "agentctl-proj", RuntimeDir: rt}); err == nil || launched {
		t.Errorf("err = %v, launched = %v", err, launched)
	}
}
