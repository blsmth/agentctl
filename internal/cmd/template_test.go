package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/prompttemplate"
)

// templateEnv isolates the global config dir and makes a project the cwd.
func templateEnv(t *testing.T) (globalDir, project string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project = t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	oldCwd, oldTTY, oldEditor := templateCwd, stdinIsTerminal, runEditor
	t.Cleanup(func() { templateCwd, stdinIsTerminal, runEditor = oldCwd, oldTTY, oldEditor })
	templateCwd = func() (string, error) { return project, nil }
	stdinIsTerminal = func() bool { return false }
	runEditor = func(string) error { t.Fatal("editor must not run"); return nil }
	gd, err := prompttemplate.GlobalDir()
	if err != nil {
		t.Fatal(err)
	}
	return gd, project
}

func TestTemplateAddShowListRm(t *testing.T) {
	gd, project := templateEnv(t)
	var out bytes.Buffer
	if err := RunTemplateAdd(&out, TemplateAddOptions{Name: "review", Description: "Review PRs", Agent: "code-reviewer", Prompt: "Review open PRs."}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(gd, "review.md")); err != nil {
		t.Fatalf("global file: %v", err)
	}
	// Adding again is refused.
	if err := RunTemplateAdd(&out, TemplateAddOptions{Name: "review", Prompt: "x"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate add err = %v", err)
	}
	pf := filepath.Join(t.TempDir(), "p.md")
	if err := os.WriteFile(pf, []byte("Project review.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunTemplateAdd(&out, TemplateAddOptions{Name: "review", PromptFile: pf, Project: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, ".agentctl", "templates", "review.md")); err != nil {
		t.Fatalf("project file: %v", err)
	}

	out.Reset()
	if err := RunTemplateShow(&out, "review"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Project review.") || !strings.Contains(out.String(), "shadows") {
		t.Errorf("show = %q", out.String())
	}

	out.Reset()
	if err := RunTemplateList(&out, true); err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Templates []prompttemplate.Template `json:"templates"`
	}
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil {
		t.Fatalf("list --json: %v\n%s", err, out.String())
	}
	if len(listed.Templates) != 1 || listed.Templates[0].Scope != prompttemplate.ScopeProject {
		t.Errorf("list = %+v", listed.Templates)
	}

	out.Reset()
	if err := RunTemplateList(&out, false); err != nil || !strings.Contains(out.String(), "review") {
		t.Errorf("list = %q, %v", out.String(), err)
	}

	if err := RunTemplateRm(&out, "review", true); err != nil {
		t.Fatal(err)
	}
	if err := RunTemplateRm(&out, "review", false); err != nil {
		t.Fatal(err)
	}
	if err := RunTemplateRm(&out, "review", false); err == nil {
		t.Error("rm of a missing template succeeded")
	}
}

func TestTemplateAdd_NoPromptNoTTY(t *testing.T) {
	templateEnv(t)
	err := RunTemplateAdd(&bytes.Buffer{}, TemplateAddOptions{Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "--prompt or --prompt-file") {
		t.Errorf("err = %v", err)
	}
}

func TestTemplateAdd_Editor(t *testing.T) {
	gd, _ := templateEnv(t)
	stdinIsTerminal = func() bool { return true }
	runEditor = func(path string) error {
		return os.WriteFile(path, []byte("---\nagent: writer\n---\nFrom the editor.\n"), 0o600)
	}
	if err := RunTemplateAdd(&bytes.Buffer{}, TemplateAddOptions{Name: "ed"}); err != nil {
		t.Fatal(err)
	}
	got, err := prompttemplate.LoadScope("ed", prompttemplate.ScopeGlobal, "")
	if err != nil || got.Agent != "writer" || got.Prompt != "From the editor." {
		t.Errorf("saved = %+v, %v (dir %s)", got, err, gd)
	}
	// A broken edit is not saved.
	runEditor = func(path string) error { return os.WriteFile(path, []byte("---\nagnet: x\n---\nb"), 0o600) }
	if err := RunTemplateAdd(&bytes.Buffer{}, TemplateAddOptions{Name: "bad"}); err == nil {
		t.Error("broken edit saved")
	}
}

func TestTemplateEdit(t *testing.T) {
	templateEnv(t)
	if _, err := prompttemplate.Save(prompttemplate.Template{Name: "e", Agent: "a", Model: "opus", Prompt: "old"}, prompttemplate.ScopeGlobal, ""); err != nil {
		t.Fatal(err)
	}
	empty, newPrompt := "", "new prompt"
	if err := RunTemplateEdit(&bytes.Buffer{}, TemplateEditOptions{Name: "e", Model: &empty, Prompt: &newPrompt}); err != nil {
		t.Fatal(err)
	}
	got, _ := prompttemplate.LoadScope("e", prompttemplate.ScopeGlobal, "")
	if got.Agent != "a" || got.Model != "" || got.Prompt != "new prompt" {
		t.Errorf("edited = %+v", got)
	}
	// No flags and no terminal: refused rather than silently unchanged.
	if err := RunTemplateEdit(&bytes.Buffer{}, TemplateEditOptions{Name: "e"}); err == nil {
		t.Error("edit with no flags and no TTY succeeded")
	}
	// A project-only template points at --project.
	if _, err := prompttemplate.Save(prompttemplate.Template{Name: "p", Prompt: "x"}, prompttemplate.ScopeProject, templateProjectDir()); err != nil {
		t.Fatal(err)
	}
	if err := RunTemplateEdit(&bytes.Buffer{}, TemplateEditOptions{Name: "p", Prompt: &newPrompt}); err == nil || !strings.Contains(err.Error(), "--project") {
		t.Errorf("err = %v, want a --project hint", err)
	}
}
