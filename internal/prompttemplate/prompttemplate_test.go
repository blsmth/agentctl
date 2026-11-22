package prompttemplate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points the global config dir at a temp dir and returns it plus a
// fresh project checkout.
func isolate(t *testing.T) (globalDir, project string) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "agentctl"), 0o700); err != nil {
		t.Fatal(err)
	}
	project = t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	gd, err := GlobalDir()
	if err != nil {
		t.Fatal(err)
	}
	return gd, project
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    Template
		wantErr string
	}{
		{
			name: "full frontmatter",
			data: "---\ndescription: Review PRs\nagent: code-reviewer\nmodel: opus\ndir: ~/code/x\neffort: high\n---\n\nReview every open PR.\n",
			want: Template{Name: "t", Description: "Review PRs", Agent: "code-reviewer", Model: "opus", Dir: "~/code/x", Effort: "high", Prompt: "Review every open PR."},
		},
		{name: "no frontmatter", data: "Just a prompt\n\nwith lines\n", want: Template{Name: "t", Prompt: "Just a prompt\n\nwith lines"}},
		{name: "empty frontmatter", data: "---\n---\nBody", want: Template{Name: "t", Prompt: "Body"}},
		{name: "crlf", data: "---\r\nagent: a\r\n---\r\nBody\r\n", want: Template{Name: "t", Agent: "a", Prompt: "Body"}},
		{name: "unclosed", data: "---\nagent: a\nBody", wantErr: "no closing"},
		{name: "bad yaml", data: "---\nagent: [a\n---\nBody", wantErr: "parsing frontmatter"},
		{name: "unknown key", data: "---\nagnet: a\n---\nBody", wantErr: "agnet"},
		{name: "empty body", data: "---\nagent: a\n---\n\n", wantErr: "no prompt"},
		{name: "bad effort", data: "---\neffort: huge\n---\nBody", wantErr: "invalid effort"},
		{name: "bad agent", data: "---\nagent: a b\n---\nBody", wantErr: "invalid agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse("t", []byte(tt.data))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestRenderRoundTrip(t *testing.T) {
	for _, tt := range []Template{
		{Name: "a", Prompt: "only body\n\n---\nnot frontmatter"},
		{Name: "b", Description: "d: with colon", Agent: "go-engineer", Model: "sonnet", Dir: "~/x", Effort: "low", Prompt: "do it"},
	} {
		got, err := Parse(tt.Name, Render(tt))
		if err != nil {
			t.Fatalf("%s: %v", tt.Name, err)
		}
		if got != tt {
			t.Errorf("round trip: got %+v, want %+v", got, tt)
		}
	}
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{
		"review-prs": true, "a.b_c": true, "": false, ".hidden": false, "-flag": false,
		"a/b": false, "../x": false, "a b": false, strings.Repeat("x", 65): false,
	} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestLoad_ProjectShadowsGlobal(t *testing.T) {
	gd, project := isolate(t)
	writeFile(t, filepath.Join(gd, "x.md"), "global body")
	writeFile(t, filepath.Join(gd, "only-global.md"), "g2")

	got, err := Load("x", project)
	if err != nil || got.Prompt != "global body" || got.Scope != ScopeGlobal {
		t.Fatalf("Load before project = %+v, %v", got, err)
	}

	pfile := filepath.Join(project, ".agentctl", "templates", "x.md")
	writeFile(t, pfile, "project body")
	got, err = Load("x", project)
	if err != nil || got.Prompt != "project body" || got.Scope != ScopeProject || got.Path != pfile {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if got.Shadows != filepath.Join(gd, "x.md") {
		t.Errorf("Shadows = %q", got.Shadows)
	}

	// From a worktree of the project, the project's template is found.
	wt := filepath.Join(project, ".worktrees", "agent-x", "sub")
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := Load("x", wt); err != nil || got.Scope != ScopeProject {
		t.Errorf("Load from worktree = %+v, %v", got, err)
	}

	// No project dir: global only.
	if got, err := Load("x", ""); err != nil || got.Scope != ScopeGlobal {
		t.Errorf("Load no project = %+v, %v", got, err)
	}
}

func TestLoad_Errors(t *testing.T) {
	gd, project := isolate(t)
	if _, err := Load("missing", project); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: err = %v, want ErrNotFound", err)
	}
	if _, err := Load("../etc", project); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("invalid name: err = %v", err)
	}
	// A broken project file never falls back to the global one.
	writeFile(t, filepath.Join(gd, "x.md"), "global")
	writeFile(t, filepath.Join(project, ".agentctl", "templates", "x.md"), "---\nbad: [\n---\nb")
	if _, err := Load("x", project); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("broken project file: err = %v, want a parse error", err)
	}
}

