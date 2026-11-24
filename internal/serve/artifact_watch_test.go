package serve

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
)

func artifactEvent(c fleetwatch.ArtifactChange) fleetwatch.Event {
	return fleetwatch.Event{Kind: fleetwatch.KindArtifact, Artifact: c}
}

func TestNotifierArtifactPush(t *testing.T) {
	added := fleetwatch.ArtifactChange{ID: "abcdefghij", Action: fleetwatch.ArtifactAdd, Title: "Benchmark report",
		Agent: "go-engineer", Project: "proj"}

	t.Run("new artifact pushes title, origin and the viewer route", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{})
		e.n.Handle(artifactEvent(added))
		got := e.take()
		if len(got) != 1 {
			t.Fatalf("pushes = %+v", got)
		}
		m := got[0]
		if m.Title != "Benchmark report" || m.Body != "go-engineer · proj" || m.URL != "/#/artifacts/abcdefghij" ||
			m.Kind != "artifact" || m.Tag != "artifact:abcdefghij" || m.Urgent {
			t.Errorf("message = %+v", m)
		}
		if p := string(m.payload()); strings.Contains(p, "content") || strings.Contains(p, "/a/") {
			t.Errorf("payload = %s", p)
		}
	})
	t.Run("no origin", func(t *testing.T) {
		e := newNotifyEnv(config.ServePushConfig{})
		e.n.Handle(artifactEvent(fleetwatch.ArtifactChange{ID: "abcdefghij", Action: fleetwatch.ArtifactAdd}))
		if got := e.take(); len(got) != 1 || got[0].Title != "New artifact" || got[0].Body != "New artifact" {
			t.Errorf("pushes = %+v", got)
		}
	})

	quiet := config.ServePushConfig{QuietHours: "11:00-13:00"} // clock is 12:00 UTC
	off := config.ServePushConfig{Artifacts: new(false)}
	muted := config.ServePushConfig{MutedProjects: []string{"proj"}}
	noNotify := added
	noNotify.NoNotify = true
	update := added
	update.Action = fleetwatch.ArtifactUpdate
	rm := added
	rm.Action = fleetwatch.ArtifactRemove
	for _, tc := range []struct {
		name string
		cfg  config.ServePushConfig
		ev   fleetwatch.ArtifactChange
	}{
		{"update", config.ServePushConfig{}, update},
		{"remove", config.ServePushConfig{}, rm},
		{"quiet hours", quiet, added},
		{"muted project", muted, added},
		{"no_notify", config.ServePushConfig{}, noNotify},
		{"serve.push.artifacts false", off, added},
	} {
		t.Run("no push on "+tc.name, func(t *testing.T) {
			e := newNotifyEnv(tc.cfg)
			e.n.Handle(artifactEvent(tc.ev))
			if got := e.take(); len(got) != 0 {
				t.Errorf("pushed: %+v", got)
			}
		})
	}
}

func TestRedactArtifactForNtfy(t *testing.T) {
	m := redactForNtfy(artifactMessage(fleetwatch.ArtifactChange{ID: "abcdefghij", Title: "Secret plan", Agent: "a", Project: "p"}))
	if m.Title != "agentctl: new artifact" || strings.Contains(m.Body, "Secret") || m.URL != "" {
		t.Errorf("redacted = %+v", m)
	}
	if ntfyTags("artifact") != "page_facing_up,agentctl" {
		t.Error("ntfy tag")
	}
}

type changeLog struct {
	mu  sync.Mutex
	got []fleetwatch.ArtifactChange
	c   chan fleetwatch.ArtifactChange
}

func newChangeLog() *changeLog { return &changeLog{c: make(chan fleetwatch.ArtifactChange, 64)} }

func (l *changeLog) publish(c fleetwatch.ArtifactChange) {
	l.mu.Lock()
	l.got = append(l.got, c)
	l.mu.Unlock()
	l.c <- c
}

