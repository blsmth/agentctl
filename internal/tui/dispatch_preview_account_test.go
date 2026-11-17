package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestDispatchPreviewShowsAccount(t *testing.T) {
	cmd := "agentctl dispatch --issue 42 --account personal"
	without := NewDispatchPreview("lead", "go-engineer", 42, "", "", cmd)
	with := NewDispatchPreview("lead", "go-engineer", 42, "", "", cmd, WithAccount("personal (Max)"))

	if strings.Contains(ansi.Strip(without.preToggle()), "account") {
		t.Error("account shown with none configured")
	}
	if got := ansi.Strip(with.preToggle()); !strings.Contains(got, "go-engineer · account personal (Max)") {
		t.Errorf("account missing from metadata:\n%s", got)
	}
	// Same row: the popup's line budget is unchanged.
	if a, b := strings.Count(with.preToggle(), "\n"), strings.Count(without.preToggle(), "\n"); a != b {
		t.Errorf("account added rows: %d vs %d", a, b)
	}
}
