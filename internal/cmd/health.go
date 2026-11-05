package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/hooks"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
)

// RunHealth prints a one-shot diagnostic summary for the current agentctl
// instance: monitor PID liveness, agent states, state-file freshness, log
// size, and any orphan monitor processes belonging to *this* machine that
// have no live tmux session behind them.
func RunHealth() {
	runtimeDir := paths.RuntimeDir()
	sessionName := tmux.SessionName

	fmt.Printf("Session: %s\n", sessionName)

	// Monitor PID status.
	fmt.Printf("  Monitor: %s\n", healthMonitorStatus(runtimeDir))

	// Agent states from state.json.
	stateFilePath := filepath.Join(runtimeDir, "state.json")
	st, err := state.Load()
	if err == nil && st != nil && len(st.Agents) > 0 {
		fmt.Printf("  Windows: %d\n", len(st.Agents))
		for _, a := range st.Agents {
			stateStr := a.State
			if stateStr == "" {
				stateStr = "unknown"
			}
			extra := ""
			if a.ContextPct > 0 {
				extra = fmt.Sprintf(" (ctx %d%%)", a.ContextPct)
			}
			fmt.Printf("    %-32s %s%s\n", a.Name, stateStr, extra)
		}
	} else {
		fmt.Printf("  Windows: (no state)\n")
	}

	// State file freshness.
	if info, statErr := os.Stat(stateFilePath); statErr == nil {
		age := time.Since(info.ModTime()).Round(10 * time.Millisecond)
		fmt.Printf("  State:   %s (last write %s ago, %s)\n",
			stateFilePath, age, healthFormatBytes(info.Size()))
	} else {
		fmt.Printf("  State:   %s (not found)\n", stateFilePath)
	}

	// Log file size. Report the monitor's shared log first -- diagnosing the
	// monitor is the whole point of health, and logging.MonitorLogPath is
	// deliberately env-independent (see its doc comment) so this is correct
	// no matter where `agentctl health` itself is invoked from. When the
	// calling process has its own distinct per-session log
	// (AGENTCTL_SESSION_ID set), print that on a second line so that
	// information isn't lost -- see healthShouldShowProcLog.
	monitorLogPath := logging.MonitorLogPath()
	procLogPath := logging.LogPath()
	fmt.Printf("  Log:      %s\n", healthFormatLogLine(monitorLogPath))
	if healthShouldShowProcLog(procLogPath, monitorLogPath) {
		fmt.Printf("  Proc log: %s\n", healthFormatLogLine(procLogPath))
	}

	fmt.Println()

	// Hook registration drift. See healthCheckHookDrift for why this compares
	// registered COMMAND STRINGS against internal/hooks' canonical map,
	// rather than merely checking that the subcommand exits 0 -- a hook event
	// silently rebound to a no-op like the shell builtin "true" exits 0 and
	// would pass any check that stops there.
	printHookDriftReport(healthCheckHookDrift(cwdOrEmpty()))

	fmt.Println()

	// Orphan monitor detection. See healthDecideOrphans for the correctness
	// rule and healthFindOrphanMonitors for how candidates and live session
	// paths are gathered. This report feeds a destructive suggestion (kill
	// the listed PIDs) so every branch below is written to fail toward
	// under-reporting, never over-reporting.
	report := healthFindOrphanMonitors(runtimeDir)
	switch {
	case len(report.Orphans) > 0:
		fmt.Printf("Orphan monitors: %d\n", len(report.Orphans))
		for _, o := range report.Orphans {
			fmt.Printf("  pid %-8s  cwd %s  (no live tmux session)\n", o.pid, o.cwd)
		}
	case report.Status == healthSessionsQueryFailed:
		// The tmux query itself errored for a reason other than "no server
		// running" (permissions, a wedged socket, etc). We cannot tell what
		// is live, so we report nothing rather than risk recommending every
		// monitor on the machine be killed over a transient hiccup.
		fmt.Println("Orphan monitors: none (tmux session query failed -- skipping orphan check as a precaution)")
	case report.Status == healthSessionsEmpty:
		// tmux reported zero live sessions (including "no server running").
		// Taken at face value this would make every monitor on the machine
		// an "orphan" by the letter of the definition, but a machine running
		// agentctl monitors with literally no tmux sessions at all is far
		// more likely mid-restart (e.g. `just relaunch`) than genuinely
		// abandoned. We deliberately do not treat this as "all orphans" --
		// see healthDecideOrphans.
		fmt.Println("Orphan monitors: none (tmux reports no live sessions -- skipping orphan check as a precaution)")
	case report.Checked == 0:
		fmt.Println("Orphan monitors: none (no other agentctl monitor processes found)")
	default:
		fmt.Printf("Orphan monitors: none (%d monitors checked, all have live sessions)\n", report.Checked)
	}
}

