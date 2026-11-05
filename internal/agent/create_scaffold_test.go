package agent

import (
	"strings"
	"testing"
)

func TestBuildAgentFile_GenericScaffold(t *testing.T) {
	out := buildAgentFile(CreateOpts{Name: "go-engineer", Description: "d", Tools: "Read", Model: "sonnet"})
	for _, banned := range []string{"Alice", "other-agent"} {
		if strings.Contains(out, banned) {
			t.Errorf("scaffold contains %q:\n%s", banned, out)
		}
	}
	if !strings.Contains(out, "You are a senior Go engineer.") {
		t.Errorf("scaffold missing generic intro:\n%s", out)
	}
}
