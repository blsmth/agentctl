package cmd

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/plugin"
)

func TestInstallPluginFromEmbed(t *testing.T) {
	home := t.TempDir()
	res, err := installPlugin(plugin.FS, home, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	for _, rel := range []string{
		".claude/commands/agentctl/restart.md",
		".claude/agents/go-engineer.md",
		".claude/shared/agentctl-signal.md",
		".claude/shared/agentctl-reference.md",
	} {
		fi, err := os.Lstat(filepath.Join(home, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s is a symlink, want a real file", rel)
		}
	}

	for _, rel := range []string{
		".claude/shared/agentctl-dev.md",
		".claude/agents/shared",
	} {
		if _, err := os.Lstat(filepath.Join(home, rel)); err == nil {
			t.Errorf("%s was installed, want it skipped", rel)
		}
	}

	// Second run is a no-op.
	res, err = installPlugin(plugin.FS, home, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Installed)+len(res.Updated)+len(res.Skipped) != 0 || len(res.Unchanged) == 0 {
		t.Errorf("second run not idempotent: %+v", res)
	}
}

func TestInstallPluginExistingFiles(t *testing.T) {
	home := t.TempDir()
	agents := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(agents, "architect.md")
	if err := os.WriteFile(custom, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A legacy symlink to a stale checkout must be replaced by a real file.
	stale := filepath.Join(home, "stale.md")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(agents, "go-engineer.md")
	if err := os.Symlink(stale, link); err != nil {
		t.Fatal(err)
	}

	res, err := installPlugin(plugin.FS, home, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(custom); string(got) != "mine" {
		t.Error("modified file overwritten without --force")
	}
	if !contains(res.Skipped, "agent:architect") {
		t.Errorf("architect not skipped: %+v", res)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink != 0 {
		t.Error("legacy symlink not replaced")
	}
	if got, _ := os.ReadFile(stale); string(got) != "old" {
		t.Error("symlink target was written through")
	}
	if !contains(res.Updated, "agent:go-engineer") {
		t.Errorf("go-engineer not reported updated: %+v", res)
	}

	if _, err := installPlugin(plugin.FS, home, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(custom); string(got) == "mine" {
		t.Error("--force did not overwrite")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestSlashCommandLinesCoversEmbeddedCommands(t *testing.T) {
	lines := strings.Join(slashCommandLines(plugin.FS), "\n")
	entries, err := fs.ReadDir(plugin.FS, "commands")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		if !strings.Contains(lines, "/agentctl:"+name) {
			t.Errorf("slash command list missing /agentctl:%s:\n%s", name, lines)
		}
	}
	if !strings.Contains(lines, "/agentctl:context") {
		t.Error("missing /agentctl:context")
	}
}

func TestFrontmatterDescription(t *testing.T) {
	if got := frontmatterDescription([]byte("---\ndescription: Hi there\n---\nbody")); got != "Hi there" {
		t.Errorf("got %q", got)
	}
	if got := frontmatterDescription([]byte("no frontmatter")); got != "" {
		t.Errorf("got %q", got)
	}
}
