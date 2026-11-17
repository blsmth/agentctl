package metricsproc

import "testing"

func TestEnsureStarted(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		wantSpawn bool
	}{
		{"disabled: nothing started", false, false},
		{"enabled, none running: started", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENTCTL_TEST", "")
			spawned := 0
			restore := SetSeamsForTest(
				func() (string, error) { return t.TempDir(), nil },
				func() bool { return tt.enabled },
				func() error { spawned++; return nil },
			)
			defer restore()
			if got := EnsureStarted(); got != tt.wantSpawn || (spawned == 1) != tt.wantSpawn {
				t.Errorf("EnsureStarted = %v spawned=%d, want spawn=%v", got, spawned, tt.wantSpawn)
			}
		})
	}
}

func TestEnsureStartedSkippedUnderTest(t *testing.T) {
	t.Setenv("AGENTCTL_TEST", "1")
	restore := SetSeamsForTest(
		func() (string, error) { return t.TempDir(), nil },
		func() bool { return true },
		func() error { t.Error("spawned under AGENTCTL_TEST"); return nil },
	)
	defer restore()
	if EnsureStarted() {
		t.Error("EnsureStarted returned true under AGENTCTL_TEST=1")
	}
}
