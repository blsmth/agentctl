package cmd

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
)

// CmdSkillsManager runs the full-screen skills browser TUI.
func CmdSkillsManager() {
	cwd, _ := os.Getwd()
	model := tui.NewSkillsModel(cwd)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.SkillsModel)

	// Return focus to origin window (if launched from inside tmux).
	originWindow := os.Getenv("AGENTCTL_ORIGIN_WINDOW")
	tmux.ReturnFocusAndKillWindow(originWindow, "skills")

	// If the user pressed 'e' or 'n', open the file in $EDITOR.
	if target := result.EditTarget(); target != "" {
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		tmux.NewTempWindow("edit-skill", fmt.Sprintf("%s '%s'", editor, target))
	}
}

// CmdMemory runs the full-screen memory file browser TUI.
func CmdMemory() {
	model := tui.NewMemoryModel()
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// CmdAgentManager runs the full-screen agent manager TUI (AgentsModel).
// It shows a filterable agent list with an inline PromptPreview pane.
// The $EDITOR integration is handled inside the TUI via tea.ExecProcess, so
// no post-quit action dispatch is needed here.
func CmdAgentManager() {
	cfg := config.Load()
	agents, err := agent.DiscoverFromCwd(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	model := tui.NewAgentsModel(agents, cfg)
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Return focus to the origin window (if launched from a tmux popup).
	originWindow := os.Getenv("AGENTCTL_ORIGIN_WINDOW")
	tmux.ReturnFocusAndKillWindow(originWindow, "agents")
}

// CmdCreateMenu shows the Bubble Tea agent creation wizard.
func CmdCreateMenu(name string) {
	if name != "" && agent.Exists(name) {
		fmt.Fprintf(os.Stderr, "Agent %q already exists\n", name)
		os.Exit(1)
	}

	model := tui.NewCreateModel(name)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.CreateModel).Result()
	if result.Cancelled {
		return
	}

	opts := agent.CreateOpts{
		Name:        result.Name,
		Description: result.Description,
		Model:       result.Model,
		Tools:       result.Tools,
		Dir:         result.Dir,
		Extends:     result.Template,
	}

	if err := agent.Create(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created agent: %s\n", result.Name)
	fmt.Printf("  File: ~/.claude/agents/%s.md\n", result.Name)
	fmt.Printf("  Launch: agentctl launch %s\n", result.Name)
}
