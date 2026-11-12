package tmux

import (
	"slices"
	"testing"
)

func TestBuildNewWindowArgs(t *testing.T) {
	tests := []struct {
		name       string
		focus      bool
		left       bool
		wantD      bool
		wantB      bool
		wantTarget string
	}{
		{"focus right", true, false, false, false, "s:"},
		{"focus left", true, true, false, true, "s:^"},
		{"background right", false, false, true, false, "s:"},
		{"background left", false, true, true, true, "s:^"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildNewWindowArgs("s", "w", "/tmp", tt.focus, tt.left, []string{"-e", "A=1"})
			if args[0] != "new-window" {
				t.Fatalf("args[0] = %q", args[0])
			}
			if got := slices.Contains(args, "-d"); got != tt.wantD {
				t.Errorf("-d present = %v, want %v", got, tt.wantD)
			}
			if got := slices.Contains(args, "-b"); got != tt.wantB {
				t.Errorf("-b present = %v, want %v", got, tt.wantB)
			}
			i := slices.Index(args, "-t")
			if i < 0 || args[i+1] != tt.wantTarget {
				t.Errorf("target = %v, want %q", args, tt.wantTarget)
			}
			if !slices.Contains(args, "A=1") {
				t.Error("env args dropped")
			}
			if n := len(args); args[n-3] != "-P" || args[n-1] != "#{window_id}" {
				t.Errorf("missing -P -F tail: %v", args)
			}
		})
	}
}
