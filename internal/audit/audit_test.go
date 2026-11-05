package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
)

// setupAuditTest points paths at a fresh temp dir for the duration of a test
// and restores state afterward. AGENTCTL_RUNTIME_DIR is inherited from any
// agentctl-managed tmux window, so it must be pinned to empty or paths.Init
// silently ignores the temp dir (see the same pattern in internal/paths's
// own tests).
func setupAuditTest(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	paths.ResetForTest()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(tmpDir)
	t.Cleanup(paths.ResetForTest)
	return tmpDir
}

func TestAppend_RoundTrip(t *testing.T) {
	setupAuditTest(t)

	rec := Record{
		Event:     EventTool,
		Tool:      "Bash",
		ToolUseID: "toolu_123",
		Session:   "sess-1",
		Agent:     "go-engineer",
		Window:    "@5",
		OK:        true,
		Summary:   "echo one",
		Input:     json.RawMessage(`{"command":"echo one"}`),
		Result:    json.RawMessage(`{"stdout":"one"}`),
	}
	if err := Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := Query(Filter{Session: "sess-1"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Query returned %d records, want 1", len(got))
	}
	r := got[0]
	if r.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", r.SchemaVersion, SchemaVersion)
	}
	if r.Timestamp == "" {
		t.Error("Timestamp is empty, want auto-filled RFC3339Nano value")
	}
	if _, err := time.Parse(time.RFC3339Nano, r.Timestamp); err != nil {
		t.Errorf("Timestamp %q is not RFC3339Nano: %v", r.Timestamp, err)
	}
	if r.Tool != "Bash" || r.ToolUseID != "toolu_123" || r.Session != "sess-1" {
		t.Errorf("record fields mismatch: %+v", r)
	}
	if !r.OK {
		t.Error("OK = false, want true")
	}
}

