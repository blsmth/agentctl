package tmux

import (
	"reflect"
	"strings"
	"testing"
)

func TestHubCloseCommands(t *testing.T) {
	got := HubCloseCommands("/dev/ttys001", []string{"agentctl-hubview", "agentctl-hubview-2", "agentctl-proj"})
	want := [][]string{
		{"detach-client", "-t", "/dev/ttys001"},
		{"kill-session", "-t", "=agentctl-hubview"},
		{"kill-session", "-t", "=agentctl-hubview-2"},
		{"kill-session", "-t", "=agentctl-hub"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, c := range got {
		if strings.Contains(strings.Join(c, " "), "agentctl-proj") {
			t.Errorf("project session targeted: %v", c)
		}
	}
}

func TestHubCloseCommandsNoClient(t *testing.T) {
	got := HubCloseCommands("", nil)
	want := [][]string{{"kill-session", "-t", "=agentctl-hub"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestHubCloseShell(t *testing.T) {
	got := hubCloseShell(HubCloseCommands("c1", nil))
	want := "tmux 'detach-client' '-t' 'c1'; tmux 'kill-session' '-t' '=agentctl-hub'"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
