// Package herd is the data layer for the hub's cross-session aggregator view.
// It is deliberately kept free of any Bubble Tea / terminal dependency so it
// can be exercised with table-driven tests and no TTY: Load (and LoadGroup)
// do the I/O (registry, tmux, state.json), while tagging, ordering, and
// rollup are pure functions over slices.
package herd

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/registry"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
)

// SnapshotStaleAfter is how old a session's state.json can be before its
// group is flagged Stale. The monitor writes state.json roughly every tick
// (500ms), so this leaves generous margin for the hub's own ~1s poll cadence
// without flapping.
const SnapshotStaleAfter = 5 * time.Second

// Row is one agent window, tagged with the session and project it belongs
// to. state.AgentState itself carries no session/project key — rows are
// tagged at load time from the registry entry the snapshot came from, so the
// per-project monitors that write state.AgentState stay unchanged.
type Row struct {
	Session string // tmux session name, e.g. "agentctl-myproject"
	Project string // display name for the project (the session's directory basename)
	state.AgentState
}

// RollupStatus is a rolled-up status across a set of rows, in the group-level
// attention-priority order used for group headers and group sorting:
// Blocked > Working > Done > Idle. This intentionally differs from the
// within-group row order (see rowSortRank) — see the design doc.
type RollupStatus int

const (
	RollupBlocked RollupStatus = iota
	RollupWorking
	RollupDone
	RollupIdle
)

// String returns the lowercase name of the rollup status.
func (r RollupStatus) String() string {
	switch r {
	case RollupBlocked:
		return "blocked"
	case RollupWorking:
		return "working"
	case RollupDone:
		return "done"
	case RollupIdle:
		return "idle"
	}
	return "unknown"
}

// Group is one session's worth of agents: the session name, a display name
// for its project, the rolled-up status across all its rows, the rows
// themselves (already ordered), and whether the underlying state.json
// snapshot was missing, unreadable, or stale. When a fresh snapshot goes
// stale (the monitor briefly wedges) Rows keeps the last-known set rather
// than going empty — a stale group still shows its agents, just dimmed and
// flagged, so a momentary hiccup doesn't make a whole project's agents
// vanish from the hub. Rows are only empty when there was never any data to
// show (snapshot missing/unreadable, or genuinely zero agents).
type Group struct {
	Session string
	Project string
	Rollup  RollupStatus
	Rows    []Row
	Stale   bool // snapshot missing, unreadable, or older than SnapshotStaleAfter

	// RuntimeDir is the session's .agentctl directory — where its state.json
	// and windows/ live. Carried through so a caller can reach that session's
	// windowstate records (see Enrich), which hold launch mode, dispatch
	// metadata, and the spawned-by parent link that state.json does not.
	RuntimeDir string

	// SnapshotAge is how old the state.json backing this group was when it
	// was read. Zero when the snapshot was missing or unreadable — check
	// Stale, not SnapshotAge == 0, to detect that case. Surfaced because a
	// non-interactive caller cannot see the hub's dimming: a wedged monitor
	// otherwise serves confident, frozen answers.
	SnapshotAge time.Duration
}

// Load discovers every live agentctl session — primarily via tmux's
// #{session_path} (ListSessionsWithPaths), enriched by the registry — and
// builds one Group per surviving entry. It never aborts on a single
// session's problem: a session whose snapshot is missing, unreadable, or
// stale renders as a degraded (Stale) group rather than failing the
// caller's whole aggregation. See docs/herd-view-design.md for why tmux,
// not the registry, is the primary discovery source.
func Load() ([]Group, error) {
	live := make(map[string]bool)
	for _, s := range tmux.ListSessions() {
		live[s] = true
	}

	entries, err := registry.Prune(live, registry.StaleAfter)
	if err != nil {
		return nil, fmt.Errorf("pruning registry: %w", err)
	}

	merged := MergeSessions(tmux.ListSessionsWithPaths(), entries)

	groups := make([]Group, 0, len(merged))
	for _, e := range merged {
		groups = append(groups, LoadGroup(e))
	}
	return OrderGroups(groups), nil
}

