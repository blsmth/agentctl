package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	callBash = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls -la"}}]}}` + "\n"
	resBash  = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"a\nb\n"}]}}` + "\n"
)

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestTailLog_PairsWithinOneRead(t *testing.T) {
	path := writeTestLog(t, t.TempDir(), "s.jsonl", callBash+resBash)
	res, err := TailLog(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || !res.Entries[0].HasResult || res.Entries[0].Output != "a\nb\n" {
		t.Fatalf("entries = %+v", res.Entries)
	}
	if len(res.Results) != 0 {
		t.Fatalf("unexpected orphans: %+v", res.Results)
	}
}

func TestTailLog_PairsAcrossTwoReads(t *testing.T) {
	path := writeTestLog(t, t.TempDir(), "s.jsonl", callBash)
	r1, _ := TailLog(path, 0)
	entries := r1.Entries
	if len(entries) != 1 || entries[0].HasResult {
		t.Fatalf("first read: %+v", entries)
	}
	appendFile(t, path, resBash)
	r2, err := TailLog(path, r1.NewOffset)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Entries) != 0 || len(r2.Results) != 1 {
		t.Fatalf("second read: entries=%d results=%d", len(r2.Entries), len(r2.Results))
	}
	if !ApplyToolResults(entries, r2.Results) || !entries[0].HasResult || entries[0].Output != "a\nb\n" {
		t.Fatalf("not paired: %+v", entries[0])
	}
	// Unknown ids are dropped without panicking.
	if ApplyToolResults(entries, []ToolResult{{ToolUseID: "zzz"}}) {
		t.Fatal("unknown id reported as changed")
	}
}

func TestParseToolResult(t *testing.T) {
	tests := []struct {
		name    string
		block   map[string]any
		out     string
		isError bool
	}{
		{"string", map[string]any{"tool_use_id": "x", "content": "hi"}, "hi", false},
		{"blocks", map[string]any{"tool_use_id": "x", "content": []any{
			map[string]any{"type": "text", "text": "one"},
			map[string]any{"type": "image"},
			map[string]any{"type": "text", "text": "two"},
		}}, "one\n[image]\ntwo", false},
		{"error", map[string]any{"tool_use_id": "x", "content": "boom", "is_error": true}, "boom", true},
		{"ansi", map[string]any{"tool_use_id": "x", "content": "\x1b[31mred\x1b[0m\x1b]0;title\x07 ok\r\n\x00a\tb\x1b[2J"}, "red ok\na\tb", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseToolResult(tt.block)
			if got.Output != tt.out || got.IsError != tt.isError || got.ToolUseID != "x" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestSanitizeToolOutput_Caps(t *testing.T) {
	got := sanitizeToolOutput(strings.Repeat("x", maxToolOutputBytes*2))
	if len(got) > maxToolOutputBytes+64 || !strings.HasSuffix(got, "(output truncated)") {
		t.Fatalf("len=%d suffix=%q", len(got), got[len(got)-30:])
	}
}

func TestTailLog_PiToolResult(t *testing.T) {
	data := `{"type":"message","id":"a1","parentId":null,"timestamp":"2026-11-17T20:10:14.895Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"echo hello-e2e"}}]}}` + "\n" +
		`{"type":"message","id":"r1","parentId":"a1","timestamp":"2026-11-17T20:10:14.925Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"bash","content":[{"type":"text","text":"hello-e2e\n"}],"isError":false}}` + "\n"
	path := writeTestLog(t, t.TempDir(), "pi.jsonl", data)
	res, err := TailLog(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].ToolName != "Bash" || res.Entries[0].Output != "hello-e2e\n" {
		t.Fatalf("entries = %+v", res.Entries)
	}
	lines := renderTerminalEntry(res.Entries[0], 40, false, false)
	if !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "echo hello-e2e") {
		t.Fatalf("render: %q", lines)
	}
}

func bashEntry(cmd, out string, hasResult, isErr bool) LogEntry {
	return LogEntry{Kind: "tool_call", ToolName: "Bash", FullInput: map[string]any{"command": cmd},
		HasResult: hasResult, Output: out, IsError: isErr}
}