func (l *changeLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, c := range l.got {
		out = append(out, c.Action+":"+c.ID)
	}
	l.got = nil
	for len(l.c) > 0 {
		<-l.c
	}
	return out
}

func testStore(t *testing.T, dir string) *artifacts.Store {
	t.Helper()
	return &artifacts.Store{Dir: func() (string, error) { return dir, nil }, Now: time.Now}
}

func addFile(t *testing.T, s *artifacts.Store, name, content string, o artifacts.AddOptions) artifacts.Meta {
	t.Helper()
	o.Source = artifacts.Source{Path: writeTemp(t, name, content)}
	m, err := s.Add(o)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestArtifactWatcherScan(t *testing.T) {
	s := testStore(t, filepath.Join(t.TempDir(), "artifacts"))
	pre := addFile(t, s, "pre.txt", "x", artifacts.AddOptions{})
	log := newChangeLog()
	aw := newArtifactWatcher(s, log.publish, 0, 0)
	aw.seed()
	aw.scan()
	if got := log.take(); len(got) != 0 {
		t.Fatalf("seeded artifacts re-announced: %v", got)
	}

	m := addFile(t, s, "a.md", "# A", artifacts.AddOptions{NoNotify: true, Origin: artifacts.Origin{Agent: "ag", Project: "pr"}})
	aw.scan()
	log.mu.Lock()
	first := log.got
	log.mu.Unlock()
	if len(first) != 1 || first[0] != (fleetwatch.ArtifactChange{ID: m.ID, Action: "add", Title: "A", Agent: "ag", Project: "pr", NoNotify: true}) {
		t.Fatalf("add = %+v", first)
	}
	log.take()
	aw.scan()
	if got := log.take(); len(got) != 0 {
		t.Errorf("no change rescanned as %v", got)
	}
	if _, err := s.Update(m.ID, artifacts.UpdateOptions{Source: artifacts.Source{Path: writeTemp(t, "a.md", "# B")}}); err != nil {
		t.Fatal(err)
	}
	aw.scan()
	if got := log.take(); strings.Join(got, ",") != "update:"+m.ID {
		t.Errorf("update = %v", got)
	}
	if _, err := s.Pin(pre.ID, true); err != nil {
		t.Fatal(err)
	}
	aw.scan()
	if got := log.take(); strings.Join(got, ",") != "update:"+pre.ID {
		t.Errorf("pin = %v", got)
	}
	if _, err := s.Remove(pre.ID); err != nil {
		t.Fatal(err)
	}
	aw.scan()
	if got := log.take(); strings.Join(got, ",") != "rm:"+pre.ID {
		t.Errorf("rm = %v", got)
	}
}

// The store dir need not exist when serve starts: the watcher creates the
// change dir, and a store write then shows up as a file event.
func TestArtifactWatcherWatchesMissingStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "artifacts")
	s := testStore(t, dir)
	aw := newArtifactWatcher(s, func(fleetwatch.ArtifactChange) {}, 0, 0)
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Skipf("fsnotify unavailable: %v", err)
	}
	defer func() { _ = w.Close() }()
	if !aw.addWatch(w) {
		t.Fatal("addWatch failed on a missing store")
	}
	if _, err := os.Stat(filepath.Join(dir, ".changes")); err != nil {
		t.Fatalf("change dir not created: %v", err)
	}
	addFile(t, s, "a.txt", "x", artifacts.AddOptions{})
	select {
	case <-w.Events:
	case err := <-w.Errors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("no fsnotify event for a store write")
	}
}

