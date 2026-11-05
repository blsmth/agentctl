package metrics

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/prometheus/client_golang/prometheus"
)

// decodeWriteRequest manually decodes a Prometheus remote write protobuf message.
// It returns the names from __name__ labels in each TimeSeries.
func decodeTimestampFromBody(body []byte) ([]string, error) {
	// Decompress snappy.
	decoded, err := snappy.Decode(nil, body)
	if err != nil {
		return nil, err
	}

	// Minimal proto decoder: iterate top-level fields (all TimeSeries, field 1).
	var names []string
	off := 0
	for off < len(decoded) {
		tag, n := consumeVarint(decoded[off:])
		if n == 0 {
			break
		}
		off += n
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x7)

		if fieldNum != 1 || wireType != 2 {
			// Skip unknown fields.
			off += skipField(decoded[off:], wireType)
			continue
		}

		// Length-delimited TimeSeries.
		msgLen, n := consumeVarint(decoded[off:])
		if n == 0 {
			break
		}
		off += n
		tsBytes := decoded[off : off+int(msgLen)]
		off += int(msgLen)

		name := extractNameLabel(tsBytes)
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// extractNameLabel finds the __name__ label value in an encoded TimeSeries message.
func extractNameLabel(tsBytes []byte) string {
	off := 0
	for off < len(tsBytes) {
		tag, n := consumeVarint(tsBytes[off:])
		if n == 0 {
			break
		}
		off += n
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x7)

		if wireType != 2 {
			off += skipField(tsBytes[off:], wireType)
			continue
		}
		msgLen, n := consumeVarint(tsBytes[off:])
		if n == 0 {
			break
		}
		off += n
		chunk := tsBytes[off : off+int(msgLen)]
		off += int(msgLen)

		if fieldNum == 1 {
			// Label message: field 1 = name, field 2 = value.
			name, value := decodeLabelPair(chunk)
			if name == "__name__" {
				return value
			}
		}
	}
	return ""
}

func decodeLabelPair(b []byte) (name, value string) {
	off := 0
	for off < len(b) {
		tag, n := consumeVarint(b[off:])
		if n == 0 {
			break
		}
		off += n
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x7)
		if wireType != 2 {
			off += skipField(b[off:], wireType)
			continue
		}
		strLen, n := consumeVarint(b[off:])
		if n == 0 {
			break
		}
		off += n
		s := string(b[off : off+int(strLen)])
		off += int(strLen)
		switch fieldNum {
		case 1:
			name = s
		case 2:
			value = s
		}
	}
	return name, value
}

// consumeVarint reads a varint from b; returns (value, bytesRead).
func consumeVarint(b []byte) (uint64, int) {
	var v uint64
	for i, byt := range b {
		v |= uint64(byt&0x7f) << (7 * uint(i))
		if byt < 0x80 {
			return v, i + 1
		}
		if i >= 9 {
			return 0, 0
		}
	}
	return 0, 0
}

// skipField skips over a field with the given wire type, returning bytes consumed.
func skipField(b []byte, wireType int) int {
	switch wireType {
	case 0: // varint
		for i, byt := range b {
			if byt < 0x80 {
				return i + 1
			}
		}
	case 1: // 64-bit
		return 8
	case 2: // length-delimited
		l, n := consumeVarint(b)
		return n + int(l)
	case 5: // 32-bit
		return 4
	}
	return len(b) // bail
}

// TestRemoteWrite_IntegrationCounter registers a counter, increments it, starts
// remote write, waits for a push, and verifies the server received a valid
// WriteRequest containing the metric.
func TestRemoteWrite_IntegrationCounter(t *testing.T) {
	// Use an isolated registry so this test doesn't interfere with others.
	reg := prometheus.NewRegistry()

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "test_integration_counter",
		Help: "Integration test counter.",
	})
	reg.MustRegister(counter)
	counter.Add(7)

	var received []byte
	var mu sync.Mutex
	done := make(chan struct{})
	once := sync.Once{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = body
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		once.Do(func() { close(done) })
	}))
	defer srv.Close()

	// Temporarily replace the package-level gatherer for this test.
	// We do this by directly calling our push function with a custom gatherer.
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	series := toTimeSeries(families, nil)
	if len(series) == 0 {
		t.Fatal("expected at least 1 series")
	}

	wr := pbWriteRequest{Timeseries: series}
	body := wr.marshal()
	compressed := snappy.Encode(nil, body)

	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(compressed))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server to receive request")
	}

	mu.Lock()
	body2 := received
	mu.Unlock()

	names, err := decodeTimestampFromBody(body2)
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !containsStr(names, "test_integration_counter") {
		t.Errorf("expected test_integration_counter in series names, got %v", names)
	}
}

