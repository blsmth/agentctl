package metrics

import (
	"errors"
	"fmt"
	"net"
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

// StartHTTPServer binds DefaultBindHost:port and serves /metrics from the
// shared registry in a background goroutine. The bind happens before it
// returns, so a port that is already taken is reported to the caller (the
// per-machine metrics serve logs it and keeps remote write running).
func StartHTTPServer(port int) error {
	addr := fmt.Sprintf("%s:%d", DefaultBindHost, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("metrics: listening on %s: %w", addr, err)
	}
	srv := newServer(addr)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.Error("metrics: HTTP server error on %s: %v", addr, err)
		}
	}()
	return nil
}
