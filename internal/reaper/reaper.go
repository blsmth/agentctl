// Package reaper detects and kills orphaned Claude Code processes -- sessions
// whose tmux pane no longer exists but whose OS process is still running.
//
// Safety invariant: the reaper never signals a process unless it is provably
// a descendant of the tmux server hosting this agentctl session (proven by
// walking the ppid chain of a single `ps` snapshot up to the server pid). A
// process reparented to pid 1 is not provably ours and is left alone. Only
// the individual pid is signaled, never its process group.
//
// The approach:
//  1. Snapshot the process table, then list every live pane PID on the server.
//  2. Narrow to Claude processes whose command line references this
//     instance's settings directory (an empty settings dir disables reaping).
//  3. Kill a candidate only if its ancestry reaches the tmux server and it is
//     not under any live pane.
package reaper

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/textutil"
)

// ProcessInfo holds the minimal fields needed for orphan detection.
type ProcessInfo struct {
	PID  int
	PPID int
	Cmd  string
}

// ListProcesses returns all running processes parsed from `ps`.
// It is the only OS call in the hot path and is kept as a thin wrapper so
// tests can substitute a fixed process list without spawning real processes.
func ListProcesses() ([]ProcessInfo, error) {
	// -ax   : all processes (not just current user's)
	// -o    : custom format
	// ww    : wide output — don't truncate the command column
	out, err := exec.Command("ps", "-axww", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parsePS(string(out)), nil
}

// parsePS parses `ps -axww -o pid=,ppid=,command=` output into a slice of
// ProcessInfo. Lines that cannot be parsed are silently skipped.
func parsePS(output string) []ProcessInfo {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	procs := make([]ProcessInfo, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Fields: pid  ppid  command (command may contain spaces)
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		cmd := strings.Join(fields[2:], " ")
		procs = append(procs, ProcessInfo{PID: pid, PPID: ppid, Cmd: cmd})
	}
	return procs
}

// BuildDescendantSet returns the set of PIDs that are equal to, or transitive
// descendants of, any PID in roots. The roots themselves are included so a
// "is this process live?" check is a single map lookup.
func BuildDescendantSet(procs []ProcessInfo, roots []int) map[int]bool {
	// Build a parent→children adjacency list.
	children := make(map[int][]int, len(procs))
	for _, p := range procs {
		children[p.PPID] = append(children[p.PPID], p.PID)
	}

	live := make(map[int]bool, len(roots)*4)
	queue := make([]int, len(roots))
	copy(queue, roots)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if live[cur] {
			continue
		}
		live[cur] = true
		queue = append(queue, children[cur]...)
	}
	return live
}