func TestList(t *testing.T) {
	gd, project := isolate(t)
	// Missing dirs: empty, no error.
	if got, probs, err := List(project); err != nil || len(got) != 0 || len(probs) != 0 {
		t.Fatalf("List empty = %v %v %v", got, probs, err)
	}
	writeFile(t, filepath.Join(gd, "b.md"), "---\ndescription: global b\n---\nB")
	writeFile(t, filepath.Join(gd, "a.md"), "A")
	writeFile(t, filepath.Join(gd, "broken.md"), "---\nx: [\n---\nB")
	writeFile(t, filepath.Join(gd, "notes.txt"), "ignored")
	writeFile(t, filepath.Join(project, ".agentctl", "templates", "b.md"), "---\ndescription: project b\n---\nPB")
	writeFile(t, filepath.Join(project, ".agentctl", "templates", "c.md"), "C")

	got, probs, err := List(project)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tm := range got {
		names = append(names, tm.Name+":"+string(tm.Scope))
	}
	if strings.Join(names, ",") != "a:global,b:project,c:project" {
		t.Errorf("List = %v", names)
	}
	if got[1].Shadows == "" || got[1].Description != "project b" {
		t.Errorf("b = %+v", got[1])
	}
	if len(probs) != 1 || !strings.HasSuffix(probs[0].Path, "broken.md") {
		t.Errorf("problems = %+v", probs)
	}
}

func TestSaveDelete(t *testing.T) {
	gd, project := isolate(t)
	tm := Template{Name: "n", Agent: "go-engineer", Prompt: "do"}
	path, err := Save(tm, ScopeGlobal, "")
	if err != nil || path != filepath.Join(gd, "n.md") {
		t.Fatalf("Save = %q, %v", path, err)
	}
	ppath, err := Save(Template{Name: "n", Prompt: "p"}, ScopeProject, project)
	if err != nil || ppath != filepath.Join(project, ".agentctl", "templates", "n.md") {
		t.Fatalf("Save project = %q, %v", ppath, err)
	}
	if got, err := LoadScope("n", ScopeGlobal, ""); err != nil || got.Agent != "go-engineer" {
		t.Errorf("LoadScope = %+v, %v", got, err)
	}
	if _, err := Save(Template{Name: "bad/name", Prompt: "p"}, ScopeGlobal, ""); err == nil {
		t.Error("Save accepted an invalid name")
	}
	if _, err := Save(Template{Name: "e"}, ScopeGlobal, ""); err == nil {
		t.Error("Save accepted an empty prompt")
	}
	if _, err := Delete("n", ScopeGlobal, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Delete("n", ScopeGlobal, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete err = %v", err)
	}
	if got, err := Load("n", project); err != nil || got.Scope != ScopeProject {
		t.Errorf("project copy survives: %+v, %v", got, err)
	}
	if _, err := Save(tm, ScopeProject, ""); err == nil {
		t.Error("Save project scope with no project dir succeeded")
	}
}

func TestCompose(t *testing.T) {
	if got := Compose("body\n", "  "); got != "body" {
		t.Errorf("no extra: %q", got)
	}
	want := "body\n\n" + AdditionalInstructionsHeading + "\n\nfocus on X"
	if got := Compose("body", "focus on X\n"); got != want {
		t.Errorf("Compose = %q, want %q", got, want)
	}
}