// MergeSessions combines tmux's live agentctl-* sessions — the primary
// discovery source, keyed by session name, with the project directory read
// directly from tmux's #{session_path} — with the registry, which is
// enrichment plus the Phase 3 remote seam.
//
// Every agentctl-* session tmux reports (other than the hub session, which
// owns no agents and whose .agentctl/ is a scratch dir) is included, even if
// it never registered — this is what makes discovery independent of monitor
// version or registry state. A registry entry for that same session name
// enriches the result: its RuntimeDir wins over the tmux-derived
// "<path>/.agentctl" (a session's cwd can differ from its project root),
// along with PID/StartedAt/Host/ProjectDir.
//
// A registry entry with no matching tmux session is stale for a local entry
// (dropped here — registry.Prune, already run by the caller, will clean up
// the file itself) but kept for a non-local (remote) entry, which is the
// Phase 3 seam: a remote host's sessions have no local tmux session to match
// against.
//
// Pure function — no I/O — so it's directly table-driven testable.
func MergeSessions(sessions []tmux.SessionInfo, entries []registry.Entry) []registry.Entry {
	byName := make(map[string]registry.Entry, len(entries))
	for _, e := range entries {
		byName[e.SessionName] = e
	}

	seen := make(map[string]bool, len(sessions))
	out := make([]registry.Entry, 0, len(sessions)+len(entries))

	for _, s := range sessions {
		if !isAgentctlSessionName(s.Name) || s.Name == tmux.HubSessionName {
			continue
		}
		if seen[s.Name] {
			continue // session names are unique in tmux; defensive dedupe
		}
		seen[s.Name] = true

		e := registry.Entry{
			SessionName: s.Name,
			ProjectDir:  s.Path,
			RuntimeDir:  filepath.Join(s.Path, ".agentctl"),
			Host:        "local",
		}
		if reg, ok := byName[s.Name]; ok {
			// Registry enrichment: authoritative RuntimeDir plus diagnostics.
			e.RuntimeDir = reg.RuntimeDir
			e.PID = reg.PID
			e.StartedAt = reg.StartedAt
			if reg.Host != "" {
				e.Host = reg.Host
			}
			if reg.ProjectDir != "" {
				e.ProjectDir = reg.ProjectDir
			}
		}
		out = append(out, e)
	}

	for _, e := range entries {
		if seen[e.SessionName] {
			continue
		}
		if e.Host != "" && e.Host != "local" {
			out = append(out, e) // remote seam: no local tmux session to match
		}
		// Local entry with no live tmux session is stale — drop it; the
		// registry's own Prune (already run by the caller) removes the file.
	}

	return out
}

// isAgentctlSessionName reports whether a tmux session name is one the hub
// discovers agents from: the "agentctl-<dir>" pattern used by every
// per-project session.
func isAgentctlSessionName(name string) bool {
	return strings.HasPrefix(name, "agentctl-")
}

// LoadGroup builds a single Group from one registry entry by reading its
// state.json. Never returns an error — a missing or unreadable snapshot
// produces a degraded (Stale) group with no rows. A snapshot that exists but
// has gone stale (older than SnapshotStaleAfter) also flags the group Stale,
// but keeps its last-known rows rather than dropping them: a briefly-wedged
// monitor shouldn't make a whole project's agents vanish from the hub.
func LoadGroup(e registry.Entry) Group {
	project := projectDisplayName(e)

	path := filepath.Join(e.RuntimeDir, "state.json")
	snap, err := state.LoadSnapshotFrom(path)
	if err != nil || snap == nil {
		return Group{
			Session:    e.SessionName,
			Project:    project,
			Rollup:     RollupIdle,
			Stale:      true,
			RuntimeDir: e.RuntimeDir,
		}
	}

	rows := SortRows(TagRows(e.SessionName, project, snap))
	g := Group{
		Session:     e.SessionName,
		Project:     project,
		Rollup:      RollupRows(rows),
		Rows:        rows,
		RuntimeDir:  e.RuntimeDir,
		SnapshotAge: time.Since(snap.Timestamp),
	}
	if snap.IsStale(SnapshotStaleAfter) {
		g.Stale = true
	}
	return g
}

// projectDisplayName derives a human-friendly project name from a registry
// entry, falling back to the session name when the project directory is
// empty or a bare path component.
func projectDisplayName(e registry.Entry) string {
	name := filepath.Base(e.ProjectDir)
	if name == "" || name == "." || name == "/" {
		return e.SessionName
	}
	return name
}

// TagRows builds the Row slice for one session from its already-loaded
// state.Snapshot, tagging each row with the session name and project display
// name. Skips non-agent windows ("shell", "improve~*") the same way the
// monitor does when it builds state.json — persistState already filters
// these, but a hand-edited or stale snapshot might not.
func TagRows(session, project string, snap *state.Snapshot) []Row {
	if snap == nil {
		return nil
	}
	rows := make([]Row, 0, len(snap.Agents))
	for _, a := range snap.Agents {
		if isNonAgentWindow(a.Name) {
			continue
		}
		rows = append(rows, Row{
			Session:    session,
			Project:    project,
			AgentState: a,
		})
	}
	return rows
}

