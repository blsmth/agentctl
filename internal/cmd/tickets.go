package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/agentlabel"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
)

// DefaultDispatchAgent is the fallback agent used when no label maps to an agent.
const DefaultDispatchAgent = agentlabel.Default

// fetchRepoLabels fetches the label names for the repo identified by slug
// using the gh CLI. Returns an empty slice on any error so the label picker is
// gracefully hidden.
func fetchRepoLabels(slug string) []string {
	out, err := exec.Command("gh", "label", "list",
		"--repo", slug,
		"--json", "name",
		"--jq", ".[].name",
	).Output()
	if err != nil {
		return nil
	}
	var labels []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			labels = append(labels, line)
		}
	}
	return labels
}

// RunTicketMenu opens the create-ticket dialog (ctrl+b Q).
// It pre-fetches repo labels for the label picker, then runs the
// StandaloneCreateTicket model as a self-contained tmux popup.
func RunTicketMenu() {
	dir, _ := os.Getwd()
	slug, err := dispatch.RepoSlug(dir)
	if err != nil {
		logging.Error("ticket menu: %v", err)
		fmt.Fprintf(os.Stderr, "Error: cannot determine GitHub repo: %v\nPress Enter to close.", err)
		_, _ = fmt.Scanln()
		os.Exit(1)
	}
	labels := fetchRepoLabels(slug)
	model := tui.NewStandaloneCreateTicket(slug, labels)
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// RunTickets opens the full-screen tickets browser TUI. When the user selects
// a ticket and chooses to launch an agent, it fetches the issue and delegates
// to launchTicketMode.
func RunTickets() {
	dir, _ := os.Getwd()
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = home
	}

	agents, _ := agent.DiscoverFromCwd(config.Load())

	model := tui.NewTicketsModel(dir, agents)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.TicketsModel).Result()
	if result.Cancelled {
		// Return focus to the window that was active before the tickets window was
		// opened, then remove the now-empty tickets window from the tab bar.
		originWindow := os.Getenv("AGENTCTL_ORIGIN_WINDOW")
		tmux.ReturnFocusAndKillWindow(originWindow, "tickets")
		return
	}

	if result.LaunchAgent {
		// If an agent was explicitly selected in the UI, use it directly.
		// Otherwise fall back to auto-detection from issue labels.
		agentName := result.AgentName
		ticketBody := result.TicketBody
		if agentName == "" {
			var fetchErr error
			agentName, ticketBody, fetchErr = fetchIssueForLaunch(dir, result.TicketNumber, result.TicketBody)
			if fetchErr != nil {
				fmt.Fprintf(os.Stderr, "Error fetching issue: %v\n", fetchErr)
				os.Exit(1)
			}
		} else if ticketBody == "" {
			// Agent selected but body not yet fetched (list view path) — fetch body now.
			var fetchErr error
			_, ticketBody, fetchErr = fetchIssueForLaunch(dir, result.TicketNumber, "")
			if fetchErr != nil {
				fmt.Fprintf(os.Stderr, "Error fetching issue: %v\n", fetchErr)
				os.Exit(1)
			}
		}
		mode := PromptModeToDispatchMode(result.PromptMode)
		if err := launchTicketMode(agentName, dir, result.TicketNumber, result.TicketTitle, ticketBody, result.CustomPrompt, "", mode); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	// Explicit kill ensures the tickets window is gone from the tab bar even when
	// tmux has remain-on-exit enabled. Focus has already moved to the new agent
	// window via SwitchToWindow inside the launch helpers above.
	tmux.ReturnFocusAndKillWindow("", "tickets")
}

// RunTicketCreateBody opens the full-screen tickets TUI in create mode.
// The title, bodyContent, and labels args are already resolved by the caller.
func RunTicketCreateBody(title, bodyContent string, labels []string) {
	opts := tui.CreateOpts{
		Title:  title,
		Body:   bodyContent,
		Labels: labels,
	}

	dir, _ := os.Getwd()
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = home
	}

	agents, _ := agent.DiscoverFromCwd(config.Load())

	model := tui.NewTicketsModelWithOpts(dir, agents, opts)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.TicketsModel).Result()
	if result.Cancelled {
		originWindow := os.Getenv("AGENTCTL_ORIGIN_WINDOW")
		tmux.ReturnFocusAndKillWindow(originWindow, "tickets")
		return
	}

	if result.LaunchAgent {
		agentName := result.AgentName
		ticketBody := result.TicketBody
		if agentName == "" {
			var fetchErr error
			agentName, ticketBody, fetchErr = fetchIssueForLaunch(dir, result.TicketNumber, result.TicketBody)
			if fetchErr != nil {
				fmt.Fprintf(os.Stderr, "Error fetching issue: %v\n", fetchErr)
				os.Exit(1)
			}
		} else if ticketBody == "" {
			var fetchErr error
			_, ticketBody, fetchErr = fetchIssueForLaunch(dir, result.TicketNumber, "")
			if fetchErr != nil {
				fmt.Fprintf(os.Stderr, "Error fetching issue: %v\n", fetchErr)
				os.Exit(1)
			}
		}
		mode := PromptModeToDispatchMode(result.PromptMode)
		if err := launchTicketMode(agentName, dir, result.TicketNumber, result.TicketTitle, ticketBody, result.CustomPrompt, "", mode); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	tmux.ReturnFocusAndKillWindow("", "tickets")
}