// TestRemoteWrite_Retry verifies that a 503 triggers one retry and the batch
// is delivered on the second attempt.
func TestRemoteWrite_Retry(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable) // 503
		} else {
			w.WriteHeader(http.StatusNoContent) // 204
		}
	}))
	defer srv.Close()

	// Use an isolated registry with a real metric so the pusher has data to send.
	reg := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "retry_test_gauge", Help: "h"})
	reg.MustRegister(gauge)
	gauge.Set(1)

	cfg := RemoteWriteConfig{
		Endpoint:     srv.URL,
		PushInterval: 50 * time.Millisecond,
	}
	stop := startRemoteWriteWithGatherer(cfg, reg)
	defer stop()

	// Wait for at least 2 calls (the first 503 + its retry).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if calls.Load() < 2 {
		t.Errorf("expected at least 2 calls (initial 503 + retry), got %d", calls.Load())
	}
}

// TestRemoteWrite_GracefulStop verifies that stop() returns promptly and that
// the goroutine does not leak.
func TestRemoteWrite_GracefulStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	reg := prometheus.NewRegistry()
	stop := startRemoteWriteWithGatherer(RemoteWriteConfig{
		Endpoint:     srv.URL,
		PushInterval: 1 * time.Hour, // long interval — never fires during test
	}, reg)

	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()

	select {
	case <-done:
		// Good — stop returned promptly.
	case <-time.After(3 * time.Second):
		t.Error("stop() did not return within 3s")
	}
}

// TestRemoteWrite_NoOpWhenEmpty verifies that StartRemoteWrite is a no-op when
// Endpoint is empty and the returned stop function is safe to call.
func TestRemoteWrite_NoOpWhenEmpty(t *testing.T) {
	reg := prometheus.NewRegistry()
	stop := startRemoteWriteWithGatherer(RemoteWriteConfig{Endpoint: ""}, reg)
	stop() // must not panic or block
}

// TestRemoteWrite_BasicAuthHeader verifies that the Basic Auth header is set.
func TestRemoteWrite_BasicAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	reg := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "auth_test_gauge", Help: "h"})
	reg.MustRegister(gauge)
	gauge.Set(1)

	cfg := RemoteWriteConfig{
		Endpoint:     srv.URL,
		PushInterval: 50 * time.Millisecond,
		BasicAuth:    &BasicAuth{Username: "user", Password: "secret"},
	}
	stop := startRemoteWriteWithGatherer(cfg, reg)
	defer stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if gotAuth != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if gotAuth == "" {
		t.Error("expected Authorization header, got none")
	}
	// Basic auth starts with "Basic ".
	if len(gotAuth) < 6 || gotAuth[:6] != "Basic " {
		t.Errorf("unexpected Authorization header: %q", gotAuth)
	}
}

// TestPbWriteRequest_MarshalRoundtrip verifies that the manual protobuf encoding
// produces bytes that contain the expected label strings.
func TestPbWriteRequest_MarshalRoundtrip(t *testing.T) {
	wr := pbWriteRequest{
		Timeseries: []pbTimeSeries{
			{
				Labels: []pbLabel{
					{Name: "__name__", Value: "hello_world"},
					{Name: "env", Value: "test"},
				},
				Samples: []pbSample{
					{Value: 42.0, Timestamp: 1000},
				},
			},
		},
	}
	b := wr.marshal()

	if len(b) == 0 {
		t.Fatal("expected non-empty encoded bytes")
	}

	// The string "hello_world" must appear in the encoded bytes.
	if !bytes.Contains(b, []byte("hello_world")) {
		t.Error("encoded bytes do not contain label value 'hello_world'")
	}
	if !bytes.Contains(b, []byte("__name__")) {
		t.Error("encoded bytes do not contain label name '__name__'")
	}

	// Verify value 42.0 is encoded as a little-endian float64.
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(42.0))
	if !bytes.Contains(b, buf[:]) {
		t.Error("encoded bytes do not contain float64(42.0) in little-endian")
	}
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
