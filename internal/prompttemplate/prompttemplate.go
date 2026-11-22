// Package prompttemplate stores named, reusable dispatch prompts: one
// Markdown file per template, YAML frontmatter (description, agent, model,
// dir, effort; all optional) plus a body that is the prompt.
//
// Global templates live in <config.GlobalDir()>/templates/<name>.md. A
// project's live in <project>/.agentctl/templates/<name>.md and shadow a
// global one of the same name, the same rule as agents. The project of a
// directory is the checkout a `.worktrees/<slug>` worktree was cut from, else
// the nearest ancestor with a .git entry, else the directory itself, so a
// dispatched worker in a worktree sees its project's templates.
//
// It is a leaf package (config, agentname, paths, launchopts and yaml only),
// so the dispatch CLI, the approval hook and later the phone and hub can all
// load templates the same way. Paths resolve on each call, never at init.
package prompttemplate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/blsmth/agentctl/internal/agentname"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/launchopts"
	"github.com/blsmth/agentctl/internal/paths"
)

// Scope says where a template is stored.
type Scope string

// Template scopes.
const (
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
)

// ext is the template file extension.
const ext = ".md"

// AdditionalInstructionsHeading heads the extra instructions a dispatch
// --prompt adds to a template's prompt. It is the wording the phone's
// "Additional instructions" field uses (dispatch.AdditionalInstructionsHeading,
// pinned by a test in internal/cmd).
const AdditionalInstructionsHeading = "## Additional instructions from the user"

// ErrNotFound is returned (wrapped) when no template has the name.
var ErrNotFound = errors.New("template not found")

