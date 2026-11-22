package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func newTemplatePreview(action string) DispatchPreviewModel {
	return NewDispatchPreview("orch", "go-engineer", 0, "**New template.**\n\nReview open PRs.", "", "agentctl template add review ...",
		WithKind(PreviewKindTemplate),
		WithTemplate(TemplatePreview{Action: action, Name: "review", Path: "~/.config/agentctl/templates/review.md"}))
}

func TestDispatchPreviewModel_TemplateKind(t *testing.T) {
	for action, want := range map[string]string{
		"add":  "orch wants to add prompt template review",
		"edit": "orch wants to edit prompt template review",
		"rm":   "orch wants to remove prompt template review",
	} {
		t.Run(action, func(t *testing.T) {
			m := newTemplatePreview(action)
			if got := len(m.metadataLines()); got != 3 {
				t.Errorf("metadata rows = %d, want 3", got)
			}
			if !m.expanded {
				t.Error("template preview should open expanded")
			}
			view := ansi.Strip(m.View().Content)
			for _, w := range []string{want, "Template", "agent go-engineer", "templates/review.md", "View template", "Review open PRs."} {
				if !strings.Contains(view, w) {
					t.Errorf("view missing %q:\n%s", w, view)
				}
			}
			if strings.Contains(view, "Dispatching") {
				t.Errorf("template view shows the dispatch row:\n%s", view)
			}
		})
	}
}

// A dispatch of a template names it in the third row.
func TestDispatchPreviewModel_DispatchTemplateRow(t *testing.T) {
	m := NewDispatchPreview("orch", "writer", 0, "Review open PRs.", "", "agentctl dispatch --template review",
		WithTemplate(TemplatePreview{Name: "review"}))
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Template") || !strings.Contains(view, "review") || !strings.Contains(view, "Dispatching") {
		t.Errorf("view:\n%s", view)
	}
}
