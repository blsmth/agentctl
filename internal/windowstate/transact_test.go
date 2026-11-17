package windowstate_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/windowstate"
)

const (
	helperEnvDir = "WINDOWSTATE_TEST_DIR"
	helperEnvN   = "WINDOWSTATE_TEST_N"
)

// TestMain doubles as the subprocess worker for TestTransactAcrossProcesses.
func TestMain(m *testing.M) {
	if dir := os.Getenv(helperEnvDir); dir != "" {
		windowstate.SetDirForTest(dir)
		n, _ := strconv.Atoi(os.Getenv(helperEnvN))
		for i := 0; i < n; i++ {
			if _, _, err := windowstate.Transact("@1", func(ws *windowstate.WindowState) { ws.SubagentCount++ }); err != nil {
				os.Exit(1)
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestTransactGoroutinesNoLostUpdates(t *testing.T) {
	defer initTemp(t)()
	const n = 40
	var mu sync.Mutex
	seen := map[int]bool{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			old, updated, err := windowstate.Transact("@1", func(ws *windowstate.WindowState) { ws.SubagentCount++ })
			if err != nil {
				t.Error(err)
				return
			}
			if updated.SubagentCount != old.SubagentCount+1 {
				t.Errorf("inconsistent pair old=%d new=%d", old.SubagentCount, updated.SubagentCount)
			}
			mu.Lock()
			if seen[old.SubagentCount] {
				t.Errorf("old value %d observed twice", old.SubagentCount)
			}
			seen[old.SubagentCount] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	ws, _ := windowstate.Read("@1")
	if ws.SubagentCount != n {
		t.Fatalf("count = %d, want %d", ws.SubagentCount, n)
	}
}

func TestTransactAcrossProcesses(t *testing.T) {
	defer initTemp(t)()
	dir := windowstate.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const procs, per = 4, 15
	var wg sync.WaitGroup
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), helperEnvDir+"="+dir, helperEnvN+"="+strconv.Itoa(per))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("subprocess: %v: %s", err, out)
			}
		}()
	}
	wg.Wait()
	ws, _ := windowstate.Read("@1")
	if ws.SubagentCount != procs*per {
		t.Fatalf("count = %d, want %d", ws.SubagentCount, procs*per)
	}
}

func TestUnknownFieldsSurviveUpdate(t *testing.T) {
	defer initTemp(t)()
	dir := windowstate.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	in := `{"state":"idle","future_field":{"a":[1,2]},"other":"x"}`
	if err := os.WriteFile(filepath.Join(dir, "@1.json"), []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := windowstate.Update("@1", func(ws *windowstate.WindowState) { ws.State = "working" }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "@1.json"))
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["future_field"]) != `{"a":[1,2]}` || string(got["other"]) != `"x"` {
		t.Errorf("unknown keys lost: %s", data)
	}
	if string(got["state"]) != `"working"` {
		t.Errorf("known field not updated: %s", data)
	}
}

func TestTombstone(t *testing.T) {
	setup := func(t *testing.T) string {
		t.Helper()
		t.Cleanup(initTemp(t))
		if err := windowstate.Update("@1", func(ws *windowstate.WindowState) { ws.State = "idle" }); err != nil {
			t.Fatal(err)
		}
		windowstate.Remove("@1")
		return windowstate.Dir()
	}
	noJSON := func(t *testing.T, dir string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(dir, "@1.json")); err == nil {
			t.Error("state file exists")
		}
	}

	t.Run("remove then update is gone", func(t *testing.T) {
		dir := setup(t)
		err := windowstate.Update("@1", func(ws *windowstate.WindowState) { ws.State = "working" })
		if !errors.Is(err, windowstate.ErrGone) {
			t.Fatalf("err = %v, want ErrGone", err)
		}
		noJSON(t, dir)
	})
	t.Run("revive then update works", func(t *testing.T) {
		setup(t)
		windowstate.Revive("@1")
		if err := windowstate.Update("@1", func(ws *windowstate.WindowState) { ws.State = "working" }); err != nil {
			t.Fatal(err)
		}
		if ws, _ := windowstate.Read("@1"); ws.State != "working" {
			t.Errorf("state = %q", ws.State)
		}
	})
	t.Run("expired tombstone ignored and deleted", func(t *testing.T) {
		dir := setup(t)
		gone := filepath.Join(dir, "@1.gone")
		old := time.Now().Add(-2 * windowstate.TombstoneTTL)
		if err := os.WriteFile(gone, []byte(old.UTC().Format(time.RFC3339Nano)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := windowstate.Update("@1", func(ws *windowstate.WindowState) { ws.State = "working" }); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(gone); err == nil {
			t.Error("expired tombstone not deleted")
		}
	})
	t.Run("All and FindBySessionID ignore tombstones", func(t *testing.T) {
		dir := setup(t)
		if len(windowstate.All()) != 0 {
			t.Errorf("All returned entries: %v", windowstate.All())
		}
		if windowstate.FindBySessionID("x") != "" {
			t.Error("FindBySessionID matched")
		}
		if _, err := os.Stat(filepath.Join(dir, "@1.gone")); err != nil {
			t.Errorf("tombstone missing: %v", err)
		}
	})
	t.Run("RemoveAll removes tombstones", func(t *testing.T) {
		dir := setup(t)
		if err := windowstate.RemoveAll(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "@1.gone")); err == nil {
			t.Error("tombstone survived RemoveAll")
		}
	})
}

func TestTransactStampsStateSince(t *testing.T) {
	defer initTemp(t)()
	write := func(fn func(*windowstate.WindowState)) windowstate.WindowState {
		t.Helper()
		_, ws, err := windowstate.Transact("@1", fn)
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}

	before := time.Now()
	ws := write(func(ws *windowstate.WindowState) { ws.State = "working" })
	if ws.StateSince.Before(before) {
		t.Fatalf("StateSince = %v, want stamped on the state change", ws.StateSince)
	}
	first := ws.StateSince

	ws = write(func(ws *windowstate.WindowState) { ws.LastToolDetail = "Editing a.go" })
	if !ws.StateSince.Equal(first) {
		t.Errorf("a write that keeps State must not move StateSince: %v -> %v", first, ws.StateSince)
	}
	ws = write(func(ws *windowstate.WindowState) { ws.State = "working" })
	if !ws.StateSince.Equal(first) {
		t.Errorf("re-writing the same State must not move StateSince")
	}

	time.Sleep(2 * time.Millisecond)
	ws = write(func(ws *windowstate.WindowState) { ws.State = "blocked" })
	if !ws.StateSince.After(first) {
		t.Errorf("a State change must move StateSince forward: %v -> %v", first, ws.StateSince)
	}
	if got, _ := windowstate.Read("@1"); !got.StateSince.Equal(ws.StateSince) {
		t.Errorf("StateSince not persisted: %v vs %v", got.StateSince, ws.StateSince)
	}
}
