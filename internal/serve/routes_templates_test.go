package serve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/prompttemplate"
)

func decodeTemplates(t *testing.T, body []byte) []templateInfo {
	t.Helper()
	var got struct {
		Templates []templateInfo `json:"templates"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	return got.Templates
}

func TestTemplatesRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	e := newReadEnv(t)

	// No templates dir at all: an empty list, not an error.
	w := e.get("/v1/templates")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := decodeTemplates(t, w.Body.Bytes()); len(got) != 0 {
		t.Fatalf("templates = %+v, want none", got)
	}
	if !strings.Contains(w.Body.String(), `"templates":[]`) {
		t.Errorf("empty list should encode as [], body %s", w.Body)
	}

	proj := t.TempDir()
	if err := os.Mkdir(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := testGroup()
	g.Dir = proj
	e.fleet.set(g)
	save := func(tpl prompttemplate.Template, scope prompttemplate.Scope) {
		t.Helper()
		if _, err := prompttemplate.Save(tpl, scope, proj); err != nil {
			t.Fatal(err)
		}
	}
	save(prompttemplate.Template{Name: "triage", Description: "global", Agent: "a", Prompt: "global body"}, prompttemplate.ScopeGlobal)
	save(prompttemplate.Template{Name: "triage", Description: "local", Agent: "b", Model: "opus", Dir: "~/code/x", Effort: "high", Prompt: "local body"}, prompttemplate.ScopeProject)
	save(prompttemplate.Template{Name: "audit", Prompt: "audit body"}, prompttemplate.ScopeGlobal)

	got := decodeTemplates(t, e.get("/v1/templates?dir="+proj).Body.Bytes())
	if len(got) != 2 || got[0].Name != "audit" || got[1].Name != "triage" {
		t.Fatalf("templates = %+v", got)
	}
	tr := got[1]
	if tr.Scope != "project" || tr.Agent != "b" || tr.Model != "opus" || tr.Effort != "high" || tr.Prompt != "local body" || tr.Dir != filepath.Join(home, "code/x") {
		t.Errorf("triage = %+v", tr)
	}

	// Without a project, only global templates; an unknown project is refused.
	got = decodeTemplates(t, e.get("/v1/templates").Body.Bytes())
	if len(got) != 2 || got[1].Scope != "global" {
		t.Errorf("global-only = %+v", got)
	}
	if w := e.get("/v1/templates?dir=/etc"); w.Code != 400 {
		t.Errorf("unknown dir status %d, want 400", w.Code)
	}
}

func TestDispatchTemplateMetadata(t *testing.T) {
	e := newWriteEnv(t, true)
	if !slices.Contains(e.Server.router.Features(), "dispatch-templates") {
		t.Error("dispatch-templates not advertised with writes enabled")
	}
	w := e.post("/v1/dispatch", `{"agent":"go-engineer","project_dir":"/code/proj","prompt":"p","template":"triage"}`)
	if w.Code != 200 || len(e.disp.got) != 1 || e.disp.got[0].Template != "triage" {
		t.Fatalf("status %d, dispatch %+v", w.Code, e.disp.got)
	}
	w = e.post("/v1/dispatch", `{"agent":"go-engineer","project_dir":"/code/proj","prompt":"p","template":"../x"}`)
	if w.Code != 400 {
		t.Errorf("bad template name status %d, want 400", w.Code)
	}
	e = newWriteEnv(t, false)
	if slices.Contains(e.Server.router.Features(), "dispatch-templates") {
		t.Error("dispatch-templates advertised with writes disabled")
	}
}
