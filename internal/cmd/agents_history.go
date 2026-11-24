package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/blsmth/agentctl/internal/agenthistory"
)

// agentsHistoryStore is the seam for the store the history commands act on:
// which is "agents", "config" or "" (routed by name: an absolute path under
// the agentctl config dir is the config store).
var agentsHistoryStore = func(which, name string) (*agenthistory.Store, error) {
	if which == "" {
		which = agenthistory.StoreForPath(name)
	}
	return agenthistory.StoreByName(which)
}

// RunAgentsHistory prints the change history of name (all tracked files when empty).
func RunAgentsHistory(w io.Writer, which, name string, limit int) error {
	s, err := agentsHistoryStore(which, name)
	if err != nil {
		return err
	}
	path := ""
	if name != "" {
		if path, err = s.Resolve(name); err != nil {
			return err
		}
	}
	entries, err := s.Log(path, limit)
	switch {
	case errors.Is(err, agenthistory.ErrNoSnapshots):
		fmt.Fprintln(w, "No snapshots yet.")
	case err != nil:
		return err
	case len(entries) == 0:
		fmt.Fprintln(w, "No history.")
	default:
		for _, e := range entries {
			fmt.Fprintf(w, "%s  %.19s  %-10s %s\n", e.Short, e.Date, e.Author, e.Subject)
		}
	}
	if repos := s.SkippedRepos(); len(repos) > 0 {
		fmt.Fprintf(w, "skipped nested repos: %s\n", strings.Join(repos, ", "))
	}
	return nil
}

// RunAgentsDiff prints rev (default: last snapshot) against the current files.
func RunAgentsDiff(w io.Writer, which, name, rev string) error {
	s, err := agentsHistoryStore(which, name)
	if err != nil {
		return err
	}
	path := ""
	if name != "" {
		if path, err = s.Resolve(name); err != nil {
			return err
		}
	}
	out, err := s.Diff(path, rev)
	if err != nil {
		return err
	}
	if out == "" {
		fmt.Fprintln(w, "No differences.")
		return nil
	}
	_, err = io.WriteString(w, out)
	return err
}

// RunAgentsRevert restores name from rev (default: undo the latest change),
// recorded as a new snapshot authored by the caller's agent, if any.
func RunAgentsRevert(w io.Writer, which, name, rev string) error {
	s, err := agentsHistoryStore(which, name)
	if err != nil {
		return err
	}
	path, err := s.Resolve(name)
	if err != nil {
		return err
	}
	author := agenthistory.DefaultAuthor
	if a := os.Getenv("AGENTCTL_AGENT_NAME"); a != "" {
		author = a
	}
	from, snap, err := s.Revert(path, rev, author)
	if err != nil {
		return err
	}
	switch {
	case snap == "":
		fmt.Fprintf(w, "%s already matches %s; nothing to revert.\n", path, shortRev(from))
	default:
		fmt.Fprintf(w, "Reverted %s to %s (new snapshot %s).\n", path, shortRev(from), shortRev(snap))
	}
	return nil
}

func shortRev(r string) string {
	if r == "" {
		return "its absence"
	}
	return r[:min(8, len(r))]
}
