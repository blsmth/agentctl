package tui

import (
	"fmt"
	"slices"
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
	for _, want := range []string{"Source", "Template", "triage", "Weekly triage", "Triage the backlog.", "from template (go-engineer)"} {
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

func TestScheduleFormClickTemplateSource(t *testing.T) {
	f := newSchedFixture(t)
	withSchedTemplates(t, schedTriage)
	m := openNewForm(t, f)
	m = schedClick(t, m, "Source", "Template")
	fm := formOf(t, m)
	if fm.focus != stopSource || fm.source != schedSourceTemplate || fm.agent.value() != "" {
		t.Fatalf("click on Template: focus %d source %d agent %q", fm.focus, fm.source, fm.agent.value())
	}
	if !strings.Contains(ansi.Strip(m.render()), "Triage the backlog.") {
		t.Errorf("template prompt not shown:\n%s", ansi.Strip(m.render()))
	}
	m = schedClick(t, m, "Source", "Own prompt")
	if formOf(t, m).source != schedSourcePrompt {
		t.Error("click on Prompt did not switch back")
	}
}

var schedFull = prompttemplate.Template{Name: "full", Agent: "go-engineer", Model: "sonnet", Effort: "high", Prompt: "Do the full thing."}

func startPick(t *testing.T, m FleetModel, name string) FleetModel {
	t.Helper()
	m = focusStopT(t, m, stopStartTmpl)
	for i := 0; i < 8 && formOf(t, m).start.value() != name; i++ {
		m = schedPress(t, m, "right")
	}
	if got := formOf(t, m).start.value(); got != name {
		t.Fatalf("could not pick %q, at %q", name, got)
	}
	return m
}

func TestScheduleStartFromTemplateCopiesAndSavesOwnPrompt(t *testing.T) {
	f := newSchedFixture(t)
	withSchedTemplates(t, schedFull, schedTriage)
	m := openNewForm(t, f)
	m = schedType(t, m, "copied")
	if out := ansi.Strip(m.render()); !strings.Contains(out, "Start from") || !strings.Contains(out, "Template (linked, not editable)") {
		t.Fatalf("form missing the picker or the linked label:\n%s", out)
	}
	m = startPick(t, m, "full")
	m = schedPress(t, m, "enter")
	fm := formOf(t, m)
	if fm.prompt.Value() != "Do the full thing." || fm.focus != stopPrompt || fm.agent.value() != "go-engineer" ||
		fm.model.value() != "sonnet" || fm.effort.value() != "high" {
		t.Fatalf("after pick: prompt %q focus %d agent %q model %q effort %q", fm.prompt.Value(), fm.focus, fm.agent.value(), fm.model.value(), fm.effort.value())
	}
	m = schedType(t, m, " Extra.")
	m = schedPress(t, m, "ctrl+s", "y")
	if m.schedForm != nil {
		t.Fatalf("not saved: %q %v", m.schedForm.err, m.schedForm.errors())
	}
	j := f.job(t, "copied")
	if j.Template != "" || j.Prompt != "Do the full thing. Extra." || j.Model != "sonnet" || j.Effort != "high" {
		t.Errorf("saved job = %+v", j)
	}
}

func TestScheduleStartFromTemplateKeepsTouchedFields(t *testing.T) {
	f := newSchedFixture(t)
	withSchedTemplates(t, schedFull)
	m := openNewForm(t, f)
	m = focusStopT(t, m, stopAgent)
	m = schedPress(t, m, "right")
	picked := formOf(t, m).agent.value()
	m = startPick(t, m, "full")
	m = schedPress(t, m, "enter")
	if got := formOf(t, m).agent.value(); got != picked {
		t.Errorf("a touched agent became %q, want %q kept", got, picked)
	}
	if formOf(t, m).prompt.Value() != "Do the full thing." {
		t.Error("prompt not copied")
	}
}

func TestScheduleStartFromTemplateNoAgentKeepsCurrent(t *testing.T) {
	f := newSchedFixture(t)
	withSchedTemplates(t, prompttemplate.Template{Name: "bare", Prompt: "Just words."})
	m := openNewForm(t, f)
	before := formOf(t, m).agent.value()
	m = startPick(t, m, "bare")
	m = schedPress(t, m, "enter")
	if fm := formOf(t, m); fm.agent.value() != before || fm.prompt.Value() != "Just words." {
		t.Errorf("agent %q (was %q), prompt %q", fm.agent.value(), before, fm.prompt.Value())
	}
}

func TestScheduleStartFromTemplateConfirmsReplace(t *testing.T) {
	f := newSchedFixture(t)
	withSchedTemplates(t, schedTriage, schedFull)
	m := openNewForm(t, f)
	m = focusStopT(t, m, stopPrompt)
	m = schedType(t, m, "mine")
	m = startPick(t, m, "triage")
	m = schedPress(t, m, "enter")
	fm := formOf(t, m)
	if fm.startAsk != "triage" || fm.prompt.Value() != "mine" {
		t.Fatalf("expected a pending confirm, got ask %q prompt %q", fm.startAsk, fm.prompt.Value())
	}
	if out := ansi.Strip(m.render()); !strings.Contains(out, "replace the prompt with triage? y/n") {
		t.Errorf("no confirm shown:\n%s", out)
	}
	m = schedPress(t, m, "n")
	if fm := formOf(t, m); fm.startAsk != "" || fm.prompt.Value() != "mine" {
		t.Fatalf("n should keep the prompt: ask %q prompt %q", fm.startAsk, fm.prompt.Value())
	}
	m = schedPress(t, m, "enter", "y")
	if got := formOf(t, m).prompt.Value(); got != "Triage the backlog." {
		t.Fatalf("y should replace, prompt %q", got)
	}
	// A second pick asks again and replaces after y.
	m = startPick(t, m, "full")
	m = schedPress(t, m, "enter")
	if formOf(t, m).startAsk != "full" {
		t.Fatal("second pick did not ask")
	}
	m = schedPress(t, m, "y")
	if fm := formOf(t, m); fm.prompt.Value() != "Do the full thing." || fm.startAsk != "" {
		t.Errorf("second pick: prompt %q ask %q", fm.prompt.Value(), fm.startAsk)
	}
}

func TestScheduleStartFromTemplateNoTemplatesNoPicker(t *testing.T) {
	f := newSchedFixture(t)
	withSchedTemplates(t)
	m := openNewForm(t, f)
	if slices.Contains(formOf(t, m).stops(), stopStartTmpl) || strings.Contains(ansi.Strip(m.render()), "Start from") {
		t.Error("the picker shows with no templates")
	}
}

func TestScheduleStartFromTemplateDirChangeKeepsPrompt(t *testing.T) {
	f := newSchedFixture(t)
	withSchedTemplates(t, schedTriage)
	m := openNewForm(t, f)
	m = startPick(t, m, "triage")
	m = schedPress(t, m, "enter")
	m = focusStopT(t, m, stopDir)
	m = schedPress(t, m, "right")
	fm := formOf(t, m)
	if fm.prompt.Value() != "Triage the backlog." {
		t.Errorf("prompt lost on dir change: %q", fm.prompt.Value())
	}
	if fm.start.value() != "triage" {
		t.Errorf("picker lost its template: %q", fm.start.value())
	}
}

func TestScheduleNewLineClickOpensFormDirectly(t *testing.T) {
	f := newSchedFixture(t)
	m := showSchedules(t, f.model(t))
	idx := -1
	for i, l := range m.lines {
		if l.kind == fleetLineSchedule && l.schedNew {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no + new schedule line")
	}
	m = schedClickLine(t, m, idx)
	if m.schedForm == nil || m.schedForm.mode != schedFormNew {
		t.Fatal("one click on + new schedule should open the form")
	}
}