// Template is one prompt template.
type Template struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Model       string `json:"model,omitempty"`
	// Dir is the project dir as written in the file ("~/" not expanded; see
	// ExpandedDir).
	Dir    string `json:"dir,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Prompt is the file body.
	Prompt string `json:"prompt"`
	Scope  Scope  `json:"scope"`
	// Path is the file the template was read from ("" before Save).
	Path string `json:"path,omitempty"`
	// Shadows is the global file a project template hides, if any.
	Shadows string `json:"shadows,omitempty"`
}

// ExpandedDir is Dir with a leading "~/" expanded.
func (t Template) ExpandedDir() string { return paths.ExpandHome(t.Dir) }

// frontmatter is the on-disk header.
type frontmatter struct {
	Description string `yaml:"description,omitempty"`
	Agent       string `yaml:"agent,omitempty"`
	Model       string `yaml:"model,omitempty"`
	Dir         string `yaml:"dir,omitempty"`
	Effort      string `yaml:"effort,omitempty"`
}

// ValidName reports whether name may name a template: the agent-name rule
// (ASCII letters, digits, '.', '_' and '-', no leading '.' or '-').
func ValidName(name string) bool { return agentname.Valid(name) }

// GlobalDir is the global templates directory.
func GlobalDir() (string, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return "", fmt.Errorf("resolving global config dir: %w", err)
	}
	return filepath.Join(dir, "templates"), nil
}

// ProjectDir is the templates directory of the project dir belongs to, or ""
// when dir is "".
func ProjectDir(dir string) string {
	root := ProjectRoot(dir)
	if root == "" {
		return ""
	}
	return filepath.Join(root, ".agentctl", "templates")
}

// ProjectRoot returns the project dir belongs to (see the package comment).
// It only stats files. It mirrors dispatchplan.ProjectRoot, which this leaf
// package cannot import.
func ProjectRoot(dir string) string {
	if dir == "" {
		return ""
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	dir = filepath.Clean(dir)
	sep := string(filepath.Separator)
	if i := strings.Index(dir+sep, sep+".worktrees"+sep); i > 0 {
		return dir[:i]
	}
	for d := dir; ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}

// ScopeDir is the directory a scope stores templates in, for projectDir
// (only used by ScopeProject).
func ScopeDir(scope Scope, projectDir string) (string, error) {
	switch scope {
	case ScopeGlobal:
		return GlobalDir()
	case ScopeProject:
		d := ProjectDir(projectDir)
		if d == "" {
			return "", errors.New("no project directory")
		}
		return d, nil
	}
	return "", fmt.Errorf("unknown template scope %q", scope)
}

// Path is the file a template of name would have in scope.
func Path(name string, scope Scope, projectDir string) (string, error) {
	if !ValidName(name) {
		return "", invalidName(name)
	}
	dir, err := ScopeDir(scope, projectDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+ext), nil
}

func invalidName(name string) error {
	return fmt.Errorf("invalid template name %q (allowed: letters, digits, '.', '_' and '-', no leading '.' or '-', at most %d)",
		name, agentname.MaxLen)
}

// Parse decodes a template file. A file with no frontmatter is all prompt.
// Unknown frontmatter keys are an error, so a typo (agnet:) does not
// silently drop a default.
func Parse(name string, data []byte) (Template, error) {
	t := Template{Name: name}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	body := content
	if strings.HasPrefix(content, "---\n") {
		// Search from the newline ending the opening delimiter, so an
		// empty header ("---\n---") closes too.
		end := strings.Index(content[3:], "\n---")
		if end < 0 {
			return Template{}, errors.New("frontmatter has no closing ---")
		}
		end-- // the header's length; -1 when empty
		header := ""
		if end > 0 {
			header = content[4 : 4+end]
		}
		var fm frontmatter
		dec := yaml.NewDecoder(strings.NewReader(header))
		dec.KnownFields(true)
		if err := dec.Decode(&fm); err != nil && !errors.Is(err, io.EOF) {
			return Template{}, fmt.Errorf("parsing frontmatter: %w", err)
		}
		t.Description = strings.TrimSpace(fm.Description)
		t.Agent = strings.TrimSpace(fm.Agent)
		t.Model = strings.TrimSpace(fm.Model)
		t.Dir = strings.TrimSpace(fm.Dir)
		t.Effort = strings.TrimSpace(fm.Effort)
		body = content[4+end+4:] // past "\n---"
		// The rest of the closing delimiter line, then one blank line.
		if nl := strings.IndexByte(body, '\n'); nl >= 0 && strings.TrimSpace(body[:nl]) == "" {
			body = body[nl+1:]
		} else if strings.TrimSpace(body) == "" {
			body = ""
		}
		body = strings.TrimPrefix(body, "\n")
	}
	t.Prompt = strings.TrimRight(body, "\n")
	if err := t.Validate(); err != nil {
		return Template{}, err
	}
	return t, nil
}

// Validate checks a template's fields: a valid name, a non-empty prompt, a
// launchable agent name and a known effort.
func (t Template) Validate() error {
	if !ValidName(t.Name) {
		return invalidName(t.Name)
	}
	if strings.TrimSpace(t.Prompt) == "" {
		return errors.New("template has no prompt")
	}
	if t.Agent != "" && !agentname.ValidLaunch(t.Agent) {
		return fmt.Errorf("invalid agent %q", t.Agent)
	}
	if t.Effort != "" && !slices.Contains(launchopts.Efforts, t.Effort) {
		return fmt.Errorf("invalid effort %q (want %s)", t.Effort, strings.Join(launchopts.Efforts, ", "))
	}
	if strings.ContainsAny(t.Description, "\n\r") {
		return errors.New("description must be one line")
	}
	return nil
}

// Render is the file content for t.
func Render(t Template) []byte {
	var b bytes.Buffer
	fm := frontmatter{Description: t.Description, Agent: t.Agent, Model: t.Model, Dir: t.Dir, Effort: t.Effort}
	if fm != (frontmatter{}) {
		out, err := yaml.Marshal(fm)
		if err == nil {
			b.WriteString("---\n")
			b.Write(out)
			b.WriteString("---\n\n")
		}
	}
	b.WriteString(strings.TrimRight(t.Prompt, "\n"))
	b.WriteString("\n")
	return b.Bytes()
}

// readFile loads one template file.
func readFile(name, path string, scope Scope) (Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Template{}, err
	}
	t, err := Parse(name, data)
	if err != nil {
		return Template{}, fmt.Errorf("template %s (%s): %w", name, path, err)
	}
	t.Scope, t.Path = scope, path
	return t, nil
}

// LoadScope reads the template of name stored in scope.
func LoadScope(name string, scope Scope, projectDir string) (Template, error) {
	path, err := Path(name, scope, projectDir)
	if err != nil {
		return Template{}, err
	}
	t, err := readFile(name, path, scope)
	if errors.Is(err, fs.ErrNotExist) {
		return Template{}, fmt.Errorf("%w: no %s template %q", ErrNotFound, scope, name)
	}
	return t, err
}

// Load reads the template of name visible from projectDir: the project's,
// else the global one. A project file that exists but does not parse is an
// error, never a silent fall back to the global template.
func Load(name, projectDir string) (Template, error) {
	if !ValidName(name) {
		return Template{}, invalidName(name)
	}
	if projectDir != "" {
		t, err := LoadScope(name, ScopeProject, projectDir)
		if err == nil {
			if g, gerr := Path(name, ScopeGlobal, ""); gerr == nil {
				if _, serr := os.Stat(g); serr == nil {
					t.Shadows = g
				}
			}
			return t, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Template{}, err
		}
	}
	t, err := LoadScope(name, ScopeGlobal, "")
	if errors.Is(err, ErrNotFound) {
		return Template{}, fmt.Errorf("%w: %q (run agentctl template list)", ErrNotFound, name)
	}
	return t, err
}

// Problem is a template file List could not read.
type Problem struct {
	Path string `json:"path"`
	Err  string `json:"error"`
}

// List returns every template visible from projectDir, project shadowing
// global, sorted by name, plus the files that could not be read.
func List(projectDir string) ([]Template, []Problem, error) {
	gdir, err := GlobalDir()
	if err != nil {
		return nil, nil, err
	}
	global, problems := scan(gdir, ScopeGlobal)
	byName := make(map[string]Template, len(global))
	for _, t := range global {
		byName[t.Name] = t
	}
	if pdir := ProjectDir(projectDir); pdir != "" && filepath.Clean(pdir) != filepath.Clean(gdir) {
		local, lp := scan(pdir, ScopeProject)
		problems = append(problems, lp...)
		for _, t := range local {
			if g, ok := byName[t.Name]; ok {
				t.Shadows = g.Path
			}
			byName[t.Name] = t
		}
		// A broken project file still hides the global template, as Load
		// refuses to fall back to it.
		for _, p := range lp {
			delete(byName, strings.TrimSuffix(filepath.Base(p.Path), ext))
		}
	}
	out := make([]Template, 0, len(byName))
	for _, t := range byName {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })
	return out, problems, nil
}

// scan reads the templates in dir; a missing dir is empty.
func scan(dir string, scope Scope) ([]Template, []Problem) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []Problem{{Path: dir, Err: err.Error()}}
	}
	var out []Template
	var problems []Problem
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ext)
		if !ok || e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if !ValidName(name) {
			problems = append(problems, Problem{Path: path, Err: invalidName(name).Error()})
			continue
		}
		t, err := readFile(name, path, scope)
		if err != nil {
			problems = append(problems, Problem{Path: path, Err: err.Error()})
			continue
		}
		out = append(out, t)
	}
	return out, problems
}

// Save validates t and writes it to scope (atomically, 0600 in a 0700 dir),
// returning the path.
func Save(t Template, scope Scope, projectDir string) (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	path, err := Path(t.Name, scope, projectDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+t.Name+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("writing template: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename
	if _, err := tmp.Write(Render(t)); err != nil {
		tmp.Close()
		return "", fmt.Errorf("writing template: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("writing template: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("writing template: %w", err)
	}
	return path, nil
}

// Delete removes the template of name from scope, returning its path.
func Delete(name string, scope Scope, projectDir string) (string, error) {
	path, err := Path(name, scope, projectDir)
	if err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: no %s template %q", ErrNotFound, scope, name)
		}
		return "", fmt.Errorf("removing template: %w", err)
	}
	return path, nil
}

// Compose is the prompt a dispatch of a template runs: the template body,
// plus extra (dispatch --prompt) under AdditionalInstructionsHeading.
func Compose(body, extra string) string {
	body = strings.TrimRight(body, "\n")
	if extra = strings.TrimSpace(extra); extra == "" {
		return body
	}
	return body + "\n\n" + AdditionalInstructionsHeading + "\n\n" + extra
}