// healthMonitorStatus returns a human-readable description of the monitor
// process recorded in <runtimeDir>/monitor.pid.
func healthMonitorStatus(runtimeDir string) string {
	pidPath := filepath.Join(runtimeDir, "monitor.pid")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return "not running (no pidfile)"
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return "not running (invalid pidfile)"
	}

	proc, findErr := os.FindProcess(pid)
	if findErr != nil {
		return fmt.Sprintf("PID %d (not found)", pid)
	}
	if sigErr := proc.Signal(syscall.Signal(0)); sigErr != nil {
		return fmt.Sprintf("PID %d (not running)", pid)
	}

	// Verify it's actually an agentctl monitor and get its uptime.
	cmdlineOut, psErr := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=,etime=").Output()
	if psErr != nil {
		return fmt.Sprintf("PID %d (running)", pid)
	}
	fields := strings.Fields(strings.TrimSpace(string(cmdlineOut)))
	// The last field from etime= is the elapsed time; everything before it is args.
	isMonitor := false
	uptime := ""
	if len(fields) >= 2 {
		uptime = fields[len(fields)-1]
		cmdline := strings.Join(fields[:len(fields)-1], " ")
		isMonitor = strings.Contains(cmdline, "agentctl") && strings.Contains(cmdline, "monitor")
	}

	if !isMonitor {
		return fmt.Sprintf("PID %d (running, but NOT an agentctl monitor — stale pidfile?)", pid)
	}
	if uptime != "" {
		return fmt.Sprintf("PID %d (running, uptime %s)", pid, uptime)
	}
	return fmt.Sprintf("PID %d (running)", pid)
}

// healthFormatLogLine renders a single log line's value: "<path> (<size>)"
// if the file exists, or "<path> (not found)" otherwise. Each log line is
// evaluated independently so a missing session log doesn't affect the
// monitor log's reporting, or vice versa.
func healthFormatLogLine(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Sprintf("%s (not found)", path)
	}
	return fmt.Sprintf("%s (%s)", path, healthFormatBytes(info.Size()))
}

// healthShouldShowProcLog reports whether RunHealth should print a separate
// "Proc log:" line for the calling process's own log, given its LogPath and
// the shared MonitorLogPath. They differ exactly when the calling process
// has AGENTCTL_SESSION_ID set (see logging.LogPath) -- in every other case
// the two paths are identical and a second line would just be noise.
func healthShouldShowProcLog(procLogPath, monitorLogPath string) bool {
	return procLogPath != monitorLogPath
}

// healthFormatBytes formats a byte count as a human-readable string.
func healthFormatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}

// cwdOrEmpty returns the current working directory, or "" if it cannot be
// determined. Kept separate from os.Getwd() call sites so healthCheckHookDrift
// can be exercised in tests without depending on the test binary's cwd.
func cwdOrEmpty() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

// ── Hook registration drift ──────────────────────────────────────────────
//
// internal/hooks.HooksConfig() is the single canonical source of truth for
// every Claude Code hook event agentctl registers. A project's live
// .claude/settings.local.json is a separate, hand-editable (and gitignored)
// file that can drift from that canonical map -- most notably an
// agentctl-owned event silently rebound to a no-op command. The Stop event
// rebound to the shell builtin "true" is the motivating case: it still exits
// 0, so any drift check that only probes whether the hook's subcommand runs
// successfully passes happily while done-detection is completely disabled.
// This check instead compares the registered COMMAND STRINGS for every
// agentctl-owned event against the canonical value.

// healthHookMismatch describes an agentctl-owned hook event whose live
// registration does not match its canonical value: the commands actually
// registered ("Actual") versus what agentctl expects ("Expected").
type healthHookMismatch struct {
	Expected string
	Actual   string
}

// healthHookDriftReport is the result of comparing a project's live
// .claude/settings.local.json hooks block against hooks.HooksConfig().
type healthHookDriftReport struct {
	SettingsPath string
	Found        bool // whether SettingsPath existed and parsed as JSON
	Missing      []string
	Mismatched   map[string]healthHookMismatch
	Unknown      []string
}

