package tui

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/blsmth/agentctl/internal/windowstate"
)

// dirWatcher reports changes under the two directories a chrome bar watches.
// Events yields a changed path: the watched directory itself (kqueue, which
// only sees that the directory's entries changed) or the file in it that
// changed (fsnotify). Every windowstate and active-window write is a temp
// file plus rename, so a directory-level change is enough to see all of them.
type dirWatcher interface {
	Add(path string) error
	Events() <-chan string
	Errors() <-chan error
	Close() error
}

// startWatcher watches runtimeDir/windows and runtimeDir (for the
// active-window file) for the lifetime of the process and returns a channel
// on which it delivers windowsChangedMsg (debounced) and
// activeWindowChangedMsg values. When no watcher can be created it falls
// back to polling at chromeBarPollInterval.
func startWatcher(runtimeDir string) chan tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() {
		w, err := newPlatformWatcher()
		if err != nil {
			w, err = newFsnotifyWatcher()
		}
		if err != nil {
			pollLoop(ch)
			return
		}
		defer func() { _ = w.Close() }()
		dw, err := newDirWatch(w, runtimeDir)
		if err != nil {
			pollLoop(ch)
			return
		}
		dw.run(ch, chromeBarDebounce)
	}()
	return ch
}

// pollLoop is the fallback when no watcher can be used: rebuild every
// chromeBarPollInterval, re-listing tmux each time.
func pollLoop(ch chan<- tea.Msg) {
	for {
		time.Sleep(chromeBarPollInterval)
		ch <- windowsChangedMsg{relist: true}
	}
}

// dirWatch turns a dirWatcher's raw changes into chrome bar messages.
type dirWatch struct {
	w          dirWatcher
	runtimeDir string
	windowsDir string
	activePath string

	windowsWatched bool
	known          map[string]bool // *.json record names at the last delivery
	activeStamp    string          // identity of the active-window file
}

// newDirWatch registers the watches (windows/ is best-effort: it may not
// exist yet, and is added once it appears) and snapshots the current state,
// so a change made after it returns is never missed.
func newDirWatch(w dirWatcher, runtimeDir string) (*dirWatch, error) {
	d := &dirWatch{
		w:          w,
		runtimeDir: runtimeDir,
		windowsDir: windowstate.WindowsDirFor(runtimeDir),
		activePath: filepath.Join(runtimeDir, "active-window"),
	}
	d.windowsWatched = w.Add(d.windowsDir) == nil
	if err := w.Add(runtimeDir); err != nil {
		return nil, err
	}
	d.known = jsonNameSet(d.windowsDir)
	d.activeStamp = fileStamp(d.activePath)
	return d, nil
}

// classify reports what a changed path means: a windowstate change (records
// are re-read at delivery) and/or an active-window change. Unrelated files
// (state.json, logs, temp files) yield neither, so they never reach the
// Bubble Tea program.
func (d *dirWatch) classify(p string) (windows, active bool) {
	if !d.windowsWatched && d.w.Add(d.windowsDir) == nil {
		// windows/ appeared after start.
		d.windowsWatched = true
		windows = true
	}
	switch {
	case p == d.windowsDir:
		windows = true
		// The kqueue backend drops a watch whose dir was deleted or renamed;
		// re-adding is a no-op while the watch is live and revives it if the
		// dir was recreated.
		d.windowsWatched = d.w.Add(d.windowsDir) == nil
	case filepath.Dir(p) == d.windowsDir:
		windows = windows || strings.HasSuffix(p, ".json")
	case p == d.runtimeDir || p == d.activePath:
		// The monitor rewrites state.json every tick, which changes the
		// runtime dir: one stat tells whether active-window was the change.
		if s := fileStamp(d.activePath); s != d.activeStamp {
			d.activeStamp = s
			active = true
		}
	}
	return windows, active
}

// run delivers debounced messages until the watcher closes: a burst of
// changes inside one debounce window yields at most one windowsChangedMsg
// plus at most one activeWindowChangedMsg. relist is set when the set of
// record files changed (a window was created or closed).
func (d *dirWatch) run(ch chan<- tea.Msg, debounce time.Duration) {
	var (
		fire    <-chan time.Time
		pending bool
		active  bool
	)
	arm := func() {
		if fire == nil {
			fire = time.After(debounce)
		}
	}
	for {
		select {
		case p, ok := <-d.w.Events():
			if !ok {
				return
			}
			win, act := d.classify(p)
			if !win && !act {
				continue
			}
			pending = pending || win
			active = active || act
			arm()
		case _, ok := <-d.w.Errors():
			if !ok {
				return
			}
			// A watcher error (fsnotify's kqueue backend reports one when a
			// temp file is renamed away before it can open it) may have hidden
			// a change: rebuild. It does not force a tmux relist; a changed
			// record set still relists, and the 30s clock tick relists anyway.
			pending = true
			arm()
		case <-fire:
			fire = nil
			if active {
				ch <- activeWindowChangedMsg{}
				active = false
			}
			if pending {
				names := jsonNameSet(d.windowsDir)
				relist := !maps.Equal(names, d.known)
				d.known = names
				ch <- windowsChangedMsg{relist: relist}
				pending = false
			}
		}
	}
}

// jsonNameSet returns the *.json record names in dir (nil when unreadable).
func jsonNameSet(dir string) map[string]bool {
	names, err := readJSONNames(dir)
	if err != nil {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// readJSONNames lists the *.json record file names in dir. It reads names
// only (no per-entry stat): the windows dir also holds a .lock and a .gone
// file for every window ever opened, so it can be large.
func readJSONNames(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	all, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range all {
		if strings.HasSuffix(n, ".json") {
			out = append(out, n)
		}
	}
	return out, nil
}

// fileStamp identifies the file at p by size, mtime and inode ("" when
// missing), so a rewrite is seen without reading it.
func fileStamp(p string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	s := strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		s += ":" + strconv.FormatUint(uint64(st.Ino), 10) //nolint:unconvert // differs per OS
	}
	return s
}

// fsnotifyWatcher adapts fsnotify to dirWatcher. It is the backend on Linux
// (inotify watches a directory with one descriptor). On macOS fsnotify's
// kqueue backend opens every file in a watched directory and re-reads the
// directory on each change, so it is only the fallback there.
type fsnotifyWatcher struct {
	w      *fsnotify.Watcher
	events chan string
	errs   chan error
}

func newFsnotifyWatcher() (dirWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	fw := &fsnotifyWatcher{w: w, events: make(chan string), errs: make(chan error)}
	go func() {
		defer close(fw.errs)
		for err := range w.Errors {
			// The kqueue backend opens each new file it sees; a temp file
			// already renamed away fails with ENOENT. The rename itself is
			// reported as an event, so nothing was missed.
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			fw.errs <- err
		}
	}()
	go func() {
		defer close(fw.events)
		for ev := range w.Events {
			if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue // chmod
			}
			fw.events <- ev.Name
		}
	}()
	return fw, nil
}

func (f *fsnotifyWatcher) Add(path string) error { return f.w.Add(path) }
func (f *fsnotifyWatcher) Events() <-chan string { return f.events }
func (f *fsnotifyWatcher) Errors() <-chan error  { return f.errs }
func (f *fsnotifyWatcher) Close() error          { return f.w.Close() }
