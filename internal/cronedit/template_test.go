package cronedit

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/prompttemplate"
)

// withTemplates makes ts resolve only the given templates.
func (ts *testStore) withTemplates(tmpls ...prompttemplate.Template) {
	ts.LoadTemplate = func(name, _ string) (prompttemplate.Template, error) {
		for _, t := range tmpls {
			if t.Name == name {
				return t, nil
			}
		}
		return prompttemplate.Template{}, fmt.Errorf("%w: %q", prompttemplate.ErrNotFound, name)
	}
}

func (ts *testStore) templateJob(name, tmpl string) cron.Job {
	j := ts.job(name)
	j.Prompt, j.Template = "", tmpl
	return j
}

func TestValidateTemplateJobs(t *testing.T) {
	ts := newTestStore(t)
	ts.withTemplates(
		prompttemplate.Template{Name: "triage", Agent: "brandon", Prompt: "triage the backlog"},
		prompttemplate.Template{Name: "noagent", Prompt: "p"},
		prompttemplate.Template{Name: "ghost", Agent: "ghost", Prompt: "p"},
	)
	tests := []struct {
		name       string
		mut        func(j *cron.Job)
		wantFields []string
		wantMsg    string
	}{
		{name: "template with agent", mut: func(*cron.Job) {}},
		{name: "agent from the template", mut: func(j *cron.Job) { j.Agent = "" }},
		{name: "template missing", mut: func(j *cron.Job) { j.Template = "gone" },
			wantFields: []string{cron.FieldTemplate}, wantMsg: `template "gone" not found`},
		{name: "no agent anywhere", mut: func(j *cron.Job) { j.Agent, j.Template = "", "noagent" },
			wantFields: []string{cron.FieldAgent}, wantMsg: "sets no agent"},
		{name: "job agent wins over the template's", mut: func(j *cron.Job) { j.Template = "ghost" }},
		{name: "template agent is checked", mut: func(j *cron.Job) { j.Agent, j.Template = "", "ghost" },
			wantFields: []string{cron.FieldAgent}, wantMsg: `unknown agent "ghost"`},
		{name: "template and prompt", mut: func(j *cron.Job) { j.Prompt = "x" },
			wantFields: []string{cron.FieldTemplate}, wantMsg: "not both"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := ts.templateJob("t", "triage")
			tt.mut(&j)
			_, errs := ts.Validate(j, "")
			if got := fields(errs); !slices.Equal(got, tt.wantFields) && (len(got) > 0 || len(tt.wantFields) > 0) {
				t.Fatalf("fields = %v (%+v), want %v", got, errs, tt.wantFields)
			}
			if tt.wantMsg != "" && !strings.Contains(errs[0].Message, tt.wantMsg) {
				t.Errorf("message = %q, want containing %q", errs[0].Message, tt.wantMsg)
			}
		})
	}
}

func TestTemplateJobDeletedShowsBroken(t *testing.T) {
	ts := newTestStore(t)
	tmpl := prompttemplate.Template{Name: "triage", Agent: "brandon", Prompt: "triage the backlog", Model: "opus"}
	ts.withTemplates(tmpl)
	if _, err := ts.Create(ts.templateJob("nightly", "triage")); err != nil {
		t.Fatal(err)
	}
	d, err := ts.Detail("nightly")
	if err != nil {
		t.Fatal(err)
	}
	if d.Invalid != "" || d.TemplateError != "" || d.TemplateResolved == nil || d.TemplateResolved.Prompt != tmpl.Prompt {
		t.Fatalf("detail = %+v", d)
	}

	ts.withTemplates() // the template is deleted
	ov, err := ts.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Jobs) != 1 || ov.Jobs[0].Invalid != `template "triage" not found` {
		t.Errorf("overview status = %+v", ov.Jobs)
	}
	d, _ = ts.Detail("nightly")
	if d.TemplateResolved != nil || d.TemplateError != `template "triage" not found` || d.Invalid == "" {
		t.Errorf("detail after delete = %+v", d)
	}
	// A broken job can still be paused.
	if _, err := ts.SetEnabled("nightly", false); err != nil {
		t.Errorf("pause broken job: %v", err)
	}
}

func TestSwitchTemplateAndPrompt(t *testing.T) {
	ts := newTestStore(t)
	ts.withTemplates(prompttemplate.Template{Name: "triage", Agent: "brandon", Prompt: "p"})
	if _, err := ts.Create(ts.templateJob("j", "triage")); err != nil {
		t.Fatal(err)
	}
	res, err := ts.Modify("j", func(j *cron.Job) error {
		j.Template, j.Prompt = "", "inline now"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Job.Template != "" || res.Job.Prompt != "inline now" || !slices.Equal(res.Changed, []string{"prompt", "template"}) {
		t.Errorf("to prompt: %+v", res)
	}
	res, err = ts.Modify("j", func(j *cron.Job) error {
		j.Template, j.Prompt, j.Agent = "triage", "", ""
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Job.Template != "triage" || res.Job.Prompt != "" || res.Job.Agent != "" {
		t.Errorf("back to template: %+v", res.Job)
	}
	if got := Diff(ts.job("j"), res.Job); !slices.ContainsFunc(got, func(c FieldChange) bool {
		return c.Field == cron.FieldTemplate && c.New == "triage"
	}) {
		t.Errorf("diff = %+v", got)
	}
}

func TestTemplateErrorText(t *testing.T) {
	if got := TemplateErrorText("x", fmt.Errorf("w: %w", prompttemplate.ErrNotFound)); got != `template "x" not found` {
		t.Errorf("not found: %q", got)
	}
	if got := TemplateErrorText("x", errors.New("bad frontmatter")); got != `template "x": bad frontmatter` {
		t.Errorf("other: %q", got)
	}
}
