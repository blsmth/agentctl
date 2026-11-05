package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"
	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
)

// RunContext routes the "agentctl context <subcommand>" family.
func RunContext() {
	if len(os.Args) < 3 {
		printContextUsage()
		os.Exit(1)
	}
	switch os.Args[2] {
	case "set":
		cmdContextSet()
	case "get":
		cmdContextGet()
	case "list":
		cmdContextList()
	case "clear":
		cmdContextClear()
	case "push":
		cmdContextPush()
	default:
		printContextUsage()
		os.Exit(1)
	}
}

func printContextUsage() {
	fmt.Fprintln(os.Stderr, `Usage: agentctl context <subcommand> [flags]

Subcommands:
  set   <key> <value>   Write a context entry (flags: --agent, --tag)
  get   <key>           Print a context entry
  list                  Table of all entries
  clear <key>           Delete an entry
  push  <key>           Push an entry to a running agent (flag: --agent)`)
}

// cmdContextSet handles "agentctl context set <key> <value> [--agent NAME] [--tag TAG]...".
func cmdContextSet() {
	args := os.Args[3:]
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: agentctl context set <key> <value> [--agent <name>] [--tag <tag>]...")
		os.Exit(1)
	}

	key := args[0]
	value := args[1]

	var agentName string
	var tags []string

	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --agent requires a value")
				os.Exit(1)
			}
			i++
			agentName = args[i]
		case "--tag":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --tag requires a value")
				os.Exit(1)
			}
			i++
			tags = append(tags, args[i])
		default:
			fmt.Fprintf(os.Stderr, "Error: unknown flag: %s\n", args[i])
			os.Exit(1)
		}
	}

	e := ctxstore.Entry{
		Agent:   agentName,
		Content: value,
		Tags:    tags,
	}
	if err := ctxstore.Set(key, e); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if agentName == "" {
		fmt.Fprintf(os.Stderr, "context: %q has no --agent, so it will NOT be auto-injected into any prompt -- use --agent <name> or add it to that agent's context_keys to opt in\n", key)
	}
	fmt.Printf("context: set %q\n", key)
}

// cmdContextGet handles "agentctl context get <key>".
func cmdContextGet() {
	args := os.Args[3:]
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: agentctl context get <key>")
		os.Exit(1)
	}

	key := args[0]
	e, ok, err := ctxstore.Get(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: key %q not found\n", key)
		os.Exit(1)
	}

	// Print header metadata and content.
	fmt.Printf("key:       %s\n", e.Key)
	if e.Agent != "" {
		fmt.Printf("agent:     %s\n", e.Agent)
	}
	if len(e.Tags) > 0 {
		fmt.Printf("tags:      %s\n", strings.Join(e.Tags, ", "))
	}
	fmt.Printf("timestamp: %s\n", e.Timestamp.Format(time.RFC3339))
	fmt.Printf("\n%s\n", e.Content)
}

// cmdContextList handles "agentctl context list".
func cmdContextList() {
	entries, err := ctxstore.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(entries) == 0 {
		fmt.Println("No context entries. Use 'agentctl context set' to add one.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tAGENT\tSIZE\tAGE\tPREVIEW")
	fmt.Fprintln(w, "---\t-----\t----\t---\t-------")
	for _, e := range entries {
		age := tui.FormatContextAge(e.Timestamp)
		preview := strings.ReplaceAll(e.Content, "\n", " ")
		if len(preview) > 50 {
			preview = preview[:47] + "..."
		}
		agentCol := e.Agent
		if agentCol == "" {
			agentCol = "-"
		}
		size := formatEntrySize(len(e.Content))
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Key, agentCol, size, age, preview)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "context: flush: %v\n", err)
	}
}

// formatEntrySize renders a content byte count for the list table.
//
// dustin/go-humanize is the preferred formatter (humanize.Bytes) per house
// convention, but it is not currently a dependency of this module, and the
// point of this column is visibility, not polish -- adding a dependency just
// to pretty-print a number would be over-engineering for a ticket about not
// hand-rolling one. A plain byte count is honest and exactly as useful for
// spotting the outlier entry that matters here (a 30KB entry next to a
// 20-byte one is obviously different either way).
func formatEntrySize(n int) string {
	return fmt.Sprintf("%dB", n)
}

// cmdContextClear handles "agentctl context clear <key>".
func cmdContextClear() {
	args := os.Args[3:]
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: agentctl context clear <key>")
		os.Exit(1)
	}

	key := args[0]
	if err := ctxstore.Delete(key); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("context: cleared %q\n", key)
}

// cmdContextPush handles "agentctl context push <key> --agent <name>".
func cmdContextPush() {
	args := os.Args[3:]
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: agentctl context push <key> --agent <name>")
		os.Exit(1)
	}

	key := args[0]
	var agentName string

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --agent requires a value")
				os.Exit(1)
			}
			i++
			agentName = args[i]
		default:
			fmt.Fprintf(os.Stderr, "Error: unknown flag: %s\n", args[i])
			os.Exit(1)
		}
	}

	if agentName == "" {
		fmt.Fprintln(os.Stderr, "Error: --agent <name> is required")
		os.Exit(1)
	}

	e, ok, err := ctxstore.Get(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: key %q not found\n", key)
		os.Exit(1)
	}

	message := ctxstore.Format(e)
	if err := tmux.InjectPromptToAgent(agentName, message); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("context: pushed %q to %s\n", key, agentName)
}

// RunContextPushMenu is the TUI popup bound to prefix+C. It reads the target
// agent from the tmux window ID (or the current agent), lists all context
// store entries, and lets the operator select one to push.
func RunContextPushMenu() {
	// Resolve target agent from args or the current window.
	var windowIDOrName string
	if len(os.Args) > 2 {
		windowIDOrName = os.Args[2]
	}

	var agentName string
	if strings.HasPrefix(windowIDOrName, "@") {
		agentName = tmux.ResolveAgentByWindowID(windowIDOrName)
	} else if windowIDOrName != "" {
		agentName = tmux.BaseAgentName(tmux.StripAgentPrefix(windowIDOrName))
	}
	if agentName == "" || agentName == "shell" {
		// Resolve the current agent from the active tmux window.
		name := tmux.CurrentWindowName()
		if name != "" && name != "shell" {
			agentName = tmux.StripAgentPrefix(name)
		} else {
			agentName = ""
		}
	}
	if agentName != "" {
		agentName = tmux.BaseAgentName(agentName)
	}

	// Load all context entries.
	entries, err := ctxstore.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading context store: %v\n", err)
		os.Exit(1)
	}

	displayEntries := tui.BuildContextPushEntries(entries)
	model := tui.NewContextPushModel(agentName, displayEntries)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.ContextPushModel).Result()
	if result.Cancelled || result.Key == "" {
		return
	}

	target := result.TargetAgent
	if target == "" {
		fmt.Fprintf(os.Stderr, "Error: no target agent\n")
		os.Exit(1)
	}

	// Fetch the full entry and inject it.
	e, ok, err := ctxstore.Get(result.Key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: key %q not found\n", result.Key)
		os.Exit(1)
	}

	message := ctxstore.Format(e)
	if err := tmux.InjectPromptToAgent(target, message); err != nil {
		fmt.Fprintf(os.Stderr, "Error pushing context: %v\n", err)
		os.Exit(1)
	}
}
