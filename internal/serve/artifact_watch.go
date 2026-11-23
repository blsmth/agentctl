package serve

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
)

// Artifact watcher timing defaults.
const (
	defaultArtifactDebounce = 200 * time.Millisecond
	defaultArtifactPoll     = 30 * time.Second
)

// artifactStamp is what the watcher remembers of an artifact to tell an
// update from no change.
type artifactStamp struct {
	latest  int
	updated time.Time
	pinned  bool
	title   string
}

func stampOf(m artifacts.Meta) artifactStamp {
	return artifactStamp{latest: m.Latest(), updated: m.Updated, pinned: m.Pinned, title: m.Title}
}

// artifactWatcher turns artifact store changes into KindArtifact bus events
// (SSE `artifact`, and the new-artifact push). It watches the store's change
// dir (artifacts.Store.ChangesDir, one fsnotify watch however many artifacts
// exist; every store write touches it), debounces, rescans the metadata and
// diffs it against what it saw last. It also rescans every poll interval and
// on Kick, so a missed or unavailable fsnotify only delays events.
type artifactWatcher struct {
	store    *artifacts.Store
	publish  func(fleetwatch.ArtifactChange)
	debounce time.Duration
	poll     time.Duration
	kick     chan struct{}

	mu     sync.Mutex
	seen   map[string]artifactStamp
	seeded bool
}

func newArtifactWatcher(store *artifacts.Store, publish func(fleetwatch.ArtifactChange), debounce, poll time.Duration) *artifactWatcher {
	if debounce <= 0 {
		debounce = defaultArtifactDebounce
	}
	if poll <= 0 {
		poll = defaultArtifactPoll
	}
	return &artifactWatcher{
		store: store, publish: publish, debounce: debounce, poll: poll,
		kick: make(chan struct{}, 1), seen: make(map[string]artifactStamp),
	}
}

// Kick asks for a rescan soon (after a write the server made itself). Never
// blocks.
func (aw *artifactWatcher) Kick() {
	select {
	case aw.kick <- struct{}{}:
	default:
	}
}

// seed records the artifacts present at startup without publishing, so a
// restart does not re-announce (or re-push) every artifact. Only the first
// call does anything; run calls it too, for callers that did not.
func (aw *artifactWatcher) seed() {
	aw.mu.Lock()
	defer aw.mu.Unlock()
	if aw.seeded {
		return
	}
	list, err := aw.store.List(artifacts.Filter{})
	if err != nil {
		// Left unseeded: the first scan that can list becomes the baseline,
		// so a transient error never turns every artifact into an add.
		logging.Debug("serve: artifact watcher: %v", err)
		return
	}
	aw.seeded = true
	for _, m := range list {
		aw.seen[m.ID] = stampOf(m)
	}
}

// scan diffs the store against what was seen and publishes add, update and
// rm changes (adds first, oldest first, so pushes arrive in publish order).
// A failed listing changes nothing.
func (aw *artifactWatcher) scan() {
	list, err := aw.store.List(artifacts.Filter{})
	if err != nil {
		logging.Debug("serve: artifact watcher: %v", err)
		return
	}
	aw.mu.Lock()
	if !aw.seeded {
		aw.seeded = true
		for _, m := range list {
			aw.seen[m.ID] = stampOf(m)
		}
		aw.mu.Unlock()
		return
	}
	var changes []fleetwatch.ArtifactChange
	now := make(map[string]artifactStamp, len(list))
	for i := len(list) - 1; i >= 0; i-- { // List is newest first
		m := list[i]
		st := stampOf(m)
		now[m.ID] = st
		prev, ok := aw.seen[m.ID]
		switch {
		case !ok:
			changes = append(changes, fleetwatch.ArtifactChange{
				ID: m.ID, Action: fleetwatch.ArtifactAdd, Title: m.Title, Agent: m.Agent,
				Project: m.Project, NoNotify: m.NoNotify,
			})
		case prev != st:
			changes = append(changes, fleetwatch.ArtifactChange{ID: m.ID, Action: fleetwatch.ArtifactUpdate, Title: m.Title})
		}
	}
	var gone []string
	for id := range aw.seen {
		if _, ok := now[id]; !ok {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		changes = append(changes, fleetwatch.ArtifactChange{ID: id, Action: fleetwatch.ArtifactRemove})
	}
	aw.seen = now
	aw.mu.Unlock()
	for _, c := range changes {
		aw.publish(c)
	}
}

// run watches until ctx is done. The change dir is created when missing, so
// a store that does not exist yet at serve start is still watched.
func (aw *artifactWatcher) run(ctx context.Context) {
	aw.seed()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logging.Warn("serve: artifact watcher: fsnotify unavailable, polling only: %v", err)
	}
	var events <-chan fsnotify.Event
	if watcher != nil {
		defer func() { _ = watcher.Close() }()
		events = watcher.Events
	}
	watching := aw.addWatch(watcher)

	tick := time.NewTicker(aw.poll)
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
			if fire == nil {
				debounce = time.NewTimer(aw.debounce)
				fire = debounce.C
			}
		case <-aw.kick:
			if fire == nil {
				debounce = time.NewTimer(aw.debounce)
				fire = debounce.C
			}
		case <-fire:
			fire = nil
			aw.scan()
		case <-tick.C:
			if !watching {
				watching = aw.addWatch(watcher)
			}
			aw.scan()
		}
	}
}

// addWatch watches the store's change dir, reporting success.
func (aw *artifactWatcher) addWatch(w *fsnotify.Watcher) bool {
	if w == nil {
		return false
	}
	dir, err := aw.store.ChangesDir()
	if err != nil {
		logging.Debug("serve: artifact watcher: %v", err)
		return false
	}
	if err := w.Add(dir); err != nil {
		logging.Debug("serve: artifact watcher: watching %s: %v", dir, err)
		return false
	}
	return true
}
