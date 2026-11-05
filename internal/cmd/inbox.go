package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/signal"
	"github.com/blsmth/agentctl/internal/tmux"
)

// inboxJSONRow is the flattened structure emitted for `inbox --json`. It
// mirrors mailbox.Message but adds a Read flag and formats Created as
// RFC3339 for stable machine consumption.
type inboxJSONRow struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Kind    string `json:"kind"`
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body"`
	Created string `json:"created"`
	Read    bool   `json:"read"`
}

func toInboxJSONRow(m mailbox.Message, read bool) inboxJSONRow {
	return inboxJSONRow{
		ID:      m.ID,
		From:    m.From,
		Kind:    m.Kind,
		Subject: m.Subject,
		Body:    m.Body,
		Created: m.Created.Format(time.RFC3339),
		Read:    read,
	}
}

// RunInbox implements `agentctl inbox [--all] [--json]`. It always resolves
// to the CURRENT window's mailbox.
//
// --json MUST emit "[]" for an empty mailbox, never null and never an error
// -- a past bug shipped `sessions --json` erroring on a fresh install, and
// this must not repeat that.
func RunInbox(all, jsonOut bool) error {
	windowID := signal.ResolveWindowID()
	if windowID == "" {
		if jsonOut {
			fmt.Println("[]")
			return nil
		}
		return fmt.Errorf("cannot determine current window ID")
	}

	pending, err := mailbox.Pending(windowID)
	if err != nil {
		return fmt.Errorf("loading pending messages: %w", err)
	}

	var read []mailbox.Message
	if all {
		read, err = mailbox.Read(windowID)
		if err != nil {
			return fmt.Errorf("loading read messages: %w", err)
		}
	}

	if jsonOut {
		rows := make([]inboxJSONRow, 0, len(pending)+len(read))
		for _, m := range pending {
			rows = append(rows, toInboxJSONRow(m, false))
		}
		for _, m := range read {
			rows = append(rows, toInboxJSONRow(m, true))
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	printInboxTable(pending, read)
	return nil
}

// RunInboxWait implements `agentctl inbox --wait [--from <addr>] [--timeout
// <dur>]`. It blocks until at least one matching pending message exists for
// THIS window, then drains and prints it in the normal inbox output format,
// and returns nil (exit 0) -- including on timeout, since a timeout is a
// normal outcome of waiting for mail that never arrives, not an error a
// calling agent should have to treat as tool failure.
func RunInboxWait(from string, timeout time.Duration) error {
	windowID := signal.ResolveWindowID()
	if windowID == "" {
		return fmt.Errorf("cannot determine current window ID")
	}
	if timeout <= 0 {
		timeout = mailbox.DefaultWaitTimeout
	}

	resolvedFrom := resolveFromFilter(from)

	matched, timedOut, err := mailbox.WaitAndClaim(windowID, resolvedFrom, timeout)
	if err != nil {
		return fmt.Errorf("waiting for mail: %w", err)
	}
	if timedOut {
		fmt.Printf("(timed out after %s, no messages)\n", timeout)
		return nil
	}

	printInboxTable(matched, nil)
	return nil
}

// resolveFromFilter resolves an --from <addr> value the same way send/wake
// resolve their <addr> argument. It is a thin wrapper around
// resolveFromFilterWithAgents that supplies the real tmux.RunningAgents(),
// kept separate so the resolution logic itself stays testable without a
// live tmux session (mirrors resolveSendAddress's own params-not-a-direct-
// call pattern in send.go).
func resolveFromFilter(from string) string {
	return resolveFromFilterWithAgents(from, tmux.RunningAgents())
}

// resolveFromFilterWithAgents is resolveFromFilter's pure core: a bare "@N"
// window ID is used as-is, an agent name is resolved against runningAgents.
// Unlike resolveSendAddress, an agent name that matches no running window is
// NOT an error here -- it falls back to the raw value so a sender whose
// window has already closed can still be matched by whatever address it
// used to enqueue with (Message.From is always a window ID, recorded at
// send time).
func resolveFromFilterWithAgents(from string, runningAgents map[string]string) string {
	if from == "" || strings.HasPrefix(from, "@") {
		return from
	}
	if wid, err := resolveSendAddress(from, runningAgents); err == nil {
		return wid
	}
	return from
}

// printInboxTable renders pending (and, if non-empty, read) messages as an
// ANSI-aware table via printANSITable (see models.go) rather than
// text/tabwriter, which miscounts column width when cells contain ANSI
// escape sequences.
func printInboxTable(pending, read []mailbox.Message) {
	if len(pending) == 0 && len(read) == 0 {
		fmt.Println(cliMuted().Render("(no messages)"))
		return
	}

	hdr := cliHeaderStyle()
	muted := cliMuted()
	accent := cliAccent()

	header := []string{
		hdr.Render("STATUS"),
		hdr.Render("KIND"),
		hdr.Render("FROM"),
		hdr.Render("SUBJECT"),
		hdr.Render("AGE"),
	}

	rows := make([][]string, 0, len(pending)+len(read))
	appendRows := func(msgs []mailbox.Message, statusLabel string, statusStyle func(...string) string) {
		for _, m := range msgs {
			subject := m.Subject
			if subject == "" {
				subject = truncateBody(m.Body, 60)
			}
			from := m.From
			if from == "" {
				from = "-"
			}
			rows = append(rows, []string{
				statusStyle(statusLabel),
				accent.Render(m.Kind),
				muted.Render(from),
				subject,
				muted.Render(fmtAge(time.Since(m.Created))),
			})
		}
	}

	appendRows(pending, "pending", cliAccent().Render)
	appendRows(read, "read", cliMuted().Render)

	printANSITable(os.Stdout, header, rows)
}

// truncateBody shortens s to at most n runes, appending an ellipsis when
// truncated, for use as a subject fallback when a message has none.
func truncateBody(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
