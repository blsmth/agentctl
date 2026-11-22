package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/prompttemplate"
)

// `agentctl template`: named, reusable dispatch prompts (internal/prompttemplate).
// Writes from an agent session go through the approval popup
// (internal/hooks/template_preview.go); a plain shell is never gated.

// TemplateAddOptions are `template add`'s flags.
type TemplateAddOptions struct {
	Name        string
	Description string
	Agent       string
	Model       string
	Dir         string
	Effort      string
	Prompt      string
	PromptFile  string
	// Project writes to the project's .agentctl/templates instead of the
	// global dir.
	Project bool
}

// TemplateEditOptions are `template edit`'s flags. A nil field is
// unchanged; an empty one clears it. With no field set the file opens in
// $EDITOR.
type TemplateEditOptions struct {
	Name        string
	Description *string
	Agent       *string
	Model       *string
	Dir         *string
	Effort      *string
	Prompt      *string
	PromptFile  *string
	Project     bool
}

// Seams for tests.
var (
	// templateCwd is the directory project templates resolve from.
	templateCwd = os.Getwd
	// stdinIsTerminal reports whether $EDITOR can be used.
	stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	// runEditor opens path in the user's editor and waits for it.
	runEditor = runUserEditor
)

func templateScope(project bool) prompttemplate.Scope {
	if project {
		return prompttemplate.ScopeProject
	}
	return prompttemplate.ScopeGlobal
}

func templateProjectDir() string {
	d, err := templateCwd()
	if err != nil {
		return ""
	}
	return d
}

// RunTemplateList prints the templates visible from the cwd.
func RunTemplateList(w io.Writer, asJSON bool) error {
	list, problems, err := prompttemplate.List(templateProjectDir())
	if err != nil {
		return err
	}
	if asJSON {
		if list == nil {
			list = []prompttemplate.Template{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Templates []prompttemplate.Template `json:"templates"`
			Problems  []prompttemplate.Problem  `json:"problems,omitempty"`
		}{list, problems})
	}
	if len(list) == 0 {
		fmt.Fprintln(w, "No templates. Add one with: agentctl template add <name> --prompt '...'")
	}
	if len(list) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSCOPE\tAGENT\tDESCRIPTION")
		for _, t := range list {
			scope := string(t.Scope)
			if t.Shadows != "" {
				scope += " (shadows global)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Name, scope, orDashStr(t.Agent), t.Description)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	for _, p := range problems {
		fmt.Fprintf(w, "warning: %s: %s\n", p.Path, p.Err)
	}
	return nil
}

func orDashStr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// RunTemplateShow prints one template: where it lives, then the file.
func RunTemplateShow(w io.Writer, name string) error {
	t, err := prompttemplate.Load(name, templateProjectDir())
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "# %s (%s: %s)\n", t.Name, t.Scope, t.Path)
	if t.Shadows != "" {
		fmt.Fprintf(w, "# shadows %s\n", t.Shadows)
	}
	_, err = w.Write(prompttemplate.Render(t))
	return err
}

// RunTemplateAdd creates a template. The prompt comes from --prompt,
// --prompt-file, or $EDITOR when stdin is a terminal.
func RunTemplateAdd(w io.Writer, opts TemplateAddOptions) error {
	if !prompttemplate.ValidName(opts.Name) {
		return fmt.Errorf("invalid template name %q", opts.Name)
	}
	if opts.Prompt != "" && opts.PromptFile != "" {
		return errors.New("--prompt and --prompt-file are mutually exclusive")
	}
	scope, cwd := templateScope(opts.Project), templateProjectDir()
	path, err := prompttemplate.Path(opts.Name, scope, cwd)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("template %q already exists at %s; use agentctl template edit", opts.Name, path)
	}
	t := prompttemplate.Template{
		Name: opts.Name, Description: opts.Description, Agent: opts.Agent,
		Model: opts.Model, Dir: opts.Dir, Effort: opts.Effort, Prompt: opts.Prompt,
	}
	switch {
	case opts.PromptFile != "":
		data, err := os.ReadFile(paths.ExpandHome(opts.PromptFile))
		if err != nil {
			return fmt.Errorf("reading --prompt-file: %w", err)
		}
		t.Prompt = string(data)
	case opts.Prompt == "":
		if t, err = editTemplate(t); err != nil {
			return err
		}
	}
	saved, err := prompttemplate.Save(t, scope, cwd)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Added template %s (%s)\n", t.Name, saved)
	return nil
}

