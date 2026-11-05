package dashboards_test

import (
	"encoding/json"
	"testing"

	"github.com/blsmth/agentctl/internal/dashboards"
)

func TestList(t *testing.T) {
	names := dashboards.List()
	if len(names) != 3 {
		t.Fatalf("List() returned %d dashboards, want 3; got %v", len(names), names)
	}

	want := map[string]bool{
		"agent-detail.json":   true,
		"cost-usage.json":     true,
		"fleet-overview.json": true,
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected dashboard name %q", n)
		}
	}
}

func TestGet(t *testing.T) {
	for _, name := range dashboards.List() {
		t.Run(name, func(t *testing.T) {
			data, err := dashboards.Get(name)
			if err != nil {
				t.Fatalf("Get(%q) error: %v", name, err)
			}
			if len(data) == 0 {
				t.Fatalf("Get(%q) returned empty data", name)
			}
			// Verify the bytes are valid JSON.
			var v interface{}
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatalf("Get(%q) returned invalid JSON: %v", name, err)
			}
		})
	}
}

func TestGetInvalidName(t *testing.T) {
	_, err := dashboards.Get("nonexistent.json")
	if err == nil {
		t.Fatal("Get(nonexistent.json) expected error, got nil")
	}
}
