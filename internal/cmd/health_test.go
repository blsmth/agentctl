package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/hooks"
)

// TestHealthDecideOrphans covers the pure orphan decision logic over
// injected inputs, per the ticket's fail-safe requirements: every uncertain
// case must resolve to "not an orphan".
func TestHealthDecideOrphans(t *testing.T) {
	liveMonitor := healthMonitorCandidate{pid: "111", cmdline: "agentctl monitor", cwd: "/live/project"}
	deadMonitor := healthMonitorCandidate{pid: "222", cmdline: "agentctl monitor", cwd: "/gone/project"}
	unknownCwdMonitor := healthMonitorCandidate{pid: "333", cmdline: "agentctl monitor", cwd: ""}

	tests := []struct {
		name           string
		candidates     []healthMonitorCandidate
		liveSessions   map[string]bool
		status         healthSessionQueryStatus
		wantOrphanPIDs []string
		wantChecked    int
	}{
		{
			name:           "cwd matches a live session -> not an orphan",
			candidates:     []healthMonitorCandidate{liveMonitor},
			liveSessions:   map[string]bool{"/live/project": true},
			status:         healthSessionsOK,
			wantOrphanPIDs: nil,
			wantChecked:    1,
		},
		{
			name:           "cwd matches nothing -> orphan",
			candidates:     []healthMonitorCandidate{deadMonitor},
			liveSessions:   map[string]bool{"/live/project": true},
			status:         healthSessionsOK,
			wantOrphanPIDs: []string{"222"},
			wantChecked:    1,
		},
		{
			name:           "unknown/empty cwd is never an orphan",
			candidates:     []healthMonitorCandidate{unknownCwdMonitor},
			liveSessions:   map[string]bool{"/live/project": true},
			status:         healthSessionsOK,
			wantOrphanPIDs: nil,
			wantChecked:    1,
		},
		{
			name:           "tmux query failed -> no orphans at all, even with candidates present",
			candidates:     []healthMonitorCandidate{deadMonitor, liveMonitor},
			liveSessions:   nil,
			status:         healthSessionsQueryFailed,
			wantOrphanPIDs: nil,
			wantChecked:    2,
		},
		{
			name:           "tmux genuinely has zero sessions -> no orphans (documented fail-safe choice)",
			candidates:     []healthMonitorCandidate{deadMonitor, liveMonitor},
			liveSessions:   map[string]bool{},
			status:         healthSessionsEmpty,
			wantOrphanPIDs: nil,
			wantChecked:    2,
		},
		{
			name:           "mixed: one live, one dead, one unknown -> only the dead one is an orphan",
			candidates:     []healthMonitorCandidate{liveMonitor, deadMonitor, unknownCwdMonitor},
			liveSessions:   map[string]bool{"/live/project": true},
			status:         healthSessionsOK,
			wantOrphanPIDs: []string{"222"},
			wantChecked:    3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := healthDecideOrphans(tt.candidates, tt.liveSessions, tt.status)

			if report.Checked != tt.wantChecked {
				t.Errorf("Checked = %d, want %d", report.Checked, tt.wantChecked)
			}
			if report.Status != tt.status {
				t.Errorf("Status = %v, want %v", report.Status, tt.status)
			}

			var gotPIDs []string
			for _, o := range report.Orphans {
				gotPIDs = append(gotPIDs, o.pid)
			}
			if !equalStringSlices(gotPIDs, tt.wantOrphanPIDs) {
				t.Errorf("orphan PIDs = %v, want %v", gotPIDs, tt.wantOrphanPIDs)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestHealthParseCandidatePIDs verifies ps-aux parsing excludes the current
// process's own PID and the PID recorded in this instance's pidfile, and
// only matches lines containing both "agentctl" and "monitor".
func TestHealthParseCandidatePIDs(t *testing.T) {
	psOutput := `USER       PID  %CPU %MEM      VSZ    RSS   TT  STAT STARTED      TIME COMMAND
alice      100   0.0  0.1 40000000  20000 s001  S    10:00am   0:00.10 /usr/local/bin/agentctl monitor
alice      101   0.0  0.1 40000000  20000 s002  S    10:01am   0:00.20 /usr/local/bin/agentctl monitor
alice      102   0.0  0.1 40000000  20000 s003  S    10:02am   0:00.30 /usr/local/bin/agentctl monitor
alice      999   0.0  0.1 40000000  20000 s004  S    10:03am   0:00.40 /usr/bin/grep agentctl
alice      103   0.0  0.1 40000000  20000 s005  S    10:04am   0:00.10 /usr/local/bin/agentctl chrome-bar-pane @1
`

	got := healthParseCandidatePIDs(psOutput, "100", "101")

	var gotPIDs []string
	for _, c := range got {
		gotPIDs = append(gotPIDs, c.pid)
	}
	want := []string{"102"}
	if !equalStringSlices(gotPIDs, want) {
		t.Errorf("candidate PIDs = %v, want %v (myPID=100 and knownPID=101 must be excluded, "+
			"non-monitor lines must be skipped)", gotPIDs, want)
	}
}

// TestHealthResolvePath_Symlinks verifies that two paths reaching the same
// directory via a symlink resolve to the same canonical value -- the macOS
// /var -> /private/var case the ticket calls out explicitly.
func TestHealthResolvePath_Symlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on windows")
	}

	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	viaReal := healthResolvePath(real)
	viaLink := healthResolvePath(link)

	if viaReal == "" || viaLink == "" {
		t.Fatalf("healthResolvePath returned empty: real=%q link=%q", viaReal, viaLink)
	}
	if viaReal != viaLink {
		t.Errorf("resolved paths differ: via real dir = %q, via symlink = %q -- want equal", viaReal, viaLink)
	}
}

// TestHealthResolvePath_Empty verifies an empty input resolves to empty
// output (never guess).
func TestHealthResolvePath_Empty(t *testing.T) {
	if got := healthResolvePath(""); got != "" {
		t.Errorf("healthResolvePath(\"\") = %q, want \"\"", got)
	}
	if got := healthResolvePath("   "); got != "" {
		t.Errorf("healthResolvePath(whitespace) = %q, want \"\"", got)
	}
}

// TestHealthResolvePath_NonexistentFallsBackToClean verifies that a path
// which no longer exists (EvalSymlinks fails) still resolves to something
// usable via Clean, rather than being discarded to "".
func TestHealthResolvePath_NonexistentFallsBackToClean(t *testing.T) {
	got := healthResolvePath("/definitely/does/not/exist/agentctl-health-test//")
	want := filepath.Clean("/definitely/does/not/exist/agentctl-health-test//")
	if got != want {
		t.Errorf("healthResolvePath(nonexistent) = %q, want %q", got, want)
	}
}

// writeSettingsHooks writes a .claude/settings.local.json under dir with the
// given raw "hooks" value, for use by the healthCheckHookDrift tests below.
func writeSettingsHooks(t *testing.T, dir string, hooksVal map[string]interface{}) {
	t.Helper()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	settings := map[string]interface{}{"hooks": hooksVal}
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(claudeDir, "settings.local.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func hookEntry(command string) []interface{} {
	return []interface{}{
		map[string]interface{}{
			"hooks": []interface{}{
				map[string]interface{}{
					"type":    "command",
					"command": command,
				},
			},
		},
	}
}

// TestHealthCheckHookDrift_NoSettingsFile verifies the "not found" case is
// reported distinctly rather than silently treated as "in sync".
func TestHealthCheckHookDrift_NoSettingsFile(t *testing.T) {
	dir := t.TempDir()
	report := healthCheckHookDrift(dir)
	if report.Found {
		t.Errorf("Found = true, want false for a directory with no settings file")
	}
	if len(report.Missing) != 0 || len(report.Mismatched) != 0 {
		t.Errorf("expected no drift findings when settings file is absent, got Missing=%v Mismatched=%v",
			report.Missing, report.Mismatched)
	}
}

// TestHealthCheckHookDrift_StopRebound is the ticket's motivating regression:
// Stop silently rebound to the shell builtin "true" (which exits 0) must be
// reported as a command-string mismatch, not missed by an exit-code-only check.
func TestHealthCheckHookDrift_StopRebound(t *testing.T) {
	dir := t.TempDir()
	writeSettingsHooks(t, dir, map[string]interface{}{
		"Stop": hookEntry("true"),
	})

	report := healthCheckHookDrift(dir)
	if !report.Found {
		t.Fatalf("Found = false, want true")
	}

	mm, ok := report.Mismatched["Stop"]
	if !ok {
		t.Fatalf("Stop not reported as mismatched; Mismatched=%v", report.Mismatched)
	}
	if !strings.Contains(mm.Expected, "hook-check-done") {
		t.Errorf("Expected command for Stop = %q, want it to mention hook-check-done", mm.Expected)
	}
	if mm.Actual != "true" {
		t.Errorf("Actual command for Stop = %q, want \"true\"", mm.Actual)
	}

	// Every other canonical event is absent from the live file entirely, so
	// they must all be reported missing (not silently ignored).
	if len(report.Missing) == 0 {
		t.Errorf("expected other canonical events to be reported missing, got none")
	}
	found := false
	for _, e := range report.Missing {
		if e == "PostToolUse" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected PostToolUse to be reported missing, Missing=%v", report.Missing)
	}
}

// TestHealthCheckHookDrift_MissingMatcherEntry covers a real second drift
// found in this project's own live settings: canonical PermissionRequest
// registers TWO matcher entries (Write and Bash), both running
// hook-permission-request. A live settings file with only the Write entry
// still "has PermissionRequest", so a naive presence check would miss it --
// but the missing Bash entry is what fires the dispatch-preview popup, so it
// must be reported as a mismatch.
func TestHealthCheckHookDrift_MissingMatcherEntry(t *testing.T) {
	dir := t.TempDir()
	canonical := hooks.HooksConfig()
	prEntries, ok := canonical["PermissionRequest"].([]interface{})
	if !ok || len(prEntries) < 2 {
		t.Fatalf("expected canonical PermissionRequest to have >= 2 matcher entries, got %#v", canonical["PermissionRequest"])
	}

	hooksVal := make(map[string]interface{}, len(canonical))
	for event, val := range canonical {
		hooksVal[event] = val
	}
	// Drop every matcher entry but the first (Write), silently losing Bash.
	hooksVal["PermissionRequest"] = []interface{}{prEntries[0]}

	writeSettingsHooks(t, dir, hooksVal)

	report := healthCheckHookDrift(dir)
	if !report.Found {
		t.Fatalf("Found = false, want true")
	}
	mm, ok := report.Mismatched["PermissionRequest"]
	if !ok {
		t.Fatalf("PermissionRequest not reported as mismatched when a matcher entry is missing entirely; Mismatched=%v", report.Mismatched)
	}
	if mm.Expected == mm.Actual {
		t.Errorf("Expected and Actual rendered identically (%q) -- the missing matcher entry must be visible in the report", mm.Expected)
	}
}

// TestHealthCheckHookDrift_MatcherValueChanged covers the reporting nit that
// accompanies the missing-matcher-entry drift above: if a matcher entry's
// MATCHER changes while its COMMAND stays identical (e.g. Bash -> Grep, both
// still running hook-permission-request), the structural comparison must
// still flag it, AND the rendered Expected/Actual strings must actually
// differ -- joining bare command strings alone would print the same text on
// both sides and make the report look self-contradictory.
func TestHealthCheckHookDrift_MatcherValueChanged(t *testing.T) {
	dir := t.TempDir()
	canonical := hooks.HooksConfig()
	prEntries, ok := canonical["PermissionRequest"].([]interface{})
	if !ok || len(prEntries) != 2 {
		t.Fatalf("expected canonical PermissionRequest to have exactly 2 matcher entries, got %#v", canonical["PermissionRequest"])
	}
	secondEntry, ok := prEntries[1].(map[string]interface{})
	if !ok {
		t.Fatalf("PermissionRequest entry 1 is %T, want map[string]interface{}", prEntries[1])
	}
	if secondEntry["matcher"] != "Bash" {
		t.Fatalf("expected canonical PermissionRequest entry 1 to have matcher \"Bash\", got %v", secondEntry["matcher"])
	}

	// Copy the second entry and change ONLY its matcher, leaving the
	// registered command untouched.
	altered := make(map[string]interface{}, len(secondEntry))
	for k, v := range secondEntry {
		altered[k] = v
	}
	altered["matcher"] = "Grep"

	hooksVal := make(map[string]interface{}, len(canonical))
	for event, val := range canonical {
		hooksVal[event] = val
	}
	hooksVal["PermissionRequest"] = []interface{}{prEntries[0], altered}

	writeSettingsHooks(t, dir, hooksVal)

	report := healthCheckHookDrift(dir)
	if !report.Found {
		t.Fatalf("Found = false, want true")
	}
	mm, ok := report.Mismatched["PermissionRequest"]
	if !ok {
		t.Fatalf("PermissionRequest not reported as mismatched when a matcher value changes; Mismatched=%v", report.Mismatched)
	}
	if mm.Expected == mm.Actual {
		t.Errorf("Expected and Actual rendered identically (%q) even though the matcher differs -- "+
			"the rendered form must include the matcher, not just the bare command string", mm.Expected)
	}
}

// TestHealthCheckHookDrift_UnknownEventPreservedAndReported verifies a
// non-agentctl hook event is reported as unknown/extra, distinct from a
// missing or mismatched agentctl-owned event.
func TestHealthCheckHookDrift_UnknownEventPreservedAndReported(t *testing.T) {
	dir := t.TempDir()
	writeSettingsHooks(t, dir, map[string]interface{}{
		"MyCustomEvent": hookEntry("echo custom"),
	})

	report := healthCheckHookDrift(dir)
	if !report.Found {
		t.Fatalf("Found = false, want true")
	}
	if len(report.Unknown) != 1 || report.Unknown[0] != "MyCustomEvent" {
		t.Errorf("Unknown = %v, want [MyCustomEvent]", report.Unknown)
	}
}

// ── Log line reporting ───────────────────────────────────────────────────

// TestHealthShouldShowProcLog covers the pure path-selection decision behind
// the "Proc log:" line: it must only appear when the calling process's own
// LogPath diverges from the shared MonitorLogPath (i.e. AGENTCTL_SESSION_ID
// was set for this process), never when the two paths are identical.
func TestHealthShouldShowProcLog(t *testing.T) {
	tests := []struct {
		name         string
		procLogPath  string
		monitorPath  string
		wantShowProc bool
	}{
		{
			name:         "identical paths -> no separate proc log line",
			procLogPath:  "/repo/.agentctl/logs/agentctl.log",
			monitorPath:  "/repo/.agentctl/logs/agentctl.log",
			wantShowProc: false,
		},
		{
			name:         "distinct per-session path -> show proc log line",
			procLogPath:  "/repo/.agentctl/logs/sessions/abc-123.log",
			monitorPath:  "/repo/.agentctl/logs/agentctl.log",
			wantShowProc: true,
		},
		{
			name:         "both empty (paths not initialised) -> no line",
			procLogPath:  "",
			monitorPath:  "",
			wantShowProc: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := healthShouldShowProcLog(tt.procLogPath, tt.monitorPath); got != tt.wantShowProc {
				t.Errorf("healthShouldShowProcLog(%q, %q) = %v, want %v",
					tt.procLogPath, tt.monitorPath, got, tt.wantShowProc)
			}
		})
	}
}

// TestHealthFormatLogLine verifies each log line is evaluated independently:
// an existing file reports its size, and a missing one reports "(not
// found)" without erroring.
func TestHealthFormatLogLine(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "agentctl.log")
	if err := os.WriteFile(existing, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write test log: %v", err)
	}
	missing := filepath.Join(dir, "does-not-exist.log")

	if got := healthFormatLogLine(existing); !strings.Contains(got, existing) || !strings.Contains(got, "5B") {
		t.Errorf("healthFormatLogLine(existing) = %q, want it to contain path and size", got)
	}
	if got := healthFormatLogLine(missing); !strings.Contains(got, missing) || !strings.Contains(got, "not found") {
		t.Errorf("healthFormatLogLine(missing) = %q, want it to contain path and \"not found\"", got)
	}
}

// TestHealthCheckHookDrift_InSync verifies a settings file that exactly
// matches hooks.HooksConfig() reports zero missing and zero mismatched
// events.
func TestHealthCheckHookDrift_InSync(t *testing.T) {
	dir := t.TempDir()
	writeSettingsHooks(t, dir, hooks.HooksConfig())

	report := healthCheckHookDrift(dir)
	if !report.Found {
		t.Fatalf("Found = false, want true")
	}
	if len(report.Missing) != 0 {
		t.Errorf("Missing = %v, want none for a fully in-sync settings file", report.Missing)
	}
	if len(report.Mismatched) != 0 {
		t.Errorf("Mismatched = %v, want none for a fully in-sync settings file", report.Mismatched)
	}
}
