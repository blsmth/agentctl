package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/registry"
	"github.com/blsmth/agentctl/internal/theme"
)

func TestParseAuditSince(t *testing.T) {
	t.Run("empty returns zero time", func(t *testing.T) {
		got, err := ParseAuditSince("")
		if err != nil {
			t.Fatalf("ParseAuditSince: %v", err)
		}
		if !got.IsZero() {
			t.Errorf("got %v, want zero time", got)
		}
	})

	t.Run("duration is relative to now", func(t *testing.T) {
		before := time.Now().Add(-time.Hour)
		got, err := ParseAuditSince("1h")
		if err != nil {
			t.Fatalf("ParseAuditSince: %v", err)
		}
		after := time.Now().Add(-time.Hour)
		if got.Before(before) || got.After(after) {
			t.Errorf("got %v, want between %v and %v", got, before, after)
		}
	})

	t.Run("RFC3339 timestamp parses exactly", func(t *testing.T) {
		got, err := ParseAuditSince("2026-01-01T00:00:00Z")
		if err != nil {
			t.Fatalf("ParseAuditSince: %v", err)
		}
		want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("invalid value errors", func(t *testing.T) {
		if _, err := ParseAuditSince("not-a-time"); err == nil {
			t.Error("expected an error for an unparseable --since value")
		}
	})
}

func setupAuditCmdTest(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	paths.ResetForTest()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(tmpDir)
	t.Cleanup(paths.ResetForTest)
}

func TestPrintAuditTable(t *testing.T) {
	setupAuditCmdTest(t)

	if err := audit.Append(audit.Record{
		Event:   audit.EventTool,
		Tool:    "Bash",
		Session: "s1",
		Agent:   "go-engineer",
		Window:  "@1",
		OK:      true,
		Summary: "echo hi",
	}); err != nil {
		t.Fatalf("audit.Append: %v", err)
	}
	if err := audit.Append(audit.Record{
		Event:   audit.EventToolFailure,
		Tool:    "Bash",
		Session: "s2",
		Agent:   "python-engineer",
		Window:  "@2",
		OK:      false,
		Error:   "boom",
		Summary: "Bash: boom",
	}); err != nil {
		t.Fatalf("audit.Append: %v", err)
	}

	t.Run("text table", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := PrintAuditTable(audit.Filter{}, false, false, true, ""); err != nil {
				t.Fatalf("PrintAuditTable: %v", err)
			}
		})
		if !strings.Contains(out, "go-engineer") || !strings.Contains(out, "python-engineer") {
			t.Errorf("table output missing expected agents: %q", out)
		}
		if !strings.Contains(out, "echo hi") {
			t.Errorf("table output missing summary: %q", out)
		}
		if strings.Contains(out, "PROJECT") {
			t.Errorf("single-project table must not have a PROJECT column: %q", out)
		}
	})

	t.Run("json output", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := PrintAuditTable(audit.Filter{Session: "s1"}, true, false, true, ""); err != nil {
				t.Fatalf("PrintAuditTable: %v", err)
			}
		})
		dec := json.NewDecoder(strings.NewReader(out))
		var rec audit.Record
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("decode JSON output: %v\noutput: %s", err, out)
		}
		if rec.Session != "s1" || rec.Tool != "Bash" {
			t.Errorf("decoded record = %+v, want session=s1 tool=Bash", rec)
		}
	})

	t.Run("failed filter", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := PrintAuditTable(audit.Filter{FailedOnly: true}, false, false, true, ""); err != nil {
				t.Fatalf("PrintAuditTable: %v", err)
			}
		})
		if strings.Contains(out, "go-engineer") {
			t.Errorf("expected only the failed record's agent, got: %q", out)
		}
		if !strings.Contains(out, "python-engineer") {
			t.Errorf("expected the failed record's agent present, got: %q", out)
		}
	})

	t.Run("project filter no match returns empty, not an error", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := PrintAuditTable(audit.Filter{Project: "no-such-project"}, false, false, true, ""); err != nil {
				t.Fatalf("PrintAuditTable: %v", err)
			}
		})
		if strings.Contains(out, "go-engineer") || strings.Contains(out, "python-engineer") {
			t.Errorf("expected no rows for a non-matching project filter, got: %q", out)
		}
	})
}

