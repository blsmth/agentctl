package serve

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
)

// TestMain points the process logger at a private dir so tests never depend
// on a resolvable home dir (CI has none) or write into the real one.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "serve-test-log")
	if err != nil {
		panic(err)
	}
	logging.InitFile(filepath.Join(dir, "serve.log"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// fakeClock is an adjustable clock for tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// shortTempDir returns a temp dir short enough for a unix socket path
// (macOS limits them to 104 bytes; t.TempDir() under /var/folders is longer).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "acs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type testServer struct {
	*Server
	dir   string
	clock *fakeClock
}

// newTestServer builds a Server over a temp state dir with a fresh CA.
func newTestServer(t *testing.T, cfg config.ServeConfig) *testServer {
	t.Helper()
	return newTestServerWith(t, cfg, nil)
}

// newTestServerWith is newTestServer with a hook to adjust Options.
func newTestServerWith(t *testing.T, cfg config.ServeConfig, mutate func(*Options)) *testServer {
	t.Helper()
	dir := t.TempDir()
	clock := newClock()
	ca, err := LoadOrCreateCA(dir, "testhost", nil, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Config:        cfg,
		Dir:           dir,
		Version:       "test",
		CA:            ca,
		AdvertiseHost: "192.168.1.10",
		HTTPSPort:     8443,
		Now:           clock.Now,
	}
	if mutate != nil {
		mutate(&opts)
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return &testServer{Server: s, dir: dir, clock: clock}
}

// pairDevice adds a device directly and returns its token.
func (ts *testServer) pairDevice(t *testing.T, name string) (Device, string) {
	t.Helper()
	d, tok, err := ts.devices.Add(name, ts.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return d, tok
}

// reqOpt customizes a test request.
type reqOpt func(*http.Request)

func from(addr string) reqOpt { return func(r *http.Request) { r.RemoteAddr = addr } }

func withCookie(tok string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: tok}) }
}

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

// do sends a request through h from a LAN address unless overridden.
func do(h http.Handler, method, target string, opts ...reqOpt) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = "192.168.1.20:51515"
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
