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
	now := closedNow()
	recs, err := closedLoad(now.Add(-d))
	if err != nil {
		return fmt.Errorf("reading closed windows: %w", err)
	}
	recs = filterClosed(recs, opts.Session)
	if opts.JSON {
		if recs == nil {
			recs = []closedlog.Record{}
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(recs)
	}
	if len(recs) == 0 {
		fmt.Fprintln(out, "No closed agents.")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TITLE\tAGENT\tPROJECT\tCLOSED\tSTATE\tPR")
	for _, r := range recs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			truncateDisplay(r.Title(), 44),
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
