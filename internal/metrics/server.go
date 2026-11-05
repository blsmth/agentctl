package metrics

import (
	"fmt"
	"net/http"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// DefaultBindHost is the interface the metrics endpoint binds to. Loopback
// only: the endpoint is unauthenticated, so exposing it on all interfaces
// must be an explicit decision, not the default.
const DefaultBindHost = "127.0.0.1"

// newServer builds the metrics http.Server for addr with timeouts set so a
// slow or malicious client cannot hold connections open indefinitely.
func newServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(Gatherer(), promhttp.HandlerOpts{}))
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
}

// StartHTTPServer starts the /metrics endpoint on DefaultBindHost:port using
// the shared registry. It runs the HTTP server in a background goroutine and
// returns immediately. Called by the monitor when metrics are enabled.
func StartHTTPServer(port int) {
	addr := fmt.Sprintf("%s:%d", DefaultBindHost, port)
	srv := newServer(addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			logging.Error("metrics: HTTP server error on %s: %v", addr, err)
		}
	}()
}
