// Package metrics provides the shared Prometheus registry and HTTP server for
// all agentctl metric sources.
package metrics

import (
	"os"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	regOnce sync.Once
	shared  *prometheus.Registry

	regMu      sync.RWMutex
	registerer prometheus.Registerer
)

// initShared ensures the base registry is created exactly once.
func initShared() {
	regOnce.Do(func() {
		shared = prometheus.NewRegistry()
		registerer = shared
	})
}

// InitRegistry initialises the shared registry with static labels that are
// injected into every metric series registered through Registerer(). If
// staticLabels does not contain an "instance" key, the system hostname is used
// as the default value. Call this once at startup, before any metrics are
// registered.
func InitRegistry(staticLabels map[string]string) {
	initShared()

	// Build effective label set: copy caller's map then add instance fallback.
	labels := make(prometheus.Labels, len(staticLabels)+1)
	for k, v := range staticLabels {
		labels[k] = v
	}
	if _, ok := labels["instance"]; !ok {
		if h, err := os.Hostname(); err == nil {
			labels["instance"] = h
		}
	}

	var r prometheus.Registerer
	if len(labels) > 0 {
		r = prometheus.WrapRegistererWith(labels, shared)
	} else {
		r = shared
	}

	regMu.Lock()
	registerer = r
	regMu.Unlock()
}

// Registry returns the shared Prometheus registry for all agentctl metrics.
// It is created once on first call (sync.Once). Use Registerer() when
// registering metrics so that static labels are automatically applied.
func Registry() *prometheus.Registry {
	initShared()
	return shared
}

// Registerer returns the prometheus.Registerer for the shared registry.
// When static labels have been configured via InitRegistry, the returned
// value is a WrappedRegisterer that adds those labels to every metric.
// Otherwise it is equivalent to Registry().
func Registerer() prometheus.Registerer {
	initShared()
	regMu.RLock()
	r := registerer
	regMu.RUnlock()
	return r
}

// Gatherer returns the Gatherer interface for the shared registry.
func Gatherer() prometheus.Gatherer {
	return Registry()
}
