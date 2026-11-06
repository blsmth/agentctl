package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/agent"
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

// RunCmdInstallPlugin is a stub kept for muscle memory: the agentctl slash
// commands now install through the Claude Code plugin marketplace, so it
// prints the two /plugin commands instead of copying files.
func RunCmdInstallPlugin() {
	fmt.Print(installPluginMessage)
}

const installPluginMessage = `agentctl install-plugin no longer copies files. Install the agentctl
slash commands through the Claude Code plugin marketplace instead, from inside Claude Code:

  /plugin marketplace add blsmth/agentctl
  /plugin install agentctl@agentctl
`
