package metrics

import (
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// resetRegistry resets the shared singleton for test isolation. Only for use
// in tests within this package.
func resetRegistry() {
	regOnce = sync.Once{}
	shared = nil
	registerer = nil
}

func TestRegistry_ReturnsSameInstance(t *testing.T) {
	r1 := Registry()
	r2 := Registry()
	if r1 != r2 {
		t.Error("Registry() returned different instances; want singleton")
	}
}

func TestRegistry_IsNotNil(t *testing.T) {
	if Registry() == nil {
		t.Error("Registry() returned nil")
	}
}

func TestGatherer_ImplementsInterface(t *testing.T) {
	var _ prometheus.Gatherer = Gatherer()
}

func TestGatherer_SameAsRegistry(t *testing.T) {
	if Gatherer() != Registry() {
		t.Error("Gatherer() should return the same underlying object as Registry()")
	}
}

func TestRegistry_CanRegisterAndGather(t *testing.T) {
	// Use an isolated registry for this test to avoid polluting the shared one.
	// We verify the contract that a metric registered on a registry appears when gathered.
	reg := prometheus.NewRegistry()
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "test_registry_counter_total",
		Help: "Test counter for registry unit test.",
	})
	reg.MustRegister(counter)
	counter.Inc()

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(mfs) == 0 {
		t.Error("expected at least one metric family after registering and incrementing a counter")
	}
}

// TestInitRegistry_StaticLabelsAppearedOnSeries verifies that metrics registered
// through Registerer() carry the static labels set by InitRegistry.
func TestInitRegistry_StaticLabelsAppearedOnSeries(t *testing.T) {
	resetRegistry()
	defer resetRegistry()

	InitRegistry(map[string]string{"instance": "test-host", "env": "test"})

	// Register a counter through the wrapped Registerer.
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "init_registry_label_test_total",
		Help: "Test counter for InitRegistry label injection.",
	})
	if err := Registerer().Register(counter); err != nil {
		t.Fatalf("Register: %v", err)
	}
	counter.Inc()

	// Gather from the base registry — wrapped labels are baked in.
	mfs, err := Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	found := false
	for _, mf := range mfs {
		if mf.GetName() != "init_registry_label_test_total" {
			continue
		}
		found = true
		for _, m := range mf.GetMetric() {
			labelMap := make(map[string]string)
			for _, lp := range m.GetLabel() {
				labelMap[lp.GetName()] = lp.GetValue()
			}
			if labelMap["instance"] != "test-host" {
				t.Errorf("expected instance=test-host; got %q", labelMap["instance"])
			}
			if labelMap["env"] != "test" {
				t.Errorf("expected env=test; got %q", labelMap["env"])
			}
		}
	}
	if !found {
		t.Error("counter not found in gathered metrics")
	}
}

// TestInitRegistry_HostnameFallback verifies that when "instance" is not in
// staticLabels, Registerer() still adds an instance label (from hostname).
func TestInitRegistry_HostnameFallback(t *testing.T) {
	resetRegistry()
	defer resetRegistry()

	// No "instance" key → should be auto-populated from hostname.
	InitRegistry(map[string]string{"env": "staging"})

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "init_registry_hostname_test_total",
		Help: "Test counter for hostname fallback.",
	})
	if err := Registerer().Register(counter); err != nil {
		t.Fatalf("Register: %v", err)
	}
	counter.Inc()

	mfs, err := Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	for _, mf := range mfs {
		if mf.GetName() != "init_registry_hostname_test_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "instance" {
					if lp.GetValue() == "" {
						t.Error("expected non-empty instance label from hostname fallback")
					}
					return // found and non-empty: pass
				}
			}
		}
		t.Error("instance label not found on metric series")
		return
	}
	t.Error("counter not found in gathered metrics")
}

// TestInitRegistry_EmptyLabels verifies that InitRegistry with an empty map
// still adds the instance label via the hostname fallback.
func TestInitRegistry_EmptyLabels(t *testing.T) {
	resetRegistry()
	defer resetRegistry()

	InitRegistry(map[string]string{})

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "init_registry_empty_labels_test_total",
		Help: "Test counter for empty labels case.",
	})
	if err := Registerer().Register(counter); err != nil {
		t.Fatalf("Register: %v", err)
	}
	counter.Inc()

	mfs, err := Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	for _, mf := range mfs {
		if mf.GetName() != "init_registry_empty_labels_test_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "instance" {
					// instance label present — pass.
					return
				}
			}
		}
		t.Error("instance label not present on metric series")
		return
	}
	t.Error("counter not found in gathered metrics")
}

// TestRegisterer_DefaultsToBaseRegistry verifies that Registerer() returns the
// base registry when InitRegistry has not been called.
func TestRegisterer_DefaultsToBaseRegistry(t *testing.T) {
	resetRegistry()
	defer resetRegistry()

	// Without InitRegistry, Registerer() should equal Registry().
	if Registerer() != Registry() {
		t.Error("Registerer() should equal Registry() before InitRegistry is called")
	}
}
