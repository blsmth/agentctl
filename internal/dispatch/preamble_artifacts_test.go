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
			for _, want := range []string{"agentctl artifact render <spec.json>", "render <spec.json> --update <id>", "agentctl artifact add <file>"} {
				if !strings.Contains(p, want) {
					t.Errorf("missing %q", want)
				}
			}
			if skill := strings.Contains(p, "agentctl:artifacts"); skill == (strings.Contains(name, "no messaging") || strings.Contains(name, "no msg")) {
				t.Errorf("skill pointer present=%v, want it only for harnesses with skills", skill)
			}
			if strings.Index(p, "### Artifacts") < strings.Index(p, "### Context store") {
				t.Errorf("Artifacts should follow Context store")
			}
		})
	}
}