// isNonAgentWindow reports whether a window name is one the monitor never
// tracks as an agent: the persistent "shell" window, or an auto-improve
// session ("improve~<agent>").
func isNonAgentWindow(name string) bool { return IsNonAgentWindow(name) }

// IsNonAgentWindow is the exported form of isNonAgentWindow, for commands
// (e.g. `agentctl sweep`) that must never touch these windows.
func IsNonAgentWindow(name string) bool {
	if name == "shell" {
		return true
	}
	return len(name) >= len("improve~") && name[:len("improve~")] == "improve~"
}

// rowSortRank returns the within-group sort priority for a row: blocked
// first, then done (unseen), then working, then idle. Lower ranks sort
// first. This deliberately differs from the group-level RollupStatus order —
// see rollupOf.
func rowSortRank(r Row) int {
	switch {
	case r.State == "blocked":
		return 0
	case r.Done:
		return 1
	case r.State == "working":
		return 2
	default:
		return 3
	}
}

// SortRows orders rows within a group: blocked first, then done (unseen),
// then working, then idle — ties broken by WindowIndex so the order matches
// tmux's display order and doesn't jitter between polls as states change.
// Returns a new slice; the input is left untouched.
func SortRows(rows []Row) []Row {
	sorted := make([]Row, len(rows))
	copy(sorted, rows)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := rowSortRank(sorted[i]), rowSortRank(sorted[j])
		if ri != rj {
			return ri < rj
		}
		return sorted[i].WindowIndex < sorted[j].WindowIndex
	})
	return sorted
}

// rollupOf returns the group-level RollupStatus contribution of a single
// row: blocked if State is "blocked", else working if State is "working",
// else done if Done is true (unseen completion), else idle. Note this order
// — working before done — differs from rowSortRank, which is deliberate: see
// the design doc's "Group rollup" section.
func rollupOf(r Row) RollupStatus {
	switch {
	case r.State == "blocked":
		return RollupBlocked
	case r.State == "working":
		return RollupWorking
	case r.Done:
		return RollupDone
	default:
		return RollupIdle
	}
}

// RollupRows computes the rolled-up status across a set of rows: blocked if
// any row is blocked, else working if any is working, else done if any is
// done (unseen), else idle. An empty slice rolls up to idle.
func RollupRows(rows []Row) RollupStatus {
	best := RollupIdle
	for _, r := range rows {
		if ro := rollupOf(r); ro < best {
			best = ro
			if best == RollupBlocked {
				break // can't get more urgent than blocked
			}
		}
	}
	return best
}

// OrderGroups sorts groups so the ones needing attention float to the top:
// by rolled-up status (using the same RollupBlocked > RollupWorking >
// RollupDone > RollupIdle precedence as RollupRows), then by session name
// for stability when rollups tie. Returns a new slice; the input is left
// untouched.
func OrderGroups(groups []Group) []Group {
	sorted := make([]Group, len(groups))
	copy(sorted, groups)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Rollup != sorted[j].Rollup {
			return sorted[i].Rollup < sorted[j].Rollup
		}
		return sorted[i].Session < sorted[j].Session
	})
	return sorted
}

// FirstBlocked returns the first blocked agent across all groups, walking
// groups then rows in the exact order Load produces (groups sorted by
// rollup, rows sorted within each group) — so the hub view and the
// `jump-next-blocked` command always agree on which agent is "next".
// Returns nil if no agent is blocked anywhere.
func FirstBlocked(groups []Group) *Row {
	for i := range groups {
		for j := range groups[i].Rows {
			if groups[i].Rows[j].State == "blocked" {
				return &groups[i].Rows[j]
			}
		}
	}
	return nil
}

// ActivityLine returns a short "what's this agent doing" summary: the last
// tool detail (e.g. "Editing monitor.go"), falling back to the last
// assistant text block when no tool activity is recorded. Callers are
// responsible for truncating to the available render width.
//
// The result is defensively flattened to a single line even though
// logstream already flattens LastToolDetail/LastText at the source: this
// view also reads state.json snapshots written by other projects' monitor
// binaries (see MergeSessions) and by older monitors already on disk before
// the source-side flatten shipped, so it cannot assume clean input.
func ActivityLine(r Row) string {
	if r.LastToolDetail != "" {
		return flattenWhitespace(r.LastToolDetail)
	}
	return flattenWhitespace(r.LastText)
}

// flattenWhitespace collapses any run of whitespace into a single space and
// trims the ends. See logstream.flattenWhitespace for the source-side
// equivalent this mirrors defensively.
func flattenWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
