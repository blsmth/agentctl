package tui

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"
)

// newPlatformWatcher is the kqueue directory watcher on macOS.
func newPlatformWatcher() (dirWatcher, error) { return newKqueueWatcher() }

// kqueueWatcher watches directories with one EVFILT_VNODE descriptor each
// and reports the directory path when its entries change (NOTE_WRITE: a file
// created, removed or renamed in it). Unlike fsnotify's kqueue backend it
// never watches the files inside, so its cost does not grow with the
// directory: a windows dir holding a .lock and .gone file for every window
// ever opened made fsnotify keep hundreds of descriptors per bar and re-read
// the directory on every windowstate write.
type kqueueWatcher struct {
	kq     int
	wake   [2]int // pipe; a byte on wake[1] stops the reader
	events chan string
	errors chan error
	done   chan struct{} // closed when the reader exits
	stop   chan struct{} // closed by Close, so a blocked send gives up

	mu    sync.Mutex
	paths map[int]string // watched fd -> path
	byDir map[string]int

	closeOnce sync.Once
}

const kqueueDirFlags = unix.NOTE_WRITE | unix.NOTE_DELETE | unix.NOTE_RENAME | unix.NOTE_REVOKE

func newKqueueWatcher() (*kqueueWatcher, error) {
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("kqueue: %w", err)
	}
	unix.CloseOnExec(kq)
	w := &kqueueWatcher{
		kq:     kq,
		events: make(chan string),
		errors: make(chan error, 1),
		done:   make(chan struct{}),
		stop:   make(chan struct{}),
		paths:  map[int]string{},
		byDir:  map[string]int{},
	}
	if err := unix.Pipe(w.wake[:]); err != nil {
		_ = unix.Close(kq)
		return nil, fmt.Errorf("kqueue wake pipe: %w", err)
	}
	unix.CloseOnExec(w.wake[0])
	unix.CloseOnExec(w.wake[1])
	var ev unix.Kevent_t
	unix.SetKevent(&ev, w.wake[0], unix.EVFILT_READ, unix.EV_ADD|unix.EV_ENABLE)
	if _, err := unix.Kevent(kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		w.closeFDs()
		return nil, fmt.Errorf("kqueue register wake pipe: %w", err)
	}
	go w.read()
	return w, nil
}

// Add watches the directory at path. Adding a watched path is a no-op.
func (w *kqueueWatcher) Add(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.byDir[path]; ok {
		return nil
	}
	fd, err := unix.Open(path, unix.O_EVTONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("watch %s: %w", path, err)
	}
	var ev unix.Kevent_t
	unix.SetKevent(&ev, fd, unix.EVFILT_VNODE, unix.EV_ADD|unix.EV_ENABLE|unix.EV_CLEAR)
	ev.Fflags = kqueueDirFlags
	if _, err := unix.Kevent(w.kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("watch %s: %w", path, err)
	}
	w.paths[fd] = path
	w.byDir[path] = fd
	return nil
}

func (w *kqueueWatcher) Events() <-chan string { return w.events }
func (w *kqueueWatcher) Errors() <-chan error  { return w.errors }

// Close stops the reader and releases every descriptor.
func (w *kqueueWatcher) Close() error {
	w.closeOnce.Do(func() {
		close(w.stop)
		_, _ = unix.Write(w.wake[1], []byte{0})
		<-w.done
		w.closeFDs()
	})
	return nil
}

func (w *kqueueWatcher) closeFDs() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for fd := range w.paths {
		_ = unix.Close(fd)
	}
	w.paths, w.byDir = map[int]string{}, map[string]int{}
	_ = unix.Close(w.wake[0])
	_ = unix.Close(w.wake[1])
	_ = unix.Close(w.kq)
}

// read blocks in kevent and forwards each changed directory's path. A
// directory that was deleted or renamed away is dropped from the watch set
// (its path is still reported so the caller can notice and re-add it).
func (w *kqueueWatcher) read() {
	defer close(w.done)
	defer close(w.events)
	buf := make([]unix.Kevent_t, 16)
	for {
		n, err := unix.Kevent(w.kq, nil, buf, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			select {
			case w.errors <- fmt.Errorf("kevent: %w", err):
			default:
			}
			return
		}
		for _, ev := range buf[:n] {
			fd := int(ev.Ident)
			if fd == w.wake[0] {
				return
			}
			w.mu.Lock()
			path, ok := w.paths[fd]
			if ok && ev.Fflags&(unix.NOTE_DELETE|unix.NOTE_RENAME|unix.NOTE_REVOKE) != 0 {
				delete(w.paths, fd)
				delete(w.byDir, path)
				_ = unix.Close(fd)
			}
			w.mu.Unlock()
			if !ok {
				continue
			}
			select {
			case w.events <- path:
			case <-w.stop:
				return
			}
		}
	}
}
