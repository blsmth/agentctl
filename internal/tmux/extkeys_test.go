package tmux

import (
	"slices"
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestExtendedKeysArgs(t *testing.T) {
	on := []string{"set-option", "-s", "extended-keys", "on"}
	csiu := []string{"set-option", "-s", "extended-keys-format", "csi-u"}
	tests := []struct {
		version string
		want    [][]string
	}{
		{"tmux 3.6a\n", [][]string{on, csiu}},
		{"tmux 3.5", [][]string{on, csiu}},
		{"tmux 4.0", [][]string{on, csiu}},
		{"tmux 3.4", [][]string{on}},
		{"tmux next-3.7", [][]string{on, csiu}},
		{"", [][]string{on}},
	}
	for _, tt := range tests {
		got := extendedKeysArgs(tt.version)
		if !slices.EqualFunc(got, tt.want, slices.Equal[[]string]) {
			t.Errorf("extendedKeysArgs(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

func TestHyperlinksArgs(t *testing.T) {
	add := []string{"set-option", "-as", "terminal-features", ",*:hyperlinks"}
	tests := []struct {
		name, current string
		want          []string
	}{
		{"defaults", "terminal-features[0] xterm*:clipboard:ccolour:cstyle:focus:title\nterminal-features[1] screen*:title\n", add},
		{"empty", "", add},
		{"already on", "terminal-features[0] xterm*:clipboard\nterminal-features[1] *:hyperlinks\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hyperlinksArgs(tt.current); !slices.Equal(got, tt.want) {
				t.Errorf("hyperlinksArgs = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLaunchBootstrapRecordsHarness(t *testing.T) {
	var ws windowstate.WindowState
	applyLaunchBootstrap(&ws, "w", "a", "", map[string]string{"AGENTCTL_HARNESS": "pi"})
	if ws.Harness != "pi" {
		t.Errorf("Harness = %q, want pi", ws.Harness)
	}
	ws = windowstate.WindowState{Harness: "pi"} // recycled @N from a pi window
	applyLaunchBootstrap(&ws, "w", "a", "", nil)
	if ws.Harness != "" {
		t.Errorf("Harness = %q after a claude launch, want empty", ws.Harness)
	}
}
