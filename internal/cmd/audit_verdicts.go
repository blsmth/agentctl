package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/blsmth/agentctl/internal/verdicts"
)

// Seams; tests replace them.
var (
	verdictsLoad = verdicts.Load
	verdictsNow  = time.Now
)

// verdictsReport is the --json shape of `agentctl audit verdicts`.
type verdictsReport struct {
	Verdicts []verdicts.Record    `json:"verdicts"`
	Authors  []verdicts.AgentStat `json:"authors"`
}

// RunAuditVerdicts lists review verdicts from the last since (default 30d),
// newest first, with the BLOCKED rate per author agent.
func RunAuditVerdicts(w io.Writer, since string, asJSON bool) error {
	if strings.TrimSpace(since) == "" {
		since = "30d"
	}
	d, err := parseSince(since)
	if err != nil {
		return err
	}
	recs, err := verdictsLoad(verdictsNow().Add(-d))
	if err != nil {
		return fmt.Errorf("loading verdicts: %w", err)
	}
	stats := verdicts.ByAuthor(recs)
	if asJSON {
		if recs == nil {
			recs = []verdicts.Record{}
		}
		if stats == nil {
			stats = []verdicts.AgentStat{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(verdictsReport{Verdicts: recs, Authors: stats})
	}
	if len(recs) == 0 {
		_, err := fmt.Fprintf(w, "No review verdicts in the last %s.\n", since)
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tPR\tVERDICT\tROUNDS\tAUTHOR\tREVIEWER\tREPO") //nolint:errcheck
	for _, r := range recs {
		rounds := "-"
		if r.Rounds > 0 {
			rounds = fmt.Sprint(r.Rounds)
		}
		fmt.Fprintf(tw, "%s\t#%d\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
			r.TS.Local().Format("01-02 15:04"), r.PR, r.Verdict, rounds,
			dash(r.AuthAgent), dash(r.RevAgent), dash(r.Repo))
	}
	fmt.Fprintln(tw, "\t\t\t\t\t\t")                                //nolint:errcheck
	fmt.Fprintln(tw, "AUTHOR AGENT\tVERDICTS\tBLOCKED\tRATE\t\t\t") //nolint:errcheck
	for _, s := range stats {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%.0f%%\t\t\t\n", s.Agent, s.Total, s.Blocked, s.Rate*100) //nolint:errcheck
	}
	return tw.Flush()
}
