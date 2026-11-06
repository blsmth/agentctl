package cmd

import (
	"strings"
	"testing"
)

func TestInstallPluginMessage(t *testing.T) {
	for _, want := range []string{
		"/plugin marketplace add blsmth/agentctl",
		"/plugin install agentctl@agentctl",
	} {
		if !strings.Contains(installPluginMessage, want) {
			t.Errorf("message missing %q", want)
		}
	}
}