// RunTemplateEdit changes a template: the given flags patch its fields and
// prompt; with none, the file opens in $EDITOR.
func RunTemplateEdit(w io.Writer, opts TemplateEditOptions) error {
	scope, cwd := templateScope(opts.Project), templateProjectDir()
	t, err := prompttemplate.LoadScope(opts.Name, scope, cwd)
	if err != nil {
		if errors.Is(err, prompttemplate.ErrNotFound) && !opts.Project {
			if p, perr := prompttemplate.LoadScope(opts.Name, prompttemplate.ScopeProject, cwd); perr == nil {
				return fmt.Errorf("%w; a project template exists at %s (pass --project)", err, p.Path)
			}
		}
		return err
	}
	if opts.Prompt != nil && opts.PromptFile != nil {
		return errors.New("--prompt and --prompt-file are mutually exclusive")
	}
	patched := false
	for _, f := range []struct {
		v   *string
		dst *string
	}{
		{opts.Description, &t.Description}, {opts.Agent, &t.Agent}, {opts.Model, &t.Model},
		{opts.Dir, &t.Dir}, {opts.Effort, &t.Effort}, {opts.Prompt, &t.Prompt},
	} {
		if f.v != nil {
			*f.dst, patched = *f.v, true
		}
	}
	if opts.PromptFile != nil {
		data, err := os.ReadFile(paths.ExpandHome(*opts.PromptFile))
		if err != nil {
			return fmt.Errorf("reading --prompt-file: %w", err)
		}
		t.Prompt, patched = string(data), true
	}
	if !patched {
		if t, err = editTemplate(t); err != nil {
			return err
		}
	}
	saved, err := prompttemplate.Save(t, scope, cwd)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Updated template %s (%s)\n", t.Name, saved)
	return nil
}

// RunTemplateRm removes a template from the global dir, or the project's
// with --project.
func RunTemplateRm(w io.Writer, name string, project bool) error {
	path, err := prompttemplate.Delete(name, templateScope(project), templateProjectDir())
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Removed template %s (%s)\n", name, path)
	return nil
}

// editTemplate opens t in $EDITOR (a temp copy; the template is saved only
// if the result parses) and returns the edited template.
func editTemplate(t prompttemplate.Template) (prompttemplate.Template, error) {
	if !stdinIsTerminal() {
		return t, errors.New("no prompt given and stdin is not a terminal: pass --prompt or --prompt-file")
	}
	if strings.TrimSpace(t.Prompt) == "" {
		t.Prompt = "<write the prompt here>"
	}
	f, err := os.CreateTemp("", "agentctl-template-"+t.Name+"-*.md")
	if err != nil {
		return t, fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(f.Name()) //nolint:errcheck
	if _, err := f.Write(prompttemplate.Render(t)); err != nil {
		f.Close()
		return t, fmt.Errorf("writing temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return t, fmt.Errorf("writing temp file: %w", err)
	}
	if err := runEditor(f.Name()); err != nil {
		return t, fmt.Errorf("editor: %w", err)
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		return t, fmt.Errorf("reading edited template: %w", err)
	}
	edited, err := prompttemplate.Parse(t.Name, data)
	if err != nil {
		return t, fmt.Errorf("edited template not saved: %w", err)
	}
	return edited, nil
}

// runUserEditor runs $VISUAL, else $EDITOR, else vi on path.
func runUserEditor(path string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command("sh", "-c", editor+` "$1"`, "sh", path)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
