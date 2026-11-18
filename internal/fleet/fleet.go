// Package fleet is the data layer for the hub's cross-session aggregator view.
// It is deliberately kept free of any Bubble Tea / terminal dependency so it
// can be exercised with table-driven tests and no TTY: Load (and LoadGroup)
// do the I/O (registry, tmux, windowstate), while tagging, ordering, and
// rollup are pure functions over slices.
package fleet

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

// Row is one agent window, tagged with the session and project it belongs
// to. state.AgentState itself carries no session/project key — rows are
// tagged at load time from the registry entry the snapshot came from, so the
// per-project monitors that write state.AgentState stay unchanged.
type Row struct {
	Session string // tmux session name, e.g. "agentctl-myproject"
	Project string // display name for the project (the session's directory basename)
	state.AgentState

	// Dispatched is true when another agent launched this window via
	// `agentctl dispatch`, false for windows launched interactively from the
	// menu or hub (and for windows with no windowstate record).
	Dispatched bool

	// Detached is true for a dispatched window that recorded no dispatcher
	// (launched from the phone or cron). It is a tree root, like a bare session.
	Detached bool

	// Shell marks a plain "shell" window. It is shown in the hub so it can be
	// selected, viewed and closed, but it is not an agent: no state, no
	// windowstate record, and it never feeds the group rollup.
	Shell bool

	// Parent is the tmux window ID (same session) of the agent that
	// dispatched this window, or "" when none or untrusted (see
	// Meta.Parent).
	Parent string

	// StateSince is when the window entered its current state (BlockedSince
	// for a blocked row); zero when unknown. Set by TagMeta.
	StateSince time.Time

	// Recap is the row's "what is it working on" line, and PRNumber/PRURL the
	// newest PR link from its session log. Set by TagLogs.
	Recap    RecapLine
	PRNumber int
	PRURL    string
}

// RollupStatus is a rolled-up status across a set of rows, in the group-level
// attention-priority order used for group headers:
// Blocked > Working > Done > Idle. It does not affect ordering.
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
// for its project, the rolled-up status across all its rows, and the rows
// themselves (already ordered). Rows are built from `tmux list-windows` plus
// windowstate (see LoadAgents), never from state.json, so a group is as live
// as tmux and the hooks are; when tmux cannot list the session the group has
// no rows.
type Group struct {
	Session string
	Project string
	Rollup  RollupStatus
	Rows    []Row

	// RuntimeDir is the session's .agentctl directory — where its windows/
	// records live. Carried through so a caller can reach that session's
	// windowstate records (see LoadMeta).
	RuntimeDir string

	// ProjectDir is the session's project directory (tmux #{session_path},
	// or the registry's ProjectDir). Used as the cwd when launching a new
	// agent into this session from the hub.
	ProjectDir string
}

// Load discovers every live agentctl session — primarily via tmux's
// #{session_path} (ListSessionsWithPaths), enriched by the registry — and
// builds one Group per surviving entry. It never aborts on a single
// session's problem: a session tmux cannot list renders as an empty group
// rather than failing the caller's whole aggregation. See docs/fleet-view-design.md for why tmux,
// not the registry, is the primary discovery source.
func Load() ([]Group, error) { return load(false) }

// LoadWithShells is Load for the hub: each group also carries its "shell"
// windows as Shell rows (never part of the rollup). status, fleet watching
// and the other readers use Load and never see them.
func LoadWithShells() ([]Group, error) { return load(true) }

func load(shells bool) ([]Group, error) {
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
		groups = append(groups, loadGroup(e, shells))
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
		if !isAgentctlSessionName(s.Name) || s.Name == tmux.HubSessionName || tmux.IsAnyViewerSession(s.Name) {
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

// LoadGroup builds a single Group from one registry entry: tmux's window
// list for the session joined with its windowstate records (LoadAgents).
// Never returns an error — if tmux cannot list the session (server gone) the
// group is empty. state.json is not consulted beyond optional token
// enrichment.
func LoadGroup(e registry.Entry) Group { return loadGroup(e, false) }

func loadGroup(e registry.Entry, withShells bool) Group {
	project := projectDisplayName(e)
	g := Group{
		Session:    e.SessionName,
		Project:    project,
		Rollup:     RollupIdle,
		RuntimeDir: e.RuntimeDir,
		ProjectDir: e.ProjectDir,
	}
	agents, shells, err := loadAgentsAndShells(e.SessionName, e.RuntimeDir)
	if err != nil {
		return g
	}
	if !withShells {
		shells = nil
	}
	rows := TagAgents(e.SessionName, project, agents)
	TagMeta(rows, LoadMeta(Group{RuntimeDir: e.RuntimeDir}))
	TagLogs(rows)
	g.Rollup = RollupRows(rows)
	if len(shells) > 0 {
		for _, s := range shells {
			rows = append(rows, Row{Session: e.SessionName, Project: project, AgentState: s, Shell: true})
		}
		rows = SortRows(rows)
	}
	g.Rows = rows
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

// TagAgents tags already-built agent views with the session and project and
// orders them by window index.
func TagAgents(session, project string, agents []state.AgentState) []Row {
	rows := make([]Row, 0, len(agents))
	for _, a := range agents {
		rows = append(rows, Row{Session: session, Project: project, AgentState: a})
	}
	return SortRows(rows)
}

// IsNonAgentWindow reports whether a window name is one the monitor never
// tracks as an agent ("shell", "improve~*"), for commands
// (e.g. `agentctl sweep`) that must never touch these windows.
func IsNonAgentWindow(name string) bool {
	if name == "shell" {
		return true
	}
	return len(name) >= len("improve~") && name[:len("improve~")] == "improve~"
}

// SortRows orders rows within a group by WindowIndex (tmux's window order)
// only. State deliberately plays no part, so agents do not jump around as
// their state changes. Returns a new slice; the input is left untouched.
func SortRows(rows []Row) []Row {
	sorted := make([]Row, len(rows))
	copy(sorted, rows)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].WindowIndex < sorted[j].WindowIndex
	})
	return sorted
}

// rollupOf returns the group-level RollupStatus contribution of a single
// row: blocked if State is "blocked", else working if State is "working",
// else done if Done is true (unseen completion), else idle.
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

// OrderGroups sorts groups alphabetically by project display name
// (case-insensitive), breaking ties by session name. Rollup status plays no
// part, so groups stay put as their agents change state. Returns a new slice;
// the input is left untouched.
func OrderGroups(groups []Group) []Group {
	sorted := make([]Group, len(groups))
	copy(sorted, groups)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := strings.ToLower(sorted[i].Project), strings.ToLower(sorted[j].Project)
		if pi != pj {
			return pi < pj
		}
		return sorted[i].Session < sorted[j].Session
	})
	return sorted
}

// FirstBlocked returns the first blocked agent across all groups, walking
// groups then rows in the exact order Load produces (groups alphabetical by
// project, rows by window index within each group) — so the hub view and the
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
