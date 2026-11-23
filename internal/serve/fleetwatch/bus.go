package fleetwatch

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// Kind is the type of an Event.
type Kind int

const (
	// KindFleet carries the full fleet. Sent when a subscriber fell behind
	// and had events dropped: it replaces whatever it had.
	KindFleet Kind = iota
	// KindWindow: a window appeared or a rendered field changed.
	KindWindow
	// KindGone: a window disappeared.
	KindGone
	// KindTransition: a window's status (blocked/working/done/idle) changed.
	// From is "" for a window seen for the first time.
	KindTransition
	// KindArtifact: the artifact store changed (see Event.Artifact). Not
	// derived from the fleet: the artifact watcher publishes it.
	KindArtifact
)

// Artifact actions.
const (
	ArtifactAdd    = "add"
	ArtifactUpdate = "update"
	ArtifactRemove = "rm"
)

// ArtifactChange is one change to the artifact store. Only ID and Action go
// to SSE clients; the rest is for the push notifier (never content).
type ArtifactChange struct {
	ID       string
	Action   string // ArtifactAdd, ArtifactUpdate or ArtifactRemove
	Title    string
	Agent    string
	Project  string
	NoNotify bool
}

// Event is one bus message. Only the fields for its Kind are set.
type Event struct {
	Kind     Kind
	Fleet    []Group        // KindFleet
	Window   fleet.Window   // KindWindow, KindTransition
	Session  string         // KindGone
	WindowID string         // KindGone
	From, To string         // KindTransition
	Artifact ArtifactChange // KindArtifact
}

// Timing defaults; tests override them through Options.
const (
	DefaultDebounce   = 200 * time.Millisecond
	DefaultRediscover = 5 * time.Second
	subBuffer         = 64
	// goneAfter is how many consecutive refreshes must miss a window before
	// it is reported gone, so one failed tmux call is not a mass exit.
	goneAfter = 2
)

// Options configures a Bus.
type Options struct {
	Debounce   time.Duration
	Rediscover time.Duration
}

// Bus rebuilds the fleet on windowstate changes and publishes diffs.
type Bus struct {
	src  Source
	opts Options

	mu      sync.Mutex
	loaded  bool
	cur     []Group
	windows map[string]fleet.Window // key: session + "\x00" + window id
	missed  map[string]int
	subs    map[*Subscription]struct{}

	refreshMu sync.Mutex // serializes Refresh
}

// NewBus returns a Bus over src. Call Run to start watching.
func NewBus(src Source, opts Options) *Bus {
	if opts.Debounce <= 0 {
		opts.Debounce = DefaultDebounce
	}
	if opts.Rediscover <= 0 {
		opts.Rediscover = DefaultRediscover
	}
	return &Bus{
		src: src, opts: opts,
		windows: make(map[string]fleet.Window),
		missed:  make(map[string]int),
		subs:    make(map[*Subscription]struct{}),
	}
}

// Subscription receives events on C. A subscriber that does not keep up never
// blocks the bus: its backlog is dropped and replaced by one KindFleet event.
type Subscription struct {
	C <-chan Event
	c chan Event
	b *Bus
}

// Close unsubscribes. Safe to call more than once.
func (s *Subscription) Close() {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	delete(s.b.subs, s)
}

// Subscribe returns the current fleet and a subscription for every change
// after it, with no gap between the two. The first call loads the fleet if
// the bus has not yet.
func (b *Bus) Subscribe() ([]Group, *Subscription) {
	b.mu.Lock()
	loaded := b.loaded
	b.mu.Unlock()
	if !loaded {
		b.Refresh()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c := make(chan Event, subBuffer)
	sub := &Subscription{C: c, c: c, b: b}
	b.subs[sub] = struct{}{}
	return b.cur, sub
}

// Snapshot returns the fleet as of the last refresh.
func (b *Bus) Snapshot() []Group {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cur
}

func key(session, windowID string) string { return session + "\x00" + windowID }

// Refresh rebuilds the fleet, diffs it against the previous one and
// publishes the resulting events. A failed load changes nothing.
func (b *Bus) Refresh() {
	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()
	groups, err := b.src.Fleet()
	if err != nil {
		logging.Debug("fleetwatch: loading fleet: %v", err)
		return
	}

	b.mu.Lock()
	events, next, snapshot := b.diffLocked(groups)
	if b.loaded && (groupOrderChanged(b.cur, snapshot) || shellsChanged(b.cur, snapshot)) {
		events = append(events, Event{Kind: KindFleet, Fleet: snapshot})
	}
	b.cur = snapshot
	b.windows = next
	b.loaded = true
	for _, ev := range events {
		b.publishLocked(ev)
	}
	b.mu.Unlock()
}

// Publish fans an event that does not come from the fleet (KindArtifact) out
// to every subscriber, with the same never-block rule as fleet events.
func (b *Bus) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.publishLocked(ev)
}

// shellsChanged reports whether any group's shell windows differ between prev
// and next. Shells are not windows, so no window event carries a change.
func shellsChanged(prev, next []Group) bool {
	old := make(map[string][]Shell, len(prev))
	for _, g := range prev {
		old[g.Session] = g.Shells
	}
	for _, g := range next {
		if !slices.Equal(old[g.Session], g.Shells) {
			return true
		}
		delete(old, g.Session)
	}
	for _, sh := range old {
		if len(sh) > 0 {
			return true
		}
	}
	return false
}