// run publishes changes after a Kick (and on file events and polls).
func TestArtifactWatcherRunKick(t *testing.T) {
	s := testStore(t, filepath.Join(t.TempDir(), "artifacts"))
	log := newChangeLog()
	aw := newArtifactWatcher(s, log.publish, 5*time.Millisecond, time.Hour)
	aw.seed()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { aw.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	m := addFile(t, s, "a.txt", "x", artifacts.AddOptions{})
	aw.Kick()
	select {
	case c := <-log.c:
		if c.ID != m.ID || c.Action != fleetwatch.ArtifactAdd {
			t.Errorf("change = %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no artifact change seen")
	}
}

func TestArtifactSSEEvent(t *testing.T) {
	ae := newArtifactEnv(t, true)
	ae.Start(context.Background())
	srv := httptest.NewServer(ae.Handler())
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/events", nil)
	req.AddCookie(&http.Cookie{Name: DeviceCookie, Value: ae.tok})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := &sseReader{sc: bufio.NewScanner(resp.Body)}
	if ev, ok := r.next(t); !ok || ev.name != "fleet" {
		t.Fatalf("first event = %+v", ev)
	}

	m := addFile(t, ae.store, "a.txt", "x", artifacts.AddOptions{})
	// A server-side write kicks a rescan; so does fsnotify. Either is enough.
	ae.artifactWatch.Kick()
	want := `{"id":"` + m.ID + `","action":"add"}`
	timeout := time.AfterFunc(10*time.Second, cancel)
	defer timeout.Stop()
	for {
		ev, ok := r.next(t)
		if !ok {
			t.Fatal("stream ended before the artifact event")
		}
		if ev.name == "artifact" {
			if ev.data != want {
				t.Errorf("artifact event = %s, want %s", ev.data, want)
			}
			return
		}
	}
}

// An artifact published the way `agentctl improve notify` does (artifacts.New
// in another process, default state dir) reaches the notifier through the
// running watcher and ends in exactly one logged push decision.
func TestArtifactPublishedViaDefaultStoreLogsPushDecision(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	logs := capturePushLog(t)
	var mu sync.Mutex
	var delivered []Message
	n := NewNotifier(config.ServePushConfig{}, time.Now, func(m Message) {
		mu.Lock()
		delivered = append(delivered, m)
		mu.Unlock()
	})
	handled := make(chan struct{}, 4)
	aw := newArtifactWatcher(artifacts.New(), func(c fleetwatch.ArtifactChange) {
		n.Handle(artifactEvent(c))
		handled <- struct{}{}
	}, 5*time.Millisecond, 50*time.Millisecond)
	aw.seed()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { aw.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	m, err := artifacts.New().Add(artifacts.AddOptions{
		Source: artifacts.Source{Path: writeTemp(t, "r.md", "# Report")},
		Title:  "Fleet improve: 2 changes",
		Kind:   artifacts.KindMarkdown,
		Origin: artifacts.Origin{Agent: "fleet-improver", Project: "improve"},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher never published the new artifact")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 1 || delivered[0].Tag != "artifact:"+m.ID {
		t.Fatalf("delivered = %+v", delivered)
	}
	if got := logs(); !strings.Contains(got, "kind=artifact") || !strings.Contains(got, "queued") {
		t.Errorf("no artifact push decision logged: %q", got)
	}
}

func TestNotifierArtifactLogsSkipReason(t *testing.T) {
	no := false
	ch := fleetwatch.ArtifactChange{ID: "abcdefghij", Action: fleetwatch.ArtifactAdd, Title: "T", Project: "proj"}
	for _, tc := range []struct {
		name string
		cfg  config.ServePushConfig
		ch   fleetwatch.ArtifactChange
		want string
	}{
		{"disabled", config.ServePushConfig{Artifacts: &no}, ch, "serve.push.artifacts is off"},
		{"muted", config.ServePushConfig{MutedProjects: []string{"proj"}}, ch, "muted"},
		{"no-notify", config.ServePushConfig{}, fleetwatch.ArtifactChange{ID: "abcdefghij", Action: "add", NoNotify: true}, "--no-notify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := capturePushLog(t)
			n := NewNotifier(tc.cfg, time.Now, func(Message) { t.Error("delivered") })
			n.Handle(artifactEvent(tc.ch))
			if got := logs(); !strings.Contains(got, "kind=artifact") || !strings.Contains(got, "skipped") || !strings.Contains(got, tc.want) {
				t.Errorf("log = %q", got)
			}
		})
	}
}