// healthCheckHookDrift compares the hooks registered in
// <cwd>/.claude/settings.local.json against the canonical
// hooks.HooksConfig() map. For every agentctl-owned event it reports whether
// the event is missing entirely, present but drifted (with the expected vs.
// actual matcher+command entries -- see healthExtractHookEntries), or
// present and correct. The drift decision itself is a full structural
// comparison (json.Marshal of the whole event value), so it also catches an
// event that has the right matchers/commands but a missing or extra
// matcher entry, e.g. PermissionRequest registering only its Write matcher
// and silently dropping the Bash one. Any event in the live settings that
// agentctl does not own is reported as unknown/extra (this is informational,
// not a problem -- see writeHooksToPath, which deliberately preserves such
// events).
func healthCheckHookDrift(cwd string) healthHookDriftReport {
	report := healthHookDriftReport{Mismatched: map[string]healthHookMismatch{}}
	if cwd == "" {
		return report
	}
	report.SettingsPath = filepath.Join(cwd, ".claude", "settings.local.json")

	data, err := os.ReadFile(report.SettingsPath)
	if err != nil {
		return report
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return report
	}
	report.Found = true

	existing, _ := settings["hooks"].(map[string]interface{})
	canonical := hooks.HooksConfig()
	home, _ := os.UserHomeDir()

	for event, canonicalVal := range canonical {
		actualVal, present := existing[event]
		if !present {
			report.Missing = append(report.Missing, event)
			continue
		}
		// Compare semantically: bare `agentctl` and any absolute path to a
		// working agentctl binary are equivalent (see normalizeHookCommand).
		canonicalVal = normalizeHookValue(canonicalVal, home)
		normActual := normalizeHookValue(actualVal, home)
		canonicalJSON, _ := json.Marshal(canonicalVal)
		actualJSON, _ := json.Marshal(normActual)
		if string(canonicalJSON) != string(actualJSON) {
			report.Mismatched[event] = healthHookMismatch{
				Expected: strings.Join(healthExtractHookEntries(canonicalVal), ", "),
				Actual:   strings.Join(healthExtractHookEntries(normActual), ", "),
			}
		}
	}

	for event := range existing {
		if _, owned := canonical[event]; !owned {
			report.Unknown = append(report.Unknown, event)
		}
	}

	sort.Strings(report.Missing)
	sort.Strings(report.Unknown)

	return report
}

// healthExtractHookEntries walks a single hook event's JSON value (a list of
// {matcher?, hooks: [{type, command, async?}]} entries) and returns every
// registered command, in order, prefixed with its matcher when one is
// present (rendered as "<matcher>:<command>"). Some events (e.g.
// PermissionRequest) register the SAME command under multiple different
// matchers -- a drift where only the matcher changes (or one matcher entry
// goes missing entirely) would otherwise render an identical joined command
// list on both sides of the report and look self-contradictory ("expected"
// and "actual" printing the same text for something flagged as mismatched).
// Including the matcher makes that class of drift visible in the rendered
// form, not just in the underlying structural comparison. Returns nil for
// any shape that doesn't match what BuildHooksConfig produces (defensive --
// a live settings.local.json is hand-editable and may not conform).
func healthExtractHookEntries(eventVal interface{}) []string {
	var out []string
	entries, ok := eventVal.([]interface{})
	if !ok {
		return out
	}
	for _, entry := range entries {
		m, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		matcher, _ := m["matcher"].(string)
		hooksArr, ok := m["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, h := range hooksArr {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			cmd, ok := hm["command"].(string)
			if !ok {
				continue
			}
			if matcher != "" {
				out = append(out, matcher+":"+cmd)
			} else {
				out = append(out, cmd)
			}
		}
	}
	return out
}

// printHookDriftReport renders a healthHookDriftReport in the same style as
// the rest of RunHealth's output.
func printHookDriftReport(report healthHookDriftReport) {
	if report.SettingsPath == "" {
		fmt.Println("Hooks:   (could not determine project directory)")
		return
	}
	if !report.Found {
		fmt.Printf("Hooks:   %s not found -- run `agentctl init` or `agentctl hooks-install` to configure\n", report.SettingsPath)
		return
	}

	if len(report.Missing) == 0 && len(report.Mismatched) == 0 {
		fmt.Printf("Hooks:   in sync with canonical (%s)\n", report.SettingsPath)
	} else {
		fmt.Printf("Hooks:   DRIFT DETECTED in %s -- run `agentctl init` or `agentctl hooks-install` to repair\n", report.SettingsPath)
		for _, event := range report.Missing {
			fmt.Printf("    MISSING    %-20s not registered at all\n", event)
		}
		mismatchedEvents := make([]string, 0, len(report.Mismatched))
		for event := range report.Mismatched {
			mismatchedEvents = append(mismatchedEvents, event)
		}
		sort.Strings(mismatchedEvents)
		for _, event := range mismatchedEvents {
			mm := report.Mismatched[event]
			marker := "MISMATCH  "
			if event == "Stop" {
				// Stop drift silently disables done-detection entirely (the
				// hook still exits 0, e.g. when rebound to the shell builtin
				// "true"), so it gets the loudest possible marker.
				marker = "⚠ MISMATCH"
			}
			fmt.Printf("    %s %-20s expected=%q actual=%q\n", marker, event, mm.Expected, mm.Actual)
		}
	}

	if len(report.Unknown) > 0 {
		fmt.Printf("    (%d non-agentctl hook event(s) present, left alone: %s)\n",
			len(report.Unknown), strings.Join(report.Unknown, ", "))
	}
}

