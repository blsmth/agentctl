package tmux

import (
	"reflect"
	"testing"
)

func TestNewShellWindowArgs(t *testing.T) {
	tests := []struct {
		name, session, dir string
		want               []string
	}{
		{"with dir", "agentctl-proj", "/work/proj", []string{
			"new-window", "-d", "-P", "-F", "#{window_id}", "-t", "agentctl-proj:",
			"-c", "/work/proj", "-n", "shell", "-e", "AGENTCTL=1"}},
		{"no dir", "s", "", []string{
			"new-window", "-d", "-P", "-F", "#{window_id}", "-t", "s:",
			"-n", "shell", "-e", "AGENTCTL=1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newShellWindowArgs(tt.session, tt.dir); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewShellWindowRefusesHub(t *testing.T) {
	for _, s := range []string{"", HubSessionName} {
		if _, err := NewShellWindow(s, "/x"); err == nil {
			t.Errorf("session %q: want error", s)
		}
	}
}
