package cmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/improverun"
)

// RunImproveRuns lists the improve runs started since the cutoff, newest
// first, with their changed files, history revisions and undo commands.
func RunImproveRuns(w io.Writer, since time.Time) error {
	runs, err := improverun.List(since)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Fprintln(w, "No improve runs.")
		return nil
	}
	for _, r := range runs {
		kind := r.Agent
		if r.Fleet {
			kind = "fleet (" + r.Agent + ")"
		}
		state := "no changes"
		switch {
		case r.Finished.IsZero():
			state = "running or unfinished"
		case len(r.Files) > 0:
			n := len(r.Files)
			state = fmt.Sprintf("%d change", n)
			if n != 1 {
				state += "s"
			}
		}
		fmt.Fprintf(w, "%s  %s  %s  %s\n", r.ID, r.Started.Format("2006-01-02 15:04"), kind, state)
		for i, f := range r.Files {
			fmt.Fprintf(w, "    %s\n", f)
			if i < len(r.Reverts) {
				fmt.Fprintf(w, "      undo: %s\n", r.Reverts[i])
			}
		}
		if len(r.Revs) > 0 {
			short := make([]string, len(r.Revs))
			for i, rev := range r.Revs {
				short[i] = rev[:min(8, len(rev))]
			}
			fmt.Fprintf(w, "    revs: %s\n", strings.Join(short, ", "))
		}
	}
	return nil
}