// ── Orphan monitor detection ─────────────────────────────────────────────
//
// The correct test for "is this monitor an orphan" is NOT "is it some other
// PID than the one in our pidfile" -- that flags every OTHER project's
// healthy monitor as an orphan, and the obvious next action on this report
// is to kill them. The correct test is: is there a live tmux session for the
// project this monitor is monitoring? A monitor is an orphan only if its
// resolved working directory does not match any live tmux session's
// resolved #{session_path}.

// healthMonitorCandidate is a candidate agentctl monitor process discovered
// via `ps aux`, plus its resolved working directory (once known). The same
// struct doubles as the reported shape for confirmed orphans -- the fields
// are identical either way.
type healthMonitorCandidate struct {
	pid     string
	cmdline string
	cwd     string // resolved, symlink-free absolute path; "" if unknown
}

// healthSessionQueryStatus classifies the outcome of querying tmux for live
// session paths, so callers can distinguish "we know there are zero live
// sessions" from "we don't actually know what's live" -- see
// healthLiveSessionPaths and healthDecideOrphans.
type healthSessionQueryStatus int

const (
	// healthSessionsOK means the query succeeded and returned at least one
	// live session path we can trust for comparison.
	healthSessionsOK healthSessionQueryStatus = iota
	// healthSessionsEmpty means tmux reported no live sessions at all. This
	// includes tmux's "no server running" error, which is how tmux reports
	// a genuinely empty server rather than returning a clean empty list.
	healthSessionsEmpty
	// healthSessionsQueryFailed means the query itself failed for some
	// other, unexplained reason (not "no server running"). Treated the same
	// as healthSessionsEmpty by healthDecideOrphans (report no orphans),
	// but kept distinct so the two cases can be reported with different,
	// honest messages -- see RunHealth.
	healthSessionsQueryFailed
)

// healthOrphanReport is the result of running orphan detection: the
// confirmed orphans, how many candidates were considered, and the state of
// the tmux session query that decision was based on.
type healthOrphanReport struct {
	Orphans []healthMonitorCandidate
	Checked int
	Status  healthSessionQueryStatus
}

// healthFindOrphanMonitors gathers candidate agentctl monitor processes (via
// `ps aux`, excluding the current process and the PID recorded in
// <runtimeDir>/monitor.pid) and the set of live tmux session paths, resolves
// both sides through healthResolvePath, and hands the pure decision off to
// healthDecideOrphans.
func healthFindOrphanMonitors(runtimeDir string) healthOrphanReport {
	knownPID := ""
	pidPath := filepath.Join(runtimeDir, "monitor.pid")
	if d, readErr := os.ReadFile(pidPath); readErr == nil {
		knownPID = strings.TrimSpace(string(d))
	}
	myPID := strconv.Itoa(os.Getpid())

	out, err := exec.Command("ps", "aux").Output()
	if err != nil {
		return healthOrphanReport{}
	}

	pairs := healthParseCandidatePIDs(string(out), myPID, knownPID)
	candidates := make([]healthMonitorCandidate, 0, len(pairs))
	for _, p := range pairs {
		cwd := healthResolvePath(processCWD(p.pid))
		candidates = append(candidates, healthMonitorCandidate{pid: p.pid, cmdline: p.cmdline, cwd: cwd})
	}

	liveSessionPaths, status := healthLiveSessionPaths()
	return healthDecideOrphans(candidates, liveSessionPaths, status)
}

// healthPIDCmdline is an intermediate (pid, cmdline) pair parsed from `ps
// aux` output, before cwd resolution.
type healthPIDCmdline struct {
	pid     string
	cmdline string
}

