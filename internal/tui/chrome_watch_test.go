package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// watchBackends are the dirWatcher implementations under test: fsnotify
// (Linux, and the macOS fallback) and the platform one (kqueue on macOS).
var watchBackends = []struct {
	name string
	new  func() (dirWatcher, error)
}{
	{"fsnotify", newFsnotifyWatcher},
	{"platform", newPlatformWatcher},
}

// forEachBackend runs fn as a subtest per watcher backend.
func forEachBackend(t *testing.T, fn func(t *testing.T, newW func() (dirWatcher, error))) {
	t.Helper()
	for _, b := range watchBackends {
		t.Run(b.name, func(t *testing.T) { fn(t, b.new) })
	}
}

// recvWatch reads one message from ch with a timeout.
func recvWatch(t *testing.T, ch chan tea.Msg, d time.Duration) (tea.Msg, bool) {
	t.Helper()
	select {
	case msg := <-ch:
		return msg, true
	case <-time.After(d):
		return nil, false
	}
}

// startTestWatcher starts a dirWatch with an explicit debounce. The watches
// are registered before it returns, so writes made afterwards are never
// missed and tests need no startup sleep. The watcher closes on cleanup.
func startTestWatcher(t *testing.T, newW func() (dirWatcher, error), runtimeDir string, debounce time.Duration) chan tea.Msg {
	t.Helper()
	w, err := newW()
	if err != nil {
		t.Skipf("watcher unavailable: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	dw, err := newDirWatch(w, runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan tea.Msg, 1)
	go dw.run(ch, debounce)
	return ch
}

// watchWait is how long to wait for a message that should arrive after one
// debounce window, with generous slack for slow runners.
func watchWait(debounce time.Duration) time.Duration {
	return 5*debounce + 2*time.Second
}

// renameWrite writes data to path the way agentctl's writers do.
func renameWrite(t *testing.T, path, data string) {
	t.Helper()
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// TestChromeBarWatcherActiveWindow: the persistent watcher delivers an
// activeWindowChangedMsg when the active-window file is written.
func TestChromeBarWatcherActiveWindow(t *testing.T) {
	forEachBackend(t, func(t *testing.T, newW func() (dirWatcher, error)) {
		f := newChromeFixture(t, nil)
		if err := os.MkdirAll(filepath.Join(f.dir, "windows"), 0o755); err != nil {
			t.Fatal(err)
		}
		const debounce = chromeBarDebounce
		ch := startTestWatcher(t, newW, f.dir, debounce)
		f.setActive(t, "@3")

		msg, ok := recvWatch(t, ch, watchWait(debounce))
		if !ok {
			t.Fatal("no message for active-window write")
		}
		if _, isActive := msg.(activeWindowChangedMsg); !isActive {
			t.Errorf("got %T, want activeWindowChangedMsg", msg)
		}
	})
}

// TestChromeBarWatcherDebouncesBurst: N rapid windowstate writes yield one
// windowsChangedMsg (and so one rebuild), not N. Existing records do not
// trigger a relist; a brand new record does.
func TestChromeBarWatcherDebouncesBurst(t *testing.T) {
	forEachBackend(t, func(t *testing.T, newW func() (dirWatcher, error)) {
		f := newChromeFixture(t, nil)
		f.writeRecord(t, "@1", `{"state":"idle"}`)
		// The debounce dwarfs any realistic burst duration, so even a starved
		// CI runner finishes all writes inside one window.
		const debounce = time.Second
		ch := startTestWatcher(t, newW, f.dir, debounce)

		const n = 25
		for i := 0; i < n; i++ {
			f.writeRecord(t, "@1", fmt.Sprintf(`{"state":"working","turn_count":%d}`, i))
		}
		msg, ok := recvWatch(t, ch, watchWait(debounce))
		if !ok {
			t.Fatal("no message after burst")
		}
		wc, isWC := msg.(windowsChangedMsg)
		if !isWC {
			t.Fatalf("got %T, want windowsChangedMsg", msg)
		}
		if wc.relist {
			t.Error("writes to an existing record must not request a relist")
		}
		if extra, got := recvWatch(t, ch, 2*debounce); got {
			t.Errorf("burst of %d writes produced a second message: %#v", n, extra)
		}

		// A new record file is a window being created: relist.
		f.writeRecord(t, "@2", `{"state":"idle"}`)
		msg, ok = recvWatch(t, ch, watchWait(debounce))
		if !ok {
			t.Fatal("no message after new record")
		}
		if wc, _ := msg.(windowsChangedMsg); !wc.relist {
			t.Errorf("new record: got %#v, want relist", msg)
		}

		// A record removed is a window gone: relist.
		if err := os.Remove(filepath.Join(f.dir, "windows", "@2.json")); err != nil {
			t.Fatal(err)
		}
		msg, ok = recvWatch(t, ch, watchWait(debounce))
		if !ok {
			t.Fatal("no message after removed record")
		}
		if wc, _ := msg.(windowsChangedMsg); !wc.relist {
			t.Errorf("removed record: got %#v, want relist", msg)
		}
	})
}

// TestChromeBarWatcherPersistentTwoEvents: two separate writes produce two
// messages on the same channel (the watcher is not recreated).
func TestChromeBarWatcherPersistentTwoEvents(t *testing.T) {
	forEachBackend(t, func(t *testing.T, newW func() (dirWatcher, error)) {
		f := newChromeFixture(t, nil)
		f.writeRecord(t, "@1", `{"state":"idle"}`)
		const debounce = chromeBarDebounce
		ch := startTestWatcher(t, newW, f.dir, debounce)

		for i, body := range []string{`{"state":"working"}`, `{"state":"idle"}`} {
			f.writeRecord(t, "@1", body)
			msg, ok := recvWatch(t, ch, watchWait(debounce))
			if !ok {
				t.Fatalf("write %d: no message", i+1)
			}
			if _, isWC := msg.(windowsChangedMsg); !isWC {
				t.Errorf("write %d: got %T, want windowsChangedMsg", i+1, msg)
			}
		}
	})
}

// TestChromeBarWatcherIgnoresUnrelatedFiles: the monitor rewrites state.json
// in the runtime dir every 500ms. Those writes must never reach the Bubble
// Tea program, which re-renders on every message.
func TestChromeBarWatcherIgnoresUnrelatedFiles(t *testing.T) {
	forEachBackend(t, func(t *testing.T, newW func() (dirWatcher, error)) {
		f := newChromeFixture(t, nil)
		f.writeRecord(t, "@1", `{"state":"idle"}`)
		f.setActive(t, "@1")
		const debounce = 50 * time.Millisecond
		ch := startTestWatcher(t, newW, f.dir, debounce)

		for i := 0; i < 20; i++ {
			renameWrite(t, filepath.Join(f.dir, "state.json"), fmt.Sprintf(`{"tick":%d}`, i))
		}
		if msg, got := recvWatch(t, ch, 10*debounce); got {
			t.Errorf("state.json writes produced %#v, want no message", msg)
		}
	})
}

// TestChromeBarWatcherWindowsDirCreatedLater: a bar started before any
// window wrote state still sees records once windows/ appears.
func TestChromeBarWatcherWindowsDirCreatedLater(t *testing.T) {
	forEachBackend(t, func(t *testing.T, newW func() (dirWatcher, error)) {
		f := newChromeFixture(t, nil)
		const debounce = chromeBarDebounce
		ch := startTestWatcher(t, newW, f.dir, debounce)

		f.writeRecord(t, "@1", `{"state":"working"}`) // creates windows/
		deadline := time.After(watchWait(debounce))
		for {
			select {
			case msg := <-ch:
				if wc, ok := msg.(windowsChangedMsg); ok && wc.relist {
					// Later writes are seen too.
					f.writeRecord(t, "@1", `{"state":"idle"}`)
					if _, ok := recvWatch(t, ch, watchWait(debounce)); !ok {
						t.Fatal("no message for a write after windows/ appeared")
					}
					return
				}
			case <-deadline:
				t.Fatal("no relist after windows/ was created")
			}
		}
	})
}
