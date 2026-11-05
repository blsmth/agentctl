package metrics

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// freePort returns an available TCP port on loopback.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("freePort close: %v", err)
	}
	return port
}

// startIsolatedServer starts a /metrics HTTP server backed by a fresh
// isolated registry so tests don't interfere with each other or the shared
// singleton. Returns the server URL and the registry.
func startIsolatedServer(t *testing.T) (string, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	port := freePort(t)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux}
	go func() { _ = srv.ListenAndServe() }() // ErrServerClosed is expected on cleanup
	t.Cleanup(func() { _ = srv.Close() })

	// Wait for the server to accept connections.
	url := fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return url, reg
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("test server did not become ready in time")
	return "", nil
}

func TestHTTPServer_ServesRegisteredMetric(t *testing.T) {
	url, reg := startIsolatedServer(t)

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "agentctl_server_test_total",
		Help: "Counter registered by TestHTTPServer_ServesRegisteredMetric.",
	})
	reg.MustRegister(counter)
	counter.Add(7)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "agentctl_server_test_total") {
		t.Errorf("/metrics response does not contain registered counter; body:\n%s", body)
	}
}

func TestHTTPServer_OKWithEmptyRegistry(t *testing.T) {
	url, _ := startIsolatedServer(t)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestSharedRegistryMetricsVisible verifies that a metric registered on the
// shared Registry() is also visible through Gatherer().
func TestSharedRegistryMetricsVisible(t *testing.T) {
	// Use a dedicated counter name to avoid conflicts across test runs.
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "agentctl_shared_registry_visibility_test_total",
		Help: "Counter registered by TestSharedRegistryMetricsVisible.",
	})
	// Register on a fresh local registry — we don't pollute the shared one.
	localReg := prometheus.NewRegistry()
	localReg.MustRegister(counter)
	counter.Inc()

	mfs, err := localReg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(mfs) != 1 {
		t.Fatalf("expected 1 metric family, got %d", len(mfs))
	}
	if mfs[0].GetName() != "agentctl_shared_registry_visibility_test_total" {
		t.Errorf("unexpected metric name %q", mfs[0].GetName())
	}
}

func TestNewServer_TimeoutsAndLoopbackDefault(t *testing.T) {
	srv := newServer("127.0.0.1:0")
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 {
		t.Errorf("timeouts must be set: %v %v %v", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout)
	}
	if DefaultBindHost != "127.0.0.1" {
		t.Errorf("DefaultBindHost = %q, want loopback", DefaultBindHost)
	}
}