// FilterClaudeProcesses returns the subset of procs whose command line
// suggests they are Claude Code sessions.  We look for the literal string
// "claude" in the command — this matches both the `claude` Node wrapper and
// any node invocation that passes a claude script path.
//
// If settingsDir is non-empty only processes whose command references that
// directory are returned, ensuring we only touch processes owned by this
// agentctl instance and not those of other projects.
func FilterClaudeProcesses(procs []ProcessInfo, settingsDir string) []ProcessInfo {
	var out []ProcessInfo
	for _, p := range procs {
		cmd := p.Cmd
		// Skip our own process and the parent monitor.
		if p.PID == os.Getpid() {
			continue
		}
		if !containsClaude(cmd) {
			continue
		}
		if settingsDir != "" && !strings.Contains(cmd, settingsDir) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// containsClaude returns true when cmd looks like a Claude Code process.
//
// Matching rules (in order):
//  1. The executable token's basename is "claude" or has a "claude-" prefix.
//  2. The executable is "node" (or a full path to node) and one of its file-path
//     arguments contains "/claude/" or "claude-code" — i.e. it is a Node.js
//     process running Claude Code directly.
//
// We intentionally do NOT match on bare argument tokens like "claude" or file
// names like "/etc/claude.conf" to avoid false positives from grep, cat, etc.
func containsClaude(cmd string) bool {
	tokens := strings.Fields(cmd)
	if len(tokens) == 0 {
		return false
	}

	exe := tokens[0]
	exeBase := exe
	if idx := strings.LastIndex(exe, "/"); idx >= 0 {
		exeBase = exe[idx+1:]
	}
	exeBase = strings.ToLower(exeBase)

	// Rule 1: executable is "claude" or "claude-*".
	if exeBase == "claude" || strings.HasPrefix(exeBase, "claude-") {
		return true
	}

	// Rule 2: node process running a claude script path.
	if exeBase == "node" {
		for _, tok := range tokens[1:] {
			if !strings.HasPrefix(tok, "/") {
				continue // skip non-path arguments
			}
			lower := strings.ToLower(tok)
			if strings.Contains(lower, "/claude/") || strings.Contains(lower, "claude-code") {
				return true
			}
		}
	}

	return false
}

// HasClaudeDescendant reports whether a live Claude Code process is running
// as panePID itself or as one of its transitive descendants.
//
// This is the reliable liveness check for "is an agent actually running in
// this pane" -- checking tmux's #{pane_current_command} is NOT: Claude runs
// as a *child* of the pane's shell, so every agentctl pane reports "zsh"
// regardless of whether Claude is alive underneath it. That exact mistake is
// what broke tmux send-keys delivery for months (see
// docs/agent-messaging-design.md §3's shouldDeliver postmortem) -- do not
// reintroduce it here.
func HasClaudeDescendant(panePID int) bool {
	if panePID <= 0 {
		return false
	}
	procs, err := ListProcesses()
	if err != nil {
		logging.Log("reaper: HasClaudeDescendant: list processes: %v", err)
		return false
	}
	live := BuildDescendantSet(procs, []int{panePID})
	for _, p := range procs {
		if live[p.PID] && containsClaude(p.Cmd) {
			return true
		}
	}
	return false
}

// KillProcess sends SIGTERM to exactly the process with the given PID -- never
// its process group. If the process does not exist the error is ignored.
func KillProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil // already gone
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("kill %d: %w", pid, err)
	}
	return nil
}

// Injection points so the reap logic is unit-testable without real
// processes, a real tmux server, or real signals. Production code never
// reassigns these.
var (
	listProcesses  = ListProcesses
	listPanePIDs   = serverPanePIDs
	queryServerPID = tmuxServerPID
	signalPID      = KillProcess
)

// tmuxServerPID returns the PID of the tmux server hosting sessionName.
func tmuxServerPID(sessionName string) (int, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", sessionName, "#{pid}").Output()
	if err != nil {
		return 0, fmt.Errorf("tmux display-message: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse tmux server pid %q: %w", strings.TrimSpace(string(out)), err)
	}
	return pid, nil
}

// serverPanePIDs returns the PIDs of every pane on the tmux server, across
// all sessions -- not just ours. Anything under any live pane is spared.
func serverPanePIDs() ([]int, error) {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F", "#{pane_pid}").Output()
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes: %w", err)
	}
	var pids []int
	for _, tok := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(tok); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// ServerPID returns the PID of the tmux server hosting sessionName, or 0 when
// the session (or tmux itself) is not running. Callers that are about to kill
// the session capture this first and pass it to ReapOrphansUnderServer, since
// the session can no longer be resolved once it is gone.
func ServerPID(sessionName string) int {
	pid, err := queryServerPID(sessionName)
	if err != nil {
		return 0
	}
	return pid
}

// orphan is a process the reaper has proven is ours and detached from every
// live pane, together with the ancestry chain that proves it.
type orphan struct {
	proc  ProcessInfo
	chain []int // proc.PID, its parent, ..., serverPID
}

// ancestryTo walks the ppid chain from pid up to ancestor using the snapshot
// in parent. It returns the chain (pid first, ancestor last) and true only if
// ancestor is reached. Reaching pid 1/0, a pid missing from the snapshot, or
// a cycle all mean "not provably a descendant".
func ancestryTo(pid, ancestor int, parent map[int]int) ([]int, bool) {
	chain := []int{pid}
	cur := pid
	for range len(parent) + 1 {
		ppid, ok := parent[cur]
		if !ok || ppid <= 1 {
			return nil, false
		}
		chain = append(chain, ppid)
		if ppid == ancestor {
			return chain, true
		}
		cur = ppid
	}
	return nil, false // cycle in a torn snapshot
}

