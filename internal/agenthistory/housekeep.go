package agenthistory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/logging"
)

// SnapshotAll snapshots the global store and the project store of every dir
// in projectDirs that has a .claude/agents directory (worktrees are skipped:
// they are copies of a repo that is itself tracked). Every failure is logged,
// never returned; a missing git is warned about once per machine.
func SnapshotAll(reason string, projectDirs []string) {
	var stores []*Store
	if s, err := HomeStore(); err != nil {
		logging.Log("agent-history: %v", err)
	} else {
		stores = append(stores, s)
	}
	if s, err := ConfigStore(); err != nil {
		logging.Log("agent-history: %v", err)
	} else {
		stores = append(stores, s)
	}
	stores = append(stores, ProjectStores(projectDirs)...)
	SnapshotStores(reason, stores)
}

// ProjectStores returns the project stores of the dirs in projectDirs that
// have a .claude/agents directory (worktrees skipped).
func ProjectStores(projectDirs []string) []*Store {
	var stores []*Store
	seen := map[string]bool{}
	for _, d := range projectDirs {
		d = filepath.Clean(d)
		if d == "." || !filepath.IsAbs(d) || seen[d] || strings.Contains(d, string(filepath.Separator)+".worktrees"+string(filepath.Separator)) {
			continue
		}
		seen[d] = true
		if fi, err := os.Stat(filepath.Join(d, ".claude", "agents")); err != nil || !fi.IsDir() {
			continue
		}
		if s, err := ProjectStore(d); err == nil {
			stores = append(stores, s)
		}
	}
	return stores
}

// SnapshotStores snapshots each store, logging failures; a missing git is
// warned about once and ends the loop.
func SnapshotStores(reason string, stores []*Store) {
	for _, s := range stores {
		rev, err := s.Snapshot(reason, DefaultAuthor)
		switch {
		case errors.Is(err, ErrGitMissing):
			warnGitMissing()
			return
		case err != nil:
			logging.Log("agent-history: snapshot %s: %v", s.WorkTree, err)
		case rev != "":
			logging.Log("agent-history: %s: %s (%s)", s.WorkTree, reason, rev[:min(8, len(rev))])
		}
	}
}

// warnGitMissing logs the missing-git warning once, recorded by a marker in
// the state dir (housekeeping runs in a fresh process every few minutes).
func warnGitMissing() {
	sd, err := stateDir()
	if err != nil {
		return
	}
	marker := filepath.Join(sd, "agent-history.nogit")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if err := os.MkdirAll(sd, 0o700); err == nil {
		os.WriteFile(marker, nil, 0o600) //nolint:errcheck
	}
	logging.Warn("agent-history: git not found on PATH; agent definition history is disabled")
}