// healthParseCandidatePIDs parses `ps aux` output for processes whose
// command line contains both "agentctl" and "monitor", excluding myPID and
// knownPID (the current process and the PID recorded in this instance's own
// monitor.pid). Pure and independently testable -- no exec, no filesystem.
func healthParseCandidatePIDs(psAuxOutput, myPID, knownPID string) []healthPIDCmdline {
	var candidates []healthPIDCmdline
	for _, line := range strings.Split(psAuxOutput, "\n") {
		if !strings.Contains(line, "agentctl") || !strings.Contains(line, "monitor") {
			continue
		}
		// ps aux columns: USER PID %CPU %MEM VSZ RSS TT STAT STARTED TIME COMMAND...
		fields := strings.Fields(line)
		if len(fields) < 11 {
			continue
		}
		pid := fields[1]
		if pid == myPID || pid == knownPID {
			continue
		}
		cmdline := strings.Join(fields[10:], " ")
		candidates = append(candidates, healthPIDCmdline{pid: pid, cmdline: cmdline})
	}
	return candidates
}

// healthResolvePath normalises a path for comparison: filepath.EvalSymlinks
// then filepath.Clean, so e.g. macOS's /var -> /private/var symlink (which
// surfaces both ways depending on whether a path came from a live process's
// cwd or from tmux) doesn't produce a false orphan via naive string
// comparison. Returns "" for an empty input. If EvalSymlinks fails (path
// momentarily gone, permission denied, etc.) falls back to Clean alone
// rather than discarding the value -- a live tmux session's path or a live
// process's cwd should virtually always resolve, so this fallback is a
// defensive measure, not the expected path.
func healthResolvePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(p)
}

// healthLiveSessionPaths queries tmux for every live session's resolved
// working directory (`#{session_path}`), returning both the set of paths
// and a status describing how much to trust it -- see
// healthSessionQueryStatus.
func healthLiveSessionPaths() (map[string]bool, healthSessionQueryStatus) {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_path}").CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "no server running") {
			return map[string]bool{}, healthSessionsEmpty
		}
		return nil, healthSessionsQueryFailed
	}

	paths := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if resolved := healthResolvePath(line); resolved != "" {
			paths[resolved] = true
		}
	}
	if len(paths) == 0 {
		// tmux reported success but literally no sessions. In practice tmux
		// represents "zero sessions" as the "no server running" error above
		// rather than a clean empty list, but handle this defensively too.
		return paths, healthSessionsEmpty
	}
	return paths, healthSessionsOK
}

// healthDecideOrphans is the pure decision function for orphan detection: a
// candidate monitor is an orphan if and only if it has a known (non-empty)
// resolved cwd AND that cwd does not match any live tmux session's resolved
// path, AND the session query that produced liveSessionPaths actually
// succeeded.
//
// Matching policy: exact resolved-path equality only. A monitor's cwd is
// always the project root (see applyConfig in internal/tmux/tmux.go, which
// launches `agentctl monitor` via `tmux run-shell -c <projectRoot>`, not
// `<projectRoot>/.agentctl`), and tmux's own #{session_path} for an agentctl
// session is that same project root. No prefix/substring matching is added
// deliberately: it would make unrelated nested projects (e.g. a project and
// a subdirectory checked out as its own repo) match each other. No
// "strip trailing /.agentctl" exception is applied either, since inspection
// of how agentctl launches the monitor confirms its cwd is never the
// runtime directory itself.
//
// Fail-safe rules, in order of precedence:
//  1. status != healthSessionsOK => zero orphans, unconditionally. We do not
//     know what is live, so we assert nothing. This covers both a failed
//     tmux query and tmux genuinely reporting zero sessions (see
//     healthSessionQueryStatus) -- getting this backwards is exactly the
//     failure mode this function exists to prevent: a transient tmux hiccup
//     (or a mid-restart moment with no sessions yet) must never turn into a
//     report recommending every monitor on the machine be killed.
//  2. A candidate with an unknown (empty) cwd is never an orphan. Never
//     guess.
//  3. Otherwise, a candidate is an orphan iff its cwd is absent from
//     liveSessionPaths.
func healthDecideOrphans(candidates []healthMonitorCandidate, liveSessionPaths map[string]bool, status healthSessionQueryStatus) healthOrphanReport {
	report := healthOrphanReport{Checked: len(candidates), Status: status}
	if status != healthSessionsOK {
		return report
	}
	for _, c := range candidates {
		if c.cwd == "" {
			continue
		}
		if !liveSessionPaths[c.cwd] {
			report.Orphans = append(report.Orphans, c)
		}
	}
	return report
}
