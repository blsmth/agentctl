package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func newCronPreview(action string) DispatchPreviewModel {
	return NewDispatchPreview("orch", "go-engineer", 0, "**Dir** /src/app\n\nTriage new issues.", "", "agentctl cron add nightly ...",
		WithKind(PreviewKindCron),
		WithAccount("work"),
		WithCron(CronPreview{Action: action, Job: "nightly", Schedule: "30 7 * * 1-5 (weekdays 07:30), next: Wed 18 Nov 07:31"}))
}

func TestDispatchPreviewModel_CronKind_Title(t *testing.T) {
	tests := []struct {
		action, want string
	}{
		{"add", "orch wants to schedule go-engineer"},
		{"", "orch wants to schedule go-engineer"},
		{"resume", "orch wants to resume cron job nightly"},
		{"run --force", "orch wants to force-run cron job nightly"},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			view := ansi.Strip(newCronPreview(tt.action).View().Content)
			if !strings.Contains(view, tt.want) {
				t.Errorf("view missing title %q:\n%s", tt.want, view)
			}
			if strings.Contains(view, "wants to dispatch") {
				t.Errorf("cron view shows the dispatch title:\n%s", view)
			}
		})
	}
}

func TestDispatchPreviewModel_CronKind_MetadataAndViewport(t *testing.T) {
	m := newCronPreview("add")
	if !m.isCron() || m.isBash() {
		t.Fatalf("isCron=%v isBash=%v, want cron only", m.isCron(), m.isBash())
	}
	if got := len(m.metadataLines()); got != 3 {
		t.Errorf("metadata rows = %d, want 3 (PopupSize budgets three)", got)
	}
	if !m.expanded {
		t.Error("cron preview should open expanded so the job details are visible")
	}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Job", "nightly", "agent go-engineer", "account work", "Schedule",
		"weekdays 07:30", "next: Wed 18 Nov 07:31", "View job", "Triage new issues."} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Dispatching") {
		t.Errorf("cron view shows the dispatch row:\n%s", view)
	}
}

func TestDispatchPreviewModel_CronKind_FitsPopup(t *testing.T) {
	m := NewDispatchPreview("orch", "go-engineer", 0, strings.Repeat("line\n\n", 60), "", "agentctl cron add x",
		WithKind(PreviewKindCron), WithCron(CronPreview{Action: "add", Job: "x", Schedule: "@daily"}))
	sz := DispatchPreviewMaxPopupSize()
	// The tmux border (2 rows) sits outside the rendered view.
	if h := strings.Count(m.View().Content, "\n") + 1; h > sz.Height-2 {
		t.Errorf("expanded cron view is %d rows, popup inner height %d", h, sz.Height-2)
	}
	if DispatchPreviewMaxPopupSize(WithKind(PreviewKindCron)) != sz {
		t.Error("popup size must not depend on kind")
	}
}
