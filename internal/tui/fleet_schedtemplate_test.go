package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/prompttemplate"
)

// withSchedTemplates makes the form list tmpls and the store resolve them
// (by name, from any dir); the returned func deletes one.
func withSchedTemplates(t *testing.T, tmpls ...prompttemplate.Template) func(name string) {
	t.Helper()
	live := map[string]prompttemplate.Template{}
	var order []string
	for _, tm := range tmpls {
		live[tm.Name] = tm
		order = append(order, tm.Name)
	}
	prevList, prevStore := fleetSchedTemplates, fleetSchedStore
	t.Cleanup(func() { fleetSchedTemplates, fleetSchedStore = prevList, prevStore })
	fleetSchedTemplates = func(string) []prompttemplate.Template {
		var out []prompttemplate.Template
		for _, n := range order {
			if tm, ok := live[n]; ok {
				out = append(out, tm)
			}
		}
		return out
	}
	fleetSchedStore = func(validate func(string, string) error) cronedit.Store {
		st := prevStore(validate)
		st.LoadTemplate = func(name, _ string) (prompttemplate.Template, error) {
			if tm, ok := live[name]; ok {
				return tm, nil
			}
			return prompttemplate.Template{}, fmt.Errorf("%w: %q", prompttemplate.ErrNotFound, name)
		}
		return st
	}
	return func(name string) { delete(live, name) }
}

var schedTriage = prompttemplate.Template{Name: "triage", Description: "Weekly triage", Agent: "go-engineer",
	Prompt: "Triage the backlog.", Scope: prompttemplate.ScopeGlobal}

func TestScheduleFormTemplateSource(t *testing.T) {
	f := newSchedFixture(t)
	withSchedTemplates(t, schedTriage, prompttemplate.Template{Name: "review", Prompt: "Review PRs."})
	m := showSchedules(t, f.model(t))
	m.selectLine(0)
	m = schedPress(t, m, "n", "t")
	m = schedType(t, m, "triage-job")
	m = focusStopT(t, m, stopSource)
	m = schedPress(t, m, "right")
	fm := formOf(t, m)
	if fm.source != schedSourceTemplate || fm.agent.value() != "" {
		t.Fatalf("source %d agent %q", fm.source, fm.agent.value())
	}
	out := ansi.Strip(m.render())
	for _, want := range []string{"Prompt from", "Template", "triage", "Weekly triage", "Triage the backlog.", "from template (go-engineer)"} {
		if !strings.Contains(out, want) {
			t.Errorf("form missing %q:\n%s", want, out)
		}
	}
	m = focusStopT(t, m, stopTemplate)
	m = schedPress(t, m, "right", "left") // through review and back to triage
	m = schedPress(t, m, "ctrl+s")
	if !formOf(t, m).review {
		t.Fatalf("no review: %v", formOf(t, m).errors())
	}
	review := strings.Join(strings.Fields(ansi.Strip(strings.Join(m.schedFormLines(m.schedPaneWidth(), m.termHeight), "\n"))), " ")
	for _, want := range []string{"Runs go-engineer (from template triage)", "template triage, read from the project at every fire", "Triage the backlog."} {
		if !strings.Contains(review, want) {
			t.Errorf("review missing %q:\n%s", want, review)
		}
	}
	m = schedPress(t, m, "y")
	if m.schedForm != nil {
		t.Fatalf("not saved: %q", m.schedForm.err)
	}
	j := f.job(t, "triage-job")
	if j.Template != "triage" || j.Agent != "" || j.Prompt != "" || j.PromptKey != "" {
		t.Errorf("saved job = %+v", j)
	}
}

func TestScheduleFormSwitchTemplateAndPrompt(t *testing.T) {
	f := newSchedFixture(t)
	del := withSchedTemplates(t, schedTriage)
	if _, err := fleetSchedStore(nil).Create(cron.Job{Name: "tj", Schedule: "0 7 * * *", Dir: f.project, Agent: "go-engineer", Template: "triage"}); err != nil {
		t.Fatal(err)
	}

	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "tj")
	card := ansi.Strip(m.render())
	if !strings.Contains(card, "template triage (read at every fire)") {
		t.Errorf("card missing the template:\n%s", card)
	}

	// Template → prompt.
	m = schedPress(t, m, "e")
	if formOf(t, m).source != schedSourceTemplate {
		t.Fatal("a template job did not open on Template")
	}
	m = focusStopT(t, m, stopSource)
	m = schedPress(t, m, "left")
	m = focusStopT(t, m, stopPrompt)
	m = schedType(t, m, "Inline now.")
	m = schedPress(t, m, "ctrl+s", "y")
	if m.schedForm != nil {
		t.Fatalf("not saved: %q %v", m.schedForm.err, m.schedForm.errors())
	}
	if j := f.job(t, "tj"); j.Template != "" || j.Prompt != "Inline now." || j.Agent != "go-engineer" {
		t.Errorf("to prompt: %+v", j)
	}

	// Prompt → template.
	m = schedPress(t, m, "e")
	m = focusStopT(t, m, stopSource)
	m = schedPress(t, m, "right")
	m = schedPress(t, m, "ctrl+s")
	review := strings.Join(strings.Fields(ansi.Strip(strings.Join(m.schedFormLines(m.schedPaneWidth(), m.termHeight), "\n"))), " ")
	if !strings.Contains(review, "Prompt (changed)") {
		t.Errorf("review does not flag the prompt change:\n%s", review)
	}
	m = schedPress(t, m, "y")
	if m.schedForm != nil {
		t.Fatalf("not saved: %q %v", m.schedForm.err, m.schedForm.errors())
	}
	if j := f.job(t, "tj"); j.Template != "triage" || j.Prompt != "" || j.Agent != "go-engineer" {
		t.Errorf("back to template: %+v", j)
	}

	// The template is deleted: the job shows broken.
	del("triage")
	ov, err := fleetSchedStore(nil).Overview()
	if err != nil || len(ov.Jobs) != 1 || ov.Jobs[0].Invalid != `template "triage" not found` {
		t.Errorf("overview after delete = %+v, %v", ov.Jobs, err)
	}
}
