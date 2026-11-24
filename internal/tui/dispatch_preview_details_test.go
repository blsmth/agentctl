package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/remoteapproval"
)

func fullDetails() remoteapproval.Details {
	return remoteapproval.Details{
		Project: "agentctl", Dir: "~/code/agentctl", DirSource: "current dir",
		AskingProject: "agentctl",
		Model:         "opus", ModelSource: "agent config", Backend: "nous", Harness: "pi",
		Worktree: "worktree", PermissionMode: "auto",
	}
}

func detailView(t *testing.T, d remoteapproval.Details, opts ...DispatchPreviewOption) string {
	t.Helper()
	opts = append([]DispatchPreviewOption{WithDetails(d)}, opts...)
	m := NewDispatchPreview("lead", "go-engineer", 7, "", "", "agentctl dispatch --issue 7", opts...)
	return ansi.Strip(m.View().Content)
}

func TestDispatchPreviewDetails_Rows(t *testing.T) {
	v := detailView(t, fullDetails())
	for _, want := range []string{
		"Project      agentctl  ~/code/agentctl",
		"Model        opus · agent config · backend nous · harness pi",
		"Runs         worktree · auto permissions",
		"Issue        #7",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "not ") {
		t.Errorf("same-project dispatch must not warn:\n%s", v)
	}
	if strings.Index(v, "Dispatching") > strings.Index(v, "Project") || strings.Index(v, "Runs") > strings.Index(v, "Issue") {
		t.Errorf("rows out of order:\n%s", v)
	}
}

func TestDispatchPreviewDetails_OmitEmpty(t *testing.T) {
	v := detailView(t, remoteapproval.Details{})
	for _, label := range []string{"Project", "Model", "Runs"} {
		if strings.Contains(v, label) {
			t.Errorf("empty details must not render %q:\n%s", label, v)
		}
	}
	v = detailView(t, remoteapproval.Details{PermissionMode: "auto"})
	if !strings.Contains(v, "Runs         auto permissions") || strings.Contains(v, "Model") {
		t.Errorf("partial details wrong:\n%s", v)
	}
}

func TestDispatchPreviewDetails_OtherProjectWarning(t *testing.T) {
	d := fullDetails()
	d.Project, d.OtherProject, d.AskingProject = "blsmth.com", true, "agentctl"
	if v := detailView(t, d); !strings.Contains(v, "not agentctl") {
		t.Errorf("missing warning:\n%s", v)
	}
}

func TestDispatchPreviewDetails_UnknownProject(t *testing.T) {
	v := detailView(t, remoteapproval.Details{DirSource: "cd in command"})
	if !strings.Contains(v, "Project      unknown  cd in command") {
		t.Errorf("missing unknown project row:\n%s", v)
	}
}

func TestDispatchPreviewDetails_NonDispatchKindsUnchanged(t *testing.T) {
	for _, kind := range []string{PreviewKindBash, PreviewKindCron, PreviewKindTemplate} {
		m := NewDispatchPreview("lead", "go-engineer", 0, "p", "", "c", WithKind(kind), WithDetails(fullDetails()))
		if n := m.detailRowCount(); n != 0 {
			t.Errorf("%s: detail rows = %d, want 0", kind, n)
		}
		base := NewDispatchPreview("lead", "go-engineer", 0, "p", "", "c", WithKind(kind))
		if m.PopupSize() != base.PopupSize() {
			t.Errorf("%s: popup size changed with details", kind)
		}
	}
}

func TestDispatchPreviewDetails_NarrowTruncates(t *testing.T) {
	d := fullDetails()
	d.Dir = "~/" + strings.Repeat("deeply/nested/", 20) + "project"
	d.OtherProject, d.AskingProject = true, "other"
	for _, w := range []int{60, 70} {
		m := NewDispatchPreview("lead", "go-engineer", 7, "x", "", "c", WithDetails(d), WithContentWidth(w))
		for _, l := range m.detailLines() {
			if got := lipgloss.Width(l); got > w {
				t.Errorf("w=%d: row width %d: %q", w, got, ansi.Strip(l))
			}
		}
		if v := ansi.Strip(m.View().Content); !strings.Contains(v, "…") || !strings.Contains(v, "not other") {
			t.Errorf("w=%d: want truncated dir with warning kept:\n%s", w, v)
		}
	}
}

func TestDispatchDetailRows(t *testing.T) {
	if n := DispatchDetailRows(remoteapproval.Details{}); n != 0 {
		t.Errorf("empty = %d", n)
	}
	if n := DispatchDetailRows(fullDetails()); n != 3 {
		t.Errorf("full = %d, want 3", n)
	}
}

// The extra rows must be budgeted: the view fits the popup in every state, and
// the popup is exactly the extra rows taller than without details.
func TestDispatchPreviewDetails_HeightBudget(t *testing.T) {
	long := strings.Repeat("A line of prompt text that wraps around the popup width. ", 6) +
		strings.Repeat("\n\n- item", 80)
	for _, width := range []int{60, 88, 120} {
		for _, expanded := range []bool{false, true} {
			t.Run(fmt.Sprintf("w%d/expanded=%v", width, expanded), func(t *testing.T) {
				opts := []DispatchPreviewOption{WithContentWidth(width), WithMaxViewportHeight(22), WithDetails(fullDetails())}
				m := NewDispatchPreview("lead", "go-engineer", 0, long, "", "c", opts...)
				m.expanded = expanded
				max := DispatchPreviewMaxPopupSize(opts...)
				if h := lipgloss.Height(m.View().Content); h > max.Height-2 {
					t.Errorf("height %d > popup inner %d", h, max.Height-2)
				}
				base := DispatchPreviewMaxPopupSize(WithContentWidth(width), WithMaxViewportHeight(22))
				if max.Height != base.Height+3 {
					t.Errorf("popup height %d, want %d", max.Height, base.Height+3)
				}
			})
		}
	}
}

// On a short client the popup opens collapsed so the hint bar stays visible.
func TestDispatchPreviewDetails_ShortClientKeepsHintBar(t *testing.T) {
	m := NewDispatchPreview("lead", "go-engineer", 0, strings.Repeat("x\n\n", 40), "", "c",
		WithDetails(fullDetails()), WithMaxViewportHeight(8))
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = updated.(DispatchPreviewModel)
	if h := lipgloss.Height(m.View().Content); h > 20 {
		t.Errorf("view height %d overflows 20 rows", h)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "allow") {
		t.Error("hint bar missing")
	}
}

func TestDispatchPreviewDetails_JSONRoundTrip(t *testing.T) {
	b, err := json.Marshal(fullDetails())
	if err != nil {
		t.Fatal(err)
	}
	var d remoteapproval.Details
	if err := json.Unmarshal(b, &d); err != nil || d != fullDetails() {
		t.Errorf("round trip: %v %+v", err, d)
	}
}
