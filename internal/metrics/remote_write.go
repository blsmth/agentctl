package metrics

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/golang/snappy"
	dto "github.com/prometheus/client_model/go"

	"github.com/blsmth/agentctl/internal/logging"
)

// RemoteWriteConfig holds configuration for the Prometheus remote write pusher.
type RemoteWriteConfig struct {
	Endpoint     string            // Remote write URL (required)
	PushInterval time.Duration     // How often to push metrics; 0 defaults to 15s
	Labels       map[string]string // Static labels added to all series
	BasicAuth    *BasicAuth        // Optional HTTP basic auth credentials
}

// BasicAuth holds HTTP basic authentication credentials.
type BasicAuth struct {
	Username string
	Password string
}

// StartRemoteWrite begins a background goroutine that gathers metrics from the
// shared registry every PushInterval and POSTs them to Endpoint using the
// Prometheus remote write v1 protocol. Returns a stop function that blocks
// until the background goroutine exits. If Endpoint is empty, StartRemoteWrite
// is a no-op and returns a no-op stop function.
func StartRemoteWrite(cfg RemoteWriteConfig) (stop func()) {
	return startRemoteWriteWithGatherer(cfg, Gatherer())
}

// gatherer is the interface satisfied by prometheus.Gatherer.
type gatherer interface {
	Gather() ([]*dto.MetricFamily, error)
}

// startRemoteWriteWithGatherer is the testable implementation of StartRemoteWrite.
func startRemoteWriteWithGatherer(cfg RemoteWriteConfig, g gatherer) func() {
	if cfg.Endpoint == "" {
		return func() {}
	}

	interval := cfg.PushInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	client := &http.Client{Timeout: 10 * time.Second}

	go func() {
		defer close(done)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := push(ctx, client, cfg, g); err != nil {
					logging.Log("metrics: remote write push error: %v", err)
				}
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// push gathers metrics, encodes them as a Prometheus remote write v1 request,
// and POSTs it to cfg.Endpoint. On a 5xx response it retries once after 1s.
func push(ctx context.Context, client *http.Client, cfg RemoteWriteConfig, g gatherer) error {
	families, err := g.Gather()
	if err != nil {
		return fmt.Errorf("gather metrics: %w", err)
	}

	series := toTimeSeries(families, cfg.Labels)
	if len(series) == 0 {
		return nil
	}

	wr := pbWriteRequest{Timeseries: series}
	body := wr.marshal()

	compressed := snappy.Encode(nil, body)

	do := func() (int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(compressed))
		if err != nil {
			return 0, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-protobuf")
		req.Header.Set("Content-Encoding", "snappy")
		req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")

		if cfg.BasicAuth != nil {
			req.SetBasicAuth(cfg.BasicAuth.Username, cfg.BasicAuth.Password)
		}

		resp, err := client.Do(req)
		if err != nil {
			return 0, fmt.Errorf("HTTP POST: %w", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}

	status, err := do()
	if err != nil {
		return err
	}

	// Retry once on server-side errors.
	if status >= 500 {
		logging.Log("metrics: remote write got %d, retrying in 1s", status)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
		status, err = do()
		if err != nil {
			return err
		}
		if status >= 400 {
			return fmt.Errorf("remote write returned %d after retry", status)
		}
	} else if status >= 400 {
		return fmt.Errorf("remote write returned %d", status)
	}

	return nil
}