// findOrphans returns the Claude processes that are safe to kill. A process
// qualifies only if ALL of the following hold:
//
//   - its command line references settingsDir (this agentctl instance);
//   - walking its ppid chain in the snapshot reaches serverPID, i.e. it is
//     provably a descendant of our tmux server (processes reparented to
//     pid 1 after their pane died are NOT provably ours and are skipped);
//   - it is not under any live pane on that server.
//
// An empty settingsDir, a missing server, or a serverPID that is not a tmux
// process in the snapshot all yield no orphans.
func findOrphans(procs []ProcessInfo, serverPID int, panePIDs []int, settingsDir string) []orphan {
	if settingsDir == "" || serverPID <= 1 {
		return nil
	}
	parent := make(map[int]int, len(procs))
	serverIsTmux := false
	for _, p := range procs {
		parent[p.PID] = p.PPID
		if p.PID == serverPID && strings.Contains(p.Cmd, "tmux") {
			serverIsTmux = true
		}
	}
	if !serverIsTmux {
		// The pid we were given is gone or recycled; nothing is provable.
		return nil
	}

	live := BuildDescendantSet(procs, panePIDs)
	var out []orphan
	for _, c := range FilterClaudeProcesses(procs, settingsDir) {
		if c.PID <= 1 || c.PID == serverPID || live[c.PID] {
			continue
		}
		chain, ok := ancestryTo(c.PID, serverPID, parent)
		if !ok {
			continue
		}
		out = append(out, orphan{proc: c, chain: chain})
	}
	return out
}

// ReapOrphans is the entry point called from the monitor while the session is
// running. It kills Claude processes of this agentctl instance (identified by
// settingsDir) that descend from the tmux server hosting sessionName but are no
// longer under any live pane. See findOrphans for the full set of rules.
//
// It is a no-op returning 0 when settingsDir is empty or tmux is not running.
// Returns the number of processes signaled.
func ReapOrphans(sessionName, settingsDir string) int {
	if settingsDir == "" {
		return 0
	}
	return ReapOrphansUnderServer(ServerPID(sessionName), settingsDir)
}

// ReapOrphansUnderServer is ReapOrphans with an already-resolved tmux server
// PID, for callers that capture it before killing the session. It is a no-op
// when settingsDir is empty or serverPID is not a live tmux server.
func ReapOrphansUnderServer(serverPID int, settingsDir string) int {
	if settingsDir == "" || serverPID <= 1 {
		return 0
	}

	// Snapshot processes BEFORE listing panes: a pane created in between is
	// absent from the snapshot and so can never be mistaken for an orphan.
	procs, err := listProcesses()
	if err != nil {
		logging.Log("reaper: list processes: %v", err)
		return 0
	}
	panePIDs, err := listPanePIDs()
	if err != nil {
		// Without the live pane set nothing can be proven detached.
		logging.Log("reaper: list pane PIDs: %v", err)
		return 0
	}

	killed := 0
	for _, o := range findOrphans(procs, serverPID, panePIDs, settingsDir) {
		logging.Log("reaper: killing orphaned Claude process pid=%d ancestry=%s (tmux server=%d, under no live pane, cmd references %s) cmd=%s",
			o.proc.PID, formatChain(o.chain), serverPID, settingsDir, textutil.Truncate(o.proc.Cmd, 120, "..."))
		if err := signalPID(o.proc.PID); err != nil {
			logging.Log("reaper: kill pid=%d: %v", o.proc.PID, err)
			continue
		}
		killed++
	}

	if killed > 0 {
		logging.Log("reaper: reaped %d orphaned Claude process(es)", killed)
	}
	return killed
}

func formatChain(chain []int) string {
	parts := make([]string, len(chain))
	for i, pid := range chain {
		parts[i] = strconv.Itoa(pid)
	}
	return strings.Join(parts, "->")
}