// RunTicketLaunch opens the ticket-launch picker TUI that lets the user choose
// an agent, ticket number, and launch mode interactively.
func RunTicketLaunch() {
	agents, err := agent.DiscoverFromCwd(config.Load())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	model := tui.NewTicketLaunchModel(agents)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.TicketLaunchModel).Result()
	if result.Cancelled {
		return
	}

	if result.Ticket != 0 {
		mode := PromptModeToDispatchMode(result.PromptMode)
		if err := launchTicketMode(result.Agent, result.Dir, result.Ticket, result.TicketTitle, result.TicketBody, result.CustomPrompt, "", mode); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// No ticket — launch a bare interactive agent session.
	if _, err := dispatch.Launch(dispatch.Request{
		AgentName: result.Agent,
		Mode:      dispatch.ModeBare,
		WorkDir:   result.Dir,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// fetchIssueForLaunch fetches the full issue (title, body, labels) for a
// ticket-launch action. It always calls gh to get labels for agent
// auto-detection. If bodyOverride is non-empty it uses that for the body
// (avoids a redundant fetch when coming from the detail view) but still fetches
// labels. Returns the resolved agent name and the issue body.
func fetchIssueForLaunch(dir string, number int, bodyOverride string) (agentName, body string, err error) {
	slug := tui.TLRepoSlug(dir)
	if slug == "" {
		return "", "", fmt.Errorf("could not determine GitHub repo for %s; run inside a GitHub checkout", dir)
	}

	out, err := exec.Command("gh", "issue", "view", fmt.Sprintf("%d", number),
		"--repo", slug, "--json", "title,body,labels").Output()
	if err != nil {
		return "", "", fmt.Errorf("gh issue view: %w", err)
	}

	var issue struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &issue); err != nil {
		return "", "", fmt.Errorf("parse issue: %w", err)
	}

	// Infer agent from labels.
	names := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		names = append(names, l.Name)
	}
	agentName = agentlabel.InferOrDefault(names)

	// Use the caller-supplied body if it's already populated (detail view),
	// otherwise use the fetched body.
	if bodyOverride != "" {
		body = bodyOverride
	} else {
		body = issue.Body
	}
	return agentName, body, nil
}

// launchTicketMode launches an agent via dispatch.Launch() for any ticket-based
// mode. ModeDispatch uses a worktree for code isolation; conversational modes do
// not. CustomPrompt is only used by ModeCustom; pass an empty string otherwise.
func launchTicketMode(agentName, dir string, ticketNumber int, ticketTitle, ticketBody, customPrompt, model string, mode dispatch.Mode) error {
	logging.Log("launchTicketMode: #%d agent=%s mode=%s dir=%s", ticketNumber, agentName, mode, dir)
	useWorktree := mode == dispatch.ModeDispatch
	repoSlug, err := dispatch.RepoSlug(dir)
	if err != nil {
		return fmt.Errorf("determining repo for ticket #%d: %w", ticketNumber, err)
	}
	_, err = dispatch.Launch(dispatch.Request{
		AgentName:    agentName,
		Mode:         mode,
		Ticket:       ticketNumber,
		TicketTitle:  ticketTitle,
		TicketBody:   ticketBody,
		CustomPrompt: customPrompt,
		WorkDir:      dir,
		UseWorktree:  &useWorktree,
		RepoSlug:     repoSlug,
		Model:        model,
	})
	return err
}

// PromptModeToDispatchMode maps the string prompt-mode values used by the TUI
// to the corresponding dispatch.Mode constants.
// Unknown or empty values fall back to ModeDiscuss.
func PromptModeToDispatchMode(pm string) dispatch.Mode {
	switch pm {
	case "work":
		return dispatch.ModeDispatch
	case "review":
		return dispatch.ModeReview
	case "triage":
		return dispatch.ModeTriage
	case "none":
		return dispatch.ModeDiscuss
	case "custom":
		return dispatch.ModeCustom
	default:
		return dispatch.ModeDiscuss
	}
}
