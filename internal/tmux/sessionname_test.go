package tmux

import "testing"

func TestSessionNameForProjectRoot(t *testing.T) {
	tests := map[string]string{
		"/home/u/my.proj":  "agentctl-my-proj",
		"/home/u/proj":     "agentctl-proj",
		"/":                "agentctl",
		"/home/u/---":      "agentctl",
		"/home/u/a b/c d.": "agentctl-c-d",
	}
	for root, want := range tests {
		if got := SessionNameForProjectRoot(root); got != want {
			t.Errorf("%q: got %q, want %q", root, got, want)
		}
	}
}
