package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/blsmth/agentctl/internal/cleanup"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/signal"
	"github.com/blsmth/agentctl/internal/tui"
	"github.com/blsmth/agentctl/internal/worktree"
)

// CleanupOptions holds the flags for `agentctl cleanup`.
type CleanupOptions struct {
	DryRun         bool
	Yes            bool
	IncludeOpenPRs bool
	JSON           bool
}

// cleanupJSONRow is the machine-readable row emitted for `cleanup --json`.
type cleanupJSONRow struct {
	Worktree       string   `json:"worktree"`
	Branch         string   `json:"branch"`
	WindowID       string   `json:"window_id,omitempty"`
	Disposition    string   `json:"disposition"`
	Reason         string   `json:"reason"`
	Action         string   `json:"action"`
	PreserveBranch bool     `json:"preserve_branch,omitempty"`
	Errors         []string `json:"errors,omitempty"`
}

// RunCleanup implements `agentctl cleanup`: reclaim worktrees and windows
// left behind by finished dispatched agents.
//
// The plan is always shown exactly once and, in every mode, printed as
// valid JSON when opts.JSON is set (never twice -- see the JSON-without-
// --yes branch below, which is the one path that both prints and stops
// without acting).
func RunCleanup(opts CleanupOptions) error {
	repoDir, err := resolveCleanupRoot()
	switch {
	case errors.Is(err, errNotAGitRepo):
		// Not an error state: report it the same way an empty repo is
		// reported, so a scripted `--json` caller still gets valid JSON.
		if opts.JSON {
			fmt.Println("[]")
			return nil
		}
		fmt.Printf("%s is not a git repository — no worktrees to clean up.\n", repoDir)
		return nil
	case err != nil:
		return fmt.Errorf("determining working directory: %w", err)
	}
	repoSlug := tui.TLRepoSlug(repoDir)
	selfWindowID := signal.ResolveWindowID()

	// Reclaim stale worktree administrative metadata (entries whose directory
	// was deleted out from under git) before Gather runs, so the report below
	// reflects post-prune state. Non-fatal: a prune failure must never block
	// the rest of cleanup.
	if err := worktree.Prune(repoDir); err != nil {
		logging.Warn("cleanup: worktree.Prune(%s): %v", repoDir, err)
	}

	candidates, err := cleanup.Gather(cleanup.Options{
		RepoDir:        repoDir,
		RepoSlug:       repoSlug,
		SelfWindowID:   selfWindowID,
		IncludeOpenPRs: opts.IncludeOpenPRs,
	})
	if err != nil {
		return fmt.Errorf("gathering cleanup candidates: %w", err)
	}

	if len(candidates) == 0 {
		if opts.JSON {
			fmt.Println("[]")
			return nil
		}
		fmt.Println("No worktrees found under .worktrees/ — nothing to clean up.")
		return nil
	}

	reclaimable := cleanup.Reclaimable(candidates)

	if opts.DryRun {
		printCleanupReport(candidates, opts.JSON)
		return nil
	}

	if len(reclaimable) == 0 {
		printCleanupReport(candidates, opts.JSON)
		if !opts.JSON {
			fmt.Println("\nNothing to reclaim.")
		}
		return nil
	}

	if !opts.Yes {
		if opts.JSON {
			// A y/N prompt makes no sense for a machine-readable, presumably
			// scripted invocation -- --yes is the explicit opt-in to act
			// here, so this prints the plan once and stops.
			printCleanupReport(candidates, true)
			fmt.Fprintln(os.Stderr, "cleanup: pass --yes to act (confirmation prompts are skipped in --json mode)")
			return nil
		}
		printCleanupReport(candidates, false)
		if !confirmYesNo(fmt.Sprintf("\nReclaim %d worktree(s)? [y/N] ", len(reclaimable))) {
			fmt.Println("Aborted — nothing changed.")
			return nil
		}
	}

	for _, c := range reclaimable {
		cleanup.Reclaim(c)
	}

	printCleanupReport(candidates, opts.JSON)
	if !opts.JSON {
		printCleanupSummary(candidates)
	}
	return nil
}