// TestPrintAuditTable_AllProjects verifies --all-projects scans every project
// the session registry knows about (not just the current one) and that its
// table output gains a PROJECT column, which single-project mode omits.
func TestPrintAuditTable_AllProjects(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root) // isolate the real registry dir too
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	registryDir := filepath.Join(root, ".config", "agentctl", "registry")
	registry.SetDirForTest(registryDir)
	t.Cleanup(func() { registry.SetDirForTest("") })

	projA := filepath.Join(root, "proja")
	projB := filepath.Join(root, "projb")
	rtA := filepath.Join(projA, ".agentctl")
	rtB := filepath.Join(projB, ".agentctl")

	if err := registry.Register(registry.Entry{SessionName: "agentctl-proja", ProjectDir: projA, RuntimeDir: rtA}); err != nil {
		t.Fatalf("registry.Register: %v", err)
	}
	if err := registry.Register(registry.Entry{SessionName: "agentctl-projb", ProjectDir: projB, RuntimeDir: rtB}); err != nil {
		t.Fatalf("registry.Register: %v", err)
	}

	// Write records directly into each project's audit dir -- these are
	// deliberately different agentctl processes/projects, so they don't go
	// through the current process's paths singleton.
	writeAuditRecordToDir(t, filepath.Join(rtA, "audit"), audit.Record{
		SchemaVersion: audit.SchemaVersion, Timestamp: "2026-01-01T00:00:00Z",
		Event: audit.EventTool, Session: "sa", Tool: "Bash", Agent: "go-engineer", Project: "proja", OK: true, Summary: "from proja",
	})
	writeAuditRecordToDir(t, filepath.Join(rtB, "audit"), audit.Record{
		SchemaVersion: audit.SchemaVersion, Timestamp: "2026-01-01T00:01:00Z",
		Event: audit.EventTool, Session: "sb", Tool: "Bash", Agent: "python-engineer", Project: "projb", OK: true, Summary: "from projb",
	})

	out := captureStdout(t, func() {
		if err := PrintAuditTable(audit.Filter{}, false, true, true, ""); err != nil {
			t.Fatalf("PrintAuditTable: %v", err)
		}
	})
	if !strings.Contains(out, "PROJECT") {
		t.Errorf("--all-projects table must have a PROJECT column, got: %q", out)
	}
	if !strings.Contains(out, "proja") || !strings.Contains(out, "projb") {
		t.Errorf("expected records from both registered projects, got: %q", out)
	}
	if !strings.Contains(out, "from proja") || !strings.Contains(out, "from projb") {
		t.Errorf("expected both projects' summaries, got: %q", out)
	}
}

