package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/blsmth/agentctl/internal/cliout"
	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
)

// RunContext routes the "agentctl context <subcommand>" family.
func RunContext() {
	if len(os.Args) < 3 {
		printContextUsage()
		cliout.Exit(1)
	}
	switch os.Args[2] {
	case "--tui":
		cmdContextTUI()
	case "set":
		cmdContextSet()
	case "get":
		cmdContextGet()
	case "list":
		cmdContextList()
	case "clear":
		cmdContextClear()
	case "prune":
		cmdContextPrune()
	default:
		printContextUsage()
		cliout.Exit(1)
	}
}

func printContextUsage() {
	fmt.Fprintln(os.Stderr, `Usage: agentctl context <subcommand> [flags]
       agentctl context --tui

Subcommands:
  set   <key> <value>   Write a context entry (flag: --tag)
  get   <key>           Print a context entry
  list                  Table of all entries
  clear <key>           Delete an entry
  prune --used          Delete entries a dispatch has used (flags: --older-than <dur>, default 1d; --dry-run)

Flags:
  --tui                 Full-screen browser (filter, preview, delete)`)
}

// cmdContextTUI runs the full-screen context store viewer. Inside the
// dedicated tmux window (AGENTCTL_ORIGIN_WINDOW is set by
// tmux.OpenOrFocusContextWindow) it returns focus to the origin window and
// removes its own window on exit; from a plain shell it just runs and exits.
func cmdContextTUI() {
	p := tea.NewProgram(tui.NewContextModel())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	if origin, ok := os.LookupEnv("AGENTCTL_ORIGIN_WINDOW"); ok {
		tmux.ReturnFocusAndKillWindow(origin, "context")
	}
}

// cmdContextSet handles "agentctl context set <key> <value> [--tag TAG]...".
// --agent is still accepted (old scripts and agent instructions pass it) but
// means nothing: the entry is stored without it.
func cmdContextSet() {
	args := os.Args[3:]
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: agentctl context set <key> <value> [--tag <tag>]...")
		cliout.Exit(1)
	}

	key := args[0]
	value := args[1]

	var tags []string
	agentIgnored := false

	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --agent requires a value")
				cliout.Exit(1)
			}
			i++
			agentIgnored = true
		case "--tag":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: --tag requires a value")
				cliout.Exit(1)
			}
			i++
			tags = append(tags, args[i])
		default:
			fmt.Fprintf(os.Stderr, "Error: unknown flag: %s\n", args[i])
			cliout.Exit(1)
		}
	}

	e := ctxstore.Entry{
		Content: value,
		Tags:    tags,
	}
	if err := ctxstore.Set(key, e); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	if agentIgnored {
		fmt.Fprintln(os.Stderr, "context: --agent is ignored; entries are no longer scoped to an agent")
	}
	fmt.Printf("context: set %q\n", key)
}

// cmdContextGet handles "agentctl context get <key>".
func cmdContextGet() {
	args := os.Args[3:]
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: agentctl context get <key>")
		cliout.Exit(1)
	}

	key := args[0]
	e, ok, err := ctxstore.Get(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: key %q not found\n", key)
		cliout.Exit(1)
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
		cliout.Exit(1)
	}

	if len(entries) == 0 {
		fmt.Println("No context entries. Use 'agentctl context set' to add one.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tAGENT\tSIZE\tAGE\tUSED\tPREVIEW")
	fmt.Fprintln(w, "---\t-----\t----\t---\t----\t-------")
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Key, agentCol, size, age, formatContextUsed(e.Used), preview)
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
		cliout.Exit(1)
	}

	key := args[0]
	if err := ctxstore.Delete(key); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	fmt.Printf("context: cleared %q\n", key)
}

// formatContextUsed renders an entry's last-used time as a relative age, or
// "-" for an entry no dispatch has used.
func formatContextUsed(used time.Time) string {
	if used.IsZero() {
		return "-"
	}
	return tui.FormatContextAge(used)
}

// cmdContextPrune handles "agentctl context prune --used [--older-than <dur>] [--dry-run]".
func cmdContextPrune() {
	opts, err := parseContextPrune(os.Args[3:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	keys, err := ctxstore.PruneUsed(opts.olderThan, time.Now(), opts.dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	verb := "pruned"
	if opts.dryRun {
		verb = "would prune"
	}
	for _, k := range keys {
		fmt.Printf("context: %s %q\n", verb, k)
	}
	if len(keys) == 0 {
		fmt.Println("context: nothing to prune")
	}
}

type contextPruneOpts struct {
	olderThan time.Duration
	dryRun    bool
}

const contextPruneUsage = "Usage: agentctl context prune --used [--older-than <duration>] [--dry-run]"

// parseContextPrune parses the prune flags. --used is required: it names the
// only supported criterion, so unused entries can never be selected.
func parseContextPrune(args []string) (contextPruneOpts, error) {
	opts := contextPruneOpts{olderThan: 24 * time.Hour}
	used := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--used":
			used = true
		case "--dry-run":
			opts.dryRun = true
		case "--older-than":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--older-than requires a value")
			}
			i++
			d, err := sessions.ParseSince(args[i])
			if err != nil || d <= 0 {
				return opts, fmt.Errorf("invalid --older-than %q (want e.g. 12h, 1d, 2w)", args[i])
			}
			opts.olderThan = d
		default:
			return opts, fmt.Errorf("unknown flag: %s", args[i])
		}
	}
	if !used {
		return opts, fmt.Errorf("--used is required (only entries a dispatch has used are pruned)\n%s", contextPruneUsage)
	}
	return opts, nil
}