// groupOrderChanged reports whether the groups present in both prev and next
// are in a different relative order: a saved-order change (the hub or the
// phone moved a project) that no window event would carry.
func groupOrderChanged(prev, next []Group) bool {
	inNext := make(map[string]bool, len(next))
	for _, g := range next {
		inNext[g.Session] = true
	}
	inPrev := make(map[string]bool, len(prev))
	var a, b []string
	for _, g := range prev {
		inPrev[g.Session] = true
		if inNext[g.Session] {
			a = append(a, g.Session)
		}
	}
	for _, g := range next {
		if inPrev[g.Session] {
			b = append(b, g.Session)
		}
	}
	return !slices.Equal(a, b)
}

// diffLocked computes events against b.windows and returns the new window
// map plus the snapshot to expose (groups, with windows that are missing but
// not yet gone kept in place).
func (b *Bus) diffLocked(groups []Group) ([]Event, map[string]fleet.Window, []Group) {
	next := make(map[string]fleet.Window, len(b.windows))
	var events []Event
	for _, g := range groups {
		for _, w := range g.Windows {
			k := key(g.Session, w.WindowID)
			next[k] = w
			delete(b.missed, k)
			prev, seen := b.windows[k]
			if seen && prev == w {
				continue
			}
			events = append(events, Event{Kind: KindWindow, Window: w})
			from := ""
			if seen {
				from = Status(prev)
			}
			if to := Status(w); from != to {
				events = append(events, Event{Kind: KindTransition, Window: w, From: from, To: to})
			}
		}
	}
	snapshot := groups
	for k, prev := range b.windows {
		if _, ok := next[k]; ok {
			continue
		}
		b.missed[k]++
		if b.missed[k] < goneAfter {
			next[k] = prev // keep until the absence is confirmed
			snapshot = keepWindow(snapshot, prev)
			continue
		}
		delete(b.missed, k)
		events = append(events, Event{Kind: KindGone, Session: prev.Session, WindowID: prev.WindowID})
	}
	return events, next, snapshot
}

// keepWindow returns groups with w present, copying only what it changes.
func keepWindow(groups []Group, w fleet.Window) []Group {
	out := make([]Group, len(groups))
	copy(out, groups)
	for i := range out {
		if out[i].Session == w.Session {
			ws := make([]fleet.Window, len(out[i].Windows), len(out[i].Windows)+1)
			copy(ws, out[i].Windows)
			out[i].Windows = append(ws, w)
			return out
		}
	}
	return out
}

// publishLocked fans ev out without blocking. A full subscriber has its
// backlog drained and replaced with a full-fleet event it can always accept.
func (b *Bus) publishLocked(ev Event) {
	for sub := range b.subs {
		select {
		case sub.c <- ev:
		default:
			drain(sub.c)
			select {
			case sub.c <- Event{Kind: KindFleet, Fleet: b.cur}:
			default:
			}
		}
	}
}

func drain(c chan Event) {
	for {
		select {
		case <-c:
		default:
			return
		}
	}
}

// Run watches windowstate directories until ctx is done. It refreshes on
// debounced file events and every Rediscover interval (sessions and windows
// come and go without touching a windows/ dir).
func (b *Bus) Run(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logging.Warn("fleetwatch: fsnotify unavailable, polling only: %v", err)
	}
	var events <-chan fsnotify.Event
	if watcher != nil {
		defer func() { _ = watcher.Close() }()
		events = watcher.Events
	}
	watched := make(map[string]bool)
	b.Refresh()
	b.syncWatches(watcher, watched)

	tick := time.NewTicker(b.opts.Rediscover)
	defer tick.Stop()
	var debounce *time.Timer
	var fire <-chan time.Time
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-events:
			// Hooks rewrite windowstate on every tool call: coalesce a burst
			// into one rebuild Debounce after its first event.
			if fire == nil {
				debounce = time.NewTimer(b.opts.Debounce)
				fire = debounce.C
			}
		case <-fire:
			fire = nil
			b.Refresh()
		case <-tick.C:
			b.Refresh()
			b.syncWatches(watcher, watched)
		}
	}
}

// syncWatches adds watches for new sessions' windows/ dirs and drops those
// of sessions that are gone.
func (b *Bus) syncWatches(w *fsnotify.Watcher, watched map[string]bool) {
	if w == nil {
		return
	}
	want := make(map[string]bool)
	for _, g := range b.Snapshot() {
		if dir := windowstate.WindowsDirFor(g.RuntimeDir); dir != "" {
			want[filepath.Clean(dir)] = true
		}
	}
	for dir := range want {
		if watched[dir] {
			continue
		}
		if err := w.Add(dir); err != nil {
			logging.Debug("fleetwatch: watching %s: %v", dir, err)
			continue // dir may not exist yet; retried on the next tick
		}
		watched[dir] = true
	}
	for dir := range watched {
		if !want[dir] {
			_ = w.Remove(dir)
			delete(watched, dir)
		}
	}
}
