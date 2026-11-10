package agentname

import (
	"strings"
	"testing"
)

func TestValidators(t *testing.T) {
	tests := []struct {
		in                    string
		valid, plugin, launch bool
	}{
		{in: "go-engineer", valid: true, launch: true},
		{in: "Plan", valid: true, launch: true},
		{in: "my-plugin:reviewer", plugin: true, launch: true},
		{in: ""},
		{in: ".hidden"},
		{in: "-flag"},
		{in: "../../x"},
		{in: "x$(touch /tmp/p)"},
		{in: "a:b:c"},
		{in: "p:$(id)"},
		{in: ":x"},
		{in: strings.Repeat("a", MaxLen), valid: true, launch: true},
		{in: strings.Repeat("a", MaxLen+1)},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := Valid(tt.in); got != tt.valid {
				t.Errorf("Valid(%q) = %v, want %v", tt.in, got, tt.valid)
			}
			if got := ValidPlugin(tt.in); got != tt.plugin {
				t.Errorf("ValidPlugin(%q) = %v, want %v", tt.in, got, tt.plugin)
			}
			if got := ValidLaunch(tt.in); got != tt.launch {
				t.Errorf("ValidLaunch(%q) = %v, want %v", tt.in, got, tt.launch)
			}
		})
	}
}
