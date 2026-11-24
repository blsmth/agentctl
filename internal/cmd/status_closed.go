package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/fleet"
)

// ClosedOptions configures `agentctl status list --closed`.
type ClosedOptions struct {
	Since   string // "7d", "36h", "2w"; default 7d
	JSON    bool
	Session string // same matching as the live listing's --session
}

// Seams for tests.
var (
	closedLoad = closedlog.Load
	closedNow  = time.Now
)

// RunStatusClosed lists the windows closed within opts.Since, newest first,
// from the closed ledger. The table is plain text (no styling), so it reads
// the same piped as on a terminal.
func RunStatusClosed(out io.Writer, opts ClosedOptions) error {
	d, err := parseSince(opts.Since)
	if err != nil {
		return err
	}
	// Handles are unique machine-wide, so they come from the whole ledger and
	// the live windows, not from the listed slice.
	ix := fleet.NewIndex(liveGroups(), allClosed())
	now := closedNow()
	recs, err := closedLoad(now.Add(-d))
	if err != nil {
		return fmt.Errorf("reading closed windows: %w", err)
	}
	recs = filterClosed(recs, opts.Session)
	if opts.JSON {
		list := make([]closedJSON, 0, len(recs))
		for _, r := range recs {
			list = append(list, closedJSON{Record: r, Handle: ix.HandleFor(r.SessionID), Label: fleet.Label(fleet.RecordRow(r), fleet.Scope{})})
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	}
	if len(recs) == 0 {
		fmt.Fprintln(out, "No closed agents.")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	labels := make([]string, len(recs))
	for i, r := range recs {
		labels[i] = fleet.Label(fleet.RecordRow(r), fleet.Scope{})
	}
	labels = fleet.UniqueLabels(labels)
	fmt.Fprintln(w, "ID\tNAME\tAGENT\tPROJECT\tCLOSED\tSTATE\tPR")
	for i, r := range recs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			dashIfEmpty(ix.HandleFor(r.SessionID)),
			dashIfEmpty(truncateDisplay(labels[i], statusLabelWidth)),
			dashIfEmpty(r.Instance),
			dashIfEmpty(r.Project()),
			closedlog.Ago(now, r.ClosedAt),
			closedState(r),
			closedPR(r),
		)
	}
	return w.Flush()
}

// filterClosed keeps the records matching a --session filter: the full tmux
// session name, or a substring of the project (as the live --session).
func filterClosed(recs []closedlog.Record, session string) []closedlog.Record {
	if session == "" {
		return recs
	}
	var out []closedlog.Record
	for _, r := range recs {
		bare := strings.TrimPrefix(r.Session, "agentctl-")
		if r.Session == session || strings.Contains(bare, session) || strings.Contains(r.Project(), session) {
			out = append(out, r)
		}
	}
	return out
}

func closedState(r closedlog.Record) string {
	s := r.FinalState()
	if r.Reason == closedlog.ReasonCascade {
		s += " (cascade)"
	}
	return s
}

func closedPR(r closedlog.Record) string {
	switch {
	case r.PRNumber > 0:
		return "#" + strconv.Itoa(r.PRNumber)
	case r.PRURL != "":
		return r.PRURL
	}
	return "-"
}

// closedJSON is a closed record plus its handle and label.
type closedJSON struct {
	closedlog.Record
	Handle string `json:"handle,omitempty"`
	Label  string `json:"label,omitempty"`
}

// liveGroups loads the live sessions for handle uniqueness; a failure (no
// tmux, monitor off) just leaves them out.
func liveGroups() []fleet.Group {
	groups, err := fleet.Load()
	if err != nil {
		return nil
	}
	return groups
}

// allClosed is the whole ledger (every kept month), for handle uniqueness.
func allClosed() []closedlog.Record {
	recs, err := closedLoad(closedNow().Add(-closedlog.MaxAge))
	if err != nil {
		return nil
	}
	return recs
}

// printClosedDetail is `status <handle>` for a session whose window is gone:
// the ledger record, and how to bring it back.
func printClosedDetail(out io.Writer, s fleet.Session, jsonOut bool) error {
	r := *s.Closed
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(closedJSON{Record: r, Handle: s.Handle, Label: s.Label})
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	line := func(k, v string) { fmt.Fprintf(w, "%s\t%s\n", k, dashIfEmpty(v)) }
	line("SESSION", s.Handle+"  "+s.Label+"  (closed)")
	line("closed", closedlog.Ago(closedNow(), r.ClosedAt))
	line("project", r.Project())
	line("agent", firstNonEmpty(r.Instance, r.Agent))
	line("state", closedState(r))
	line("mode", r.Mode)
	line("ticket", strings.TrimSpace(r.Ticket+" "+r.TicketTitle))
	line("branch", r.Branch)
	line("work dir", firstNonEmpty(r.WorkDir, r.ProjectDir))
	line("pr", closedPR(r))
	line("session id", r.SessionID)
	line("resume", "agentctl resume "+s.Handle)
	return w.Flush()
}