func TestRenderTerminalEntry(t *testing.T) {
	var many []string
	for i := 1; i <= 12; i++ {
		many = append(many, fmt.Sprintf("line%d", i))
	}
	long := strings.Repeat("abcdefghij", 12)

	tests := []struct {
		name     string
		e        LogEntry
		expanded bool
		contains []string
		absent   []string
	}{
		{"running", bashEntry("make", "", false, false), false, []string{"$ make", "running…"}, nil},
		{"short output", bashEntry("ls", "a\nb", true, false), false, []string{"$ ls", "a", "b"}, []string{"more"}},
		{"truncated", bashEntry("seq", strings.Join(many, "\n"), true, false), false, []string{"… 4 more lines", "line12", "line5"}, []string{"line4\n", "line1\n"}},
		{"expanded", bashEntry("seq", strings.Join(many, "\n"), true, false), true, []string{"line1", "line12"}, []string{"more lines"}},
		{"error", bashEntry("false", "nope", true, true), false, []string{"nope"}, nil},
		{"edit", LogEntry{Kind: "tool_call", ToolName: "Edit", FullInput: map[string]any{"file_path": "/x/y.go"}}, false, []string{"✎", "/x/y.go"}, nil},
		{"text", newTextEntry("hello there"), false, []string{"hello there"}, nil},
	}
	for _, width := range []int{40, 100} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s/%d", tt.name, width), func(t *testing.T) {
				lines := renderTerminalEntry(tt.e, width, false, tt.expanded)
				joined := ansi.Strip(strings.Join(lines, "\n"))
				for _, c := range tt.contains {
					if !strings.Contains(joined, c) {
						t.Errorf("missing %q in %q", c, joined)
					}
				}
				for _, c := range tt.absent {
					if strings.Contains(joined, c) {
						t.Errorf("unexpected %q in %q", c, joined)
					}
				}
				for _, l := range lines {
					if w := lipgloss.Width(l); w > width {
						t.Errorf("line width %d > %d: %q", w, width, l)
					}
				}
			})
		}
		t.Run(fmt.Sprintf("long command wraps/%d", width), func(t *testing.T) {
			lines := renderTerminalEntry(bashEntry(long, "", true, false), width, false, false)
			if len(lines) < 2 {
				t.Fatalf("expected wrapped command, got %q", lines)
			}
			for _, l := range lines {
				if w := lipgloss.Width(l); w > width {
					t.Errorf("line width %d > %d", w, width)
				}
			}
		})
	}
}

func TestHudTerminalToggleAndRebuild(t *testing.T) {
	m := NewHudModel("")
	m.logEntries = []LogEntry{bashEntry("ls", "a", true, false)}
	m.rebuildStreamLineBuffer()
	if strings.Contains(ansi.Strip(strings.Join(m.streamLineBuffer, "\n")), "$ ls") {
		t.Fatal("default mode should not render terminal feed")
	}
	next, _ := m.Update(keyPress("t"))
	m = next.(HudModel)
	if !m.terminalMode || !strings.Contains(ansi.Strip(strings.Join(m.streamLineBuffer, "\n")), "$ ls") {
		t.Fatalf("toggle failed: %v", m.streamLineBuffer)
	}
	if !NewHudModel("").WithTerminalMode(true).terminalMode {
		t.Fatal("WithTerminalMode")
	}
}

func TestRenderTerminalBash_BoundedWork(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 600; i++ {
		sb.WriteString(strings.Repeat("x", 120) + "\n")
	}
	e := bashEntry("cat big", sb.String(), true, false)
	lines := renderTerminalEntry(e, 80, false, false)
	// prompt + "more lines" + 8 tail rows
	if len(lines) != 10 {
		t.Fatalf("got %d lines", len(lines))
	}
	if !strings.Contains(ansi.Strip(lines[1]), "more lines") {
		t.Fatalf("missing hidden marker: %q", lines[1])
	}
	entries := make([]LogEntry, 500)
	for i := range entries {
		entries[i] = e
	}
	start := time.Now()
	for _, en := range entries {
		renderTerminalEntry(en, 80, false, false)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("500 collapsed big entries took %v", d)
	}
}

func BenchmarkRenderTerminalBashCollapsed(b *testing.B) {
	e := bashEntry("cat big", strings.Repeat(strings.Repeat("x", 120)+"\n", 600), true, false)
	for i := 0; i < b.N; i++ {
		renderTerminalEntry(e, 80, false, false)
	}
}

func TestFollowHudDefaultsToTerminalMode(t *testing.T) {
	if !NewFollowHudModel().terminalMode {
		t.Fatal("follow HUD should default to terminal mode")
	}
}

func TestTailLog_OrphanResultFromMidFileStart(t *testing.T) {
	// A tail that starts mid-file sees a result whose call is before the offset.
	path := writeTestLog(t, t.TempDir(), "s.jsonl", resBash)
	res, err := TailLog(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 0 || len(res.Results) != 1 {
		t.Fatalf("entries=%d results=%d", len(res.Entries), len(res.Results))
	}
	entries := []LogEntry{{Kind: "text", FullText: "x"}}
	if ApplyToolResults(entries, res.Results) || entries[0].HasResult {
		t.Fatal("orphan result changed an unrelated entry")
	}
	if ApplyToolResults(nil, res.Results) {
		t.Fatal("orphan result changed nil entries")
	}
}

func TestRetargetDoesNotPairResultsToPreviousTarget(t *testing.T) {
	m := NewFollowHudModel()
	m.logEntries = []LogEntry{bashEntry("ls", "", false, false)}
	m.logEntries[0].ToolUseID = "t1"
	m.retarget("sess-b", t.TempDir(), "@2")
	if len(m.logEntries) != 0 || m.logOffset != 0 {
		t.Fatal("retarget kept entries/offset")
	}
	path := writeTestLog(t, t.TempDir(), "b.jsonl", resBash) // result for id t1
	m.logPath = path
	res, _ := TailLog(m.logPath, m.logOffset)
	ApplyToolResults(m.logEntries, res.Results)
	if len(m.logEntries) != 0 {
		t.Fatal("result paired into previous target's state")
	}
}
