package dispatch

import (
	"strings"
	"testing"
)

func TestPreamblesTeachArtifacts(t *testing.T) {
	for name, p := range AllPreambles() {
		if !strings.Contains(name, "Preamble") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(p, "### Artifacts\n") {
				t.Fatalf("missing Artifacts section")
			}
			for _, want := range []string{"agentctl artifact add <file>", "agentctl artifact update <id> <file>", "relative paths", "agentctl:artifacts"} {
				if !strings.Contains(p, want) {
					t.Errorf("missing %q", want)
				}
			}
			if strings.Index(p, "### Artifacts") < strings.Index(p, "### Context store") {
				t.Errorf("Artifacts should follow Context store")
			}
		})
	}
}