func TestAppend_FileMode(t *testing.T) {
	setupAuditTest(t)

	if err := Append(Record{Event: EventTool, Session: "sess-mode"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	path := filepath.Join(paths.AuditDir(), "sess-mode.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want %o", got, 0o600)
	}
}

func TestAppend_UnknownSessionFallback(t *testing.T) {
	setupAuditTest(t)

	if err := Append(Record{Event: EventTool}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	path := filepath.Join(paths.AuditDir(), "unknown.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected unknown.jsonl to exist: %v", err)
	}
}

func TestQuery_Filters(t *testing.T) {
	setupAuditTest(t)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	records := []Record{
		{Timestamp: base.Format(time.RFC3339Nano), Event: EventTool, Tool: "Bash", Agent: "go-engineer", Window: "@1", Session: "s1", OK: true},
		{Timestamp: base.Add(time.Minute).Format(time.RFC3339Nano), Event: EventTool, Tool: "Edit", Agent: "go-engineer", Window: "@1", Session: "s1", OK: true},
		{Timestamp: base.Add(2 * time.Minute).Format(time.RFC3339Nano), Event: EventToolFailure, Tool: "Bash", Agent: "python-engineer", Window: "@2", Session: "s2", OK: false, Error: "boom"},
		{Timestamp: base.Add(3 * time.Minute).Format(time.RFC3339Nano), Event: EventTool, Tool: "Read", Agent: "python-engineer", Window: "@2", Session: "s2", OK: true},
	}
	for _, r := range records {
		if err := Append(r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	tests := []struct {
		name   string
		filter Filter
		want   int
	}{
		{"no filter", Filter{}, 4},
		{"by session", Filter{Session: "s1"}, 2},
		{"by agent", Filter{Agent: "python-engineer"}, 2},
		{"by window", Filter{Window: "@1"}, 2},
		{"by tool", Filter{Tool: "Bash"}, 2},
		{"failed only", Filter{FailedOnly: true}, 1},
		{"since", Filter{Since: base.Add(90 * time.Second)}, 2},
		{"limit", Filter{Limit: 1}, 1},
		{"session and tool", Filter{Session: "s2", Tool: "Read"}, 1},
		{"unknown session", Filter{Session: "does-not-exist"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Query(tt.filter)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("Query(%+v) returned %d records, want %d (%+v)", tt.filter, len(got), tt.want, got)
			}
		})
	}
}

func TestQuery_LimitKeepsMostRecent(t *testing.T) {
	setupAuditTest(t)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		rec := Record{
			Timestamp: base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano),
			Event:     EventTool,
			Session:   "s1",
			Tool:      "Bash",
			Summary:   string(rune('a' + i)),
			OK:        true,
		}
		if err := Append(rec); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := Query(Filter{Session: "s1", Limit: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Summary != "c" {
		t.Errorf("Summary = %q, want %q (most recent)", got[0].Summary, "c")
	}
}

func TestQuery_SkipsLinesMissingSchemaVersion(t *testing.T) {
	setupAuditTest(t)

	// Write a mixed-schema file directly: one old-writer line (no "v" field,
	// the shape the deleted AuditCmd wrote) and one current-schema line.
	auditDir := paths.AuditDir()
	if err := os.MkdirAll(auditDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(auditDir, "mixed.jsonl")
	oldLine := `{"ts":"2025-01-01T00:00:00Z","user":"bob","agent":"go-engineer","instance":"go-engineer","window":"@3","session":"mixed","cwd":"/tmp","cmd":"echo hi"}`
	newLine, err := json.Marshal(Record{
		SchemaVersion: SchemaVersion,
		Timestamp:     time.Now().Format(time.RFC3339Nano),
		Event:         EventTool,
		Session:       "mixed",
		Tool:          "Bash",
		OK:            true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	content := oldLine + "\n" + string(newLine) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := Query(Filter{Session: "mixed"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Query returned %d records, want 1 (old-schema line should be skipped): %+v", len(got), got)
	}
	if got[0].Tool != "Bash" {
		t.Errorf("Tool = %q, want %q", got[0].Tool, "Bash")
	}
}

// TestQuery_V1AndV2RecordsStillParse pins that the v3 schema bump (adding
// EventToolDenied and Record.DeniedBy) does not break readers on records
// written by the v1 (no Project/RepoRoot) or v2 (Project/RepoRoot, no
// DeniedBy) writers -- both must still parse and be returned by Query.
func TestQuery_V1AndV2RecordsStillParse(t *testing.T) {
	setupAuditTest(t)

	auditDir := paths.AuditDir()
	if err := os.MkdirAll(auditDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(auditDir, "versions.jsonl")

	v1Line := `{"v":1,"ts":"2026-01-01T00:00:00Z","event":"tool","session":"versions","tool":"Bash","ok":true}`
	v2Line := `{"v":2,"ts":"2026-01-01T00:01:00Z","event":"tool","session":"versions","tool":"Edit","project":"agentctl","repo_root":"/repo","ok":true}`
	content := v1Line + "\n" + v2Line + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := Query(Filter{Session: "versions"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Query returned %d records, want 2 (v1 and v2 must both still parse): %+v", len(got), got)
	}
	if got[0].SchemaVersion != 1 || got[0].Tool != "Bash" {
		t.Errorf("record[0] = %+v, want v1 Bash record", got[0])
	}
	if got[1].SchemaVersion != 2 || got[1].Project != "agentctl" {
		t.Errorf("record[1] = %+v, want v2 record with Project populated", got[1])
	}
	// Neither predates DeniedBy -- both should read back empty, not error.
	if got[0].DeniedBy != "" || got[1].DeniedBy != "" {
		t.Errorf("DeniedBy should be empty for v1/v2 records, got %q and %q", got[0].DeniedBy, got[1].DeniedBy)
	}
}

// TestQuery_FilterByEventAndDeniedOnly verifies the v3 Filter additions:
// Event narrows to an exact event name, and DeniedOnly narrows to
// EventToolDenied specifically.
func TestQuery_FilterByEventAndDeniedOnly(t *testing.T) {
	setupAuditTest(t)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	records := []Record{
		{Timestamp: base.Format(time.RFC3339Nano), Event: EventTool, Session: "s1", Tool: "Bash", OK: true},
		{Timestamp: base.Add(time.Minute).Format(time.RFC3339Nano), Event: EventToolFailure, Session: "s1", Tool: "Bash", OK: false},
		{Timestamp: base.Add(2 * time.Minute).Format(time.RFC3339Nano), Event: EventToolDenied, Session: "s1", Tool: "Bash", OK: false, DeniedBy: "rule"},
		{Timestamp: base.Add(3 * time.Minute).Format(time.RFC3339Nano), Event: EventToolDenied, Session: "s1", Tool: "Bash", OK: false, DeniedBy: "human"},
	}
	for _, r := range records {
		if err := Append(r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := Query(Filter{Session: "s1", Event: EventToolDenied})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Filter{Event: EventToolDenied} returned %d, want 2", len(got))
	}

	got, err = Query(Filter{Session: "s1", DeniedOnly: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Filter{DeniedOnly: true} returned %d, want 2", len(got))
	}
	for _, r := range got {
		if r.Event != EventToolDenied {
			t.Errorf("DeniedOnly returned non-denied record: %+v", r)
		}
	}
}

func TestQuery_SkipsMalformedLines(t *testing.T) {
	setupAuditTest(t)

	auditDir := paths.AuditDir()
	if err := os.MkdirAll(auditDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(auditDir, "broken.jsonl")
	content := "not json at all\n" + `{"v":1,"ts":"2026-01-01T00:00:00Z","event":"tool","session":"broken","tool":"Bash","ok":true}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := Query(Filter{Session: "broken"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Query returned %d records, want 1", len(got))
	}
}

func TestAppend_TruncatesOversizedInputAndResult(t *testing.T) {
	setupAuditTest(t)

	bigInput, err := json.Marshal(strings.Repeat("x", maxInputBytes*2))
	if err != nil {
		t.Fatalf("marshal bigInput: %v", err)
	}
	bigResult, err := json.Marshal(strings.Repeat("y", maxResultBytes*3))
	if err != nil {
		t.Fatalf("marshal bigResult: %v", err)
	}

	rec := Record{
		Event:   EventTool,
		Session: "big",
		Tool:    "Bash",
		OK:      true,
		Input:   json.RawMessage(bigInput),
		Result:  json.RawMessage(bigResult),
	}
	if err := Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	path := filepath.Join(paths.AuditDir(), "big.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The whole line (minus trailing newline) must be under the cap.
	line := strings.TrimRight(string(data), "\n")
	if len(line) >= maxLineBytes {
		t.Errorf("line length = %d, want < %d", len(line), maxLineBytes)
	}

	got, err := Query(Filter{Session: "big"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}

	var inputMarker, resultMarker truncatedMarker
	if err := json.Unmarshal(got[0].Input, &inputMarker); err != nil {
		t.Fatalf("unmarshal input marker: %v", err)
	}
	if !inputMarker.Truncated || inputMarker.OriginalBytes != len(bigInput) {
		t.Errorf("input marker = %+v, want truncated with original_bytes=%d", inputMarker, len(bigInput))
	}
	if err := json.Unmarshal(got[0].Result, &resultMarker); err != nil {
		t.Fatalf("unmarshal result marker: %v", err)
	}
	if !resultMarker.Truncated || resultMarker.OriginalBytes != len(bigResult) {
		t.Errorf("result marker = %+v, want truncated with original_bytes=%d", resultMarker, len(bigResult))
	}
}

// TestAppend_AsymmetricCaps_InputSurvivesResultTruncated pins the v3
// asymmetric size caps: a 10KB Input is well under maxInputBytes
// (16KB) and must survive intact, while a 10KB Result is well over
// maxResultBytes (4KB) and must be truncated. For a security log the input
// is the evidence and the result is comparatively noise.
func TestAppend_AsymmetricCaps_InputSurvivesResultTruncated(t *testing.T) {
	setupAuditTest(t)

	const size = 10 * 1024
	input, err := json.Marshal(strings.Repeat("i", size))
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	result, err := json.Marshal(strings.Repeat("r", size))
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	rec := Record{
		Event:   EventTool,
		Session: "asymmetric",
		Tool:    "Bash",
		OK:      true,
		Input:   json.RawMessage(input),
		Result:  json.RawMessage(result),
	}
	if err := Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := Query(Filter{Session: "asymmetric"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}

	if string(got[0].Input) != string(input) {
		t.Errorf("Input was mutated/truncated, want it to survive intact (10KB < maxInputBytes=%d)", maxInputBytes)
	}

	var resultMarker truncatedMarker
	if err := json.Unmarshal(got[0].Result, &resultMarker); err != nil {
		t.Fatalf("Result is not a truncation marker (want truncated, 10KB > maxResultBytes=%d): %v", maxResultBytes, err)
	}
	if !resultMarker.Truncated || resultMarker.OriginalBytes != len(result) {
		t.Errorf("result marker = %+v, want truncated with original_bytes=%d", resultMarker, len(result))
	}
}

func TestTruncateJSON(t *testing.T) {
	tests := []struct {
		name  string
		raw   json.RawMessage
		limit int
		want  bool // whether the result should be a truncation marker
	}{
		{"nil passes through", nil, 10, false},
		{"under limit passes through", json.RawMessage(`{"a":1}`), 100, false},
		{"exactly at limit passes through", json.RawMessage(`12345`), 5, false},
		{"over limit truncated", json.RawMessage(`{"a":"12345678901"}`), 5, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateJSON(tt.raw, tt.limit)
			var marker truncatedMarker
			isMarker := json.Unmarshal(got, &marker) == nil && marker.Truncated
			if isMarker != tt.want {
				t.Errorf("truncateJSON(%s, %d) = %s, want marker=%v", tt.raw, tt.limit, got, tt.want)
			}
			if !tt.want && string(got) != string(tt.raw) {
				t.Errorf("truncateJSON(%s, %d) = %s, want unchanged", tt.raw, tt.limit, got)
			}
		})
	}
}

func TestAppend_ErrorsWhenRuntimeDirUnset(t *testing.T) {
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)

	if err := Append(Record{Event: EventTool, Session: "x"}); err == nil {
		t.Error("Append with unset runtime dir: want error, got nil")
	}
}
