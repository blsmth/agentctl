package cmd

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/plugin"
)

// RunCmdInstallTemplates copies built-in agent templates into ./.claude/agents/.
// Existing files are skipped unless force is true.
// Flag parsing is handled by Kong via InstallTemplatesCmd.
func RunCmdInstallTemplates(force bool) {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: getting working directory: %v\n", err)
		os.Exit(1)
	}
	destDir := filepath.Join(dir, ".claude", "agents")

	installed, skipped, err := agent.InstallTemplates(destDir, force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(installed) == 0 && len(skipped) == 0 {
		fmt.Println("No templates found.")
		return
	}

	for _, name := range installed {
		fmt.Printf("  installed  %s\n", name)
	}
	for _, name := range skipped {
		fmt.Printf("  skipped    %s  (already exists; use --force to overwrite)\n", name)
	}

	if len(installed) > 0 {
		fmt.Printf("\n%d template(s) installed to %s\n", len(installed), destDir)
	}
	if len(skipped) > 0 {
		fmt.Printf("%d template(s) skipped\n", len(skipped))
	}
}

// pluginInstallSpec maps a directory of the embedded plugin tree to its
// destination under ~/.claude/.
type pluginInstallSpec struct {
	srcDir  string // directory within the plugin FS
	destDir string // absolute destination directory
	label   string
	skip    func(name string) bool // optional: name without .md; true = do not install
}

// contributorOnlySkills are embedded skills meant for people hacking on
// agentctl itself. They are not installed for end users.
var contributorOnlySkills = map[string]bool{
	"agentctl-dev": true,
}

// pluginInstallResult lists what installPlugin did, as "<label>:<name>" tags.
type pluginInstallResult struct {
	Installed []string // newly written
	Updated   []string // replaced (legacy symlink, forced, or changed content)
	Unchanged []string // already identical
	Skipped   []string // differing regular file left alone (no --force)
	Errors    []string
}

// installPlugin copies the plugin's commands, agents and skills out of fsys
// (normally the embedded plugin.FS) into home/.claude/. Legacy symlinks from
// older source-checkout installs are replaced with real files. A differing
// regular file is kept unless force is true.
func installPlugin(fsys fs.FS, home string, force bool) (pluginInstallResult, error) {
	specs := []pluginInstallSpec{
		{"commands", filepath.Join(home, ".claude", "commands", "agentctl"), "command", nil},
		{"agents", filepath.Join(home, ".claude", "agents"), "agent", nil},
		// ~/.claude/shared is agent.SharedDir: the directory @shared/<name>.md
		// includes in agent bodies are expanded from.
		{"skills", agent.SharedDirIn(home), "skill", func(n string) bool { return contributorOnlySkills[n] }},
	}

	var res pluginInstallResult
	for _, spec := range specs {
		entries, err := fs.ReadDir(fsys, spec.srcDir)
		if err != nil {
			return res, fmt.Errorf("reading embedded %s: %w", spec.srcDir, err)
		}
		if err := os.MkdirAll(spec.destDir, 0o755); err != nil {
			return res, fmt.Errorf("creating %s: %w", spec.destDir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			base := strings.TrimSuffix(entry.Name(), ".md")
			if spec.skip != nil && spec.skip(base) {
				continue
			}
			tag := spec.label + ":" + base
			data, err := fs.ReadFile(fsys, spec.srcDir+"/"+entry.Name())
			if err != nil {
				return res, fmt.Errorf("reading embedded %s: %w", entry.Name(), err)
			}
			dst := filepath.Join(spec.destDir, entry.Name())

			replacing := false
			if fi, statErr := os.Lstat(dst); statErr == nil {
				if fi.Mode()&os.ModeSymlink != 0 {
					replacing = true
				} else {
					existing, readErr := os.ReadFile(dst)
					if readErr == nil && bytes.Equal(existing, data) {
						res.Unchanged = append(res.Unchanged, tag)
						continue
					}
					if !force {
						res.Skipped = append(res.Skipped, tag)
						continue
					}
					replacing = true
				}
				if err := os.Remove(dst); err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: removing existing file: %v", tag, err))
					continue
				}
			}

			if err := os.WriteFile(dst, data, 0o644); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", tag, err))
				continue
			}
			if replacing {
				res.Updated = append(res.Updated, tag)
			} else {
				res.Installed = append(res.Installed, tag)
			}
		}
	}
	return res, nil
}

// RunCmdInstallPlugin installs the agentctl Claude Code plugin by copying the
// plugin tree embedded in the binary into ~/.claude/:
//   - commands/*.md → ~/.claude/commands/agentctl/<name>.md  (slash commands)
//   - agents/*.md   → ~/.claude/agents/<name>.md              (agent definitions)
//   - skills/*.md   → ~/.claude/shared/<name>.md              (shared skills; contributor-only ones are skipped)
//
// It needs no source checkout. Flag parsing is handled by Kong via
// InstallPluginCmd.
func RunCmdInstallPlugin(force bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine home directory: %v\n", err)
		os.Exit(1)
	}

	res, err := installPlugin(plugin.FS, home, force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	for _, name := range res.Installed {
		fmt.Printf("  installed  %s\n", name)
	}
	for _, name := range res.Updated {
		fmt.Printf("  updated    %s\n", name)
	}
	for _, name := range res.Skipped {
		fmt.Printf("  skipped    %s  (modified file exists; use --force to overwrite)\n", name)
	}
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "  error      %s\n", e)
	}

	written := len(res.Installed) + len(res.Updated)
	if written+len(res.Unchanged)+len(res.Skipped)+len(res.Errors) == 0 {
		fmt.Println("Nothing to install.")
		return
	}
	fmt.Println("\nagentctl plugin installed")
	fmt.Printf("  %d file(s) written, %d up to date, %d skipped\n", written, len(res.Unchanged), len(res.Skipped))
	fmt.Println("\nSlash commands available in Claude Code:")
	for _, line := range slashCommandLines(plugin.FS) {
		fmt.Println(line)
	}
	if len(res.Errors) > 0 {
		os.Exit(1)
	}
}

// slashCommandLines renders one "  /agentctl:<name> — <description>" line per
// command in the embedded commands dir, so the list can never drift from what
// is actually installed.
func slashCommandLines(fsys fs.FS) []string {
	entries, err := fs.ReadDir(fsys, "commands")
	if err != nil {
		return nil
	}
	var lines []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := fs.ReadFile(fsys, "commands/"+e.Name())
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		line := "  /agentctl:" + name
		if d := frontmatterDescription(data); d != "" {
			line += " — " + d
		}
		lines = append(lines, line)
	}
	return lines
}

// frontmatterDescription returns the "description:" value from a markdown
// file's leading YAML frontmatter, or "" when absent.
func frontmatterDescription(data []byte) string {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return ""
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	for _, l := range strings.Split(rest[:end], "\n") {
		if v, ok := strings.CutPrefix(l, "description:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}