// TestCollapseWhitespace pins the whitespace-collapsing behavior a DETAIL
// cell relies on: any run of whitespace, including embedded newlines and
// tabs, collapses to a single space.
func TestCollapseWhitespace(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no whitespace", "hello", "hello"},
		{"embedded newline", "line one\nline two", "line one line two"},
		{"multiple newlines and blank lines", "a\n\n\nb", "a b"},
		{"tabs and spaces", "a\t\t b   c", "a b c"},
		{"leading and trailing whitespace", "  hello world  ", "hello world"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := collapseWhitespace(tt.in); got != tt.want {
				t.Errorf("collapseWhitespace(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestPrintAuditTable_EmbeddedNewlineDoesNotBreakAlignment is the regression
// guard for a real corpus defect: a record whose Summary/Input contains a
// raw newline (e.g. multi-line Bash stdout) would otherwise split what's
// meant to be one printed row into several physical lines, breaking
// alignment for every row printed after it. Every printed line must still
// render at the same width as every other line.
func TestPrintAuditTable_EmbeddedNewlineDoesNotBreakAlignment(t *testing.T) {
	setupAuditCmdTest(t)
	t.Setenv("COLUMNS", "")

	if err := audit.Append(audit.Record{
		Event:   audit.EventTool,
		Tool:    "Bash",
		Session: "s1",
		Agent:   "go-engineer",
		Window:  "@1",
		OK:      true,
		Summary: "line one\nline two\nline three",
	}); err != nil {
		t.Fatalf("audit.Append: %v", err)
	}
	if err := audit.Append(audit.Record{
		Event:   audit.EventTool,
		Tool:    "Read",
		Session: "s1",
		Agent:   "go-engineer",
		Window:  "@1",
		OK:      true,
		Summary: "Read completed",
	}); err != nil {
		t.Fatalf("audit.Append: %v", err)
	}

	// full=false (not true, unlike this file's other tests): equal-width
	// rendering across rows is only guaranteed for the DETAIL column when
	// it's padded to detailColumnWidth's computed budget -- --full only caps
	// DETAIL, it doesn't pad it (see PrintAuditTable's detailPrintWidth doc
	// comment), so it wouldn't exercise the property this test checks.
	out := captureStdout(t, func() {
		if err := PrintAuditTable(audit.Filter{}, false, false, false, ""); err != nil {
			t.Fatalf("PrintAuditTable: %v", err)
		}
	})

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// header + separator + 2 data rows
	if len(lines) != 4 {
		t.Fatalf("expected 4 rendered lines, got %d:\n%s", len(lines), out)
	}
	wantWidth := lipgloss.Width(stripANSI(lines[0]))
	for i, line := range lines {
		if w := lipgloss.Width(stripANSI(line)); w != wantWidth {
			t.Errorf("line %d width = %d, want %d (alignment broken): %q", i, w, wantWidth, stripANSI(line))
		}
	}
	if strings.Contains(out, "line one\nline two") {
		t.Errorf("expected embedded newline collapsed, got raw newline in output: %q", out)
	}
}

// TestFmtAuditDuration pins sub-second duration rendering: durations under a
// second must render in milliseconds, not truncate to "0s" via fmtDuration's
// whole-second formatting.
func TestFmtAuditDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"sub-second renders as ms", 450 * time.Millisecond, "450ms"},
		{"very short renders as ms", 12 * time.Millisecond, "12ms"},
		{"zero renders as 0ms", 0, "0ms"},
		{"one second renders as fmtDuration", time.Second, "1s"},
		{"tens of seconds", 45 * time.Second, "45s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fmtAuditDuration(tt.d); got != tt.want {
				t.Errorf("fmtAuditDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// TestAuditModeCell pins the MODE column's "quiet by default" rendering:
// blank for the empty string and the literal "default" mode, the mode name
// verbatim otherwise.
func TestAuditModeCell(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{"empty", "", ""},
		{"default", "default", ""},
		{"acceptEdits", "acceptEdits", "acceptEdits"},
		{"bypassPermissions", "bypassPermissions", "bypassPermissions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := auditModeCell(tt.mode); got != tt.want {
				t.Errorf("auditModeCell(%q) = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

// TestFilterByMode covers the client-side --mode filtering implemented in
// this package (not audit.Filter, which is owned elsewhere).
func TestFilterByMode(t *testing.T) {
	records := []audit.Record{
		{Agent: "a", PermissionMode: "default"},
		{Agent: "b", PermissionMode: "acceptEdits"},
		{Agent: "c", PermissionMode: ""},
	}

	t.Run("empty mode imposes no filter", func(t *testing.T) {
		got := filterByMode(records, "")
		if len(got) != 3 {
			t.Errorf("len(got) = %d, want 3", len(got))
		}
	})

	t.Run("filters to exact match", func(t *testing.T) {
		got := filterByMode(records, "acceptEdits")
		if len(got) != 1 || got[0].Agent != "b" {
			t.Errorf("got %+v, want only agent b", got)
		}
	})

	t.Run("no matches returns empty slice", func(t *testing.T) {
		got := filterByMode(records, "no-such-mode")
		if len(got) != 0 {
			t.Errorf("got %+v, want empty", got)
		}
	})
}

// TestAuditDetail covers the DETAIL column's tool-input-summary-first,
// output-summary-fallback cascade, including the "Task" vs "Agent" tool name
// mismatch the live corpus surfaced (see toolinput.Summarize's doc comment).
func TestAuditDetail(t *testing.T) {
	tests := []struct {
		name string
		rec  audit.Record
		want string
	}{
		{
			name: "bash input summary wins over output summary",
			rec: audit.Record{
				Tool:    "Bash",
				Input:   json.RawMessage(`{"command":"go test ./..."}`),
				Summary: "PASS",
			},
			want: "go test ./...",
		},
		{
			name: "falls back to summary when input has nothing useful",
			rec: audit.Record{
				Tool:    "Read",
				Input:   nil,
				Summary: "Read completed",
			},
			want: "Read completed",
		},
		{
			name: "Agent tool name (not Task) still summarizes subagent input",
			rec: audit.Record{
				Tool:    "Agent",
				Input:   json.RawMessage(`{"subagent_type":"go-engineer","description":"fix the bug"}`),
				Summary: "Agent completed",
			},
			want: "go-engineer: fix the bug",
		},
		{
			name: "embedded newline in fallback summary is collapsed",
			rec: audit.Record{
				Tool:    "SomeFutureTool",
				Summary: "line one\nline two",
			},
			want: "line one line two",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := auditDetail(tt.rec, 200); got != tt.want {
				t.Errorf("auditDetail() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestPrintAuditTable_ModeFilter verifies --mode filtering applies to the
// text table (and thus, by the same code path, --json).
func TestPrintAuditTable_ModeFilter(t *testing.T) {
	setupAuditCmdTest(t)
	t.Setenv("COLUMNS", "")

	if err := audit.Append(audit.Record{
		Event: audit.EventTool, Tool: "Bash", Session: "s1", Agent: "go-engineer",
		Window: "@1", OK: true, Summary: "default mode call", PermissionMode: "default",
	}); err != nil {
		t.Fatalf("audit.Append: %v", err)
	}
	if err := audit.Append(audit.Record{
		Event: audit.EventTool, Tool: "Bash", Session: "s1", Agent: "python-engineer",
		Window: "@2", OK: true, Summary: "accept edits call", PermissionMode: "acceptEdits",
	}); err != nil {
		t.Fatalf("audit.Append: %v", err)
	}

	out := captureStdout(t, func() {
		if err := PrintAuditTable(audit.Filter{}, false, false, true, "acceptEdits"); err != nil {
			t.Fatalf("PrintAuditTable: %v", err)
		}
	})
	if strings.Contains(out, "go-engineer") {
		t.Errorf("expected the default-mode record filtered out, got: %q", out)
	}
	if !strings.Contains(out, "python-engineer") {
		t.Errorf("expected the acceptEdits record present, got: %q", out)
	}
}

// TestPrintAuditTable_DeniedVsFailed is the regression guard for the gap
// between the EventToolDenied/DeniedBy fields and the table renderer (which
// was built without knowing about them): a denied record must render
// DENIED, distinct from FAIL, and a genuine tool_failure record must keep
// rendering FAIL exactly as before.
func TestPrintAuditTable_DeniedVsFailed(t *testing.T) {
	setupAuditCmdTest(t)
	t.Setenv("COLUMNS", "")

	if err := audit.Append(audit.Record{
		Event:   audit.EventToolFailure,
		Tool:    "Bash",
		Session: "s1",
		Agent:   "go-engineer",
		Window:  "@1",
		OK:      false,
		Error:   "boom",
		Summary: "Bash: boom",
	}); err != nil {
		t.Fatalf("audit.Append: %v", err)
	}
	if err := audit.Append(audit.Record{
		Event:    audit.EventToolDenied,
		Tool:     "Bash",
		Session:  "s1",
		Agent:    "python-engineer",
		Window:   "@2",
		OK:       false,
		Summary:  "rm -rf /",
		DeniedBy: "rule",
	}); err != nil {
		t.Fatalf("audit.Append: %v", err)
	}

	out := captureStdout(t, func() {
		if err := PrintAuditTable(audit.Filter{}, false, false, true, ""); err != nil {
			t.Fatalf("PrintAuditTable: %v", err)
		}
	})

	if !strings.Contains(out, "DENIED") {
		t.Errorf("expected a DENIED row, got: %q", out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("expected a FAIL row, got: %q", out)
	}
}

// TestAuditStatusCell pins the status-cell rendering rule directly (both
// table render sites now delegate to this one function): EventToolDenied
// renders "DENIED" regardless of OK, a failed non-denial record renders
// "FAIL", and an ok non-denial record renders "ok".
func TestAuditStatusCell(t *testing.T) {
	green := cliGreen()
	red := cliStyle(theme.Current().Red)
	denied := cliStyle(theme.Current().Yellow)

	tests := []struct {
		name string
		rec  audit.Record
		want string
	}{
		{
			name: "denied renders DENIED, not FAIL",
			rec:  audit.Record{Event: audit.EventToolDenied, OK: false},
			want: "DENIED",
		},
		{
			name: "failure renders FAIL unchanged",
			rec:  audit.Record{Event: audit.EventToolFailure, OK: false},
			want: "FAIL",
		},
		{
			name: "ok renders ok unchanged",
			rec:  audit.Record{Event: audit.EventTool, OK: true},
			want: "ok",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Rendered cells carry ANSI color codes (lipgloss only strips them
			// against a non-TTY os.Stdout, which a direct function call here
			// isn't), so check the visible text is present rather than an
			// exact match -- same convention TestPrintAuditTable* uses.
			if got := auditStatusCell(tt.rec, green, red, denied); !strings.Contains(got, tt.want) {
				t.Errorf("auditStatusCell() = %q, want to contain %q", got, tt.want)
			}
		})
	}
}

// TestAuditDetail_DeniedBy pins that a denial record's DeniedBy surfaces in
// the DETAIL cell, and that "rule" and "human" are distinguishable at a
// glance -- a policy block and a human refusal mean very different things to
// a reviewer.
func TestAuditDetail_DeniedBy(t *testing.T) {
	tests := []struct {
		name string
		rec  audit.Record
		want string
	}{
		{
			name: "denied by rule",
			rec: audit.Record{
				Event:    audit.EventToolDenied,
				Tool:     "Bash",
				Input:    json.RawMessage(`{"command":"rm -rf /"}`),
				DeniedBy: "rule",
			},
			want: "denied by rule: rm -rf /",
		},
		{
			name: "denied by human",
			rec: audit.Record{
				Event:    audit.EventToolDenied,
				Tool:     "Bash",
				Input:    json.RawMessage(`{"command":"rm -rf /"}`),
				DeniedBy: "human",
			},
			want: "denied by human: rm -rf /",
		},
		{
			name: "non-denial record is unaffected by an empty DeniedBy",
			rec: audit.Record{
				Event: audit.EventTool,
				Tool:  "Bash",
				Input: json.RawMessage(`{"command":"echo hi"}`),
			},
			want: "echo hi",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := auditDetail(tt.rec, 200); got != tt.want {
				t.Errorf("auditDetail() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDetailColumnWidth pins the elastic-DETAIL-column sizing math: adding
// the PROJECT column (--all-projects) must shrink the remaining DETAIL
// budget by exactly its width plus one more column gap.
func TestDetailColumnWidth(t *testing.T) {
	t.Setenv("COLUMNS", "")
	single := detailColumnWidth(false)
	all := detailColumnWidth(true)
	if single <= 0 || all <= 0 {
		t.Fatalf("expected positive widths, got single=%d all=%d", single, all)
	}
	if all >= single {
		t.Errorf("expected --all-projects DETAIL width (%d) to be narrower than single-project (%d)", all, single)
	}
	if single < minAuditDetailWidth || all < minAuditDetailWidth {
		t.Errorf("expected DETAIL width floor of %d, got single=%d all=%d", minAuditDetailWidth, single, all)
	}
}

// TestPrintAuditTable_RenderedRowsMatchWidth is the regression guard for the
// column-width bug: text/tabwriter sizes each column to its widest actual
// member, so the auditCol* constants (and detailColumnWidth's arithmetic
// built on them) were decorative unless every cell was actually forced to its
// declared width. The header, the dash-repeat separator, and the data rows
// used to render at three different widths -- the data rows badly over the
// terminal budget, which is exactly what caused wrapping.
//
// The fixture's DETAIL is deliberately far longer than any terminal width
// used here, and its TS carries full RFC3339Nano sub-second precision (what a
// real record actually has, not a pre-shortened test value), so this exposes
// both bugs the ticket describes: the DETAIL column not actually being capped
// by detailColumnWidth's budget, and the TS column's real width disagreeing
// with the auditColTS constant the budget math assumed.
//
// COLUMNS values here are chosen above detailColumnWidth's minAuditDetailWidth
// floor -- see TestDetailColumnWidth's neighbor for what happens at/below it --
// so this pins the "must not overflow the terminal" property, not just the
// "must be internally consistent" one.
func TestPrintAuditTable_RenderedRowsMatchWidth(t *testing.T) {
	setupAuditCmdTest(t)

	longDetail := strings.Repeat("go test ./... -run TestSomethingVeryLongThatOverflows ", 10)
	if err := audit.Append(audit.Record{
		Event:     audit.EventTool,
		Tool:      "Bash",
		Session:   "s1",
		Agent:     "go-engineer",
		Window:    "@1",
		OK:        true,
		Timestamp: "2026-09-29T14:17:18.418908-04:00",
		Input:     json.RawMessage(`{"command":"` + longDetail + `"}`),
	}); err != nil {
		t.Fatalf("audit.Append: %v", err)
	}

	for _, columns := range []int{140, 220} {
		t.Run("COLUMNS="+strconv.Itoa(columns), func(t *testing.T) {
			t.Setenv("COLUMNS", strconv.Itoa(columns))

			out := captureStdout(t, func() {
				if err := PrintAuditTable(audit.Filter{}, false, false, false, ""); err != nil {
					t.Fatalf("PrintAuditTable: %v", err)
				}
			})

			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if len(lines) != 3 { // header + separator + 1 data row
				t.Fatalf("expected 3 rendered lines (header, separator, 1 data row), got %d:\n%s", len(lines), out)
			}

			headerWidth := lipgloss.Width(stripANSI(lines[0]))
			if headerWidth <= 0 {
				t.Fatalf("expected a positive rendered width, got %d", headerWidth)
			}
			if headerWidth > columns {
				t.Errorf("rendered width %d exceeds terminal budget %d (would wrap): %q", headerWidth, columns, stripANSI(lines[0]))
			}
			for i, line := range lines {
				stripped := stripANSI(line)
				if w := lipgloss.Width(stripped); w != headerWidth {
					t.Errorf("line %d (%s) width = %d, want %d (header/separator/data must render at the same width): %q",
						i, []string{"header", "separator", "data"}[i], w, headerWidth, stripped)
				}
			}
		})
	}
}

func writeAuditRecordToDir(t *testing.T, dir string, rec audit.Record) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	path := filepath.Join(dir, rec.Session+".jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