// errNotAGitRepo reports that cwd is not inside a git repository, so there is
// nothing for cleanup to reason about. This is a normal outcome rather than a
// failure: `agentctl cleanup` is reachable from anywhere, and worktrees only
// exist inside repos.
var errNotAGitRepo = errors.New("not a git repository")

// resolveCleanupRoot returns the top-level directory of the git repository
// containing the current working directory. This lets `agentctl cleanup` work
// from any subdirectory of the main checkout, not only its root.
//
// When cwd is not in a repo at all it returns that cwd together with
// errNotAGitRepo, so the caller can say so plainly. Previously it swallowed
// the error and returned the bare cwd, which pushed the failure down into
// `git worktree list` and surfaced as "gathering cleanup candidates: listing
// worktrees: git worktree list: exit status 128" -- naming neither the
// directory nor the actual problem.
//
// Note this resolves to whichever working tree cwd is actually inside: run
// from within a dispatch worktree itself, it returns that worktree's own
// root, which normally has no .worktrees/ of its own -- cleanup is meant to
// be run from the main checkout (e.g. the "shell" window), the same place a
// human operator dispatches agents from.
func resolveCleanupRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root, err := worktree.Toplevel(cwd)
	if err != nil {
		return cwd, errNotAGitRepo
	}
	return root, nil
}

// confirmYesNo prints prompt and reads a line from stdin, defaulting to "no"
// on anything other than an explicit y/yes -- destructive commands should
// never treat an empty or garbled answer as consent.
func confirmYesNo(prompt string) bool {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}

// dispositionRank orders the report so reclaimed/reclaimable rows sort
// before skips, and within reclaimable rows, landed/empty (unconditional)
// sort before open-pr (conditional on --include-open-prs).
func dispositionRank(d cleanup.Disposition) int {
	switch d {
	case cleanup.DispositionLanded:
		return 0
	case cleanup.DispositionEmpty:
		return 1
	case cleanup.DispositionOpenPR:
		return 2
	default: // DispositionSkip
		return 3
	}
}

func printCleanupReport(candidates []cleanup.Candidate, jsonOut bool) {
	if jsonOut {
		rows := make([]cleanupJSONRow, 0, len(candidates))
		for _, c := range candidates {
			rows = append(rows, cleanupJSONRow{
				Worktree:       shortenPath(c.WorktreePath),
				Branch:         c.Branch,
				WindowID:       c.WindowID,
				Disposition:    string(c.Disposition),
				Reason:         c.Reason,
				Action:         c.ActionText(),
				PreserveBranch: c.PreserveBranch,
				Errors:         c.Errors,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rows) //nolint:errcheck
		return
	}

	sorted := append([]cleanup.Candidate(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := dispositionRank(sorted[i].Disposition), dispositionRank(sorted[j].Disposition)
		if ri != rj {
			return ri < rj
		}
		return sorted[i].WorktreePath < sorted[j].WorktreePath
	})

	muted := cliMuted()
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(muted).
		Headers("WORKTREE", "BRANCH", "WINDOW", "DISPOSITION", "ACTION").
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return cliHeaderStyle()
			}
			return lipgloss.NewStyle()
		})

	for _, c := range sorted {
		window := c.WindowID
		if window == "" {
			window = "-"
		}
		t.Row(shortenPath(c.WorktreePath), c.Branch, window, string(c.Disposition), c.ActionText())
	}

	fmt.Println(t.Render())
}

func printCleanupSummary(candidates []cleanup.Candidate) {
	var landed, empty, openPR, skip, errored int
	for _, c := range candidates {
		switch c.Disposition {
		case cleanup.DispositionLanded:
			landed++
		case cleanup.DispositionEmpty:
			empty++
		case cleanup.DispositionOpenPR:
			openPR++
		default:
			skip++
		}
		if len(c.Errors) > 0 {
			errored++
		}
	}
	reclaimed := landed + empty + openPR

	fmt.Printf("\n%d worktree(s) evaluated: %d reclaimed (%d landed, %d empty, %d open-pr), %d skipped",
		len(candidates), reclaimed, landed, empty, openPR, skip)
	if errored > 0 {
		fmt.Printf(", %d with errors", errored)
	}
	fmt.Println()
}
