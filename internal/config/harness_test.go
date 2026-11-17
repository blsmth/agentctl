package config

import "testing"

// TestHarnessIsGlobalOnly: switching an agent's harness changes its approval
// layer (pi has no permission system), so a project config cannot set it.
func TestHarnessIsGlobalOnly(t *testing.T) {
	global := `{"agents":{"pinned":{"harness":"pi"},"plain":{}}}`
	project := `{"agents":{"pinned":{"harness":"claude"},"plain":{"harness":"pi"},"newone":{"harness":"pi"}}}`
	cfg := loadWith(t, global, project)
	want := map[string]string{"pinned": "pi", "plain": "", "newone": ""}
	for name, h := range want {
		if got := cfg.ForAgent(name).Harness; got != h {
			t.Errorf("agents.%s.harness = %q, want %q", name, got, h)
		}
	}
	if !warnedContaining("agents.plain.harness") {
		t.Error("expected a sanitize warning for the project harness")
	}
}
