package launchopts

import (
	"strings"
	"testing"
)

func TestValidModel(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"claude-opus-5-5", true},
		{"claude-opus-5-5[1m]", true},
		{"deepseek/deepseek-v4.1-flash-1017:batch", true},
		{"qwen3:30b", true},
		{"hermes/foo-bar_1.2", true},
		{"~anthropic/claude-sonnet-latest", true},
		{"", false},
		{"/x", false},
		{"a b", false},
		{"a$b", false},
		{"a`b", false},
		{"a;b", false},
		{strings.Repeat("a", 101), false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := ValidModel(tt.in); got != tt.want {
				t.Errorf("ValidModel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
