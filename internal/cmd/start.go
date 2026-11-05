package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/selfpath"
)

// CmdStart prints a branded startup banner for the current agent session.
// bannerArt is the ASCII art logo embedded by the calling binary (banner.txt).
// It reads identity metadata from environment variables injected by LaunchAgent
// and the ticket launch functions.
func CmdStart(bannerArt string) {
	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	ticket := os.Getenv("AGENTCTL_TICKET")
	ticketTitle := os.Getenv("AGENTCTL_TICKET_TITLE")
	mode := os.Getenv("AGENTCTL_MODE")
	instanceName := os.Getenv("AGENTCTL_INSTANCE_NAME")

	// Resolve display name: prefer instance name, fall back to agent name.
	displayName := instanceName
	if displayName == "" {
		displayName = agentName
	}
	if displayName == "" {
		displayName = "(unknown)"
	}

	// Working directory, shortened with ~ for home; truncated if too long.
	cwd, _ := os.Getwd()
	if home, err := os.UserHomeDir(); err == nil {
		cwd = strings.ReplaceAll(cwd, home, "~")
	}
	const maxCwdLen = 48
	if len(cwd) > maxCwdLen {
		cwd = "…" + cwd[len(cwd)-maxCwdLen+1:]
	}

	started := time.Now().Format("2006-01-02 15:04:05")

	// Ticket display: "#N — title" or "(none)".
	ticketDisplay := "(none)"
	if ticket != "" {
		ticketDisplay = "#" + ticket
		if ticketTitle != "" {
			ticketDisplay += " — " + ticketTitle
		}
	}
	if mode == "" {
		mode = "(none)"
	}

	// ASCII art logo loaded from the embedded banner.txt file.
	logo := strings.Split(strings.TrimRight(bannerArt, "\n"), "\n")

	// Width: determine box width from the widest content line.
	metaLines := []string{
		"  🤖  Agent:   " + displayName,
		"  🎫  Ticket:  " + ticketDisplay,
		"  🔧  Mode:    " + mode,
		"  📁  Dir:     " + cwd,
		"  ⏰  Started: " + started,
	}

	// Measure widths (approximate; emojis count as 2 cols in terminals).
	// We'll use a fixed box width of 62 to handle most cases.
	const boxInner = 62

	pad := func(s string, width int) string {
		// Rough padding — emojis are 2 cols wide, so subtract extra 1 per emoji.
		emojiCount := strings.Count(s, "🤖") + strings.Count(s, "🎫") +
			strings.Count(s, "🔧") + strings.Count(s, "📁") + strings.Count(s, "⏰")
		visLen := len([]rune(s)) + emojiCount // approximate visual width
		if visLen >= width {
			return s
		}
		return s + strings.Repeat(" ", width-visLen)
	}

	top := "╔" + strings.Repeat("═", boxInner+2) + "╗"
	div := "╠" + strings.Repeat("═", boxInner+2) + "╣"
	bot := "╚" + strings.Repeat("═", boxInner+2) + "╝"
	side := func(content string) string {
		return "║ " + pad(content, boxInner) + " ║"
	}

	fmt.Println(top)
	for _, line := range logo {
		// Truncate logo lines that exceed box width.
		r := []rune(line)
		if len(r) > boxInner {
			r = r[:boxInner]
		}
		padded := string(r) + strings.Repeat(" ", boxInner-len(r))
		fmt.Println("║ " + padded + " ║")
	}
	fmt.Println(div)
	for _, ml := range metaLines {
		fmt.Println(side(ml))
	}
	fmt.Println(bot)

	// Signal the monitor that this agent is now working.
	// Fire-and-forget — don't block startup on signal failure.
	go exec.Command(selfpath.Path(), "signal", "start").Run() //nolint:errcheck
}
