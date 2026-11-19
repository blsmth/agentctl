package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestEffectiveDispatchApproval(t *testing.T) {
	tests := []struct {
		value     string
		want      string
		wantWarn  bool
		wantValid bool
	}{
		{"", DispatchApprovalNonReview, false, true},
		{"all", DispatchApprovalAll, false, true},
		{"non-review", DispatchApprovalNonReview, false, true},
		{"off", DispatchApprovalOff, false, true},
		{"none", DispatchApprovalAll, true, false},
		{"OFF", DispatchApprovalAll, true, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.value), func(t *testing.T) {
			c := Config{DispatchApproval: tt.value}
			if got := c.EffectiveDispatchApproval(); got != tt.want {
				t.Errorf("EffectiveDispatchApproval() = %q, want %q", got, tt.want)
			}
			if got := ValidDispatchApproval(tt.value); got != tt.wantValid {
				t.Errorf("ValidDispatchApproval() = %v, want %v", got, tt.wantValid)
			}
			if got := c.DispatchApprovalWarning() != ""; got != tt.wantWarn {
				t.Errorf("DispatchApprovalWarning() non-empty = %v, want %v", got, tt.wantWarn)
			}
		})
	}
}

// captureIgnoredLines records the "config: ignoring ..." summary lines.
func captureIgnoredLines(t *testing.T) *[]string {
	t.Helper()
	var lines []string
	old := warnLog
	warnLog = func(format string, args ...any) {
		if msg := fmt.Sprintf(format, args...); strings.HasPrefix(msg, "config: ignoring ") {
			lines = append(lines, msg)
		}
	}
	t.Cleanup(func() { warnLog = old })
	return &lines
}

func TestProjectDispatchApprovalNeverLoosens(t *testing.T) {
	tests := []struct {
		name     string
		global   string
		project  string
		want     string
		wantWarn bool
	}{
		{"project off over unset stays non-review", `{}`, `{"dispatch_approval":"off"}`, DispatchApprovalNonReview, true},
		{"project all over global off tightens", `{"dispatch_approval":"off"}`, `{"dispatch_approval":"all"}`, DispatchApprovalAll, false},
		{"project non-review over global off tightens", `{"dispatch_approval":"off"}`, `{"dispatch_approval":"non-review"}`, DispatchApprovalNonReview, false},
		{"project non-review over global all is dropped", `{"dispatch_approval":"all"}`, `{"dispatch_approval":"non-review"}`, DispatchApprovalAll, true},
		{"project unset keeps global off", `{"dispatch_approval":"off"}`, `{}`, DispatchApprovalOff, false},
		{"project unknown value fails closed", `{}`, `{"dispatch_approval":"bogus"}`, DispatchApprovalAll, false},
		{"project off over global unknown is dropped", `{"dispatch_approval":"bogus"}`, `{"dispatch_approval":"off"}`, DispatchApprovalAll, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := captureIgnoredLines(t)
			writeLayers(t, tt.global, tt.project)
			cfg := Load()
			if got := cfg.EffectiveDispatchApproval(); got != tt.want {
				t.Errorf("EffectiveDispatchApproval() = %q, want %q", got, tt.want)
			}
			warned := false
			for _, l := range *lines {
				if strings.Contains(l, `"dispatch_approval"`) {
					warned = true
				}
			}
			if warned != tt.wantWarn {
				t.Errorf("warned about dispatch_approval = %v, want %v (lines %q)", warned, tt.wantWarn, *lines)
			}
		})
	}
}
